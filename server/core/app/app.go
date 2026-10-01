// Package app 是应用的生命周期与装配：配置校验、日志、数据库、路由、模块注册、优雅关停。
//
// main.go 只做四件事：读配置 → app.New → 显式 Register 模块 → Run。
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/dict"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/auditimpl"
	"github.com/goalladmin/goalladmin/server/core/internal/authimpl"
	"github.com/goalladmin/goalladmin/server/core/internal/captcha"
	"github.com/goalladmin/goalladmin/server/core/internal/metrics"
	"github.com/goalladmin/goalladmin/server/core/internal/middleware"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/monitor"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// Deps 是注入给模块的依赖。模块在 Init 里拿到，通过构造函数传给自己的 service、repo。
type Deps struct {
	Conf    *conf.Config
	Log     *slog.Logger
	DB      *gorm.DB
	Perms   *rbac.Registry   // 权限码与菜单注册表；Setup 后只读
	Portals *portal.Registry // 端注册表；模块在 Init 里注册自己的端
	Auth    auth.Service     // 会话与密码能力；Init 阶段为 nil，Routes 起可用
	RBAC    *rbac.Service    // 授权服务；Init 阶段为 nil，Routes 起可用（无数据库时为 nil）
	Dict    *dict.Service    // 字典服务；Init 阶段为 nil，Routes 起可用（无数据库时为 nil）
	Monitor monitor.Service  // 服务器状态（D-030）；New 之后即可用
	Audit   audit.Service    // 安全审计（D-032）：错误日志、安全事件、调查时间线；New 之后即可用
}

// Module 是业务模块的接口（规范 §4.3）。
type Module interface {
	Name() string
	Init(deps *Deps) error
	Perms() []rbac.Perm
	Menus() []rbac.MenuNode
	Routes(r *Router)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// MigrationSource 是模块可选实现的接口：提供自己的迁移目录和版本表（规范 §8.2）。
type MigrationSource interface {
	Migrations() (fsys fs.FS, dir string, table string)
}

// DictSource 是模块可选实现的接口：在代码里声明字典（docs/decisions.md D-023）。
// 启动时校验并同步进库；编码必须以"模块名."开头。
type DictSource interface {
	Dicts() []dict.Dict
}

// DataResourceSource 是模块可选实现的接口：在代码里声明数据资源（按部门的数据权限，D-039）。
// 编码必须以"模块名:"开头，只能约束本模块的权限码。
type DataResourceSource interface {
	DataResources() []rbac.DataResource
}

// Option 定制 App 的构造。
type Option func(*App)

// WithDB 注入已打开的数据库句柄（测试用）；传 nil 表示不连数据库。
func WithDB(gdb *gorm.DB) Option {
	return func(a *App) { a.deps.DB = gdb; a.dbProvided = true }
}

// WithLogger 注入 logger（测试用）。
func WithLogger(l *slog.Logger) Option {
	return func(a *App) { a.deps.Log = l }
}

// WithClock 注入时钟（测试用，用来模拟令牌过期、刷新宽限期）。
func WithClock(now func() time.Time) Option {
	return func(a *App) { a.now = now }
}

// WithBcryptCost 调整密码哈希代价（测试用，降低到 4 加快测试）。
func WithBcryptCost(cost int) Option {
	return func(a *App) { a.bcryptCost = cost }
}

// WithStatusCacheTTL 调整会话与账号状态缓存的时长（测试用）。
func WithStatusCacheTTL(ttl time.Duration) Option {
	return func(a *App) { a.statusCacheTTL = ttl }
}

// App 是一个进程里的应用实例。
type App struct {
	deps       *Deps
	dbProvided bool
	engine     *gin.Engine
	router     *Router
	modules    []Module
	started    []Module
	setupDone  bool

	portalGroups   map[string]*gin.RouterGroup
	authenticators map[string]*authimpl.Authenticator
	authorizer     Authorizer
	captcha        *captcha.Captcha
	metrics        *metrics.Collector
	audit          *auditimpl.Recorder
	guardResolver  GuardResolver
	routes         []Route
	rawRoutes      []RawRoute

	now            func() time.Time
	bcryptCost     int
	pwdParallel    int // 同时核对密码的上限，0 用默认值；只有测试会设（D-058）
	statusCacheTTL time.Duration
}

// New 校验配置、初始化日志和数据库、装配 HTTP 引擎。不启动任何东西。
func New(cfg *conf.Config, opts ...Option) (*App, error) {
	if cfg == nil {
		return nil, errors.New("app: 配置为空")
	}
	if err := conf.Validate(cfg); err != nil {
		return nil, err
	}
	a := &App{
		deps:           &Deps{Conf: cfg, Perms: rbac.NewRegistry(), Portals: portal.NewRegistry()},
		portalGroups:   map[string]*gin.RouterGroup{},
		authenticators: map[string]*authimpl.Authenticator{},
		now:            time.Now,
	}
	for _, o := range opts {
		o(a)
	}
	if a.deps.Log == nil {
		a.deps.Log = logx.New(cfg.Log.Level, cfg.Log.Format, nil)
	}
	slog.SetDefault(a.deps.Log)

	if !a.dbProvided {
		gdb, err := db.Open(cfg.Database, a.deps.Log)
		if err != nil {
			return nil, err
		}
		a.deps.DB = gdb
	}

	if cfg.Server.IsRelease() {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}
	// gin 自带的调试输出（路由表、警告）不需要：路由清单由 Setup 打日志，请求日志由 AccessLog 记。
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	e := gin.New()
	e.HandleMethodNotAllowed = true
	e.RedirectTrailingSlash = false
	e.RedirectFixedPath = false
	if err := e.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return nil, fmt.Errorf("app: trustedProxies: %w", err)
	}
	// 请求统计放在最前面，耗时包含整个处理过程；健康检查不计入（D-030）。
	// 配置关掉服务器状态时（D-031）不挂这个中间件，什么都不采集。
	a.metrics = metrics.New(func() time.Time { return a.now() }, healthPath, readyPath)
	a.deps.Monitor = &monitorService{c: a.metrics, db: a.deps.DB, enabled: cfg.Monitor.Server, ttl: cfg.Monitor.ServerCache, now: func() time.Time { return a.now() }}
	if cfg.Monitor.Server {
		e.Use(a.metrics.Middleware())
	}
	// 审计排在 Recovery 外层：请求结束后能看到 panic 转成的 500（D-032）。
	a.audit = auditimpl.New(auditimpl.Options{DB: a.deps.DB, Log: a.deps.Log, Now: func() time.Time { return a.now() }, Skip: []string{healthPath, readyPath}})
	a.deps.Audit = a.audit
	e.Use(
		middleware.ContextLogger(a.deps.Log),
		middleware.RequestID(),
		a.contextDB(),
		middleware.Deadline(cfg.Server.HandlerTimeout),
		middleware.AccessLog(),
		a.audit.Middleware(),
		middleware.Recovery(),
		middleware.SecurityHeaders(),
		middleware.BodyLimit(cfg.Server.MaxBodyBytes),
	)
	e.NoRoute(middleware.NoRoute())
	e.NoMethod(middleware.NoMethod())
	a.engine = e
	a.router = &Router{app: a}
	a.registerHealth()
	return a, nil
}

