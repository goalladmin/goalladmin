package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

func testConfig() *conf.Config {
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.MaxBodyBytes = 64
	return cfg
}

func newApp(t *testing.T, opts ...app.Option) *app.App {
	t.Helper()
	opts = append([]app.Option{app.WithDB(nil), app.WithLogger(logx.New("error", "text", io.Discard))}, opts...)
	a, err := app.New(testConfig(), opts...)
	require.NoError(t, err)
	return a
}

func call(t *testing.T, h http.Handler, method, target string, body string, headers map[string]string) (*httptest.ResponseRecorder, httpx.Envelope) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "响应必须是统一信封: %s", w.Body.String())
	return w, env
}

// ---- 一个记录调用顺序的假模块 ----

type fakeModule struct {
	name  string
	calls *[]string
	perms []rbac.Perm
	menus []rbac.MenuNode
	route func(r *app.Router)
	fail  string
}

func (m *fakeModule) rec(s string) { *m.calls = append(*m.calls, m.name+":"+s) }
func (m *fakeModule) Name() string { return m.name }
func (m *fakeModule) Init(d *app.Deps) error {
	m.rec("init")
	if d.Conf == nil || d.Log == nil || d.Perms == nil {
		return errors.New("deps 不完整")
	}
	if m.fail == "init" {
		return errors.New("init failed")
	}
	return nil
}
func (m *fakeModule) Perms() []rbac.Perm     { m.rec("perms"); return m.perms }
func (m *fakeModule) Menus() []rbac.MenuNode { m.rec("menus"); return m.menus }
func (m *fakeModule) Routes(r *app.Router) {
	m.rec("routes")
	if m.route != nil {
		m.route(r)
	}
}
func (m *fakeModule) Start(ctx context.Context) error {
	m.rec("start")
	if m.fail == "start" {
		return errors.New("start failed")
	}
	return nil
}
func (m *fakeModule) Stop(ctx context.Context) error { m.rec("stop"); return nil }

func TestNew_RejectsInvalidConfig(t *testing.T) {
	cfg := testConfig()
	cfg.Server.Mode = conf.ModeRelease // release 缺密钥
	_, err := app.New(cfg, app.WithDB(nil))
	require.ErrorIs(t, err, conf.ErrInvalidConfig)
}

func TestHealth(t *testing.T) {
	a := newApp(t)
	w, env := call(t, a.Handler(), "GET", "/healthz", "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, 0, env.Code)
	require.NotContains(t, w.Body.String(), "version")

	w, env = call(t, a.Handler(), "GET", "/readyz", "", nil)
	require.Equal(t, 503, w.Code, "没有数据库时 readyz 必须 503")
	require.Equal(t, httpx.CodeUnavailable, env.Code)
}

func TestNoRouteAndNoMethodAreEnvelopes(t *testing.T) {
	a := newApp(t)
	w, env := call(t, a.Handler(), "POST", "/api/platform/v1/auth/login", `{}`, nil)
	require.Equal(t, 404, w.Code)
	require.Equal(t, httpx.CodeNotFound, env.Code)
	require.NotEmpty(t, env.RequestID)

	w, env = call(t, a.Handler(), "POST", "/healthz", "", nil)
	require.Equal(t, 405, w.Code)
	require.Equal(t, httpx.CodeMethodNotAllowed, env.Code)
}

func TestRequestID_EchoOrGenerate(t *testing.T) {
	a := newApp(t)
	w, env := call(t, a.Handler(), "GET", "/healthz", "", map[string]string{httpx.HeaderRequestID: "client-id-0001"})
	require.Equal(t, "client-id-0001", env.RequestID)
	require.Equal(t, "client-id-0001", w.Header().Get(httpx.HeaderRequestID))

	w, env = call(t, a.Handler(), "GET", "/healthz", "", map[string]string{httpx.HeaderRequestID: "bad id!"})
	require.NotEqual(t, "bad id!", env.RequestID)
	require.Len(t, env.RequestID, 32)
	require.Equal(t, env.RequestID, w.Header().Get(httpx.HeaderRequestID))
}

func TestSecurityHeaders(t *testing.T) {
	a := newApp(t)
	w, _ := call(t, a.Handler(), "GET", "/healthz", "", nil)
	require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", w.Header().Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"))
}

