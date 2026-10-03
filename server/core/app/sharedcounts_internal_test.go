package app

// 规范 §13.2 第 165 条（D-076）：共用 Redis 的真实应用实例合计次数。
// 本文件验证计数；验证码跨实例消费另见第 166 条，测试时钟只在验证窗口到期时推进。

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

type sharedCountsModule struct {
	invalidationTestModule
	policy portal.LoginPolicy
}

func (m *sharedCountsModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: testPortal, Users: m.users, Login: m.policy})
}

func newSharedCountsPair(t *testing.T, policy portal.LoginPolicy) (a, b *authFixture) {
	t.Helper()
	gdb, clock, addr, prefix := newInvalidationDB(t)
	users := newMemUsers()
	fixture := func() *authFixture {
		m := &sharedCountsModule{invalidationTestModule: invalidationTestModule{authTestModule{users: users}}, policy: policy}
		app := newInvalidationApp(t, gdb, clock, addr, prefix, testPortal, testSecret, m)
		return &authFixture{t: t, app: app, users: users, clock: clock}
	}
	return fixture(), fixture()
}

func sharedCountsFrom(ip string) reqOpt {
	return func(r *http.Request) { r.RemoteAddr = net.JoinHostPort(ip, "5000") }
}

func sharedCountsLogin(f *authFixture, name, password, ip string, challenge bool) resp {
	f.t.Helper()
	body := gin.H{"username": name, "password": password}
	if challenge {
		cap := f.do(http.MethodGet, "/auth/captcha", nil, sharedCountsFrom(ip))
		require.Equal(f.t, 0, cap.env.Code, cap.rec.Body.String())
		id := cap.data()["captchaId"].(string)
		answer := f.app.captcha.Peek(id)
		require.NotEmpty(f.t, answer)
		body["captchaId"], body["captchaCode"] = id, answer
	}
	return f.do(http.MethodPost, "/auth/login", body, sharedCountsFrom(ip))
}

func TestSharedCounts_165_LoginRateAggregates(t *testing.T) {
	t.Run("来源IP", func(t *testing.T) {
		a, b := newInvalidationPair(t)
		for i := range 20 {
			f := []*authFixture{a, b}[i%2]
			r := sharedCountsLogin(f, fmt.Sprintf("missing-%d", i), "wrong-pass-123", "198.51.100.1", false)
			require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 次: %s", i+1, r.rec.Body.String())
		}
		for _, f := range []*authFixture{a, b} {
			r := sharedCountsLogin(f, "another", "wrong-pass-123", "198.51.100.1", false)
			require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
			require.Equal(t, http.StatusTooManyRequests, r.rec.Code)
		}
		require.Equal(t, httpx.CodeLoginFailed, sharedCountsLogin(b, "other", "wrong-pass-123", "198.51.100.2", false).env.Code)
		a.clock.Advance(time.Minute)
		require.Equal(t, httpx.CodeLoginFailed, sharedCountsLogin(a, "new-window", "wrong-pass-123", "198.51.100.1", false).env.Code)
	})
	t.Run("账号", func(t *testing.T) {
		a, b := newInvalidationPair(t)
		for i := range 10 {
			f := []*authFixture{a, b}[i%2]
			r := sharedCountsLogin(f, "missing", "wrong-pass-123", fmt.Sprintf("198.51.100.%d", i+1), false)
			require.Equal(t, httpx.CodeLoginFailed, r.env.Code)
		}
		// 两个实例合计的第 11 次：不拒绝，改为要验证码（D-103）；哪个实例收到都一样
		for _, f := range []*authFixture{b, a} {
			r := sharedCountsLogin(f, "missing", "wrong-pass-123", "198.51.100.99", false)
			require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code, r.rec.Body.String())
			require.NotEqual(t, http.StatusTooManyRequests, r.rec.Code)
		}
		// 带上验证码就去核对密码
		require.Equal(t, httpx.CodeLoginFailed, sharedCountsLogin(a, "missing", "wrong-pass-123", "198.51.100.99", true).env.Code)
		require.Equal(t, httpx.CodeLoginFailed, sharedCountsLogin(a, "other", "wrong-pass-123", "198.51.100.99", false).env.Code)
	})
}

