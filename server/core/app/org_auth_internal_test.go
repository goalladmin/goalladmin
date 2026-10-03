package app

// 主体端（D-061）的认证反向测试：按主体登录、主体隔离的登录防护、主体停用、会话与账号的主体核对、主账号。
// 用一个内存的主体端用户源（shop 端）和真实 MySQL（会话表、登录日志）跑完整的 HTTP 链路。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/internal/token"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

const (
	shopPortal = "shop"
	shopSecret = "shop-secret-0123456789abcdef0123456789abcdef"
)

// ---- 内存的主体端用户源 ----

type memOrgUsers struct {
	mu     sync.Mutex
	orgs   map[uint64]*portal.Org
	users  map[uint64]*portal.Account
	nextID uint64
	// beforeLockOrg 在登录建会话的事务里锁主体之前调用一次（模拟"核对完密码之后主体被停用"）
	beforeLockOrg func()
	// lockLog 按顺序记下加锁调用："org"（LockOrgByID）、"user"（LockByID）
	lockLog []string
	// onLock 在每次 LockByID 读账号之前调用（不拿着 mu）：测试用它让请求停在事务里"等账号行锁"的位置
	onLock func(id uint64)
}

func (m *memOrgUsers) setOnLock(fn func(id uint64)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onLock = fn
}

// takeLockLog 取走并清空加锁记录。
func (m *memOrgUsers) takeLockLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.lockLog
	m.lockLog = nil
	return out
}

func newMemOrgUsers() *memOrgUsers {
	return &memOrgUsers{orgs: map[uint64]*portal.Org{}, users: map[uint64]*portal.Account{}, nextID: 1}
}

func (m *memOrgUsers) addOrg(code, name string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	m.orgs[id] = &portal.Org{ID: id, Code: code, Name: name, Status: 1}
	return id
}

func (m *memOrgUsers) addUser(orgID uint64, username, hash string, owner bool) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.nextID
	m.nextID++
	m.users[id] = &portal.Account{ID: id, OrgID: orgID, Username: username, DisplayName: username, PasswordHash: hash, Status: 1}
	if owner {
		m.orgs[orgID].OwnerUserID = id
	}
	return id
}

func (m *memOrgUsers) setOrg(id uint64, fn func(o *portal.Org)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.orgs[id])
}

func (m *memOrgUsers) setUser(id uint64, fn func(a *portal.Account)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.users[id])
}

// FindByUsername 在主体端不会被框架调用（账号名只在主体内唯一）：调到了就是框架的错。
func (m *memOrgUsers) FindByUsername(context.Context, string) (*portal.Account, error) {
	panic("主体端不应按全局账号名查账号")
}

func (m *memOrgUsers) FindByID(_ context.Context, id uint64) (*portal.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.users[id]
	if !ok {
		return nil, portal.ErrAccountNotFound
	}
	c := *a
	return &c, nil
}

func (m *memOrgUsers) LockByID(ctx context.Context, id uint64) (*portal.Account, error) {
	m.mu.Lock()
	m.lockLog = append(m.lockLog, "user")
	hook := m.onLock
	m.mu.Unlock()
	if hook != nil {
		hook(id)
	}
	return m.FindByID(ctx, id)
}

func (m *memOrgUsers) UpdatePasswordHash(_ context.Context, id uint64, hash string, mustChange bool) error {
	m.setUser(id, func(a *portal.Account) { a.PasswordHash = hash; a.MustChangePwd = mustChange })
	return nil
}

func (m *memOrgUsers) RehashPassword(_ context.Context, id uint64, oldHash, newHash string) error {
	m.setUser(id, func(a *portal.Account) {
		if a.PasswordHash == oldHash {
			a.PasswordHash = newHash
		}
	})
	return nil
}

func (m *memOrgUsers) TouchLogin(context.Context, uint64, string, time.Time) error { return nil }

// FindOrgByCode 模拟数据库不区分大小写的排序规则：按大写比较。
func (m *memOrgUsers) FindOrgByCode(_ context.Context, code string) (*portal.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, o := range m.orgs {
		if strings.EqualFold(o.Code, code) {
			c := *o
			return &c, nil
		}
	}
	return nil, portal.ErrOrgNotFound
}

