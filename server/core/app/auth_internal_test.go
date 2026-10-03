package app

// 规范 §13.2 第 1–11 条：认证的反向测试。用一个内存用户源和真实 MySQL（会话表）跑完整的 HTTP 链路。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/audit"
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
	testPortal = "test"
	testSecret = "test-secret-0123456789abcdef0123456789abcdef"
	testOrigin = "http://app.test"
)

// ---- 假时钟 ----

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// ---- 内存用户源 ----

type memUsers struct {
	mu     sync.Mutex
	byID   map[uint64]*portal.Account
	byName map[string]uint64
	next   uint64
	// alias 模拟不区分重音之类差异的排序规则：按这个名字查会查到另一个账号
	alias map[string]string
	// failNext 让下一次 FindByID 返回这个错误（模拟数据库临时故障）
	failNext error
	// afterFind 在一次 FindByID 取好副本之后调用一次（模拟"读完之后别人提交了修改"）
	afterFind func()
	rehashes  int // RehashPassword 被调用的次数
	finds     int // FindByID 被调用的次数
}

func newMemUsers() *memUsers {
	return &memUsers{byID: map[uint64]*portal.Account{}, byName: map[string]uint64{}, next: 1}
}

func (m *memUsers) add(username, hash string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := m.next
	m.next++
	m.byID[id] = &portal.Account{ID: id, Username: username, DisplayName: username, PasswordHash: hash, Status: 1}
	m.byName[username] = id
	return id
}

func (m *memUsers) set(id uint64, fn func(a *portal.Account)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.byID[id])
}

func (m *memUsers) FindByUsername(_ context.Context, username string) (*portal.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if real, ok := m.alias[username]; ok {
		username = real
	}
	id, ok := m.byName[username]
	if !ok {
		return nil, portal.ErrAccountNotFound
	}
	a := *m.byID[id]
	return &a, nil
}

func (m *memUsers) FindByID(_ context.Context, id uint64) (*portal.Account, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finds++
	if err := m.failNext; err != nil {
		m.failNext = nil
		return nil, err
	}
	a, ok := m.byID[id]
	if !ok {
		return nil, portal.ErrAccountNotFound
	}
	c := *a
	if fn := m.afterFind; fn != nil {
		m.afterFind = nil
		fn()
	}
	return &c, nil
}

func (m *memUsers) UpdatePasswordHash(_ context.Context, id uint64, hash string, mustChange bool) error {
	m.set(id, func(a *portal.Account) { a.PasswordHash = hash; a.MustChangePwd = mustChange })
	return nil
}

// RehashPassword 只换哈希（portal.PasswordRehasher，D-070）：库里的哈希已经不是 oldHash 时什么都不做。
func (m *memUsers) RehashPassword(_ context.Context, id uint64, oldHash, newHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rehashes++
	if a := m.byID[id]; a != nil && a.PasswordHash == oldHash {
		a.PasswordHash = newHash
	}
	return nil
}

func (m *memUsers) TouchLogin(context.Context, uint64, string, time.Time) error { return nil }

// ---- 假模块：注册 test 端和一条 AuthOnly 路由 ----

type authTestModule struct{ users *memUsers }

func (m *authTestModule) Name() string { return "authtest" }
func (m *authTestModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: testPortal, Users: m.users})
}
func (m *authTestModule) Perms() []rbac.Perm     { return nil }
func (m *authTestModule) Menus() []rbac.MenuNode { return nil }
func (m *authTestModule) Routes(r *Router) {
	r.Portal(testPortal).GET("/ping", rbac.AuthOnly(), func(c *gin.Context) {
		p := auth.MustFromCtx(c.Request.Context())
		httpx.OK(c, gin.H{"user": p.Username, "sid": p.SessionID})
	})
}
func (m *authTestModule) Start(context.Context) error { return nil }
func (m *authTestModule) Stop(context.Context) error  { return nil }

// ---- 测试用的应用与客户端 ----

type authFixture struct {
	t     *testing.T
	app   *App
	users *memUsers
	clock *fakeClock
}

func newAuthFixture(t *testing.T, opts ...Option) *authFixture {
	t.Helper()
	return newAuthFixtureCfg(t, nil, opts...)
}

// newAuthFixtureCfg 另外允许在建应用前调整配置。
func newAuthFixtureCfg(t *testing.T, tweak func(cfg *conf.Config), opts ...Option) *authFixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)

	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.AllowedOrigins = []string{testOrigin}
	cfg.Portals[testPortal] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: testSecret}
	if tweak != nil {
		tweak(cfg)
	}

	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	users := newMemUsers()
	f := &authFixture{t: t, users: users, clock: clock}
	// 密码哈希的参数降到最低，测试才跑得快；放在最前面，测试自己传的选项可以盖掉它
	base := []Option{WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithClock(clock.Now), WithPasswordHashParams(64, 1)}
	a, err := New(cfg, append(base, opts...)...)
	require.NoError(t, err)
	a.Register(&authTestModule{users: users})
	require.NoError(t, a.Setup())
	f.app = a
	return f
}

func (f *authFixture) addUser(username, password string) uint64 {
	return f.users.add(username, f.hashOf(password))
}

// hashOf 用这个应用的哈希器生成密码哈希（不经过并发闸门）。
func (f *authFixture) hashOf(password string) string {
	h, err := f.app.hasher.Hash(password)
	require.NoError(f.t, err)
	return h
}

type resp struct {
	rec *httptest.ResponseRecorder
	env httpx.Envelope
}

func (r resp) data() map[string]any {
	m, _ := r.env.Data.(map[string]any)
	return m
}

func (r resp) cookie(name string) string {
	for _, c := range r.rec.Result().Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

type reqOpt func(*http.Request)

func bearerOpt(tok string) reqOpt {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}
func cookieOpt(v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "ga_rt_" + testPortal, Value: v}) }
}
func webClient() reqOpt {
	return func(r *http.Request) { r.Header.Set("X-GA-Client", "web"); r.Header.Set("Origin", testOrigin) }
}

// noClient 去掉客户端标识和 Origin（模拟跨站页面发来的请求）。
func noClient() reqOpt {
	return func(r *http.Request) { r.Header.Del("X-GA-Client"); r.Header.Del("Origin") }
}
func xff(ip string) reqOpt { return func(r *http.Request) { r.Header.Set("X-Forwarded-For", ip) } }

func (f *authFixture) do(method, path string, body any, opts ...reqOpt) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, "/api/"+testPortal+"/v1"+path, rd)
	req.RemoteAddr = "203.0.113.10:5000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if path == "/auth/login" || path == "/auth/captcha" {
		webClient()(req) // 浏览器里的登录都带这两个头（D-051）；测反例的用 opts 覆盖掉
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

func (f *authFixture) login(username, password string) resp {
	return f.do("POST", "/auth/login", gin.H{"username": username, "password": password})
}