func TestSharedCounts_165_DistributedFailuresAndPairLock(t *testing.T) {
	a, b := newSharedCountsPair(t, portal.LoginPolicy{IPRatePerMinute: 120, AccountRatePerMinute: 120})
	a.addUser("alice", "alice-pass-123")
	for i := range 3 {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeLoginFailed, f.login("alice", "wrong-pass-123").env.Code)
	}
	for _, f := range []*authFixture{a, b} {
		require.Equal(t, httpx.CodeCaptchaRequired, f.login("alice", "alice-pass-123").env.Code, "两实例合计三次失败")
	}
	for i := 3; i < 10; i++ {
		f := []*authFixture{a, b}[i%2]
		r := sharedCountsLogin(f, "alice", "wrong-pass-123", "203.0.113.10", true)
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 次失败", i+1)
	}
	for _, f := range []*authFixture{a, b} {
		r := f.login("alice", "alice-pass-123")
		require.Equal(t, httpx.CodeLocked, r.env.Code)
		require.NotEmpty(t, r.data()["lockedUntil"])
	}
	require.Equal(t, 0, sharedCountsLogin(b, "alice", "alice-pass-123", "198.51.100.2", false).env.Code, "组合锁定不锁别的来源")
	a.clock.Advance(16 * time.Minute)
	require.Equal(t, 0, a.login("alice", "alice-pass-123").env.Code, "锁定与失败窗口到期后放行")
}

func TestSharedCounts_165_SuccessKeepsAccountFailures(t *testing.T) {
	a, b := newSharedCountsPair(t, portal.LoginPolicy{IPRatePerMinute: 120, AccountRatePerMinute: 120})
	a.addUser("alice", "alice-pass-123")
	for i := range 3 {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeLoginFailed, f.login("alice", "wrong-pass-123").env.Code)
	}
	require.Equal(t, 0, sharedCountsLogin(b, "alice", "alice-pass-123", "203.0.113.10", true).env.Code)
	require.Equal(t, 0, a.login("alice", "alice-pass-123").env.Code, "成功登录清组合失败，另一实例不再要求验证码")
	// 原来的三次账号失败仍在；分散到其他 IP 再失败 47 次，合计 50 次便锁账号。
	for i := range 47 {
		f := []*authFixture{a, b}[i%2]
		r := sharedCountsLogin(f, "alice", "wrong-pass-123", fmt.Sprintf("198.51.100.%d", i+1), false)
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 次跨IP失败", i+1)
	}
	for _, f := range []*authFixture{a, b} {
		require.Equal(t, httpx.CodeLocked, sharedCountsLogin(f, "alice", "alice-pass-123", "198.51.100.200", false).env.Code,
			"成功登录不能清账号跨IP累计")
	}
}

func TestSharedCounts_165_UnlockFailuresShareLoginState(t *testing.T) {
	a, b := newInvalidationPair(t)
	a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	require.Equal(t, 0, a.do(http.MethodPost, "/auth/lock", nil, bearerOpt(access)).env.Code)
	for i := range 3 {
		f := []*authFixture{a, b}[i%2]
		r := f.do(http.MethodPost, "/auth/unlock", gin.H{"password": "wrong-pass-123"}, bearerOpt(access))
		require.Equal(t, httpx.CodeValidation, r.env.Code)
	}
	require.Equal(t, httpx.CodeCaptchaRequired, b.login("alice", "alice-pass-123").env.Code)
	require.Equal(t, 0, b.do(http.MethodPost, "/auth/unlock", gin.H{"password": "alice-pass-123"}, bearerOpt(access)).env.Code)
	require.Equal(t, httpx.CodeCaptchaRequired, a.login("alice", "alice-pass-123").env.Code, "解锁成功不清登录失败")
}

func TestSharedCounts_165_NormalizationAndIsolation(t *testing.T) {
	a, b := newInvalidationPair(t)
	a.addUser("admin", "admin-pass-123")
	variants := []string{"admin", " ADMIN ", "Admin"}
	for i, name := range variants {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeLoginFailed, f.login(name, "wrong-pass-123").env.Code)
	}
	require.Equal(t, httpx.CodeCaptchaRequired, b.login("ADMIN", "admin-pass-123").env.Code)
	cApp := newInvalidationApp(t, a.app.deps.DB, a.clock, redisx.TestAddr(t), redisx.TestPrefix(t), testPortal, testSecret,
		&invalidationTestModule{authTestModule{users: a.users}})
	c := &authFixture{t: t, app: cApp, users: a.users, clock: a.clock}
	require.Equal(t, 0, c.login("admin", "admin-pass-123").env.Code, "另一个前缀没有这三次失败")

	orgs := newMemOrgUsers()
	newShop := func() *orgFixture {
		app := newInvalidationApp(t, a.app.deps.DB, a.clock, redisx.TestAddr(t), a.app.deps.Conf.Redis.KeyPrefix, shopPortal, shopSecret, &orgTestModule{users: orgs})
		return &orgFixture{authFixture: &authFixture{t: t, app: app, clock: a.clock}, orgs: orgs}
	}
	x, y := newShop(), newShop()
	x.seedTwoOrgs()
	require.Equal(t, 0, x.shopLogin("M10000001", "admin", "a-secret-pass-1").env.Code, "同用户名与IP跨端隔离")
	for i := range 3 {
		f := []*orgFixture{x, y}[i%2]
		r := f.shopLogin(" m10000001 ", variants[i], "wrong-pass-123")
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code)
	}
	require.Equal(t, httpx.CodeCaptchaRequired, y.shopLogin("M10000001", "admin", "a-secret-pass-1").env.Code)
	require.Equal(t, 0, y.shopLogin("M10000002", "admin", "b-secret-pass-1").env.Code, "相同端内两个主体的同名账号隔离")
}