// Register 显式注册模块。名字重复是装配错误，直接 panic。
func (a *App) Register(mods ...Module) {
	for _, m := range mods {
		if m == nil {
			panic("app: Register 收到 nil 模块")
		}
		for _, existing := range a.modules {
			if existing.Name() == m.Name() {
				panic(fmt.Sprintf("app: 模块 %q 重复注册", m.Name()))
			}
		}
		a.modules = append(a.modules, m)
	}
}

// Deps 返回依赖集合（CLI 命令和测试用）。
func (a *App) Deps() *Deps { return a.deps }

// Handler 返回 http.Handler（测试用 httptest）。
func (a *App) Handler() http.Handler { return a.engine }

// Router 返回路由注册入口（测试用；模块在 Routes 回调里拿到同一个对象）。
func (a *App) Router() *Router { return a.router }

// SetGuardResolver 安装守卫解析器。由认证/授权装配代码调用，模块不要调用。
func (a *App) SetGuardResolver(r GuardResolver) { a.guardResolver = r }

// Context 返回带 logger 和数据库句柄的 ctx，供 CLI 命令和后台任务使用。
func (a *App) Context(parent context.Context) context.Context {
	ctx := logx.WithLogger(parent, a.deps.Log)
	if a.deps.DB != nil {
		ctx = db.WithDB(ctx, a.deps.DB)
	}
	return ctx
}

// MigrationSet 是一份迁移来源：框架自己的，或某个实现了 MigrationSource 的模块的。
type MigrationSet struct {
	Name  string // "core" 或模块名
	FS    fs.FS
	Dir   string
	Table string
}