func (f *authFixture) mustLogin(username, password string) (access, refresh string) {
	f.t.Helper()
	r := f.login(username, password)
	require.Equal(f.t, 200, r.rec.Code, r.rec.Body.String())
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	access, _ = r.data()["accessToken"].(string)
	refresh = r.cookie("ga_rt_" + testPortal)
	require.NotEmpty(f.t, access)
	require.NotEmpty(f.t, refresh)
	return access, refresh
}

func (f *authFixture) ping(access string) resp { return f.do("GET", "/ping", nil, bearerOpt(access)) }

func (f *authFixture) refresh(cookie string, opts ...reqOpt) resp {
	return f.do("POST", "/auth/refresh", nil, append([]reqOpt{cookieOpt(cookie), webClient()}, opts...)...)
}

// 用任意密钥、算法、声明造一个令牌。
func forgeToken(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, claims)
	s, err := tok.SignedString(key)
	require.NoError(t, err)
	return s
}

// ============ 1. 错误密码与不存在的账号：同码、同文案、耗时相当 ============

func TestAuth_1_WrongPasswordAndUnknownUserLookAlike(t *testing.T) {
	f := newAuthFixture(t, WithPasswordHashParams(19456, 2)) // 生产用的参数：一次比较要花得出来的时间
	f.addUser("alice", "correct-horse-9")

	bad := f.login("alice", "wrong-password-9")
	unknown := f.login("nobody", "wrong-password-9")
	require.Equal(t, 200, bad.rec.Code)
	require.Equal(t, 200, unknown.rec.Code)
	require.Equal(t, httpx.CodeLoginFailed, bad.env.Code)
	require.Equal(t, httpx.CodeLoginFailed, unknown.env.Code)
	require.Equal(t, bad.env.Msg, unknown.env.Msg)

	// 账号不存在时也必须做一次哈希比较：耗时不能明显短于一次真实比较
	h := f.hashOf("x")
	start := time.Now()
	f.app.hasher.Verify(h, "y")
	oneCompare := time.Since(start)
	start = time.Now()
	f.login("nobody2", "wrong-password-9")
	unknownDur := time.Since(start)
	require.GreaterOrEqual(t, unknownDur, oneCompare/2, "账号不存在的路径没有做假哈希比较")
}

// ============ 2. 过期、签名错误、alg=none、RS256 ============

func TestAuth_2_BadTokensAreRejected(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, _ := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(access).rec.Code)

	// 过期
	f.clock.Advance(16 * time.Minute)
	r := f.ping(access)
	require.Equal(t, 401, r.rec.Code)
	require.Equal(t, httpx.CodeTokenInvalid, r.env.Code)
	f.clock.Advance(-16 * time.Minute)

	now := f.clock.Now()
	base := jwt.MapClaims{"iss": "goalladmin", "aud": testPortal, "sub": "1", "sid": strings.Repeat("a", 32), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}

	// 签名错误
	require.Equal(t, 401, f.ping(forgeToken(t, jwt.SigningMethodHS256, []byte("another-secret-0123456789abcdef0123456789"), base)).rec.Code)
	// alg=none
	require.Equal(t, 401, f.ping(forgeToken(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, base)).rec.Code)
	// RS256 头 + 原来的载荷和签名
	{
		parts := strings.Split(access, ".")
		hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
		require.Equal(t, 401, f.ping(hdr+"."+parts[1]+"."+parts[2]).rec.Code)
	}
	// 没有令牌
	require.Equal(t, 401, f.do("GET", "/ping", nil).rec.Code)
	// 乱码
	require.Equal(t, 401, f.ping("not.a.jwt").rec.Code)
}

// ============ 3. aud 不是当前端 ============

func TestAuth_3_AudienceMustMatchPortal(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, _ := f.mustLogin("alice", "correct-horse-9")

	// 解出真实 sid，用正确密钥但错误 aud 重签
	parsed, _ := jwt.Parse(access, func(*jwt.Token) (any, error) { return []byte(testSecret), nil })
	claims := parsed.Claims.(jwt.MapClaims)
	claims["aud"] = "other"
	r := f.ping(forgeToken(t, jwt.SigningMethodHS256, []byte(testSecret), claims))
	require.Equal(t, 401, r.rec.Code)
	require.Equal(t, httpx.CodeTokenInvalid, r.env.Code)
}

// ============ 4. 登出后访问令牌与刷新凭证立即失效 ============

func TestAuth_4_LogoutInvalidatesBothTokens(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, refresh := f.mustLogin("alice", "correct-horse-9")

	r := f.do("POST", "/auth/logout", nil, bearerOpt(access), webClient())
	require.Equal(t, 200, r.rec.Code, r.rec.Body.String())

	require.Equal(t, 401, f.ping(access).rec.Code, "登出后访问令牌必须立即失效")
	rr := f.refresh(refresh)
	require.Equal(t, 401, rr.rec.Code, "登出后刷新凭证必须失效")
}

// ============ 5. 账号停用后已登录会话立即失效 ============

func TestAuth_5_DisabledAccountLosesSessionImmediately(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "correct-horse-9")
	access, refresh := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(access).rec.Code)

	// 系统管理模块停用账号时应做的三件事：改状态、吊销会话、清缓存
	f.users.set(id, func(a *portal.Account) { a.Status = 0 })
	require.NoError(t, f.app.Deps().Auth.RevokeUserSessions(f.app.Context(context.Background()), testPortal, id, auth.RevokeDisabled))
	f.app.Deps().Auth.ForgetAccount(testPortal, id)

	require.Equal(t, 401, f.ping(access).rec.Code)
	require.Equal(t, 401, f.refresh(refresh).rec.Code)
	r := f.login("alice", "correct-horse-9")
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "停用账号不能登录")
}

// ============ 6. 修改密码后其他会话失效，当前会话保留 ============

func TestAuth_6_PasswordChangeRevokesOtherSessions(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access1, _ := f.mustLogin("alice", "correct-horse-9")
	access2, refresh2 := f.mustLogin("alice", "correct-horse-9")

	r := f.do("PUT", "/auth/password", gin.H{"oldPassword": "correct-horse-9", "newPassword": "new-horse-battery-7"}, bearerOpt(access1))
	require.Equal(t, 200, r.rec.Code, r.rec.Body.String())
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	require.Equal(t, 200, f.ping(access1).rec.Code, "当前会话保留")
	require.Equal(t, 401, f.ping(access2).rec.Code, "其他会话失效")
	require.Equal(t, 401, f.refresh(refresh2).rec.Code)

	require.Equal(t, httpx.CodeLoginFailed, f.login("alice", "correct-horse-9").env.Code, "旧密码不能再登录")
	f.mustLogin("alice", "new-horse-battery-7")

	// 密码策略
	weak := f.do("PUT", "/auth/password", gin.H{"oldPassword": "new-horse-battery-7", "newPassword": "short1"}, bearerOpt(access1))
	require.Equal(t, httpx.CodeValidation, weak.env.Code)
	require.Contains(t, weak.rec.Body.String(), "newPassword")
	wrongOld := f.do("PUT", "/auth/password", gin.H{"oldPassword": "nope-nope-1", "newPassword": "another-good-pass-3"}, bearerOpt(access1))
	require.Equal(t, httpx.CodeValidation, wrongOld.env.Code)
	require.Contains(t, wrongOld.rec.Body.String(), "oldPassword")
}