func TestSharedCounts_165_IPNormalization(t *testing.T) {
	for _, addresses := range [][]string{{"198.51.100.1", "::ffff:198.51.100.1"}, {"2001:db8:1::1", "2001:db8:1::ffff"}} {
		t.Run(addresses[0], func(t *testing.T) {
			a, b := newInvalidationPair(t)
			for i := range 20 {
				f := []*authFixture{a, b}[i%2]
				r := sharedCountsLogin(f, fmt.Sprintf("missing-%d", i), "wrong-pass-123", addresses[i%2], false)
				require.Equal(t, httpx.CodeLoginFailed, r.env.Code)
			}
			require.Equal(t, httpx.CodeTooManyRequests, sharedCountsLogin(b, "last", "wrong-pass-123", addresses[1], false).env.Code)
			require.Equal(t, httpx.CodeLoginFailed, sharedCountsLogin(a, "other", "wrong-pass-123", "2001:db8:2::1", false).env.Code)
		})
	}
}

func TestSharedCounts_165_CaptchaRate(t *testing.T) {
	a, b := newInvalidationPair(t)
	for i := range 30 {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, 0, f.do(http.MethodGet, "/auth/captcha", nil).env.Code, "第 %d 张", i+1)
	}
	for _, f := range []*authFixture{a, b} {
		require.Equal(t, httpx.CodeTooManyRequests, f.do(http.MethodGet, "/auth/captcha", nil).env.Code, "两实例合计最多30张")
	}
	require.Equal(t, 0, b.do(http.MethodGet, "/auth/captcha", nil, sharedCountsFrom("198.51.100.2")).env.Code)
	a.clock.Advance(time.Minute)
	require.Equal(t, 0, a.do(http.MethodGet, "/auth/captcha", nil).env.Code)
}

func sharedCountsChange(f *authFixture, access, old, next string) resp {
	return f.do(http.MethodPut, "/auth/password", gin.H{"oldPassword": old, "newPassword": next}, bearerOpt(access))
}

func TestSharedCounts_165_OldPasswordTries(t *testing.T) {
	a, b := newInvalidationPair(t)
	a.addUser("alice", "alice-pass-000")
	access, refresh := a.mustLogin("alice", "alice-pass-000")
	for i := range 3 {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeValidation, sharedCountsChange(f, access, "wrong-pass-123", "alice-pass-999").env.Code)
	}
	require.Equal(t, 0, sharedCountsChange(b, access, "alice-pass-000", "alice-pass-001").env.Code)
	for i := range 5 {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeValidation, sharedCountsChange(f, access, "wrong-pass-123", "alice-pass-999").env.Code,
			"成功改密后清零，两实例重新合计五次")
	}
	for _, f := range []*authFixture{a, b} {
		require.Equal(t, httpx.CodeTooManyRequests, sharedCountsChange(f, access, "alice-pass-001", "alice-pass-002").env.Code)
	}
	a.clock.Advance(15*time.Minute + time.Second)
	r := b.do(http.MethodPost, "/auth/refresh", nil, cookieOpt(refresh), webClient())
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	access = r.data()["accessToken"].(string)
	require.Equal(t, 0, sharedCountsChange(a, access, "alice-pass-001", "alice-pass-002").env.Code, "同一会话窗口到期后重新计")
}