func TestBodyLimit_413BeforeHandlerRuns(t *testing.T) {
	a := newApp(t)
	ran := false
	a.Register(&fakeModule{name: "m", calls: new([]string), route: func(r *app.Router) {
		r.Raw("POST", "/echo", "测试", func(c *gin.Context) {
			ran = true
			var v map[string]any
			if err := httpx.BindJSON(c, &v); err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, v)
		})
	}})
	require.NoError(t, a.Setup())

	big := `{"x":"` + strings.Repeat("a", 200) + `"}`
	w, env := call(t, a.Handler(), "POST", "/echo", big, map[string]string{"Content-Type": "application/json"})
	require.Equal(t, 413, w.Code)
	require.Equal(t, httpx.CodeBodyTooLarge, env.Code)
	require.False(t, ran, "带 Content-Length 的超限请求不能进 handler")

	// 分块传输（无 Content-Length）：在 handler 读取时超限，同样 413
	req := httptest.NewRequest("POST", "/echo", io.NopCloser(bytes.NewReader([]byte(big))))
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	require.Equal(t, 413, rec.Code)

	w, env = call(t, a.Handler(), "POST", "/echo", `{"x":1}`, map[string]string{"Content-Type": "application/json"})
	require.Equal(t, 200, w.Code)
	require.Equal(t, 0, env.Code)
}

func TestRecovery_500WithoutLeak(t *testing.T) {
	a := newApp(t)
	a.Register(&fakeModule{name: "m", calls: new([]string), route: func(r *app.Router) {
		r.Raw("GET", "/boom", "测试", func(c *gin.Context) { panic("secret internal detail") })
	}})
	require.NoError(t, a.Setup())
	w, env := call(t, a.Handler(), "GET", "/boom", "", nil)
	require.Equal(t, 500, w.Code)
	require.Equal(t, httpx.CodeInternal, env.Code)
	require.NotContains(t, w.Body.String(), "secret internal detail")
}

func TestModuleLifecycleOrder(t *testing.T) {
	a := newApp(t)
	var calls []string
	a.Register(
		&fakeModule{name: "a", calls: &calls, perms: []rbac.Perm{{Code: "a:thing:list", Name: "list", Portal: "platform"}}},
		&fakeModule{name: "b", calls: &calls},
	)
	ctx := context.Background()
	require.NoError(t, a.Start(ctx))
	require.NoError(t, a.Stop(ctx))
	require.Equal(t, []string{
		"a:init", "b:init",
		"a:perms", "a:menus", "b:perms", "b:menus",
		"a:routes", "b:routes",
		"a:start", "b:start",
		"b:stop", "a:stop", // 逆序停止
	}, calls)
	require.True(t, a.Deps().Perms.Has("platform", "a:thing:list"))
}

func TestStart_StopsAlreadyStartedModulesOnFailure(t *testing.T) {
	a := newApp(t)
	var calls []string
	a.Register(&fakeModule{name: "ok", calls: &calls}, &fakeModule{name: "bad", calls: &calls, fail: "start"})
	err := a.Start(context.Background())
	require.ErrorContains(t, err, "bad 启动失败")
	require.Contains(t, calls, "ok:stop", "启动失败时已启动的模块要被停掉")
}

func TestRegister_DuplicateNamePanics(t *testing.T) {
	a := newApp(t)
	c := new([]string)
	require.Panics(t, func() { a.Register(&fakeModule{name: "x", calls: c}, &fakeModule{name: "x", calls: c}) })
}