// ============ 7. 刷新轮换；旧凭证在宽限期外重放 → 整个会话被吊销 ============

func TestAuth_7_RefreshRotationAndReuseDetection(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	_, c0 := f.mustLogin("alice", "correct-horse-9")

	r1 := f.refresh(c0)
	require.Equal(t, 200, r1.rec.Code, r1.rec.Body.String())
	c1 := r1.cookie("ga_rt_" + testPortal)
	require.NotEmpty(t, c1)
	require.NotEqual(t, c0, c1, "刷新必须轮换凭证")
	access1, _ := r1.data()["accessToken"].(string)
	require.Equal(t, 200, f.ping(access1).rec.Code)

	f.clock.Advance(11 * time.Second)
	replay := f.refresh(c0)
	require.Equal(t, 401, replay.rec.Code, "宽限期外重放旧凭证必须被拒")

	// 会话整体被吊销：新凭证和新访问令牌都不再可用
	require.Equal(t, 401, f.refresh(c1).rec.Code)
	require.Equal(t, 401, f.ping(access1).rec.Code)

	// 刷新接口的防跨站要求
	_, c2 := f.mustLogin("alice", "correct-horse-9")
	noHeader := f.do("POST", "/auth/refresh", nil, cookieOpt(c2))
	require.Equal(t, 403, noHeader.rec.Code, "缺少 X-GA-Client 必须拒绝")
	badOrigin := f.do("POST", "/auth/refresh", nil, cookieOpt(c2), func(r *http.Request) {
		r.Header.Set("X-GA-Client", "web")
		r.Header.Set("Origin", "http://evil.test")
	})
	require.Equal(t, 403, badOrigin.rec.Code, "Origin 不在白名单必须拒绝")
	noCookie := f.do("POST", "/auth/refresh", nil, webClient())
	require.Equal(t, 401, noCookie.rec.Code)
}

// ============ 8. 并发刷新：一个成功，一个 409，重试后成功，会话不被误吊销 ============

func TestAuth_8_ConcurrentRefresh(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	_, c0 := f.mustLogin("alice", "correct-horse-9")

	// 顺序版：同一个旧凭证在宽限期内用两次
	r1 := f.refresh(c0)
	require.Equal(t, 200, r1.rec.Code)
	c1 := r1.cookie("ga_rt_" + testPortal)
	r2 := f.refresh(c0)
	require.Equal(t, 409, r2.rec.Code, r2.rec.Body.String())
	require.Equal(t, httpx.CodeRefreshRetry, r2.env.Code)
	r3 := f.refresh(c1)
	require.Equal(t, 200, r3.rec.Code, "重试后成功")
	c2 := r3.cookie("ga_rt_" + testPortal)

	// 真并发版：两个 goroutine 同时用 c2
	var wg sync.WaitGroup
	results := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = f.refresh(c2).rec.Code
		}(i)
	}
	wg.Wait()
	require.ElementsMatch(t, []int{200, 409}, results)
}

// ============ 9. 必须改密的账号只能访问 /auth/* ============

func TestAuth_9_MustChangePasswordBlocksOtherRoutes(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "temp-password-1")
	f.users.set(id, func(a *portal.Account) { a.MustChangePwd = true })

	r := f.login("alice", "temp-password-1")
	require.Equal(t, 200, r.rec.Code)
	require.Equal(t, true, r.data()["mustChangePwd"])
	access, _ := r.data()["accessToken"].(string)

	blocked := f.ping(access)
	require.Equal(t, 403, blocked.rec.Code)
	require.Equal(t, httpx.CodePwdChangeRequired, blocked.env.Code)

	me := f.do("GET", "/auth/me", nil, bearerOpt(access))
	require.Equal(t, 200, me.rec.Code, "/auth/me 允许访问")
	require.Equal(t, true, me.data()["user"].(map[string]any)["mustChangePwd"])

	ch := f.do("PUT", "/auth/password", gin.H{"oldPassword": "temp-password-1", "newPassword": "real-password-22"}, bearerOpt(access))
	require.Equal(t, 200, ch.rec.Code, ch.rec.Body.String())
	require.Equal(t, 0, ch.env.Code)
	require.Equal(t, 200, f.ping(access).rec.Code, "改密后当前会话可访问其他路由")
}

// ============ 10. 3 次失败后要验证码；10 次后锁定 ============