func TestSharedCounts_165_SuccessfulPasswordChanges(t *testing.T) {
	a, b := newInvalidationPair(t)
	id := a.addUser("alice", "alice-pass-000")
	a.addUser("bob", "bob-pass-000")
	access, _ := a.mustLogin("alice", "alice-pass-000")
	current := "alice-pass-000"
	for i := 1; i <= 5; i++ {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeValidation, sharedCountsChange(f, access, "wrong-pass-123", "alice-pass-999").env.Code)
		require.Equal(t, httpx.CodeValidation, sharedCountsChange(f, access, current, "short").env.Code)
		next := fmt.Sprintf("alice-pass-%03d", i)
		require.Equal(t, 0, sharedCountsChange(f, access, current, next).env.Code, "第 %d 次成功，失败不占成功次数", i)
		current = next
	}
	for _, f := range []*authFixture{a, b} {
		require.Equal(t, httpx.CodeTooManyRequests, sharedCountsChange(f, access, current, "alice-pass-006").env.Code)
	}
	otherSession, _ := b.mustLogin("alice", current)
	require.Equal(t, httpx.CodeTooManyRequests, sharedCountsChange(a, otherSession, current, "alice-pass-006").env.Code, "换会话也按账号计")
	bob, _ := b.mustLogin("bob", "bob-pass-000")
	require.Equal(t, 0, sharedCountsChange(a, bob, "bob-pass-000", "bob-pass-001").env.Code, "别的账号不受影响")
	// 连续被账号配额挡回来的请求必须退掉旧密码机会，否则强制改密会被误挡。
	for i := range 6 {
		f := []*authFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeTooManyRequests, sharedCountsChange(f, otherSession, current, "alice-pass-006").env.Code)
	}
	a.users.set(id, func(acc *portal.Account) { acc.MustChangePwd = true })
	require.Equal(t, 0, sharedCountsChange(b, otherSession, current, "alice-pass-900").env.Code, "被要求改密的那一次免计且放行")
	require.Equal(t, httpx.CodeTooManyRequests, sharedCountsChange(a, otherSession, "alice-pass-900", "alice-pass-901").env.Code, "免计后原成功次数仍在")
}

func TestSharedCounts_165_CounterOutageAndRecovery(t *testing.T) {
	for _, failure := range []string{"Cut", "Freeze"} {
		t.Run(failure, func(t *testing.T) {
			gdb, clock, addr, prefix := newInvalidationDB(t)
			proxy := redisx.NewTestProxy(t, addr)
			users := newMemUsers()
			fixture := func(via string) *authFixture {
				app := newInvalidationApp(t, gdb, clock, via, prefix, testPortal, testSecret, &invalidationTestModule{authTestModule{users: users}})
				return &authFixture{t: t, app: app, users: users, clock: clock}
			}
			a, b := fixture(proxy.Addr()), fixture(addr)
			for i := range 30 {
				f := []*authFixture{a, b}[i%2]
				require.Equal(t, 0, f.do(http.MethodGet, "/auth/captcha", nil).env.Code)
			}
			var queries atomic.Int64
			require.NoError(t, gdb.Callback().Query().After("gorm:query").Register("sharedcounts:outage-queries", func(*gorm.DB) { queries.Add(1) }))
			t.Cleanup(func() { _ = gdb.Callback().Query().Remove("sharedcounts:outage-queries") })
			if failure == "Cut" {
				proxy.Cut()
			} else {
				proxy.Freeze()
			}
			started := time.Now()
			r := a.do(http.MethodGet, "/auth/captcha", nil)
			require.Equal(t, 0, r.env.Code, "Redis故障退回本实例已有15次计数")
			require.Less(t, time.Since(started), 1500*time.Millisecond, "故障不让请求长时间等待")
			require.Zero(t, queries.Load(), "计数回退不查数据库")
			if failure == "Cut" {
				require.Eventually(t, func() bool { return !a.app.redis.Available() }, 2*time.Second, 10*time.Millisecond)
				for range 14 {
					require.Equal(t, 0, a.do(http.MethodGet, "/auth/captcha", nil).env.Code)
				}
				require.Equal(t, httpx.CodeTooManyRequests, a.do(http.MethodGet, "/auth/captcha", nil).env.Code, "影子计数保留健康期间用过的次数")
				require.Zero(t, queries.Load(), "不可用期间的新请求不增加数据库查询")
				proxy.Restore()
			}
			require.Equal(t, httpx.CodeTooManyRequests, b.do(http.MethodGet, "/auth/captcha", nil).env.Code, "另一实例仍用原共享计数")
			require.Eventually(t, func() bool { return a.app.redis.Available() }, 8*time.Second, 20*time.Millisecond)
			require.Equal(t, httpx.CodeTooManyRequests, a.do(http.MethodGet, "/auth/captcha", nil).env.Code, "恢复不能清掉已用满的共享配额")
			clock.Advance(time.Minute)
			require.Equal(t, 0, a.do(http.MethodGet, "/auth/captcha", nil).env.Code)
		})
	}
}