func (m *memOrgUsers) FindOrgByID(_ context.Context, id uint64) (*portal.Org, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orgs[id]
	if !ok {
		return nil, portal.ErrOrgNotFound
	}
	c := *o
	return &c, nil
}

func (m *memOrgUsers) LockOrgByID(ctx context.Context, id uint64) (*portal.Org, error) {
	m.mu.Lock()
	m.lockLog = append(m.lockLog, "org")
	fn := m.beforeLockOrg
	m.beforeLockOrg = nil
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
	return m.FindOrgByID(ctx, id)
}

func (m *memOrgUsers) FindByOrgUsername(_ context.Context, orgID uint64, username string) (*portal.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.users {
		if a.OrgID == orgID && strings.EqualFold(a.Username, username) {
			c := *a
			return &c, nil
		}
	}
	return nil, portal.ErrAccountNotFound
}

// ---- 假模块：注册 shop 端（Scoped）和一条 AuthOnly 路由 ----

type orgTestModule struct{ users *memOrgUsers }

func (m *orgTestModule) Name() string { return "orgtest" }
func (m *orgTestModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: shopPortal, Users: m.users, Scoped: true})
}
func (m *orgTestModule) Perms() []rbac.Perm     { return nil }
func (m *orgTestModule) Menus() []rbac.MenuNode { return nil }
func (m *orgTestModule) Routes(r *Router) {
	r.Portal(shopPortal).GET("/whoami", rbac.AuthOnly(), func(c *gin.Context) {
		p := auth.MustFromCtx(c.Request.Context())
		httpx.OK(c, gin.H{"user": p.Username, "userId": p.UserID, "orgId": p.OrgID, "super": p.Super})
	})
}
func (m *orgTestModule) Start(context.Context) error { return nil }
func (m *orgTestModule) Stop(context.Context) error  { return nil }

// ---- 夹具 ----

type orgFixture struct {
	*authFixture
	orgs *memOrgUsers
}

func newOrgFixture(t *testing.T, opts ...Option) *orgFixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)

	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.AllowedOrigins = []string{testOrigin}
	cfg.Portals[testPortal] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: testSecret}
	cfg.Portals[shopPortal] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: shopSecret}

	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	f := &orgFixture{authFixture: &authFixture{t: t, users: newMemUsers(), clock: clock}, orgs: newMemOrgUsers()}
	base := []Option{WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithClock(clock.Now), WithPasswordHashParams(64, 1)}
	a, err := New(cfg, append(base, opts...)...)
	require.NoError(t, err)
	a.Register(&authTestModule{users: f.users}, &orgTestModule{users: f.orgs})
	require.NoError(t, a.Setup())
	f.app = a
	return f
}

func (f *orgFixture) hash(password string) string { return f.hashOf(password) }

// shop 向 shop 端发请求。
func (f *orgFixture) shop(method, path string, body any, opts ...reqOpt) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, "/api/"+shopPortal+"/v1"+path, rd)
	req.RemoteAddr = "203.0.113.10:5000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if path == "/auth/login" || path == "/auth/captcha" {
		webClient()(req)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return resp{rec: rec, env: env}
}

func (f *orgFixture) shopLogin(org, username, password string) resp {
	return f.shop("POST", "/auth/login", gin.H{"org": org, "username": username, "password": password})
}

func (f *orgFixture) mustShopLogin(org, username, password string) (access, refresh string) {
	f.t.Helper()
	r := f.shopLogin(org, username, password)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	access, _ = r.data()["accessToken"].(string)
	refresh = r.cookie("ga_rt_" + shopPortal)
	require.NotEmpty(f.t, access)
	require.NotEmpty(f.t, refresh)
	return access, refresh
}

func shopCookie(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ga_rt_" + shopPortal, Value: v}) }
}

func (f *orgFixture) whoami(access string) resp {
	return f.shop("GET", "/whoami", nil, bearerOpt(access))
}

func (f *orgFixture) shopRefresh(cookie string) resp {
	return f.shop("POST", "/auth/refresh", nil, shopCookie(cookie), webClient())
}

