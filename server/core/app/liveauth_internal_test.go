package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// liveTestModule 注册 test 端：一条走缓存的路由，和一个"按库核对"的分组（含子分组、一条公开路由）。
type liveTestModule struct{ users *memUsers }

func (m *liveTestModule) Name() string { return "livetest" }
func (m *liveTestModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: testPortal, Users: m.users})
}
func (m *liveTestModule) Perms() []rbac.Perm     { return nil }
func (m *liveTestModule) Menus() []rbac.MenuNode { return nil }
func (m *liveTestModule) Routes(r *Router) {
	who := func(c *gin.Context) {
		httpx.OK(c, gin.H{"user": auth.MustFromCtx(c.Request.Context()).Username})
	}
	r.Portal(testPortal).GET("/ping", rbac.AuthOnly(), who)
	grp := r.Portal(testPortal).Group("/live")
	live := grp.LiveAuth()
	live.GET("/ping", rbac.AuthOnly(), who)
	live.Group("/sub").GET("/ping", rbac.AuthOnly(), who)
	live.GET("/open", rbac.Public(), func(c *gin.Context) { httpx.OK(c, gin.H{"open": true}) })
	// 同一个路径：GET 用按库核对的入口注册，POST 用原来的分组注册——LiveAuth 不改原来的分组，
	// 认证器按"方法 + 路径"认，不是只按路径
	live.GET("/mixed", rbac.AuthOnly(), who)
	grp.POST("/mixed", rbac.AuthOnly(), who)
	// 写接口、带参数的路径、分组的根路径：标记跟着路由自己的处理链走，和路径怎么写无关
	live.POST("/act", rbac.AuthOnly(), who)
	live.GET("/item/:id", rbac.AuthOnly(), who)
	live.GET("/", rbac.AuthOnly(), who)
}
func (m *liveTestModule) Start(context.Context) error { return nil }
func (m *liveTestModule) Stop(context.Context) error  { return nil }

func newLiveFixture(t *testing.T) *authFixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.AllowedOrigins = []string{testOrigin}
	cfg.Portals[testPortal] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: testSecret}
	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	users := newMemUsers()
	a, err := New(cfg, WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithClock(clock.Now), WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(&liveTestModule{users: users})
	require.NoError(t, a.Setup())
	return &authFixture{t: t, app: a, users: users, clock: clock}
}