type sharedCountsOrgModule struct{ merchantTestModule }

func (sharedCountsOrgModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{
		Code: org.Merchant().Portal(), Users: d.Orgs.Users(org.Merchant()), Scoped: true,
		Login: portal.LoginPolicy{IPRatePerMinute: 120, AccountRatePerMinute: 120},
	})
}

// 事务回归使用真实主体账号来源，密码写入和会话吊销都会加入请求携带的外层事务。
func newSharedCountsOrgPair(t *testing.T) (a, b *orgSvcFixture, created *org.Created) {
	t.Helper()
	gdb, clock, addr, prefix := newInvalidationDB(t)
	fixture := func() *orgSvcFixture {
		app := newInvalidationApp(t, gdb, clock, addr, prefix, org.Merchant().Portal(), merchantSecret, sharedCountsOrgModule{})
		return &orgSvcFixture{t: t, app: app, clock: clock, ctx: app.Context(context.Background())}
	}
	a, b = fixture(), fixture()
	password, err := a.app.Deps().Orgs.NewInitialPassword()
	require.NoError(t, err)
	created, err = a.app.Deps().Orgs.Create(a.ctx, org.Merchant(), org.CreateInput{Name: "事务测试商户", OwnerUsername: "admin"}, password, 1)
	require.NoError(t, err)
	return a, b, created
}

func sharedCountsContext(ctx context.Context) reqOpt {
	return func(r *http.Request) { *r = *r.WithContext(ctx) }
}

func sharedCountsOrgLogin(f *orgSvcFixture, code, password string, challenge bool, opts ...reqOpt) resp {
	f.t.Helper()
	body := gin.H{"org": code, "username": "admin", "password": password}
	if challenge {
		cap := f.do(http.MethodGet, "/auth/captcha", nil)
		require.Equal(f.t, 0, cap.env.Code, cap.rec.Body.String())
		id := cap.data()["captchaId"].(string)
		answer := f.app.captcha.Peek(id)
		require.NotEmpty(f.t, answer)
		body["captchaId"], body["captchaCode"] = id, answer
	}
	return f.do(http.MethodPost, "/auth/login", body, opts...)
}

func sharedCountsOrgChange(f *orgSvcFixture, access, old, next string, opts ...reqOpt) resp {
	return f.do(http.MethodPut, "/auth/password", gin.H{"oldPassword": old, "newPassword": next}, append([]reqOpt{bearerOpt(access)}, opts...)...)
}

func sharedCountsSessions(t *testing.T, f *orgSvcFixture, ctx context.Context, want int64) {
	t.Helper()
	count, err := f.app.Deps().Auth.CountActiveSessions(ctx, org.Merchant().Portal())
	require.NoError(t, err)
	require.Equal(t, want, count)
}

