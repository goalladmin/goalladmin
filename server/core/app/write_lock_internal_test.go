package app

// 规范 §13.2 第 189 条：后台写事务按会话行里的锁屏状态决定是否执行。

import (
	"context"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
)

func TestAuth_189_WriteChecksLockStateInTransaction(t *testing.T) {
	f := newAuthFixture(t)
	uid := f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	sid, _, _ := strings.Cut(cookie, ".")
	require.Equal(t, 200, f.ping(access).rec.Code)
	p := auth.Principal{Portal: testPortal, UserID: uid, SessionID: sid}
	ctx := auth.WithPrincipal(f.testCtx(), p)
	sm := f.app.authenticators[testPortal].Sessions()
	// 状态由另一处调用更新，本次写入沿用已认证的身份；写事务必须读会话行。
	require.NoError(t, sm.Lock(ctx, testPortal, sid))
	called := false
	err := f.app.deps.RBAC.WithActor(ctx, p, func(context.Context, auth.Principal) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, httpx.ErrSessionLocked)
	require.False(t, called)
	err = f.app.deps.RBAC.WithSelf(ctx, p, func(context.Context) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, httpx.ErrSessionLocked)
	require.False(t, called)
	// 锁屏仍是有效会话，原有只检查有效性的入口保持语义。
	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error { return sm.LockActive(ctx, testPortal, sid) }))
	require.ErrorIs(t, db.Tx(ctx, func(ctx context.Context) error { return sm.LockWritable(ctx, testPortal, sid) }), session.ErrLocked)
	require.NoError(t, sm.Unlock(ctx, testPortal, sid))
	require.NoError(t, f.app.deps.RBAC.WithActor(ctx, p, func(context.Context, auth.Principal) error {
		called = true
		return nil
	}))
	require.True(t, called)
	called = false
	require.NoError(t, f.app.deps.RBAC.WithSelf(ctx, p, func(context.Context) error {
		called = true
		return nil
	}))
	require.True(t, called)
}

func TestAuth_189_PasswordChangeChecksLockAfterCalculation(t *testing.T) {
	f := newAuthFixture(t)
	uid := f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	sid, _, _ := strings.Cut(cookie, ".")
	require.Equal(t, 200, f.ping(access).rec.Code)
	oldHash := f.users.byID[uid].PasswordHash
	// 读取旧密码后另一处完成锁屏：计算可继续，落库前必须拒绝。
	f.users.mu.Lock()
	f.users.afterFind = func() {
		require.NoError(t, f.app.authenticators[testPortal].Sessions().Lock(f.testCtx(), testPortal, sid))
	}
	f.users.mu.Unlock()
	r := f.do("PUT", "/auth/password", gin.H{"oldPassword": "correct-horse-9", "newPassword": "another-horse-8"}, bearerOpt(access))
	require.Equal(t, 423, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeSessionLocked, r.env.Code)
	f.users.mu.Lock()
	newHash := f.users.byID[uid].PasswordHash
	f.users.mu.Unlock()
	require.Equal(t, oldHash, newHash)
}
