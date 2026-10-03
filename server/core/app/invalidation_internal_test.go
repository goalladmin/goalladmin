package app

// 规范 §13.2 第 164 条（D-075）：两个应用共用临时 MySQL 库和 Redis 通道，
// 先把状态读进各自缓存，再经真实服务入口改动。时钟不推进，传播不能靠 TTL 通过。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/dict"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

const invalidationPerm = "invalidationtest:data:read"

type invalidationTestModule struct{ authTestModule }

func (*invalidationTestModule) Name() string { return "invalidationtest" }
func (*invalidationTestModule) Perms() []rbac.Perm {
	return []rbac.Perm{{Code: invalidationPerm, Portal: testPortal, Name: "Read"}}
}
func (*invalidationTestModule) Menus() []rbac.MenuNode {
	return []rbac.MenuNode{{
		Name: "invalidation-page", Portal: testPortal, Path: "/invalidation", Component: "invalidation/index",
		TitleKey: "menu.invalidation", Perm: invalidationPerm,
	}}
}
func (m *invalidationTestModule) Routes(r *Router) {
	m.authTestModule.Routes(r)
	r.Portal(testPortal).GET("/cached", rbac.AuthOnly(), func(c *gin.Context) {
		p := auth.MustFromCtx(c.Request.Context())
		httpx.OK(c, gin.H{"displayName": p.DisplayName, "super": p.Super})
	})
	r.Portal(testPortal).GET("/protected", rbac.Require(invalidationPerm), func(c *gin.Context) {
		httpx.OK(c, nil)
	})
}

func newInvalidationDB(t *testing.T) (*gorm.DB, *fakeClock, string, string) {
	t.Helper()
	addr := redisx.TestAddr(t)
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	return gdb, clock, addr, redisx.TestPrefix(t)
}

func newInvalidationApp(t *testing.T, gdb *gorm.DB, clock *fakeClock, addr, prefix, code, secret string, module Module) *App {
	t.Helper()
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.AllowedOrigins = []string{testOrigin}
	cfg.Redis.Addr, cfg.Redis.KeyPrefix = addr, prefix
	cfg.Portals[code] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: secret}
	a, err := New(cfg, WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithClock(clock.Now), WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
	a.Register(module)
	require.NoError(t, a.Setup())
	require.Eventually(t, func() bool { return a.invalidationReady.Load() }, 5*time.Second, 10*time.Millisecond, "等待订阅确认")
	return a
}

func newInvalidationPair(t *testing.T) (a, b *authFixture) {
	t.Helper()
	gdb, clock, addr, prefix := newInvalidationDB(t)
	users := newMemUsers()
	newFixture := func() *authFixture {
		app := newInvalidationApp(t, gdb, clock, addr, prefix, testPortal, testSecret, &invalidationTestModule{authTestModule{users: users}})
		return &authFixture{t: t, app: app, users: users, clock: clock}
	}
	a, b = newFixture(), newFixture()
	require.NotEmpty(t, a.app.invalidationSource)
	require.NotEqual(t, a.app.invalidationSource, b.app.invalidationSource, "实例标识各自生成")
	return a, b
}

func eventuallyInvalidationCode(t *testing.T, want int, call func() resp) {
	t.Helper()
	var last resp
	require.Eventually(t, func() bool {
		last = call()
		return last.env.Code == want
	}, 2*time.Second, 10*time.Millisecond, "通知后响应码应为 %d", want)
	require.Equal(t, want, last.env.Code, last.rec.Body.String())
}