func TestSharedCounts_165_LoginWaitsForOuterTransaction(t *testing.T) {
	a, b, owner := newSharedCountsOrgPair(t)
	for i := range 3 {
		f := []*orgSvcFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeLoginFailed, f.login(owner.Org.Code, "admin", "wrong-pass-123").env.Code)
	}
	require.Equal(t, httpx.CodeCaptchaRequired, b.login(owner.Org.Code, "admin", owner.Password).env.Code)
	rollback := errors.New("roll back the login transaction")
	err := db.Tx(a.ctx, func(tx context.Context) error {
		r := sharedCountsOrgLogin(a, owner.Org.Code, owner.Password, true, sharedCountsContext(tx))
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		sharedCountsSessions(t, a, tx, 1)
		sharedCountsSessions(t, b, b.ctx, 0)
		require.Equal(t, httpx.CodeCaptchaRequired, b.login(owner.Org.Code, "admin", owner.Password).env.Code,
			"未提交的成功登录不能清掉远端组合失败")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	sharedCountsSessions(t, b, b.ctx, 0)
	require.Equal(t, httpx.CodeCaptchaRequired, b.login(owner.Org.Code, "admin", owner.Password).env.Code,
		"建会话回滚后共享失败保持原值")
	require.NoError(t, db.Tx(a.ctx, func(tx context.Context) error {
		r := sharedCountsOrgLogin(a, owner.Org.Code, owner.Password, true, sharedCountsContext(tx))
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		require.Equal(t, httpx.CodeCaptchaRequired, b.login(owner.Org.Code, "admin", owner.Password).env.Code)
		return nil
	}))
	sharedCountsSessions(t, b, b.ctx, 1)
	require.Equal(t, 0, b.login(owner.Org.Code, "admin", owner.Password).env.Code,
		"外层提交才清组合失败，handler 的 defer 不能提前结束成功票据")
	sharedCountsSessions(t, b, b.ctx, 2)
}

func TestSharedCounts_165_PasswordCountersWaitForOuterTransaction(t *testing.T) {
	a, b, owner := newSharedCountsOrgPair(t)
	access, _, _ := a.mustLogin(owner.Org.Code, "admin", owner.Password)
	current := "owner-pass-000"
	require.Equal(t, 0, sharedCountsOrgChange(a, access, owner.Password, current).env.Code, "首次强制改密免计")
	other, _, must := b.mustLogin(owner.Org.Code, "admin", current)
	require.False(t, must)
	sharedCountsSessions(t, b, b.ctx, 2)
	provider := a.app.Deps().Orgs.Users(org.Merchant())
	passwordIs := func(ctx context.Context, want string) {
		t.Helper()
		account, err := provider.FindByID(ctx, owner.OwnerID)
		require.NoError(t, err)
		require.True(t, a.app.hasher.Verify(account.PasswordHash, want), "真实账号密码应对应 %q", want)
	}
	for i := range 4 {
		f := []*orgSvcFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeValidation, sharedCountsOrgChange(f, access, "wrong-pass-123", "owner-pass-999").env.Code)
	}
	rollback := errors.New("roll back the password transaction")
	err := db.Tx(a.ctx, func(tx context.Context) error {
		r := sharedCountsOrgChange(a, access, current, "owner-pass-rollback-1", sharedCountsContext(tx))
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		passwordIs(tx, "owner-pass-rollback-1")
		passwordIs(b.ctx, current)
		sharedCountsSessions(t, a, tx, 1)
		sharedCountsSessions(t, b, b.ctx, 2)
		for _, f := range []*orgSvcFixture{a, b} {
			require.Equal(t, httpx.CodeTooManyRequests, sharedCountsOrgChange(f, access, "wrong-pass-123", "owner-pass-999").env.Code,
				"第五次旧密码核对尚未提交，不能提前清零")
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	passwordIs(b.ctx, current)
	sharedCountsSessions(t, b, b.ctx, 2)
	for _, f := range []*orgSvcFixture{a, b} {
		require.Equal(t, httpx.CodeTooManyRequests, sharedCountsOrgChange(f, access, current, "owner-pass-999").env.Code,
			"写入回滚后旧密码的五次核对仍保留")
	}
	// 换成另一会话继续。回滚不占成功次数，但实际核对过的旧密码机会不退款。
	for i := range 4 {
		f := []*orgSvcFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeValidation, sharedCountsOrgChange(f, other, "wrong-pass-123", "owner-pass-999").env.Code)
	}
	require.NoError(t, db.Tx(b.ctx, func(tx context.Context) error {
		r := sharedCountsOrgChange(b, other, current, "owner-pass-001", sharedCountsContext(tx))
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		require.Equal(t, httpx.CodeTooManyRequests, sharedCountsOrgChange(a, other, "wrong-pass-123", "owner-pass-999").env.Code,
			"真实提交前另一实例仍看到第五次核对")
		return nil
	}))
	current = "owner-pass-001"
	passwordIs(a.ctx, current)
	sharedCountsSessions(t, a, a.ctx, 1)
	for i := 2; i <= 5; i++ {
		f := []*orgSvcFixture{a, b}[i%2]
		require.Equal(t, httpx.CodeValidation, sharedCountsOrgChange(f, other, "wrong-pass-123", "owner-pass-999").env.Code,
			"成功提交清掉共享旧密码核对次数")
		next := fmt.Sprintf("owner-pass-%03d", i)
		require.Equal(t, 0, sharedCountsOrgChange(f, other, current, next).env.Code, "回滚退款后仍有完整五次成功配额")
		current = next
	}
	for _, f := range []*orgSvcFixture{a, b} {
		require.Equal(t, httpx.CodeTooManyRequests, sharedCountsOrgChange(f, other, current, "owner-pass-006").env.Code,
			"五次真实提交耗尽账号成功次数")
	}
}