// MigrationSets 按执行顺序返回全部迁移来源：框架在前，模块按注册顺序。
func (a *App) MigrationSets() []MigrationSet {
	out := []MigrationSet{{Name: "core", FS: migrations.Core(), Dir: migrations.CoreDir, Table: migrations.CoreTable}}
	for _, m := range a.modules {
		if src, ok := m.(MigrationSource); ok {
			fsys, dir, table := src.Migrations()
			out = append(out, MigrationSet{Name: m.Name(), FS: fsys, Dir: dir, Table: table})
		}
	}
	return out
}

// Migrate 执行框架迁移和各模块的迁移。
func (a *App) Migrate(ctx context.Context) error {
	if a.deps.DB == nil {
		return errors.New("app: 没有数据库连接，无法迁移")
	}
	ctx = a.Context(ctx)
	// 迁移用单独的连接池，不带默认的读写超时（D-037）；测试注入的句柄直接用
	mdb := a.deps.DB
	if !a.dbProvided {
		m, err := db.OpenMigrator(a.deps.Conf.Database, a.deps.Log)
		if err != nil {
			return fmt.Errorf("app: 迁移连接: %w", err)
		}
		defer func() { _ = db.Close(m) }()
		mdb = m
	}
	for _, set := range a.MigrationSets() {
		applied, err := db.MigrateUp(ctx, mdb, set.FS, set.Dir, set.Table)
		if err != nil {
			return fmt.Errorf("%s: %w", set.Name, err)
		}
		a.deps.Log.Info("migrations applied", "source", set.Name, "table", set.Table, "applied", len(applied))
	}
	return nil
}

// Setup 初始化模块并注册权限、菜单、路由。可重复调用，只执行一次。
func (a *App) Setup() error {
	if a.setupDone {
		return nil
	}
	for _, m := range a.modules {
		if err := m.Init(a.deps); err != nil {
			return fmt.Errorf("app: 模块 %s 初始化失败: %w", m.Name(), err)
		}
	}
	if err := a.setupPortals(); err != nil {
		return err
	}
	for _, m := range a.modules {
		if err := a.deps.Perms.AddPerms(m.Name(), m.Perms()); err != nil {
			return fmt.Errorf("app: %w", err)
		}
		if err := a.deps.Perms.AddMenus(m.Name(), m.Menus()); err != nil {
			return fmt.Errorf("app: %w", err)
		}
		if src, ok := m.(DataResourceSource); ok {
			if err := a.deps.Perms.AddDataResources(m.Name(), src.DataResources()); err != nil {
				return fmt.Errorf("app: %w", err)
			}
		}
	}
	if err := a.deps.Perms.Finalize(); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	if a.deps.DB != nil && a.authorizer == nil {
		svc, err := rbac.NewService(rbac.Options{
			Registry: a.deps.Perms, Base: a.Context(context.Background()), Log: a.deps.Log, Now: a.now, CacheTTL: a.statusCacheTTL,
			Portals: a.deps.Portals,
			// 锁内认定操作人时确认会话仍然有效（D-046）：用会话表的普通读，不走状态缓存
			SessionActive: func(ctx context.Context, portal, sid string) (bool, error) {
				an, ok := a.authenticators[portal]
				if !ok {
					return false, nil
				}
				sess, err := an.Sessions().Get(ctx, sid)
				if err != nil || sess == nil {
					return false, err
				}
				return sess.Portal == portal && sess.Active(a.now().UTC()), nil
			},
			// 本人写操作在事务里锁住自己的会话行再确认有效（D-059）：吊销写的也是这一行，两边排队
			LockSession: func(ctx context.Context, portal, sid string) (bool, error) {
				an, ok := a.authenticators[portal]
				if !ok {
					return false, nil
				}
				err := an.Sessions().LockActive(ctx, portal, sid)
				if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInactive) {
					return false, nil
				}
				return err == nil, err
			},
		})
		if err != nil {
			return err
		}
		a.deps.RBAC = svc
		a.SetAuthorizer(svc)
	}
	if err := a.setupDicts(); err != nil {
		return err
	}
	for _, m := range a.modules {
		m.Routes(a.router)
	}
	a.setupDone = true
	for _, r := range a.rawRoutes {
		a.deps.Log.Info("raw route", "method", r.Method, "path", r.Path, "purpose", r.Purpose)
	}
	a.deps.Log.Info("routes registered", "portal_routes", len(a.routes), "raw_routes", len(a.rawRoutes), "modules", len(a.modules))
	return nil
}