// loginLogs 读 shop 端的登录日志，按写入顺序。
func (f *orgFixture) loginLogs() []session.LoginLog {
	var rows []session.LoginLog
	require.NoError(f.t, f.app.deps.DB.Where("portal = ?", shopPortal).Order("id").Find(&rows).Error)
	return rows
}

// 两个商户：A（主账号 admin、员工 staff），B（主账号也叫 admin，密码不同）。
func (f *orgFixture) seedTwoOrgs() (orgA, orgB, adminA, staffA, adminB uint64) {
	orgA = f.orgs.addOrg("M10000001", "商户甲")
	orgB = f.orgs.addOrg("M10000002", "商户乙")
	adminA = f.orgs.addUser(orgA, "admin", f.hash("a-secret-pass-1"), true)
	staffA = f.orgs.addUser(orgA, "staff", f.hash("staff-pass-1"), false)
	adminB = f.orgs.addUser(orgB, "admin", f.hash("b-secret-pass-1"), true)
	return
}

// ============ 登录：编号 + 账号 + 密码；身份里的主体和主账号 ============

func TestOrgAuth_LoginCarriesOrgIntoPrincipal(t *testing.T) {
	f := newOrgFixture(t)
	orgA, _, adminA, staffA, _ := f.seedTwoOrgs()

	// 编号归一化：大小写、首尾空白不影响
	access, _ := f.mustShopLogin("  m10000001 ", "Admin", "a-secret-pass-1")
	who := f.whoami(access)
	require.Equal(t, 0, who.env.Code, who.rec.Body.String())
	require.EqualValues(t, orgA, who.data()["orgId"])
	require.EqualValues(t, adminA, who.data()["userId"])
	require.Equal(t, true, who.data()["super"], "主账号在主体端就是主体内的超管")

	me := f.shop("GET", "/auth/me", nil, bearerOpt(access))
	require.Equal(t, 0, me.env.Code, me.rec.Body.String())
	org, _ := me.data()["org"].(map[string]any)
	require.Equal(t, "M10000001", org["code"])
	require.Equal(t, "商户甲", org["name"])

	staff, _ := f.mustShopLogin("M10000001", "staff", "staff-pass-1")
	who = f.whoami(staff)
	require.EqualValues(t, staffA, who.data()["userId"])
	require.Equal(t, false, who.data()["super"], "员工不是主账号")

	// 会话行和登录日志都带主体
	var s session.Session
	require.NoError(t, f.app.deps.DB.Where("portal = ? AND user_id = ?", shopPortal, adminA).First(&s).Error)
	require.Equal(t, orgA, s.OrgID)
	logs := f.loginLogs()
	require.NotEmpty(t, logs)
	require.Equal(t, orgA, logs[0].OrgID)
	require.Equal(t, "M10000001", logs[0].OrgCode)

	// 平台端（非 Scoped）不受影响：不看 org，会话的主体为 0
	f.addUser("alice", "correct-horse-9")
	pa, _ := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(pa).rec.Code)
	var ps session.Session
	require.NoError(t, f.app.deps.DB.Where("portal = ?", testPortal).First(&ps).Error)
	require.Zero(t, ps.OrgID)
}

func TestOrgAuth_OrgCodeIsRequired(t *testing.T) {
	f := newOrgFixture(t)
	f.seedTwoOrgs()
	for _, org := range []string{"", "   "} {
		r := f.shopLogin(org, "admin", "a-secret-pass-1")
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		require.Contains(t, r.rec.Body.String(), `"field":"org"`)
		require.Empty(t, r.cookie("ga_rt_"+shopPortal))
	}
	require.Empty(t, f.loginLogs(), "没有编号的请求没有核对密码，不写登录日志")
}

// ============ 编号不存在、主体停用、账号不存在、密码错：对外完全一样 ============

