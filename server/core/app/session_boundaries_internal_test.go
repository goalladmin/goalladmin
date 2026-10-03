package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// 规范 §13.2 第 181 条（D-097）：会话号只认规范写法；状态缓存续期不延长有效期；刷新先核对凭证再查账号和 IP 名单。

// loginWithLetterSID 登录并返回访问令牌、刷新 Cookie 和会话号；会话号里要有字母（大写写法才和原来的不同）。
func (f *authFixture) loginWithLetterSID(username, password string) (access, cookie, sid string) {
	f.t.Helper()
	for i := 0; i < 20; i++ {
		access, cookie = f.mustLogin(username, password)
		sid, _, _ = strings.Cut(cookie, ".")
		if strings.ToUpper(sid) != sid {
			return access, cookie, sid
		}
	}
	f.t.Fatal("连续 20 个会话号都没有字母")
	return "", "", ""
}

func TestAuth_181_NonCanonicalSessionIDIsNotFound(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, _, sid := f.loginWithLetterSID("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(access).rec.Code) // 状态进了缓存
	ctx := f.testCtx()
	svc := f.app.deps.Auth

	// 大写、带空白、全角的写法：库里按不区分大小写能匹配到这一行，但一律当作不存在，会话不动
	for _, alias := range []string{strings.ToUpper(sid), sid[:31] + strings.ToUpper(sid[31:]) + " ", strings.Repeat("ａ", 32)} {
		err := svc.RevokeSession(ctx, testPortal, alias, auth.RevokeAdmin)
		require.True(t, errors.Is(err, httpx.ErrNotFound), "%q: %v", alias, err)
		_, err = svc.SessionOwner(ctx, testPortal, alias)
		require.True(t, errors.Is(err, httpx.ErrNotFound), "%q: %v", alias, err)
		// 事务里锁会话行的入口同样不认
		sm := f.app.authenticators[testPortal].Sessions()
		err = db.Tx(ctx, func(ctx context.Context) error { return sm.LockActive(ctx, testPortal, alias) })
		require.ErrorIs(t, err, session.ErrNotFound, alias)
	}
	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
		return f.app.authenticators[testPortal].Sessions().LockActive(ctx, testPortal, sid)
	}))
	var revoked int64
	require.NoError(t, f.app.deps.DB.Raw("SELECT COUNT(*) FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", sid).Scan(&revoked).Error)
	require.Zero(t, revoked, "别的写法不能吊销会话")
	require.Equal(t, 200, f.ping(access).rec.Code)

	// 规范写法：吊销成功，缓存同时清掉，下一个请求立即 401（不等缓存到期）
	owner, err := svc.SessionOwner(ctx, testPortal, sid)
	require.NoError(t, err)
	require.NotZero(t, owner)
	require.NoError(t, svc.RevokeSession(ctx, testPortal, sid, auth.RevokeAdmin))
	require.Equal(t, 401, f.ping(access).rec.Code)
}

func TestAuth_181_StatusCacheIsNotExtendedByTouch(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	sid, _, _ := strings.Cut(cookie, ".")

	f.clock.Advance(50 * time.Second)
	require.Equal(t, 200, f.ping(access).rec.Code) // 读库、进缓存（15 秒），距上次活动不到一分钟，不回写

	// 别的程序直接在库里吊销了这个会话：本进程收不到失效通知
	require.NoError(t, f.app.deps.DB.Exec("UPDATE ga_session SET revoked_at = ?, revoke_reason = 'admin' WHERE sid = ?", f.clock.Now().UTC(), sid).Error)

	f.clock.Advance(12 * time.Second)
	require.Equal(t, 200, f.ping(access).rec.Code, "缓存周期之内仍按旧状态（规范 §5.3）；这一次距上次活动超过一分钟，会回写最后活动时间")

	f.clock.Advance(4 * time.Second) // 距进缓存 16 秒
	require.Equal(t, 401, f.ping(access).rec.Code, "回写最后活动时间不能让旧状态多活一个缓存周期")
}

func TestAuth_181_RefreshChecksCredentialBeforeAccountAndIPRules(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "correct-horse-9")
	_, cookie := f.mustLogin("alice", "correct-horse-9")
	sid, _, _ := strings.Cut(cookie, ".")
	_, err := f.app.deps.IPACL.SetAllow(f.testCtx(), ipacl.UserTarget(testPortal, 0, id), []ipacl.Entry{{CIDR: "203.0.113.10"}}, 1, "")
	require.NoError(t, err)
	out := fromIP("198.51.100.1")
	forged := sid + "." + strings.Repeat("0", 64)

	// 只知道会话号、凭证是编的：名单外的来源得到的是 401，不是"IP 不允许"；不留下带着这个账号的 ip_denied 记录
	r := f.refresh(forged, out)
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeTokenInvalid, r.env.Code)
	require.Zero(t, f.countSecurity("ip_denied"))
	require.EqualValues(t, 1, f.countSecurity("refresh_mismatch"))
	// 名单内的来源拿编的凭证，回答一样
	r = f.refresh(forged)
	require.Equal(t, 401, r.rec.Code)
	require.Equal(t, httpx.CodeTokenInvalid, r.env.Code)

	// 账号停用之后，编的凭证得到的回答不变，会话也不因此被吊销（停用本身已经吊销过的另算，这里直接改库）
	f.users.set(id, func(a *portal.Account) { a.Status = 0 })
	r = f.refresh(forged, out)
	require.Equal(t, 401, r.rec.Code)
	require.Equal(t, httpx.CodeTokenInvalid, r.env.Code)
	var revoked int64
	require.NoError(t, f.app.deps.DB.Raw("SELECT COUNT(*) FROM ga_session WHERE sid = ? AND revoked_at IS NOT NULL", sid).Scan(&revoked).Error)
	require.Zero(t, revoked)
	f.users.set(id, func(a *portal.Account) { a.Status = 1 })

	// 真凭证：名单外照旧回 2003、不轮换；名单内照常刷新
	requireIPDenied(t, f.refresh(cookie, out))
	require.EqualValues(t, 1, f.countSecurity("ip_denied"))
	ok := f.refresh(cookie)
	require.Equal(t, 200, ok.rec.Code, ok.rec.Body.String())
	require.Equal(t, 0, ok.env.Code)
}