func TestAuth_10_CaptchaAfterThreeFailuresThenLockout(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")

	for i := 0; i < 3; i++ {
		r := f.login("alice", "wrong-9")
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code)
	}
	// 第 4 次：没带验证码 → 要求验证码；即使密码正确也不放行
	r := f.login("alice", "correct-horse-9")
	require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code)
	require.Equal(t, true, r.data()["captchaRequired"])

	// 拿验证码，填对答案 → 放行
	cap := f.do("GET", "/auth/captcha", nil)
	require.Equal(t, 200, cap.rec.Code)
	id, _ := cap.data()["captchaId"].(string)
	require.NotEmpty(t, id)
	require.True(t, strings.HasPrefix(cap.data()["image"].(string), "data:image/png;base64,"))
	answer := f.app.captcha.Peek(id)
	require.NotEmpty(t, answer)
	ok := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "correct-horse-9", "captchaId": id, "captchaCode": answer})
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())

	// 验证码一次性：同一 id 再用一次不行
	again := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "wrong-9", "captchaId": id, "captchaCode": answer})
	require.NotEqual(t, 0, again.env.Code)

	// 锁定：换个账号，连续失败 10 次（每次都带正确验证码）
	f.addUser("bob", "correct-horse-9")
	for i := 0; i < 10; i++ {
		cap := f.do("GET", "/auth/captcha", nil)
		cid, _ := cap.data()["captchaId"].(string)
		r := f.do("POST", "/auth/login", gin.H{"username": "bob", "password": "wrong-9", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 次", i+1)
	}
	locked := f.login("bob", "correct-horse-9")
	require.Equal(t, httpx.CodeLocked, locked.env.Code, locked.rec.Body.String())
	require.NotEmpty(t, locked.data()["lockedUntil"])

	// 锁定 15 分钟后解除
	f.clock.Advance(16 * time.Minute)
	cap = f.do("GET", "/auth/captcha", nil)
	cid, _ := cap.data()["captchaId"].(string)
	after := f.do("POST", "/auth/login", gin.H{"username": "bob", "password": "correct-horse-9", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
	require.Equal(t, 0, after.env.Code, after.rec.Body.String())
}

// ============ 11. 伪造 X-Forwarded-For 不能绕过限流 ============

func TestAuth_11_ForgedXFFDoesNotBypassRateLimit(t *testing.T) {
	f := newAuthFixture(t) // trustedProxies 为空：只认 RemoteAddr
	f.addUser("alice", "correct-horse-9")
	got429 := false
	for i := 0; i < 25; i++ {
		r := f.do("POST", "/auth/login", gin.H{"username": fmt.Sprintf("user%d", i), "password": "x-9"}, xff(fmt.Sprintf("10.0.0.%d", i)))
		if r.rec.Code == 429 {
			require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
			require.Equal(t, 20, i, "第 21 个请求应触发限流")
			got429 = true
			break
		}
	}
	require.True(t, got429)

	// 一分钟后恢复
	f.clock.Advance(61 * time.Second)
	require.NotEqual(t, 429, f.login("alice", "correct-horse-9").rec.Code)
}

// ============ 附：登录成功后的 me、登录日志、会话记录 ============

func TestAuth_MeAndLoginLog(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	f.login("alice", "wrong-9")
	access, _ := f.mustLogin("alice", "correct-horse-9")

	me := f.do("GET", "/auth/me", nil, bearerOpt(access))
	require.Equal(t, 200, me.rec.Code)
	user := me.data()["user"].(map[string]any)
	require.Equal(t, "alice", user["username"])
	require.Equal(t, false, user["super"])
	require.Equal(t, []any{}, me.data()["perms"])

	var logs []struct {
		Username string
		Success  bool
		Reason   string
		IP       string
	}
	require.NoError(t, f.app.Deps().DB.Raw("SELECT username, success, reason, ip FROM ga_login_log ORDER BY id").Scan(&logs).Error)
	require.Len(t, logs, 2)
	require.Equal(t, "bad_password", logs[0].Reason)
	require.False(t, logs[0].Success)
	require.True(t, logs[1].Success)
	require.Equal(t, "203.0.113.10", logs[1].IP)

	var sessions int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_session WHERE portal = ? AND revoked_at IS NULL", testPortal).Scan(&sessions).Error)
	require.EqualValues(t, 1, sessions)

	routes := f.app.Routes()
	var public []string
	for _, r := range routes {
		if r.Guard == rbac.GuardPublic {
			public = append(public, r.Method+" "+r.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET /api/test/v1/auth/captcha", "POST /api/test/v1/auth/login", "POST /api/test/v1/auth/refresh",
	}, public, "只有这三条认证接口是 Public")
}

// ============ 补充：刷新凭证与会话所属端必须一致（第二个端启用前的边界） ============

func TestAuth_RefreshRejectsSessionFromAnotherPortal(t *testing.T) {
	f := newAuthFixture(t)
	uid := f.addUser("alice", "correct-horse-9")
	ctx := f.app.Context(context.Background())
	mgr := f.app.authenticators[testPortal].Sessions()

	// 直接在另一个端名下造一个同用户 ID 的会话，拿它的刷新凭证去 platform 端刷新
	other, tok, err := mgr.Create(ctx, "other", 0, uid, "203.0.113.9", "ua")
	require.NoError(t, err)
	r := f.refresh(tok.String())
	require.Equal(t, 401, r.rec.Code, "别的端的刷新凭证不能换出本端的令牌")

	// 会话本身没有被动过：在它自己的端里仍然可以轮换
	s, _, err := mgr.Rotate(ctx, "other", tok, "203.0.113.9", "ua")
	require.NoError(t, err)
	require.Equal(t, other.SID, s.SID)

	// 用别的端的会话 ID 配本端密钥签一个访问令牌也不行：签名过得去，会话所属端对不上
	access, _, err := token.New(testPortal, []byte(testSecret), 15*time.Minute, f.clock.Now).Sign(uid, other.SID)
	require.NoError(t, err)
	require.Equal(t, 401, f.ping(access).rec.Code)
}

// ============ 吊销会话以 (端, sid) 为键 ============

func TestAuth_RevokeSessionIsScopedToPortal(t *testing.T) {
	f := newAuthFixture(t)
	uid := f.addUser("alice", "correct-horse-9")
	ctx := f.app.Context(context.Background())
	mgr := f.app.authenticators[testPortal].Sessions()
	svc := f.app.Deps().Auth

	// 别的端的会话：拿着它的 sid 在 platform 端吊销，得到 404，会话不受影响
	other, tok, err := mgr.Create(ctx, "other", 0, uid, "203.0.113.9", "ua")
	require.NoError(t, err)
	err = svc.RevokeSession(ctx, testPortal, other.SID, auth.RevokeAdmin)
	require.ErrorIs(t, err, httpx.ErrNotFound, "别的端的会话对本端来说不存在")
	s, _, err := mgr.Rotate(ctx, "other", tok, "203.0.113.9", "ua")
	require.NoError(t, err, "别的端的会话没有被动过")
	require.Equal(t, other.SID, s.SID)

	// 本端的会话：正常吊销后访问令牌立即失效；重复吊销幂等；不存在的 sid 是 404
	access, _ := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(access).rec.Code)
	rows, _, err := svc.ListSessions(ctx, testPortal, uid, 1, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	sid := rows[0].SID
	require.NoError(t, svc.RevokeSession(ctx, testPortal, sid, auth.RevokeAdmin))
	require.Equal(t, 401, f.ping(access).rec.Code)
	require.NoError(t, svc.RevokeSession(ctx, testPortal, sid, auth.RevokeAdmin))
	require.ErrorIs(t, svc.RevokeSession(ctx, testPortal, strings.Repeat("f", 32), auth.RevokeAdmin), httpx.ErrNotFound)
	require.Error(t, svc.RevokeSession(ctx, "no-such-portal", sid, auth.RevokeAdmin))
}

// ============ 签名正确但会话不存在的令牌是 401，不是 500 ============

func TestAuth_UnknownSessionIs401(t *testing.T) {
	f := newAuthFixture(t)
	uid := f.addUser("alice", "correct-horse-9")
	access, _, err := token.New(testPortal, []byte(testSecret), 15*time.Minute, f.clock.Now).Sign(uid, strings.Repeat("e", 32))
	require.NoError(t, err)
	r := f.ping(access)
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeTokenInvalid, r.env.Code)
}

// ============ 登录名大小写变体共用一份限流、锁定配额 ============

func TestAuth_UsernameCaseVariantsShareLoginGuard(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")

	// 用三种写法各错一次：按归一化后的键计数，第 4 次（不管怎么写）都要求验证码
	for _, name := range []string{"ALICE", " Alice ", "aLiCe"} {
		r := f.login(name, "wrong-9")
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, name)
	}
	r := f.login("alice", "correct-horse-9")
	require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code, "三次失败后大小写变体不能再拿到新的配额")

	// 归一化后能登录：带验证码用大写写法登录同一个账号
	cap := f.do("GET", "/auth/captcha", nil)
	id, _ := cap.data()["captchaId"].(string)
	ok := f.do("POST", "/auth/login", gin.H{"username": "ALICE ", "password": "correct-horse-9", "captchaId": id, "captchaCode": f.app.captcha.Peek(id)})
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())

	// 锁定同样合并计数：10 次失败分散在不同写法里，之后正确密码也被锁
	f.addUser("bob", "correct-horse-9")
	variants := []string{"bob", "BOB", "Bob", " bob"}
	for i := 0; i < 10; i++ {
		cap := f.do("GET", "/auth/captcha", nil)
		cid, _ := cap.data()["captchaId"].(string)
		r := f.do("POST", "/auth/login", gin.H{"username": variants[i%len(variants)], "password": "wrong-9", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, "第 %d 次", i+1)
	}
	require.Equal(t, httpx.CodeLocked, f.login("Bob", "correct-horse-9").env.Code)
}

// ============ 登录名的别名（排序规则忽略的差异）不能碰到账号 ============

// 用户表的排序规则可能把 ádmin 和 admin 当成同一个值，但限流、锁定按输入的登录名计数：
// 别名能查到账号就等于给同一个账号开出第二份尝试配额。查到的账号必须和输入逐字相同，否则按不存在处理（D-043）。
func TestAuth_63_AliasOfUsernameDoesNotReachAccount(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	f.users.alias = map[string]string{"álice": "alice", "alíce": "alice"}

	// 拿着正确密码、用别名登录：必须失败，而且和"账号不存在"一模一样
	r := f.login("álice", "correct-horse-9")
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
	unknown := f.login("nobody-here", "correct-horse-9")
	require.Equal(t, unknown.rec.Code, r.rec.Code)
	require.Equal(t, unknown.env.Code, r.env.Code)
	require.Equal(t, unknown.env.Msg, r.env.Msg, "别名和不存在的账号响应必须相同")
	require.Equal(t, unknown.env.Data, r.env.Data)

	// 别名上的失败不算到本账号头上：本账号还没有进入验证码阶段
	for i := 0; i < 3; i++ {
		require.Equal(t, httpx.CodeLoginFailed, f.login("alíce", "wrong-9").env.Code)
	}
	ok := f.login("alice", "correct-horse-9")
	require.Equal(t, 0, ok.env.Code, "本账号的配额不受别名影响，仍能直接登录")
	// 反过来别名也拿不到本账号的密码判定：10 次错密码只会锁住别名自己的键
	for i := 0; i < 10; i++ {
		cap := f.do("GET", "/auth/captcha", nil)
		cid, _ := cap.data()["captchaId"].(string)
		f.do("POST", "/auth/login", gin.H{"username": "álice", "password": "wrong-9", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
	}
	require.Equal(t, httpx.CodeLocked, f.login("álice", "correct-horse-9").env.Code, "别名自己的键被锁")
	cap := f.do("GET", "/auth/captcha", nil)
	cid, _ := cap.data()["captchaId"].(string)
	again := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "correct-horse-9", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
	require.Equal(t, 0, again.env.Code, again.rec.Body.String())
}

// ============ 会话到期后缓存不再放行 ============

// 会话状态有进程内缓存；到期时间随状态一起缓存，命中时也要比一次，不能靠缓存周期到期才发现（D-043）。
// 访问令牌比会话活得久只会发生在会话最长寿命（30 天）的边上：临近上限时刷新一次，拿到的访问令牌
// 会跨过会话到期的那一刻。这里把缓存周期设得比那段时间长，来暴露"缓存命中就当有效"的问题。
func TestAuth_64_ExpiredSessionNotServedFromCache(t *testing.T) {
	f := newAuthFixture(t, WithStatusCacheTTL(30*time.Minute))
	f.addUser("alice", "correct-horse-9")
	_, cookie := f.mustLogin("alice", "correct-horse-9")
	// 每 6 天刷新一次把会话续到第 24 天，再走到最长寿命前 10 分钟刷新：会话到期在 30 天整，访问令牌到 30 天 5 分
	for i := 0; i < 4; i++ {
		f.clock.Advance(6 * 24 * time.Hour)
		r := f.refresh(cookie)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		cookie = r.cookie("ga_rt_" + testPortal)
	}
	f.clock.Advance(5*24*time.Hour + 23*time.Hour + 50*time.Minute)
	r := f.refresh(cookie)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	access, _ := r.data()["accessToken"].(string)
	require.NotEmpty(t, access)
	require.Equal(t, 200, f.ping(access).rec.Code) // 进缓存
	f.clock.Advance(12 * time.Minute)              // 会话已过最长寿命，访问令牌还有 3 分钟，缓存条目还有十几分钟
	p := f.ping(access)
	require.Equal(t, 401, p.rec.Code, p.rec.Body.String())
}

// setsCookie 报告响应有没有写这个 Cookie（包括清除）。
func (r resp) setsCookie(name string) bool {
	for _, c := range r.rec.Result().Cookies() {
		if c.Name == name {
			return true
		}
	}
	return false
}

func sessionOpt(sid string) reqOpt {
	return func(r *http.Request) { r.Header.Set("X-GA-Session", sid) }
}

// ============ 75. 刷新绑定会话：别的标签页换了人，这个标签页不能用别人的 Cookie 续上（D-048） ============

func TestAuth_75_RefreshBoundToExpectedSession(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	f.addUser("bob", "battery-staple-7")
	ra := f.login("alice", "correct-horse-9")
	sidA, _ := ra.data()["sessionId"].(string)
	require.NotEmpty(t, sidA, "登录的出参带会话号")
	rb := f.login("bob", "battery-staple-7")
	sidB, _ := rb.data()["sessionId"].(string)
	cookieB := rb.cookie("ga_rt_" + testPortal)

	// 标签页 1 以为自己是 alice，浏览器里的 Cookie 已经是 bob 的：拒绝，不轮换、不动 Cookie
	r := f.refresh(cookieB, sessionOpt(sidA))
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.False(t, r.setsCookie("ga_rt_"+testPortal), "Cookie 是别的标签页的登录，不能改")
	// bob 的 Cookie 还能用（没被轮换掉）；带对了会话号照常刷新，出参的会话号不变
	r = f.refresh(cookieB, sessionOpt(sidB))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, sidB, r.data()["sessionId"])
	// 不带会话号（页面刚打开）照旧按 Cookie 刷新
	r = f.refresh(r.cookie("ga_rt_" + testPortal))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
}

// ============ 76. 退出只清自己会话的 Cookie（D-048） ============

func TestAuth_76_LogoutKeepsSomeoneElsesCookie(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	f.addUser("bob", "battery-staple-7")
	accessA, cookieA := f.mustLogin("alice", "correct-horse-9")
	_, cookieB := f.mustLogin("bob", "battery-staple-7")

	// alice 的退出在途时 bob 在同一浏览器登录了：请求带的是 bob 的 Cookie，alice 的会话照样吊销，bob 的 Cookie 不清
	r := f.do("POST", "/auth/logout", nil, bearerOpt(accessA), cookieOpt(cookieB), webClient())
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.False(t, r.setsCookie("ga_rt_"+testPortal), "不能清掉别人的 Cookie")
	require.Equal(t, 401, f.refresh(cookieA).rec.Code, "alice 的会话已吊销")
	require.Equal(t, 0, f.refresh(cookieB).env.Code, "bob 的登录还在")

	// 自己的 Cookie 也不清（D-049）：退出的响应可能迟到，到达时浏览器里的 Cookie 可能已经是别人新登录的；
	// 吊销后这个 Cookie 已经没有用处，下次刷新回 401，下次登录会换掉它
	accessA, cookieA = f.mustLogin("alice", "correct-horse-9")
	r = f.do("POST", "/auth/logout", nil, bearerOpt(accessA), cookieOpt(cookieA), webClient())
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.False(t, r.setsCookie("ga_rt_"+testPortal), "服务端从不删除刷新 Cookie")
	require.Equal(t, 401, f.refresh(cookieA).rec.Code, "留在浏览器里的 Cookie 已经没用")
}

// ============ 79. 只知道会话号换不出、也吊销不了会话（D-049） ============

func TestAuth_79_UnknownSecretDoesNotRevokeSession(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	tok, ok := session.ParseRefreshToken(cookie)
	require.True(t, ok)

	// 拿着会话号和编出来的 secret 来刷新：401，会话不动、Cookie 不动
	forged := tok.SID + "." + strings.Repeat("0", 64)
	for i := 0; i < 3; i++ {
		r := f.refresh(forged)
		require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
		require.False(t, r.setsCookie("ga_rt_"+testPortal), "对不上的凭证不能动 Cookie")
	}
	require.Equal(t, 200, f.ping(access).rec.Code, "访问令牌照常可用")
	r := f.refresh(cookie)
	require.Equal(t, 200, r.rec.Code, "合法持有人的凭证照常轮换")
	c1 := r.cookie("ga_rt_" + testPortal)
	require.NotEmpty(t, c1)

	// 轮换之后再编：同样只拒绝；上一个凭证在宽限期内是 409，宽限期外才吊销（第 7 条）
	require.Equal(t, 401, f.refresh(forged).rec.Code)
	require.Equal(t, 409, f.refresh(cookie).rec.Code)
	require.Equal(t, 200, f.refresh(c1).rec.Code)

	// 记了安全事件：refresh_mismatch（警告级，带会话和用户），不是 refresh_reuse
	ev := f.securityEvents()
	m, found := ev["refresh_mismatch"]
	require.True(t, found, "%v", ev)
	require.Equal(t, audit.LevelWarning, m.Level)
	require.Equal(t, tok.SID, m.SessionID)
	require.EqualValues(t, 4, m.Count)
	require.NotZero(t, m.UserID)
	_, reuse := ev["refresh_reuse"]
	require.False(t, reuse, "对不上的凭证不算重放")
}

// ============ 80. 服务端从不删除刷新 Cookie（D-049） ============

func TestAuth_80_ServerNeverDeletesRefreshCookie(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	name := "ga_rt_" + testPortal

	// 会话被吊销后拿旧 Cookie 刷新：401，不删 Cookie（这条响应迟到时浏览器里可能已经是别人的 Cookie）
	require.Equal(t, 0, f.do("POST", "/auth/logout", nil, bearerOpt(access), cookieOpt(cookie), webClient()).env.Code)
	r := f.refresh(cookie)
	require.Equal(t, 401, r.rec.Code)
	require.False(t, r.setsCookie(name))
	// 不存在的会话、格式不对的 Cookie：同样只是 401
	r = f.refresh(strings.Repeat("a", 32) + "." + strings.Repeat("b", 64))
	require.Equal(t, 401, r.rec.Code)
	require.False(t, r.setsCookie(name))
	r = f.refresh("garbage")
	require.Equal(t, 401, r.rec.Code)
	require.False(t, r.setsCookie(name))
	// 宽限期外重放导致的吊销也不删
	access, cookie = f.mustLogin("alice", "correct-horse-9")
	c1 := f.refresh(cookie).cookie(name)
	require.NotEmpty(t, c1)
	f.clock.Advance(11 * time.Second)
	r = f.refresh(cookie)
	require.Equal(t, 401, r.rec.Code)
	require.False(t, r.setsCookie(name))
	require.Equal(t, 401, f.refresh(c1).rec.Code, "会话已因重放吊销")
	require.Equal(t, 401, f.ping(access).rec.Code)

	// 写 Cookie 的只有登录和轮换
	rl := f.login("alice", "correct-horse-9")
	require.True(t, rl.setsCookie(name))
	rr := f.refresh(rl.cookie(name))
	require.Equal(t, 200, rr.rec.Code)
	require.True(t, rr.setsCookie(name))
}

// ============ 81. 刷新时查账号遇到临时故障：回 503，不轮换、不吊销，稍后照样能刷新（D-050） ============

func TestAuth_81_RefreshTransientAccountErrorKeepsSession(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	_, refresh := f.mustLogin("alice", "correct-horse-9")

	f.users.mu.Lock()
	f.users.failNext = errors.New("dial tcp: i/o timeout")
	f.users.mu.Unlock()
	r := f.refresh(refresh)
	require.Equal(t, httpx.CodeUnavailable, r.env.Code, r.rec.Body.String())
	require.False(t, r.setsCookie("ga_rt_"+testPortal), "没有轮换，就没有新 Cookie")

	// 远超 10 秒宽限期之后，同一个凭证照样能用：它从没被轮换过，不会被当成重放
	f.clock.Advance(time.Minute)
	r = f.refresh(refresh)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.True(t, r.setsCookie("ga_rt_"+testPortal))
}

// 规范 §13.2 第 85 条：登录和刷新、退出一样要求 X-GA-Client 与 Origin 白名单，并且只收 application/json（D-051）。
// 跨站页面顶层提交表单时浏览器会写下响应里的 Cookie（SameSite=Strict 也挡不住），受害者就登进了攻击者的账号；
// 拒绝发生在算密码、建会话之前：不建会话、不写 Cookie、不记登录失败。
func TestAuth_85_LoginRejectsCrossSiteRequests(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	body := gin.H{"username": "alice", "password": "correct-horse-9"}
	cases := map[string]reqOpt{
		"没有客户端标识和 Origin": noClient(),
		"Origin 不在白名单":    func(r *http.Request) { r.Header.Set("Origin", "http://evil.test") },
		"没有 X-GA-Client":  func(r *http.Request) { r.Header.Del("X-GA-Client") },
		"text/plain 请求体":  func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"表单请求体":           func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
	}
	for name, opt := range cases {
		r := f.do("POST", "/auth/login", body, opt)
		require.Equal(t, 403, r.rec.Code, name)
		require.Equal(t, httpx.CodeForbidden, r.env.Code, name)
		require.Empty(t, r.rec.Header().Values("Set-Cookie"), "%s：不能写刷新 Cookie", name)
	}
	var n int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_session").Scan(&n).Error)
	require.Zero(t, n, "被拒的登录不能建会话")
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_login_log").Scan(&n).Error)
	require.Zero(t, n, "被拒的登录不算一次登录尝试")

	// 同源的正常登录照常
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	require.NotEmpty(t, access)
	require.NotEmpty(t, cookie)
}

// 规范 §13.2 第 90 条：刷新失败时，只有服务端确定会话已经结束（不存在、已吊销、已过期、这次因重放被吊销）才带
// auth.sessionEnded；Cookie 属于别的会话、凭证对不上、没有 Cookie 都不带——前端只凭它把没确认的退出算作确认（D-052）。
func TestAuth_90_RefreshSaysWhenSessionEnded(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	tok, ok := session.ParseRefreshToken(cookie)
	require.True(t, ok)
	keyOf := func(r resp) string {
		require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
		return r.env.Key
	}

	other := strings.Repeat("f", 32)
	require.Equal(t, "auth.sessionSwitched", keyOf(f.refresh(cookie, func(r *http.Request) { r.Header.Set("X-GA-Session", other) })))
	require.NotEqual(t, "auth.sessionEnded", keyOf(f.refresh(tok.SID+"."+strings.Repeat("0", 64))), "凭证对不上：会话还活着")
	require.NotEqual(t, "auth.sessionEnded", keyOf(f.do("POST", "/auth/refresh", nil, webClient())), "没有 Cookie：不知道会话的状态")
	require.NotEqual(t, "auth.sessionEnded", keyOf(f.refresh("garbage")), "看不懂的 Cookie")

	// 重放：上一个凭证在宽限期外再用，整个会话被吊销
	r := f.refresh(cookie)
	require.Equal(t, 200, r.rec.Code)
	f.clock.Advance(11 * time.Second)
	require.Equal(t, "auth.sessionEnded", keyOf(f.refresh(cookie)))

	// 退出吊销之后再刷新
	a2, c2 := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 0, f.do("POST", "/auth/logout", nil, bearerOpt(a2), webClient()).env.Code)
	require.Equal(t, "auth.sessionEnded", keyOf(f.refresh(c2)))
	require.Equal(t, 401, f.ping(access).rec.Code, "重放吊销的会话，访问令牌也不能用了")
}

// 规范 §13.2 第 90 条（补充，D-053）：账号停用后刷新要吊销会话；吊销没写成（数据库出错）时不能回 auth.sessionEnded——
// 会话其实还在，前端却会把没确认的退出算作确认。回 503。
func TestAuth_90b_SessionEndedOnlyAfterRevokeSucceeds(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "correct-horse-9")
	_, cookie := f.mustLogin("alice", "correct-horse-9")
	f.users.set(id, func(a *portal.Account) { a.Status = 0 })

	gdb := f.app.Deps().DB
	name := "test:fail-revoke"
	require.NoError(t, gdb.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if m, ok := tx.Statement.Dest.(map[string]any); ok && tx.Statement.Table == "ga_session" {
			if _, revoking := m["revoked_at"]; revoking {
				_ = tx.AddError(errors.New("injected: lost connection"))
			}
		}
	}))
	r := f.refresh(cookie)
	require.NoError(t, gdb.Callback().Update().Remove(name))
	require.Equal(t, 503, r.rec.Code, r.rec.Body.String())
	require.NotEqual(t, "auth.sessionEnded", r.env.Key)

	var revoked int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_session WHERE revoked_at IS NOT NULL").Scan(&revoked).Error)
	require.Zero(t, revoked, "吊销没写成")
}