func TestOrgAuth_FailuresLookAlike(t *testing.T) {
	f := newOrgFixture(t, WithPasswordHashParams(19456, 2)) // 生产用的参数：一次比较要花得出来的时间
	_, orgB, _, _, _ := f.seedTwoOrgs()
	f.orgs.setOrg(orgB, func(o *portal.Org) { o.Status = 0 })

	cases := []struct{ org, user, pwd, reason string }{
		{"M99999999", "admin", "a-secret-pass-1", "no_org"},       // 编号不存在
		{"M10000002", "admin", "b-secret-pass-1", "org_disabled"}, // 主体停用（密码是对的）
		{"M10000001", "nobody", "a-secret-pass-1", "no_account"},  // 账号不存在
		{"M10000001", "admin", "wrong-pass-123", "bad_password"},  // 密码错
		{"M10000001", "admin", "b-secret-pass-1", "bad_password"}, // 别的商户同名账号的密码
	}
	var first resp
	for i, tc := range cases {
		r := f.shopLogin(tc.org, tc.user, tc.pwd)
		require.Equal(t, 200, r.rec.Code)
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 种：%s", i, r.rec.Body.String())
		require.Empty(t, r.cookie("ga_rt_"+shopPortal))
		if i == 0 {
			first = r
			continue
		}
		require.Equal(t, first.env.Msg, r.env.Msg, "第 %d 种的提示和编号不存在时不同", i)
		require.Equal(t, first.data(), r.data(), "第 %d 种的出参和编号不存在时不同", i)
	}
	logs := f.loginLogs()
	require.Len(t, logs, len(cases))
	for i, tc := range cases {
		require.Equal(t, tc.reason, logs[i].Reason, "第 %d 种", i)
		require.Equal(t, portal.NormalizeOrgCode(tc.org), logs[i].OrgCode)
		require.False(t, logs[i].Success)
	}
	require.Zero(t, logs[0].OrgID, "编号不存在时没有主体")
	require.Equal(t, orgB, logs[1].OrgID)

	// 编号不存在也做一次假的密码比较：耗时不能明显短于一次真实比较
	h := f.hashOf("x")
	start := time.Now()
	f.app.hasher.Verify(h, "y")
	oneCompare := time.Since(start)
	start = time.Now()
	f.shopLogin("M88888888", "admin", "whatever-123")
	require.GreaterOrEqual(t, time.Since(start), oneCompare/2, "编号不存在的路径没有做假哈希比较")
}

// ============ 登录防护按"编号/账号名"计数：商户 A 的 admin 被锁不影响商户 B 的 admin ============

func TestOrgAuth_LoginGuardIsPerOrg(t *testing.T) {
	f := newOrgFixture(t)
	f.seedTwoOrgs()

	captcha := func() (string, string) {
		c := f.shop("GET", "/auth/captcha", nil)
		id, _ := c.data()["captchaId"].(string)
		return id, f.app.captcha.Peek(id)
	}
	// 大小写、空白变体的编号共用一份配额：变着写也只有 10 次
	variants := []string{"M10000001", "m10000001", " M10000001", "M10000001 "}
	for i := 0; i < 10; i++ {
		id, ans := captcha()
		r := f.shop("POST", "/auth/login", gin.H{"org": variants[i%len(variants)], "username": "admin", "password": "wrong-pass-123", "captchaId": id, "captchaCode": ans})
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 次：%s", i+1, r.rec.Body.String())
	}
	locked := f.shopLogin("m10000001", "admin", "a-secret-pass-1")
	require.Equal(t, httpx.CodeLocked, locked.env.Code, "商户 A 的 admin 应被锁定：%s", locked.rec.Body.String())

	// 商户 B 的同名账号照常登录，第一次也不要验证码
	r := f.shopLogin("M10000002", "admin", "b-secret-pass-1")
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	// 商户 A 的其他账号也不受影响
	r = f.shopLogin("M10000001", "staff", "staff-pass-1")
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
}

// ============ 主体停用：会话最长一个缓存周期后失效，刷新立即失效 ============