// 规范 §13.2 第 162 条（D-073）：标了"按库核对"的分组下的路由（子分组继承；公开路由不算），认证时不用会话状态、
// 账号状态两个缓存。别的程序改了库（这里直接改库、改用户来源，不经过本程序的认证器）：
// 按库核对的路由在下一个请求就拒绝；没标的路由在缓存到期之前照旧放行——这是它们保留的窗口；
// 按库核对的那一次读到了变化，顺手把缓存清掉，没标的路由随后也拒绝。
func TestAuth_162_LiveAuthSkipsStatusCache(t *testing.T) {
	f := newLiveFixture(t)
	get := func(tok, path string) resp { return f.do("GET", path, nil, bearerOpt(tok)) }
	finds := func() int {
		f.users.mu.Lock()
		defer f.users.mu.Unlock()
		return f.users.finds
	}

	// 路由表：分组和子分组下需要登录的路由标上了，公开的、别的分组的、认证接口都没标
	want := map[string]bool{
		"GET /api/test/v1/live/ping": true, "GET /api/test/v1/live/sub/ping": true, "GET /api/test/v1/live/mixed": true,
		"POST /api/test/v1/live/act": true, "GET /api/test/v1/live/item/:id": true, "GET /api/test/v1/live": true,
	}
	got := map[string]bool{}
	for _, r := range f.app.Routes() {
		if r.LiveAuth {
			got[r.Method+" "+r.Path] = true
		}
	}
	require.Equal(t, want, got)
	require.Equal(t, 0, f.do("GET", "/live/open", nil).env.Code, "公开路由照常不用登录")

	// 没有变化时：按库核对的路由每个请求都读库；没标的路由读一次之后走缓存
	alice := f.addUser("alice", "correct-horse-9")
	tok, _ := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	base := finds()
	for range 3 {
		require.Equal(t, 0, get(tok, "/ping").env.Code)
	}
	require.Equal(t, base, finds(), "没标的路由走缓存")
	for i, path := range []string{"/live/ping", "/live/sub/ping", "/live/mixed"} {
		require.Equal(t, 0, get(tok, path).env.Code, path)
		require.Equal(t, base+i+1, finds(), "%s 每个请求读一次库", path)
	}
	require.Equal(t, 0, f.do("POST", "/live/mixed", nil, bearerOpt(tok)).env.Code)
	require.Equal(t, base+3, finds(), "同一个路径的 POST 没标：走缓存")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	require.Equal(t, base+3, finds(), "按库核对读到的和缓存一样：缓存照常留着，没标的路由不用重新读库")

	// 会话在库里被吊销（别的程序做的）
	var sid string
	require.NoError(t, f.app.deps.DB.Raw("SELECT sid FROM ga_session WHERE portal = ? AND user_id = ? AND revoked_at IS NULL", testPortal, alice).Scan(&sid).Error)
	require.NoError(t, f.app.deps.DB.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'elsewhere' WHERE sid = ?", sid).Error)
	require.Equal(t, 0, get(tok, "/ping").env.Code, "没标的路由：缓存到期之前照旧放行")
	require.Equal(t, 0, f.do("POST", "/live/mixed", nil, bearerOpt(tok)).env.Code)
	r := get(tok, "/live/sub/ping")
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.Equal(t, 401, get(tok, "/ping").rec.Code, "按库核对读到会话已吊销，缓存也清了：没标的路由随后也拒绝")

	// 账号在别处被停用
	bob := f.addUser("bob", "correct-horse-9")
	tok, _ = f.mustLogin("bob", "correct-horse-9")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	require.Equal(t, 0, get(tok, "/live/ping").env.Code)
	f.users.set(bob, func(a *portal.Account) { a.Status = 0 })
	require.Equal(t, 0, get(tok, "/ping").env.Code, "没标的路由：缓存到期之前照旧放行")
	require.Equal(t, 401, get(tok, "/live/ping").rec.Code)
	require.Equal(t, 401, get(tok, "/ping").rec.Code)

	// 账号在别处被要求改密
	carol := f.addUser("carol", "correct-horse-9")
	tok, _ = f.mustLogin("carol", "correct-horse-9")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	f.users.set(carol, func(a *portal.Account) { a.MustChangePwd = true })
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	require.Equal(t, httpx.CodePwdChangeRequired, get(tok, "/live/mixed").env.Code)
	require.Equal(t, httpx.CodePwdChangeRequired, get(tok, "/ping").env.Code)

	// 账号在别处被删掉
	dave := f.addUser("dave", "correct-horse-9")
	tok, _ = f.mustLogin("dave", "correct-horse-9")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	f.users.mu.Lock()
	delete(f.users.byID, dave)
	f.users.mu.Unlock()
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	require.Equal(t, 401, get(tok, "/live/ping").rec.Code)
	require.Equal(t, 401, get(tok, "/ping").rec.Code)

	// 写接口、带参数的路径、分组的根路径一样按库核对
	for i, rt := range [][2]string{{"POST", "/live/act"}, {"GET", "/live/item/7"}, {"GET", "/live/"}} {
		name := []string{"gina", "hank", "iris"}[i]
		id := f.addUser(name, "correct-horse-9")
		tok, _ = f.mustLogin(name, "correct-horse-9")
		require.Equal(t, 0, get(tok, "/ping").env.Code)
		require.Equal(t, 0, f.do(rt[0], rt[1], nil, bearerOpt(tok)).env.Code, rt)
		f.users.set(id, func(a *portal.Account) { a.Status = 0 })
		require.Equal(t, 0, get(tok, "/ping").env.Code, rt)
		require.Equal(t, 401, f.do(rt[0], rt[1], nil, bearerOpt(tok)).rec.Code, rt)
	}

	// 读库期间缓存正好有过别的失效（代数变了，这次读到的状态不会写回缓存）：缓存里旧的那一条也要清掉，
	// 不然没标的路由还会拿着它放行
	frank := f.addUser("frank", "correct-horse-9")
	tok, _ = f.mustLogin("frank", "correct-horse-9")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	f.users.mu.Lock()
	f.users.byID[frank].Status = 0
	f.users.afterFind = func() { f.app.authenticators[testPortal].ForgetAccount(999999) }
	f.users.mu.Unlock()
	require.Equal(t, 401, get(tok, "/live/ping").rec.Code)
	require.Equal(t, 401, get(tok, "/ping").rec.Code)

	// 读库出错：按库核对的路由回 503，不当成"没问题"放过去
	erin := f.addUser("erin", "correct-horse-9")
	_ = erin
	tok, _ = f.mustLogin("erin", "correct-horse-9")
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	f.users.mu.Lock()
	f.users.failNext = context.DeadlineExceeded
	f.users.mu.Unlock()
	require.Equal(t, 503, get(tok, "/live/ping").rec.Code)
	require.Equal(t, 0, get(tok, "/live/ping").env.Code)
	// 会话表读不了也一样（没标的路由这时还拿着缓存放行）
	require.NoError(t, f.app.deps.DB.Exec("RENAME TABLE ga_session TO ga_session_gone").Error)
	require.Equal(t, 503, get(tok, "/live/ping").rec.Code)
	require.Equal(t, 0, get(tok, "/ping").env.Code)
	require.NoError(t, f.app.deps.DB.Exec("RENAME TABLE ga_session_gone TO ga_session").Error)
	require.Equal(t, 0, get(tok, "/live/ping").env.Code)
}