func TestSetup_RejectsBadDeclarations(t *testing.T) {
	a := newApp(t)
	a.Register(&fakeModule{name: "m", calls: new([]string), perms: []rbac.Perm{{Code: "BadCode", Portal: "platform"}}})
	require.ErrorContains(t, a.Setup(), "权限码")

	b := newApp(t)
	b.Register(&fakeModule{name: "m", calls: new([]string), menus: []rbac.MenuNode{{Portal: "platform", Name: "child", Parent: "missing"}}})
	require.ErrorContains(t, b.Setup(), "父节点")

	c := newApp(t)
	c.Register(
		&fakeModule{name: "m1", calls: new([]string), perms: []rbac.Perm{{Code: "x:y:z", Name: "1", Portal: "platform"}}},
		&fakeModule{name: "m2", calls: new([]string), perms: []rbac.Perm{{Code: "x:y:z", Name: "2", Portal: "platform"}}},
	)
	require.ErrorContains(t, c.Setup(), "已由")
}

func TestRaw_RequiresPurposeAndIsListed(t *testing.T) {
	a := newApp(t)
	require.Panics(t, func() { a.Router().Raw("GET", "/x", "", func(*gin.Context) {}) })
	a.Router().Raw("GET", "/x", "测试用", func(c *gin.Context) { httpx.OK(c, nil) })
	paths := make([]string, 0, len(a.RawRoutes()))
	for _, r := range a.RawRoutes() {
		paths = append(paths, r.Path)
	}
	require.Contains(t, paths, "/healthz")
	require.Contains(t, paths, "/readyz")
	require.Contains(t, paths, "/x")
}

func TestPortal_UnknownPanics(t *testing.T) {
	a := newApp(t)
	require.Panics(t, func() { a.Router().Portal("nope") })
}

func TestRequestContextCarriesDB(t *testing.T) {
	gdb := db.OpenTestDB(t)
	a, err := app.New(testConfig(), app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.Register(&fakeModule{name: "m", calls: new([]string), route: func(r *app.Router) {
		r.Raw("GET", "/one", "测试", func(c *gin.Context) {
			var n int
			if err := db.From(c.Request.Context()).Raw("SELECT 1").Scan(&n).Error; err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, n)
		})
	}})
	require.NoError(t, a.Migrate(context.Background()), "Setup 之前必须先迁移：授权服务要读策略表")
	require.NoError(t, a.Setup())
	w, env := call(t, a.Handler(), "GET", "/one", "", nil)
	require.Equal(t, 200, w.Code)
	require.EqualValues(t, 1, env.Data)

	w, _ = call(t, a.Handler(), "GET", "/readyz", "", nil)
	require.Equal(t, 200, w.Code)

	var cnt int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_role").Scan(&cnt).Error)
	require.EqualValues(t, 1, cnt)
}