// 规范 §13.2 第 98 条：本人改密核对旧密码有次数限制（D-055）。同一会话 15 分钟内最多 5 次，之后连正确的旧密码也回 429、
// 不再做 bcrypt，记安全事件 pwd_change_throttled；改密成功清零。
func TestAuth_98_ChangePasswordAttemptsLimited(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, cookie := f.mustLogin("alice", "correct-horse-9")
	change := func(tok, old string) resp {
		return f.do("PUT", "/auth/password", gin.H{"oldPassword": old, "newPassword": "brand-new-pass-7"}, bearerOpt(tok))
	}
	for i := 0; i < 5; i++ {
		require.Equal(t, httpx.CodeValidation, change(access, "wrong-guess-1").env.Code)
	}
	r := change(access, "correct-horse-9")
	require.Equal(t, 429, r.rec.Code, "次数用完：正确的旧密码也不再核对")
	ev := f.securityEvents()
	_, found := ev["pwd_change_throttled"]
	require.True(t, found, "%v", ev)

	// 过了窗口重新计（访问令牌也过期了，用同一个会话刷新一个）；成功之后清零
	f.clock.Advance(16 * time.Minute)
	rr := f.refresh(cookie)
	require.Equal(t, 0, rr.env.Code, rr.rec.Body.String())
	access, _ = rr.data()["accessToken"].(string)
	require.Equal(t, httpx.CodeValidation, change(access, "wrong-guess-1").env.Code)
	require.Equal(t, 0, change(access, "correct-horse-9").env.Code)
}