func TestOrgAuth_DisabledOrgLosesSessions(t *testing.T) {
	f := newOrgFixture(t)
	orgA, _, adminA, _, _ := f.seedTwoOrgs()
	access, refresh := f.mustShopLogin("M10000001", "admin", "a-secret-pass-1")
	require.Equal(t, 200, f.whoami(access).rec.Code)
	b, _ := f.mustShopLogin("M10000002", "admin", "b-secret-pass-1")

	f.orgs.setOrg(orgA, func(o *portal.Org) { o.Status = 0 })

	// 刷新按库认定：立即失效，会话被吊销，并告诉前端会话已结束
	rr := f.shopRefresh(refresh)
	require.Equal(t, 401, rr.rec.Code, rr.rec.Body.String())
	require.Contains(t, rr.rec.Body.String(), "auth.sessionEnded")
	// 访问令牌：状态缓存过期后（最长 15 秒）失效；清缓存则立即失效
	f.clock.Advance(16 * time.Second)
	require.Equal(t, 401, f.whoami(access).rec.Code, "主体停用后，状态缓存过期就不能再用")
	f.app.Deps().Auth.ForgetAccount(shopPortal, adminA)
	require.Equal(t, 401, f.whoami(access).rec.Code)
	// 登录同样不行
	require.Equal(t, httpx.CodeLoginFailed, f.shopLogin("M10000001", "admin", "a-secret-pass-1").env.Code)
	// 别的主体不受影响
	require.Equal(t, 200, f.whoami(b).rec.Code)

	// 一条语句吊销主体的全部会话
	f.orgs.setOrg(orgA, func(o *portal.Org) { o.Status = 1 })
	x1, _ := f.mustShopLogin("M10000001", "admin", "a-secret-pass-1")
	x2, _ := f.mustShopLogin("M10000001", "staff", "staff-pass-1")
	n, err := f.app.authenticators[shopPortal].Sessions().RevokeOrg(f.app.Context(context.Background()), shopPortal, orgA, auth.RevokeDisabled)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(2))
	f.app.authenticators[shopPortal].ForgetSession(sidOf(t, x1))
	f.app.authenticators[shopPortal].ForgetSession(sidOf(t, x2))
	require.Equal(t, 401, f.whoami(x1).rec.Code)
	require.Equal(t, 401, f.whoami(x2).rec.Code)
	require.Equal(t, 200, f.whoami(b).rec.Code, "别的主体的会话不受影响")
}

// 核对完密码、建会话之前主体被停用：这次登录失败，不留下会话（同 D-047 的账号停用）。
func TestOrgAuth_OrgDisabledDuringLogin(t *testing.T) {
	f := newOrgFixture(t)
	orgA, _, adminA, _, _ := f.seedTwoOrgs()
	f.orgs.beforeLockOrg = func() { f.orgs.setOrg(orgA, func(o *portal.Org) { o.Status = 0 }) }
	r := f.shopLogin("M10000001", "admin", "a-secret-pass-1")
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
	require.Empty(t, r.cookie("ga_rt_"+shopPortal))
	var n int64
	require.NoError(t, f.app.deps.DB.Model(&session.Session{}).Where("portal = ? AND user_id = ?", shopPortal, adminA).Count(&n).Error)
	require.Zero(t, n, "不能留下会话")
}

// 131.（规范 §13.2）主体端登录建会话的事务先锁主体行、再锁账号行（D-063 第 5 条）：后台写操作先锁主体行、再锁目标账号，
// 登录反过来就会和它们互相等待。主体端的用户来源必须实现 portal.OrgLocker，没实现的注册时就拒绝。
func TestOrgAuth_131_LoginLocksOrgBeforeAccount(t *testing.T) {
	f := newOrgFixture(t)
	f.seedTwoOrgs()
	f.orgs.takeLockLog()
	f.mustShopLogin("M10000001", "admin", "a-secret-pass-1")
	require.Equal(t, []string{"org", "user"}, f.orgs.takeLockLog())

	// 没有 OrgLocker 的主体端用户来源：注册失败
	err := portal.NewRegistry().Register(portal.Portal{Code: "shop2", Users: noOrgLock{f.orgs}, Scoped: true})
	require.ErrorContains(t, err, "OrgLocker")
}

// noOrgLock 只暴露 OrgUserProvider，不暴露 LockOrgByID。
type noOrgLock struct{ m *memOrgUsers }