// setupDicts 收集并校验模块声明的字典；有数据库时创建字典服务并把声明同步进库。
func (a *App) setupDicts() error {
	var decls []dict.Decl
	for _, m := range a.modules {
		if src, ok := m.(DictSource); ok {
			for _, d := range src.Dicts() {
				decls = append(decls, dict.Decl{Module: m.Name(), Dict: d})
			}
		}
	}
	portalOK := func(code string) bool { _, ok := a.deps.Portals.Get(code); return ok }
	if err := dict.ValidateDecls(decls, portalOK); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	if a.deps.DB == nil {
		return nil
	}
	svc := dict.NewService(dict.Options{Log: a.deps.Log, Now: a.now, CacheTTL: a.statusCacheTTL, PortalOK: portalOK})
	if err := svc.Sync(a.Context(context.Background()), decls); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	a.deps.Dict = svc
	return nil
}

// Start 完成装配（含自动迁移）并启动模块的后台任务。不监听 HTTP。
func (a *App) Start(ctx context.Context) error {
	if a.deps.Conf.Migrate.Auto && a.deps.DB != nil {
		if err := a.Migrate(ctx); err != nil {
			return err
		}
	}
	if err := a.Setup(); err != nil {
		return err
	}
	ctx = a.Context(ctx)
	a.audit.Start()
	for _, m := range a.modules {
		if err := m.Start(ctx); err != nil {
			_ = a.Stop(ctx)
			return fmt.Errorf("app: 模块 %s 启动失败: %w", m.Name(), err)
		}
		a.started = append(a.started, m)
	}
	return nil
}

// Stop 按启动的逆序停止模块并关闭数据库。
func (a *App) Stop(ctx context.Context) error {
	var errs []error
	for i := len(a.started) - 1; i >= 0; i-- {
		if err := a.started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("模块 %s 停止失败: %w", a.started[i].Name(), err))
		}
	}
	a.started = nil
	// 审计记录里攒着的次数在关库前写完
	a.audit.Stop(ctx)
	if !a.dbProvided {
		if err := db.Close(a.deps.DB); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Run 启动一切并阻塞到 ctx 取消或收到 SIGINT/SIGTERM，然后优雅关停。
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := a.Start(ctx); err != nil {
		return err
	}
	// 四个超时都要设（规范 §12.1）：只设 ReadHeaderTimeout 挡不住慢速正文和长期空闲连接，直接暴露后端时会被慢请求耗尽连接。
	sc := a.deps.Conf.Server
	srv := &http.Server{
		Addr:              sc.Addr,
		Handler:           a.engine,
		ReadHeaderTimeout: sc.ReadHeaderTimeout,
		ReadTimeout:       sc.ReadTimeout,
		WriteTimeout:      sc.WriteTimeout,
		IdleTimeout:       sc.IdleTimeout,
		MaxHeaderBytes:    64 << 10,
		BaseContext:       func(net.Listener) context.Context { return a.Context(context.Background()) },
	}
	errCh := make(chan error, 1)
	go func() {
		a.deps.Log.Info("http server listening", "addr", srv.Addr, "mode", a.deps.Conf.Server.Mode)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			_ = a.Stop(context.Background())
			return fmt.Errorf("app: http server: %w", err)
		}
	case <-ctx.Done():
	}

	a.deps.Log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), a.deps.Conf.Server.ShutdownTimeout)
	defer cancel()
	var errs []error
	if err := srv.Shutdown(shutdownCtx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if err := a.Stop(shutdownCtx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// contextDB 把数据库句柄放进每个请求的 ctx。
func (a *App) contextDB() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.deps.DB != nil {
			c.Request = c.Request.WithContext(db.WithDB(c.Request.Context(), a.deps.DB))
		}
		c.Next()
	}
}

// registerHealth 注册 /healthz 与 /readyz（规范 §12.1：不带版本和配置信息）。
func (a *App) registerHealth() {
	a.router.Raw(http.MethodGet, healthPath, "存活探针", func(c *gin.Context) {
		httpx.OK(c, gin.H{"status": "ok"})
	})
	a.router.Raw(http.MethodGet, readyPath, "就绪探针（检查数据库）", func(c *gin.Context) {
		if a.deps.DB == nil {
			httpx.Fail(c, httpx.ErrUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx, a.deps.DB); err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		httpx.OK(c, gin.H{"status": "ok"})
	})
}