// 规范 §13.2 第 98 条：公开的验证码接口按来源 IP 限速（D-055）：同一 IP 每分钟 30 张，第 31 张回 429；别的 IP 不受影响。
func TestAuth_98b_CaptchaRateLimited(t *testing.T) {
	f := newAuthFixture(t)
	from := func(ip string) reqOpt { return func(r *http.Request) { r.RemoteAddr = ip + ":5000" } }
	for i := 0; i < 30; i++ {
		require.Equal(t, 0, f.do("GET", "/auth/captcha", nil, from("198.51.100.1")).env.Code, i)
	}
	require.Equal(t, 429, f.do("GET", "/auth/captcha", nil, from("198.51.100.1")).rec.Code)
	require.Equal(t, 0, f.do("GET", "/auth/captcha", nil, from("198.51.100.2")).env.Code)
	f.clock.Advance(time.Minute)
	require.Equal(t, 0, f.do("GET", "/auth/captcha", nil, from("198.51.100.1")).env.Code)
}

// 规范 §13.2 第 110 条（D-058）：被限流、被锁定的登录没有核对密码，只记（按分钟合并的）安全事件，不逐条写登录日志——
// 不用登录就能无限发的请求不能用来灌表。核对密码（bcrypt）的并发有上限，满了回 429，不算一次失败。
func TestAuth_110_CheapRejectionsAndPasswordGate(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	count := func() int64 {
		var n int64
		require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_login_log").Scan(&n).Error)
		return n
	}
	from := func(i int) reqOpt {
		return func(r *http.Request) { r.RemoteAddr = fmt.Sprintf("198.51.100.%d:5000", i) }
	}
	for i := 0; i < 10; i++ { // 账号每分钟 10 次；换着 IP，不触发验证码
		require.Equal(t, httpx.CodeLoginFailed, f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "wrong-9"}, from(i)).env.Code, i)
	}
	require.EqualValues(t, 10, count())
	// 账号的次数用完之后不拒绝，改为要验证码（D-103）：没带验证码的和别的验证码失败一样记一行登录日志，不记限流事件
	for i := 10; i < 15; i++ {
		r := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "wrong-9"}, from(i))
		require.NotEqual(t, 429, r.rec.Code, i)
		require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code, i)
	}
	require.EqualValues(t, 15, count())
	require.NotContains(t, f.securityEvents(), "login_rate_limited")
	// 来源的次数（每分钟 20 次）用完才是限流：回 429、不写登录日志，只记合并的安全事件
	src := func(r *http.Request) { r.RemoteAddr = "203.0.113.9:5000" }
	for i := 0; i < 20; i++ {
		require.Equal(t, httpx.CodeLoginFailed, f.do("POST", "/auth/login", gin.H{"username": fmt.Sprintf("ghost%d", i), "password": "wrong-9"}, src).env.Code, i)
	}
	require.EqualValues(t, 35, count())
	for i := 20; i < 25; i++ {
		require.Equal(t, 429, f.do("POST", "/auth/login", gin.H{"username": fmt.Sprintf("ghost%d", i), "password": "wrong-9"}, src).rec.Code)
	}
	require.EqualValues(t, 35, count(), "被限流的请求不写登录日志")
	require.Contains(t, f.securityEvents(), "login_rate_limited") // 先把攒着的写进去
	var limited int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COALESCE(SUM(count), 0) FROM ga_security_event WHERE kind = 'login_rate_limited'").Scan(&limited).Error)
	require.EqualValues(t, 5, limited, "安全事件照记，次数一个不少")

	// 锁定：同一账号 + IP 失败 10 次（第 3 次起带验证码）
	f.addUser("carol", "correct-horse-9")
	withCaptcha := func(user string) resp {
		cap := f.do("GET", "/auth/captcha", nil)
		cid, _ := cap.data()["captchaId"].(string)
		return f.do("POST", "/auth/login", gin.H{"username": user, "password": "wrong-9", "captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
	}
	for i := 0; i < 10; i++ {
		require.Equal(t, httpx.CodeLoginFailed, withCaptcha("carol").env.Code, i)
	}
	require.EqualValues(t, 45, count())
	r := withCaptcha("carol")
	require.Equal(t, httpx.CodeLocked, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 45, count(), "被锁定的请求不写登录日志")
	require.Contains(t, f.securityEvents(), "login_locked")

	// 核对密码的并发上限：占满之后的登录回 429，不记失败
	g := newAuthFixture(t, func(a *App) { a.pwdParallel = 1; a.pwdWait = 50 * time.Millisecond })
	g.addUser("bob", "correct-horse-9")
	access, _ := g.mustLogin("bob", "correct-horse-9")
	require.Equal(t, 200, g.ping(access).rec.Code) // 账号状态进缓存，下面的改密请求第一次查账号是在核对密码之前
	entered, release := make(chan struct{}), make(chan struct{})
	g.users.afterFind = func() { close(entered); <-release }
	done := make(chan resp, 1)
	go func() {
		done <- g.do("PUT", "/auth/password", gin.H{"oldPassword": "wrong-9", "newPassword": "Another-horse-10"}, bearerOpt(access))
	}()
	<-entered // 改密请求占着唯一的位置
	busyc := make(chan resp, 1)
	go func() { busyc <- g.login("bob", "correct-horse-9") }()
	select {
	case busy := <-busyc:
		require.Equal(t, 429, busy.rec.Code, busy.rec.Body.String())
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("位置占满时登录没有在等待时限之后回 429")
	}
	close(release)
	<-done
	access2, _ := g.mustLogin("bob", "correct-horse-9")
	require.NotEmpty(t, access2, "让出位置之后照常登录，刚才的 429 没有算失败")
}

// 规范 §13.2 第 111 条（D-058）：release 模式的刷新 Cookie 带 __Host- 前缀（Secure、Path=/、不带 Domain），
// 同一主域下的其他主机写不进同名 Cookie；刷新时带了多个同名的刷新 Cookie 一律拒绝、记安全事件，不猜哪个是真的。
func TestAuth_111_RefreshCookieCannotBePlanted(t *testing.T) {
	f := newAuthFixtureCfg(t, func(cfg *conf.Config) {
		cfg.Server.Mode = conf.ModeRelease
		cfg.Database.Password = "release-mode-test-password"
		delete(cfg.Portals, conf.DefaultPortalCode) // 只留测试端，免得还要给平台端配密钥
	})
	f.addUser("alice", "correct-horse-9")
	r := f.login("alice", "correct-horse-9")
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	name := "__Host-ga_rt_" + testPortal
	var ck *http.Cookie
	for _, c := range r.rec.Result().Cookies() {
		if c.Name == name {
			ck = c
		}
	}
	require.NotNil(t, ck, "release 模式的刷新 Cookie 名带 __Host- 前缀")
	require.True(t, ck.Secure)
	require.True(t, ck.HttpOnly)
	require.Equal(t, "/", ck.Path)
	require.Empty(t, ck.Domain)
	require.Equal(t, http.SameSiteStrictMode, ck.SameSite)

	with := func(vals ...string) reqOpt {
		return func(req *http.Request) {
			for _, v := range vals {
				req.AddCookie(&http.Cookie{Name: name, Value: v})
			}
		}
	}
	// 另一个账号的刷新凭证被塞进来，排在前面
	f.addUser("mallory", "correct-horse-9")
	planted := f.login("mallory", "correct-horse-9")
	var plantedVal string
	for _, c := range planted.rec.Result().Cookies() {
		if c.Name == name {
			plantedVal = c.Value
		}
	}
	dup := f.do("POST", "/auth/refresh", nil, webClient(), with(plantedVal, ck.Value))
	require.Equal(t, 401, dup.rec.Code, dup.rec.Body.String())
	require.NotContains(t, dup.rec.Body.String(), "auth.sessionEnded", "本来的会话可能还活着，不说会话已结束")
	require.Contains(t, f.securityEvents(), "refresh_cookie_dup")
	// 只有自己的那个：照常刷新
	ok := f.do("POST", "/auth/refresh", nil, webClient(), with(ck.Value))
	require.Equal(t, 200, ok.rec.Code, ok.rec.Body.String())

	// debug 模式名字不带前缀、路径只到认证接口；重复的同样拒绝
	d := newAuthFixture(t)
	d.addUser("bob", "correct-horse-9")
	_, rt := d.mustLogin("bob", "correct-horse-9")
	require.Equal(t, 401, d.do("POST", "/auth/refresh", nil, webClient(), cookieOpt("0"+rt[1:]), cookieOpt(rt)).rec.Code)
	require.Equal(t, 200, d.refresh(rt).rec.Code)
}