func (n noOrgLock) FindByUsername(ctx context.Context, u string) (*portal.Account, error) {
	return n.m.FindByUsername(ctx, u)
}
func (n noOrgLock) FindByID(ctx context.Context, id uint64) (*portal.Account, error) {
	return n.m.FindByID(ctx, id)
}
func (n noOrgLock) UpdatePasswordHash(ctx context.Context, id uint64, h string, must bool) error {
	return n.m.UpdatePasswordHash(ctx, id, h, must)
}
func (n noOrgLock) TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error {
	return n.m.TouchLogin(ctx, id, ip, at)
}
func (n noOrgLock) FindOrgByCode(ctx context.Context, c string) (*portal.Org, error) {
	return n.m.FindOrgByCode(ctx, c)
}
func (n noOrgLock) FindOrgByID(ctx context.Context, id uint64) (*portal.Org, error) {
	return n.m.FindOrgByID(ctx, id)
}
func (n noOrgLock) FindByOrgUsername(ctx context.Context, org uint64, u string) (*portal.Account, error) {
	return n.m.FindByOrgUsername(ctx, org, u)
}

// ============ 会话的主体和账号的主体必须一致；主体端没有不带主体的会话 ============

func TestOrgAuth_SessionOrgMustMatchAccount(t *testing.T) {
	f := newOrgFixture(t)
	_, orgB, adminA, _, _ := f.seedTwoOrgs()
	access, refresh := f.mustShopLogin("M10000001", "admin", "a-secret-pass-1")

	// 账号被挪到了另一个主体（数据被改过）：会话按无效处理，刷新也不行
	f.orgs.setUser(adminA, func(a *portal.Account) { a.OrgID = orgB })
	f.app.Deps().Auth.ForgetAccount(shopPortal, adminA)
	r := f.whoami(access)
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.Equal(t, 401, f.shopRefresh(refresh).rec.Code)
}

func TestOrgAuth_ScopedSessionWithoutOrgIsInvalid(t *testing.T) {
	f := newOrgFixture(t)
	_, _, adminA, _, _ := f.seedTwoOrgs()
	ctx := f.app.Context(context.Background())
	// 直接往会话表里塞一个不带主体的会话，再用 shop 端的真密钥签一个访问令牌：仍然 401
	s, _, err := f.app.authenticators[shopPortal].Sessions().Create(ctx, shopPortal, 0, adminA, "203.0.113.9", "ua")
	require.NoError(t, err)
	tok, _, err := token.New(shopPortal, []byte(shopSecret), 15*time.Minute, f.clock.Now).Sign(adminA, s.SID)
	require.NoError(t, err)
	require.Equal(t, 401, f.whoami(tok).rec.Code)
}

// 主体端的令牌拿到平台端（反之亦然）：受众不对，一律 401。
func TestOrgAuth_TokensDoNotCrossPortals(t *testing.T) {
	f := newOrgFixture(t)
	f.seedTwoOrgs()
	f.addUser("alice", "correct-horse-9")
	shopAccess, _ := f.mustShopLogin("M10000001", "admin", "a-secret-pass-1")
	platAccess, _ := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 401, f.ping(shopAccess).rec.Code)
	require.Equal(t, 401, f.whoami(platAccess).rec.Code)

	// 用 shop 端的密钥签一个受众是 test 端的令牌：test 端用自己的密钥验不过
	now := f.clock.Now()
	forged := forgeToken(t, jwt.SigningMethodHS256, []byte(shopSecret), jwt.MapClaims{
		"iss": "goalladmin", "aud": testPortal, "sub": "1", "sid": strings.Repeat("a", 32), "iat": now.Unix(), "exp": now.Add(time.Minute).Unix(),
	})
	require.Equal(t, 401, f.ping(forged).rec.Code)
}

// sidOf 从访问令牌里取会话号（只解码，不验签）。
func sidOf(t *testing.T, access string) string {
	t.Helper()
	parsed, _, err := jwt.NewParser().ParseUnverified(access, jwt.MapClaims{})
	require.NoError(t, err)
	sid, _ := parsed.Claims.(jwt.MapClaims)["sid"].(string)
	require.NotEmpty(t, sid)
	return sid
}
