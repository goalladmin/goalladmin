package rbac

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// 规范 §13.2 第 185 条（D-101）：ClearUserRoles 清空主体端账号的角色。只在事务里用；提交后授权快照里这个账号
// 立即没有权限；别的账号不受影响；回滚的不生效；有超管角色的端不接受。
func TestClearUserRoles_185(t *testing.T) {
	f := newOrgFix(t)
	s, ctxA := f.s, f.as(f.ownerA)
	ra := f.role(t, f.ownerA, "viewer", "shop:thing:list")
	rb := f.role(t, f.ownerA, "editor", "shop:thing:edit")
	require.NoError(t, s.AssignUserRoles(ctxA, f.ownerA, shopP, f.staffA.UserID, []uint64{ra.ID, rb.ID}))
	require.NoError(t, s.AssignUserRoles(ctxA, f.ownerA, shopP, f.staffA2.UserID, []uint64{ra.ID}))
	allowed := func(p auth.Principal, perm string) bool {
		ok, err := s.Allowed(f.as(p), shopP, p.UserID, perm)
		require.NoError(t, err)
		return ok
	}
	require.True(t, allowed(f.staffA, "shop:thing:edit"))

	// 不在事务里：拒绝，什么都不动
	require.ErrorIs(t, s.ClearUserRoles(f.ctx, shopP, f.staffA.UserID), ErrNoTx)
	require.True(t, allowed(f.staffA, "shop:thing:edit"))

	// 回滚的事务：不生效
	boom := context.Canceled
	err := db.Tx(f.ctx, func(ctx context.Context) error {
		require.NoError(t, s.ClearUserRoles(ctx, shopP, f.staffA.UserID))
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.True(t, allowed(f.staffA, "shop:thing:edit"))
	roles, err := s.UserRoles(ctxA, shopP, f.staffA.UserID)
	require.NoError(t, err)
	require.Len(t, roles, 2)

	// 提交：这个账号立即没有权限，同主体别的账号照旧，角色本身还在
	require.NoError(t, db.Tx(f.ctx, func(ctx context.Context) error { return s.ClearUserRoles(ctx, shopP, f.staffA.UserID) }))
	require.False(t, allowed(f.staffA, "shop:thing:edit"))
	require.False(t, allowed(f.staffA, "shop:thing:list"))
	require.True(t, allowed(f.staffA2, "shop:thing:list"))
	roles, err = s.UserRoles(ctxA, shopP, f.staffA.UserID)
	require.NoError(t, err)
	require.Empty(t, roles)
	all, err := s.Roles(ctxA, shopP)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// 有超管角色的端：不接受（那里改角色要过"最后一个超管"的检查）
	pr := portal.NewRegistry()
	require.NoError(t, pr.Register(portal.Portal{Code: "back", Users: f.users}))
	reg := NewRegistry()
	require.NoError(t, reg.Finalize())
	plain, err := NewService(Options{Registry: reg, Base: f.ctx, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), CacheTTL: time.Minute, Portals: pr})
	require.NoError(t, err)
	err = db.Tx(f.ctx, func(ctx context.Context) error { return plain.ClearUserRoles(ctx, "back", 1) })
	require.ErrorIs(t, err, ErrUnscopedPortal)
	// 本进程没有注册、库里却有超管角色的端（平台端）：同样不接受
	err = db.Tx(f.ctx, func(ctx context.Context) error { return plain.ClearUserRoles(ctx, "platform", 1) })
	require.ErrorIs(t, err, ErrUnscopedPortal)
	// 本进程没有注册的主体端（平台程序管理主体端的账号）：可以
	require.NoError(t, db.Tx(f.ctx, func(ctx context.Context) error { return plain.ClearUserRoles(ctx, shopP, f.staffA2.UserID) }))
	require.Eventually(t, func() bool { s.InvalidatePolicy(); return !allowed(f.staffA2, "shop:thing:list") }, 2*time.Second, 20*time.Millisecond)
}