func TestInvalidation_164_SessionCommitAndRollback(t *testing.T) {
	a, b := newInvalidationPair(t)
	id := a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	require.Equal(t, 0, a.ping(access).env.Code, "预热接收实例的会话缓存")
	sid := a.ping(access).data()["sid"].(string)
	ctx := b.app.Context(context.Background())
	rollback := errors.New("rollback the test transaction")
	var sessionReads atomic.Int64
	require.NoError(t, a.app.Deps().DB.Callback().Query().After("gorm:query").Register("invalidation:session-reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "ga_session" {
			sessionReads.Add(1)
		}
	}))
	t.Cleanup(func() { _ = a.app.Deps().DB.Callback().Query().Remove("invalidation:session-reads") })
	barrier := func(display string) {
		// 在同一发送端同步发布一个账号通知，等它被收到。之前若误发过会话消息，这时也已经被处理。
		a.users.set(id, func(acc *portal.Account) { acc.DisplayName = display })
		b.app.Deps().Auth.ForgetAccount(testPortal, id)
		require.Eventually(t, func() bool {
			return a.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"] == display
		}, 2*time.Second, 10*time.Millisecond)
	}

	err := db.Tx(ctx, func(tx context.Context) error {
		require.NoError(t, b.app.Deps().Auth.RevokeSession(tx, testPortal, sid, auth.RevokeAdmin))
		barrier("before rollback")
		require.Equal(t, 0, a.ping(access).env.Code, "提交前不发布、不清远端缓存")
		require.Zero(t, sessionReads.Load(), "提交前不能清掉 A 的会话缓存")
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	barrier("after rollback")
	require.Equal(t, 0, a.ping(access).env.Code, "回滚保留原会话")
	require.Zero(t, sessionReads.Load(), "回滚也不清掉 A 的会话缓存")
	require.NoError(t, db.Tx(ctx, func(tx context.Context) error {
		require.NoError(t, b.app.Deps().Auth.RevokeSession(tx, testPortal, sid, auth.RevokeAdmin))
		require.Equal(t, 0, a.ping(access).env.Code, "第二次仍在事务里")
		return nil
	}))
	eventuallyInvalidationCode(t, httpx.CodeTokenInvalid, func() resp { return a.ping(access) })
}

func TestInvalidation_164_LockUnlockAndPassword(t *testing.T) {
	a, b := newInvalidationPair(t)
	a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	other, _ := a.mustLogin("alice", "alice-pass-123")
	require.Equal(t, 0, a.ping(access).env.Code)
	require.Equal(t, 0, a.ping(other).env.Code)
	require.Equal(t, 0, b.do(http.MethodPost, "/auth/lock", nil, bearerOpt(access)).env.Code)
	eventuallyInvalidationCode(t, httpx.CodeSessionLocked, func() resp { return a.ping(access) })
	require.Equal(t, 0, a.ping(other).env.Code, "锁屏只影响指定会话")
	require.Equal(t, 0, b.do(http.MethodPost, "/auth/unlock", gin.H{"password": "alice-pass-123"}, bearerOpt(access)).env.Code)
	eventuallyInvalidationCode(t, 0, func() resp { return a.ping(access) })
	require.Equal(t, 0, b.do(http.MethodPut, "/auth/password", gin.H{
		"oldPassword": "alice-pass-123", "newPassword": "alice-new-pass-456",
	}, bearerOpt(access)).env.Code)
	eventuallyInvalidationCode(t, httpx.CodeTokenInvalid, func() resp { return a.ping(other) })
	require.Equal(t, 0, a.ping(access).env.Code, "本人改密保留当前会话")
}

func TestInvalidation_164_AccountAndPrefixIsolation(t *testing.T) {
	a, b := newInvalidationPair(t)
	id := a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	// 第三个实例共用数据库，但属于另一个 Redis 前缀。它的缓存只能靠自己的 TTL 更新。
	cApp := newInvalidationApp(t, a.app.Deps().DB, a.clock, redisx.TestAddr(t), redisx.TestPrefix(t), testPortal, testSecret,
		&invalidationTestModule{authTestModule{users: a.users}})
	c := &authFixture{t: t, app: cApp, users: a.users, clock: a.clock}
	require.Equal(t, "alice", a.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"])
	require.Equal(t, "alice", c.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"])
	a.users.set(id, func(acc *portal.Account) { acc.DisplayName = "新的显示名" })
	b.app.Deps().Auth.ForgetAccount(testPortal, id)
	require.Eventually(t, func() bool {
		return a.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"] == "新的显示名"
	}, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, "alice", c.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"], "另一通道没有收到通知")
	a.users.set(id, func(acc *portal.Account) { acc.Status = 0 })
	b.app.Deps().Auth.ForgetAccount(testPortal, id)
	eventuallyInvalidationCode(t, httpx.CodeTokenInvalid, func() resp { return a.ping(access) })
	require.Equal(t, 0, c.ping(access).env.Code, "另一通道仍沿用未过期的账号状态")
}

type invalidationReadUsers struct {
	*memUsers
	userID  atomic.Uint64
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (u *invalidationReadUsers) FindByID(ctx context.Context, id uint64) (*portal.Account, error) {
	acc, err := u.memUsers.FindByID(ctx, id)
	if err == nil && id == u.userID.Load() && u.armed.CompareAndSwap(true, false) {
		close(u.entered)
		select {
		case <-u.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return acc, err
}

type invalidationReadModule struct {
	invalidationTestModule
	users *invalidationReadUsers
}

func (m *invalidationReadModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: testPortal, Users: m.users})
}

func TestInvalidation_164_AccountReadInFlight(t *testing.T) {
	gdb, clock, addr, prefix := newInvalidationDB(t)
	users := newMemUsers()
	blocked := &invalidationReadUsers{memUsers: users, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(blocked.release) }) }
	aApp := newInvalidationApp(t, gdb, clock, addr, prefix, testPortal, testSecret, &invalidationReadModule{
		invalidationTestModule: invalidationTestModule{authTestModule{users: users}}, users: blocked,
	})
	bApp := newInvalidationApp(t, gdb, clock, addr, prefix, testPortal, testSecret, &invalidationTestModule{authTestModule{users: users}})
	t.Cleanup(release)
	a := &authFixture{t: t, app: aApp, users: users, clock: clock}
	uid := a.addUser("alice", "alice-pass-123")
	markerID := a.addUser("marker", "marker-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	markerAccess, _ := a.mustLogin("marker", "marker-pass-123")
	require.Equal(t, "alice", a.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"])
	require.Equal(t, "marker", a.do(http.MethodGet, "/cached", nil, bearerOpt(markerAccess)).data()["displayName"])
	blocked.userID.Store(uid)
	blocked.armed.Store(true)
	aApp.authenticators[testPortal].InvalidateAccount(uid)
	done := make(chan resp, 1)
	go func() { done <- a.do(http.MethodGet, "/cached", nil, bearerOpt(access)) }()
	select {
	case <-blocked.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("账号查询没有进入预期的窗口")
	}
	users.set(uid, func(acc *portal.Account) { acc.DisplayName = "新的显示名" })
	bApp.Deps().Auth.ForgetAccount(testPortal, uid)
	// 第二个账号的消息是同通道屏障：确认第一个账号的失效消息已在旧读返回之前到达。
	users.set(markerID, func(acc *portal.Account) { acc.DisplayName = "消息已到达" })
	bApp.Deps().Auth.ForgetAccount(testPortal, markerID)
	require.Eventually(t, func() bool {
		return a.do(http.MethodGet, "/cached", nil, bearerOpt(markerAccess)).data()["displayName"] == "消息已到达"
	}, 2*time.Second, 10*time.Millisecond)
	release()
	select {
	case first := <-done:
		require.Equal(t, 0, first.env.Code)
		require.Equal(t, "alice", first.data()["displayName"], "已经读出的旧状态只作用于这个在途请求")
	case <-time.After(2 * time.Second):
		t.Fatal("在途请求没有结束")
	}
	require.Equal(t, "新的显示名", a.do(http.MethodGet, "/cached", nil, bearerOpt(access)).data()["displayName"],
		"收到通知之后，旧查询结果不能重新填进缓存")
}

func TestInvalidation_164_PolicyMenuDictAndIP(t *testing.T) {
	a, b := newInvalidationPair(t)
	uid := a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	ctx := b.app.Context(context.Background())
	cli := auth.Principal{Portal: testPortal, Super: true}

	role, err := b.app.Deps().RBAC.CreateRole(ctx, cli, testPortal, rbac.RoleInput{Code: "reader", Name: "Reader", Status: 1})
	require.NoError(t, err)
	require.NoError(t, b.app.Deps().RBAC.GrantRolePerms(ctx, cli, testPortal, role.ID, []string{invalidationPerm}))
	require.NoError(t, b.app.Deps().RBAC.AssignUserRoles(ctx, cli, testPortal, uid, []uint64{role.ID}))
	eventuallyInvalidationCode(t, 0, func() resp { return a.do(http.MethodGet, "/protected", nil, bearerOpt(access)) })
	require.NoError(t, b.app.Deps().RBAC.GrantRolePerms(ctx, cli, testPortal, role.ID, nil))
	eventuallyInvalidationCode(t, httpx.CodeForbidden, func() resp { return a.do(http.MethodGet, "/protected", nil, bearerOpt(access)) })

	menus, err := a.app.Deps().RBAC.MenuAdmin(a.app.Context(context.Background()), cli)
	require.NoError(t, err)
	require.Len(t, menus, 1)
	require.Empty(t, menus[0].Titles)
	require.NoError(t, b.app.Deps().RBAC.UpdateMenu(ctx, cli, "invalidation-page", rbac.MenuDisplayInput{Titles: map[string]string{"zh-CN": "新名称"}}))
	require.Eventually(t, func() bool {
		menus, err = a.app.Deps().RBAC.MenuAdmin(a.app.Context(context.Background()), cli)
		return err == nil && len(menus) == 1 && menus[0].Titles["zh-CN"] == "新名称"
	}, 2*time.Second, 10*time.Millisecond)

	d, err := b.app.Deps().Dict.CreateDict(ctx, dict.DictInput{Code: "invalidation", Portal: testPortal, Name: "测试字典"})
	require.NoError(t, err)
	item, err := b.app.Deps().Dict.CreateItem(ctx, d.ID, dict.ItemInput{Value: "a", Label: "旧名称"})
	require.NoError(t, err)
	view, err := a.app.Deps().Dict.Get(a.app.Context(context.Background()), testPortal, d.Code, dict.DefaultLang)
	require.NoError(t, err)
	require.Len(t, view.Items, 1)
	require.Equal(t, "旧名称", view.Items[0].Label)
	_, err = b.app.Deps().Dict.UpdateItem(ctx, d.ID, item.ID, dict.ItemUpdate{Value: "a", Label: "新名称"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		view, err = a.app.Deps().Dict.Get(a.app.Context(context.Background()), testPortal, d.Code, dict.DefaultLang)
		return err == nil && len(view.Items) == 1 && view.Items[0].Label == "新名称"
	}, 2*time.Second, 10*time.Millisecond)

	require.Equal(t, 0, a.ping(access).env.Code, "预热 IP 名单")
	deny, err := b.app.Deps().IPACL.AddDeny(ctx, ipacl.DenyInput{CIDR: "203.0.113.10"}, 0, "")
	require.NoError(t, err)
	eventuallyInvalidationCode(t, httpx.CodeIPDenied, func() resp { return a.ping(access) })
	_, err = b.app.Deps().IPACL.Remove(ctx, deny.ID)
	require.NoError(t, err)
	eventuallyInvalidationCode(t, 0, func() resp { return a.ping(access) })
}

func TestInvalidation_164_CrossProgramOrg(t *testing.T) {
	gdb, clock, addr, prefix := newInvalidationDB(t)
	platform := newInvalidationApp(t, gdb, clock, addr, prefix, testPortal, testSecret, &authTestModule{users: newMemUsers()})
	merchant := newInvalidationApp(t, gdb, clock, addr, prefix, org.Merchant().Portal(), merchantSecret, merchantTestModule{})
	f := &orgSvcFixture{t: t, app: merchant, clock: clock, ctx: merchant.Context(context.Background())}
	ctx, orgs := platform.Context(context.Background()), platform.Deps().Orgs
	require.NotContains(t, platform.authenticators, org.Merchant().Portal(), "平台程序没有主体端的认证器")
	create := func(name string) *org.Created {
		pwd, err := orgs.NewInitialPassword()
		require.NoError(t, err)
		m, err := orgs.Create(ctx, org.Merchant(), org.CreateInput{Name: name, OwnerUsername: "admin"}, pwd, 1)
		require.NoError(t, err)
		// 测试从已完成首次改密的状态开始，不依赖待验证的改密通知。
		require.NoError(t, gdb.Exec("UPDATE ga_merchant_user SET must_change_pwd = 0 WHERE id = ?", m.OwnerID).Error)
		return m
	}
	m, other := create("商户甲"), create("商户乙")
	access, _, _ := f.mustLogin(m.Org.Code, "admin", m.Password)
	otherAccess, _, _ := f.mustLogin(other.Org.Code, "admin", other.Password)
	require.Equal(t, true, f.whoami(access).data()["super"], "预热主账号状态")
	require.Equal(t, 0, f.whoami(otherAccess).env.Code)
	staffHash, err := merchant.hasher.Hash("staff-pass-123")
	require.NoError(t, err)
	staff, err := orgs.CreateMember(ctx, org.Merchant(), m.Org.ID, org.MemberInput{Username: "staff", DisplayName: "Staff"}, staffHash, 0)
	require.NoError(t, err)
	require.NoError(t, gdb.Exec("UPDATE ga_merchant_user SET must_change_pwd = 0 WHERE id = ?", staff.ID).Error)
	staffAccess, _, _ := f.mustLogin(m.Org.Code, "staff", "staff-pass-123")
	require.Equal(t, false, f.whoami(staffAccess).data()["super"], "预热普通账号状态")
	require.NoError(t, orgs.ChangeOwner(ctx, org.Merchant(), m.Org.ID, staff.ID, 1))
	// 换下来的主账号：会话随更换一起吊销（D-101）；换上来的：已缓存的状态跟着变成主账号
	eventuallyInvalidationCode(t, httpx.CodeTokenInvalid, func() resp { return f.whoami(access) })
	require.Eventually(t, func() bool {
		next := f.whoami(staffAccess)
		return next.env.Code == 0 && next.data()["super"] == true
	}, 2*time.Second, 10*time.Millisecond, "主账号更换传播到已缓存的主体端")
	access, _, _ = f.mustLogin(m.Org.Code, "admin", m.Password)
	require.Equal(t, false, f.whoami(access).data()["super"], "原主账号重新登录后是普通账号")

	pwd, err := orgs.NewInitialPassword()
	require.NoError(t, err)
	require.NoError(t, orgs.ResetOwnerPassword(ctx, org.Merchant(), m.Org.ID, pwd))
	eventuallyInvalidationCode(t, httpx.CodeTokenInvalid, func() resp { return f.whoami(staffAccess) })
	require.Equal(t, 0, f.whoami(access).env.Code, "重置只吊销主账号会话")
	require.NoError(t, orgs.SetStatus(ctx, org.Merchant(), m.Org.ID, org.StatusDisabled, 1))
	eventuallyInvalidationCode(t, httpx.CodeTokenInvalid, func() resp { return f.whoami(access) })
	require.Equal(t, 0, f.whoami(otherAccess).env.Code, "另一个主体照常访问")
}

func TestInvalidation_164_OutageAndRecovery(t *testing.T) {
	gdb, clock, addr, prefix := newInvalidationDB(t)
	proxy := redisx.NewTestProxy(t, addr)
	users := newMemUsers()
	newFixture := func(via string) *authFixture {
		a := newInvalidationApp(t, gdb, clock, via, prefix, testPortal, testSecret, &invalidationTestModule{authTestModule{users: users}})
		return &authFixture{t: t, app: a, users: users, clock: clock}
	}
	a, b := newFixture(proxy.Addr()), newFixture(addr)
	id := a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	cached := func() resp { return a.do(http.MethodGet, "/cached", nil, bearerOpt(access)) }
	require.Equal(t, "alice", cached().data()["displayName"])
	users.mu.Lock()
	finds := users.finds
	users.mu.Unlock()
	var queries atomic.Int64
	require.NoError(t, gdb.Callback().Query().After("gorm:query").Register("invalidation:outage-queries", func(*gorm.DB) { queries.Add(1) }))
	t.Cleanup(func() { _ = gdb.Callback().Query().Remove("invalidation:outage-queries") })

	proxy.Cut()
	require.Eventually(t, func() bool { return !a.app.redis.Available() }, 3*time.Second, 10*time.Millisecond, "订阅断线向连接层报告")
	users.set(id, func(acc *portal.Account) { acc.DisplayName = "断线期间的变更" })
	b.app.Deps().Auth.ForgetAccount(testPortal, id)
	for range 5 {
		r := cached()
		require.Equal(t, 0, r.env.Code, "Redis 断开不拒绝服务")
		require.Equal(t, "alice", r.data()["displayName"], "断开时沿用未过期的缓存")
	}
	users.mu.Lock()
	findsAfter := users.finds
	users.mu.Unlock()
	require.Equal(t, finds, findsAfter, "故障不清账号缓存、不增加用户源查询")
	require.Zero(t, queries.Load(), "故障不清会话或授权缓存、不增加数据库查询")

	proxy.Restore()
	require.Eventually(t, func() bool {
		if !a.app.redis.Available() || !a.app.invalidationReady.Load() {
			return false
		}
		r := cached()
		return r.env.Code == 0 && r.data()["displayName"] == "断线期间的变更"
	}, 8*time.Second, 20*time.Millisecond, "恢复和重新订阅清掉漏收通知对应的缓存")
}

func TestInvalidation_164_MessageProtocol(t *testing.T) {
	a, b := newInvalidationPair(t)
	uid := a.addUser("alice", "alice-pass-123")
	access, _ := a.mustLogin("alice", "alice-pass-123")
	cached := func() resp { return a.do(http.MethodGet, "/cached", nil, bearerOpt(access)) }
	require.Equal(t, "alice", cached().data()["displayName"])
	sid := a.ping(access).data()["sid"].(string)
	a.users.set(uid, func(acc *portal.Account) { acc.DisplayName = "通知后读到的新值" })
	encode := func(e invalidation) string {
		payload, err := json.Marshal(e)
		require.NoError(t, err)
		return string(payload)
	}
	base := invalidation{Version: 1, Source: b.app.invalidationSource, Kind: "account", Portal: testPortal, Key: strconv.FormatUint(uid, 10)}
	change := func(fn func(*invalidation)) string {
		e := base
		fn(&e)
		return encode(e)
	}

	// 额外的观察订阅不处理消息；屏障之前若收到任何失效消息，就是接收路径重发了通知。
	listenCtx, cancel := context.WithCancel(context.Background())
	listened, done, messages := make(chan struct{}), make(chan struct{}), make(chan string, 64)
	var subscribed sync.Once
	go func() {
		defer close(done)
		a.app.redis.Listen(listenCtx, func(payload string) {
			select {
			case messages <- payload:
			case <-listenCtx.Done():
			}
		}, func() { subscribed.Do(func() { close(listened) }) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("观察订阅未能停止")
		}
	})
	select {
	case <-listened:
	case <-time.After(2 * time.Second):
		t.Fatal("观察订阅未确认")
	}
	var queries atomic.Int64
	require.NoError(t, a.app.Deps().DB.Callback().Query().After("gorm:query").Register("invalidation:message-queries", func(*gorm.DB) { queries.Add(1) }))
	t.Cleanup(func() { _ = a.app.Deps().DB.Callback().Query().Remove("invalidation:message-queries") })
	a.users.mu.Lock()
	finds := a.users.finds
	a.users.mu.Unlock()

	invalid := []struct{ name, payload string }{
		{"空消息", ""},
		{"不是JSON", "message"},
		{"未知版本", change(func(e *invalidation) { e.Version = 2 })},
		{"缺少版本", change(func(e *invalidation) { e.Version = 0 })},
		{"未知类型", change(func(e *invalidation) { e.Kind = "unknown" })},
		{"未知字段", strings.TrimSuffix(encode(base), "}") + `,"state":"ignored"}`},
		{"尾随JSON", encode(base) + `{}`},
		{"尾随杂项", encode(base) + `!`},
		{"超长消息", encode(base) + strings.Repeat(" ", 1025)},
		{"本实例消息", change(func(e *invalidation) { e.Source = a.app.invalidationSource })},
		{"来源格式不对", change(func(e *invalidation) { e.Source = "remote" })},
		{"端码格式不对", change(func(e *invalidation) { e.Portal = "../test" })},
		{"未注册的端", change(func(e *invalidation) { e.Portal = "other" })},
		{"账号零值", change(func(e *invalidation) { e.Key = "0" })},
		{"账号负数", change(func(e *invalidation) { e.Key = "-1" })},
		{"账号前导零", change(func(e *invalidation) { e.Key = "0" + e.Key })},
		{"账号非数字", change(func(e *invalidation) { e.Key = "alice" })},
		{"账号溢出", change(func(e *invalidation) { e.Key = "18446744073709551616" })},
		{"会话格式不对", change(func(e *invalidation) { e.Kind, e.Key = "session", sid+"a" })},
		{"主体带键", change(func(e *invalidation) { e.Kind = "org" })},
		{"菜单带键", change(func(e *invalidation) { e.Kind = "menu" })},
		{"授权带端", change(func(e *invalidation) { e.Kind, e.Key = "policy", "" })},
		{"字典带端", change(func(e *invalidation) { e.Kind, e.Key = "dict", "invalidation" })},
		{"字典键格式不对", change(func(e *invalidation) { e.Kind, e.Portal, e.Key = "dict", "", "../dict" })},
		{"字典键过长", change(func(e *invalidation) { e.Kind, e.Portal, e.Key = "dict", "", strings.Repeat("a", 65) })},
		{"IP名单带端", change(func(e *invalidation) { e.Kind, e.Key = "ip", "" })},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			a.app.receiveInvalidation(tc.payload)
			r := cached()
			require.Equal(t, 0, r.env.Code)
			require.Equal(t, "alice", r.data()["displayName"], "非法消息不能清账号缓存")
			a.users.mu.Lock()
			findsAfter := a.users.finds
			a.users.mu.Unlock()
			require.Equal(t, finds, findsAfter, "非法消息不增加用户源查询")
			require.Zero(t, queries.Load(), "非法消息不清会话、授权或 IP 缓存")
		})
	}

	valid := []invalidation{
		base,
		{Version: 1, Source: base.Source, Kind: "account", Portal: testPortal},
		{Version: 1, Source: base.Source, Kind: "session", Portal: testPortal, Key: sid},
		{Version: 1, Source: base.Source, Kind: "session", Portal: testPortal},
		{Version: 1, Source: base.Source, Kind: "org", Portal: testPortal},
		{Version: 1, Source: base.Source, Kind: "policy"},
		{Version: 1, Source: base.Source, Kind: "menu", Portal: testPortal},
		{Version: 1, Source: base.Source, Kind: "dict", Key: "invalidation"},
		{Version: 1, Source: base.Source, Kind: "dict"},
		{Version: 1, Source: base.Source, Kind: "ip"},
	}
	for _, event := range valid {
		a.app.receiveInvalidation(encode(event))
	}
	a.users.mu.Lock()
	findsAfter := a.users.finds
	a.users.mu.Unlock()
	require.Equal(t, finds, findsAfter, "有效消息的接收只清内存，不同步读用户源")
	require.Zero(t, queries.Load(), "有效消息的接收不查库")
	barrier := fmt.Sprintf("invalidation-test-barrier:%s", a.app.invalidationSource)
	require.NoError(t, a.app.redis.Publish(context.Background(), barrier))
	select {
	case payload := <-messages:
		require.Equal(t, barrier, payload, "接收失效消息不能再次广播")
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到通知屏障")
	}
	require.Equal(t, "通知后读到的新值", cached().data()["displayName"], "远端的有效消息让下一次请求重读状态")
	require.Positive(t, queries.Load(), "状态在请求路径重新加载")
}