func TestHandlerTimeout_CancelsRequestContextWith503(t *testing.T) {
	cfg := testConfig()
	cfg.Server.HandlerTimeout = 100 * time.Millisecond
	a, err := app.New(cfg, app.WithDB(nil), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.Register(&fakeModule{name: "m", calls: new([]string), route: func(r *app.Router) {
		r.Raw("GET", "/hang", "测试", func(c *gin.Context) {
			select {
			case <-c.Request.Context().Done():
				httpx.Fail(c, c.Request.Context().Err())
			case <-time.After(5 * time.Second):
				httpx.OK(c, nil)
			}
		})
		r.Raw("GET", "/fast", "测试", func(c *gin.Context) {
			dl, ok := c.Request.Context().Deadline()
			httpx.OK(c, ok && time.Until(dl) <= 100*time.Millisecond)
		})
	}})
	require.NoError(t, a.Setup())

	start := time.Now()
	w, env := call(t, a.Handler(), "GET", "/hang", "", nil)
	require.Equal(t, 503, w.Code)
	require.Equal(t, httpx.CodeUnavailable, env.Code)
	require.Less(t, time.Since(start), 2*time.Second)

	w, env = call(t, a.Handler(), "GET", "/fast", "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, true, env.Data, "每个请求的 ctx 都带着处理时限")
}

func TestHandlerTimeout_AbortsHungQueryAndReleasesConnection(t *testing.T) {
	gdb := db.OpenTestDB(t)
	cfg := testConfig()
	cfg.Server.HandlerTimeout = 300 * time.Millisecond
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.Register(&fakeModule{name: "m", calls: new([]string), route: func(r *app.Router) {
		r.Raw("GET", "/slow", "测试", func(c *gin.Context) {
			if err := db.From(c.Request.Context()).Exec("SELECT SLEEP(5)").Error; err != nil {
				httpx.Fail(c, err)
				return
			}
			httpx.OK(c, nil)
		})
	}})
	require.NoError(t, a.Migrate(context.Background()))
	require.NoError(t, a.Setup())

	start := time.Now()
	w, env := call(t, a.Handler(), "GET", "/slow", "", nil)
	require.Equal(t, 503, w.Code)
	require.Equal(t, httpx.CodeUnavailable, env.Code)
	require.Less(t, time.Since(start), 2*time.Second, "数据库不回话时到点就放弃，不等查询自己结束")

	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.Equal(t, 0, sqlDB.Stats().InUse, "超时的查询要把连接还回池子")

	// 请求已经超时，之后才写的错误日志不能因此丢掉
	var n int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_error_log WHERE route = '/slow' AND code = ?", httpx.CodeUnavailable).Scan(&n).Error)
	require.EqualValues(t, 1, n)

	w, _ = call(t, a.Handler(), "GET", "/readyz", "", nil)
	require.Equal(t, 200, w.Code, "超时之后服务照常可用")
}

// 生产路径：app 自己按配置连库，迁移走单独的连接池（D-037），完成后关掉、主连接池照常可用。
func TestMigrate_UsesDedicatedPoolWhenAppOpensDB(t *testing.T) {
	gdb := db.OpenTestDB(t)
	var name string
	require.NoError(t, gdb.Raw("SELECT DATABASE()").Scan(&name).Error)

	dsn := os.Getenv(db.TestDSNEnv)
	cred, rest, _ := strings.Cut(dsn, "@tcp(")
	user, pw, _ := strings.Cut(cred, ":")
	addr, rest, _ := strings.Cut(rest, ")/")
	_, params, _ := strings.Cut(rest, "?")
	host, portStr, _ := strings.Cut(addr, ":")
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)

	cfg := testConfig()
	cfg.Database.Host, cfg.Database.Port, cfg.Database.User, cfg.Database.Password = host, port, user, pw
	cfg.Database.Name, cfg.Database.Params = name, params
	a, err := app.New(cfg, app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	defer func() { _ = a.Stop(context.Background()) }()

	require.NoError(t, a.Migrate(context.Background()))
	var n int64
	require.NoError(t, a.Deps().DB.Raw("SELECT COUNT(*) FROM ga_role WHERE code = 'super'").Scan(&n).Error)
	require.EqualValues(t, 1, n)
	sqlDB, err := a.Deps().DB.DB()
	require.NoError(t, err)
	require.Equal(t, cfg.Database.MaxOpenConns, sqlDB.Stats().MaxOpenConnections, "主连接池不是迁移用的那个")
}

// 182（D-098）：debug 模式监听非回环地址时，启动时有一条 WARN；只听本机时没有。
func TestRun_182_DebugOnNonLoopbackWarns(t *testing.T) {
	run := func(addr string) string {
		cfg := conf.Default()
		cfg.Server.Addr = addr
		var buf bytes.Buffer
		a, err := app.New(cfg, app.WithDB(nil), app.WithLogger(logx.New("warn", "text", &buf)))
		require.NoError(t, err)
		require.NoError(t, a.Setup())
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		cancel() // 告警在开始监听之前就打了；输出在 Run 返回之后才读
		require.NoError(t, <-done)
		return buf.String()
	}
	out := run("0.0.0.0:0")
	require.Contains(t, out, "level=WARN")
	require.Contains(t, out, "debug mode is listening on a non-loopback address")
	require.Contains(t, out, "GA_SERVER_MODE=release")
	require.NotContains(t, run("127.0.0.1:0"), "non-loopback")
}
