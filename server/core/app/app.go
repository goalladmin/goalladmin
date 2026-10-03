// Package app 是应用的生命周期与装配：配置校验、日志、数据库、路由、模块注册、优雅关停。
//
// main.go 只做四件事：读配置 → app.New → 显式 Register 模块 → Run。
package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"sync/atomic"
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
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/monitor"
	"github.com/goalladmin/goalladmin/server/core/org"
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
	IPACL   *ipacl.Service   // IP 黑名单与白名单（D-062）；New 之后即可用，Setup 时读入名单（无数据库时为 nil）
	Orgs    *org.Service     // 主体与主体账号（D-065）：代理商、商户；New 之后即可用（无数据库时为 nil）
	// VerifyCaptcha 消费本端一次性验证码；Routes 起可用，错误答案也消费。
	VerifyCaptcha func(ctx context.Context, portal, id, answer string) bool
	// NewRateWindow 创建共用的操作计数窗口；namespace 区分端和用途，同名窗口的参数必须一致（D-076）。
	// New 之后即可用，没配 Redis 或不可用时使用本实例内存。
	NewRateWindow func(namespace string, limit int, span time.Duration, maxKeys int) RateWindow
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

// WithPasswordHashParams 调整 Argon2id 的内存（KiB）和轮数。只给测试用：降到 64 KiB、1 轮加快测试；
// 生产用的参数是固定的（D-070），不合法的值按默认参数处理。配置选的是别的算法时不起作用。
func WithPasswordHashParams(memoryKiB, iterations uint32) Option {
	return func(a *App) { a.argonMemory, a.argonTime = memoryKiB, iterations }
}

// WithBcryptCost 调整 bcrypt 的代价。只给测试用：降到 4 加快测试；生产用的是 12，不合法的值按 12 处理。
// 配置选的是别的算法时不起作用——默认的 Argon2id 用 WithPasswordHashParams。
func WithBcryptCost(cost int) Option {
	return func(a *App) { a.bcryptCost = cost }
}

// WithPBKDF2Iterations 调整 PBKDF2 的迭代次数。只给测试用：降到几十次加快测试；生产用的是 60 万次（SHA-256）、
// 22 万次（SHA-512）（D-072），不合法的值按生产用的处理。配置选的是别的算法时不起作用。
func WithPBKDF2Iterations(n uint32) Option {
	return func(a *App) { a.pbkdf2Iterations = n }
}

// WithoutMigrations 表示这个程序不执行迁移（D-061）：代理商、商户程序和平台程序共用一个数据库，表结构只由平台程序改。
// 启动时只读地核对迁移都已应用（db.MigrateCheck），落后就拒绝启动；Migrate 直接返回错误。
func WithoutMigrations() Option {
	return func(a *App) { a.migrateCheckOnly = true }
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

	sessionCleanupCancel context.CancelFunc
	sessionCleanupDone   chan struct{}
	readiness            *readinessCheck

	portalGroups   map[string]*gin.RouterGroup
	authenticators map[string]*authimpl.Authenticator
	authorizer     Authorizer
	captcha        *captcha.Captcha
	metrics        *metrics.Collector
	audit          *auditimpl.Recorder
	guardResolver  GuardResolver
	routes         []Route
	rawRoutes      []RawRoute

	now              func() time.Time
	migrateCheckOnly bool // 不执行迁移，只核对（WithoutMigrations）
	// 密码哈希的参数，只有测试会设（D-070、D-072）：零值用生产用的参数。算法由配置 server.passwordHash 定
	argonMemory, argonTime uint32
	bcryptCost             int
	pbkdf2Iterations       uint32
	hasher                 *password.Hasher // 整个进程共用的密码哈希器：各个端的认证器、主体服务都用它
	pwdParallel            int              // 同时做密码计算的上限，只有测试会设；0 用配置 server.passwordParallel，再没有就按 CPU 数（D-058、D-070）
	pwdWait                time.Duration    // 登录、解锁在位置占满时最多等多久，只有测试会设；0 用默认值（D-071）
	// pwdBudget 是整个进程共用的密码计算位置（D-068、D-071）：登录、解锁可以用全部，本人改密、后台生成密码哈希最多占一半
	pwdBudget      *ratelimit.Budget
	statusCacheTTL time.Duration
	// redis 是可选的 Redis 连接（D-074）：没配 redis.addr 时是 nil，程序只用本实例的内存
	redis              *redisx.Client
	invalidationSource string
	invalidationReady  atomic.Bool
	invalidationCancel context.CancelFunc
	invalidationDone   chan struct{}
}

// hashPassword 用进程的哈希器生成密码哈希，先在密码计算的位置里占一个（后一类：最多占一半，D-068、D-071）；
// 满了回 429，不排队。主体服务（org.NewInitialPassword）用它。
func (a *App) hashPassword(plain string) (string, error) {
	leave, ok := a.pwdBudget.EnterBackground()
	if !ok {
		return "", httpx.ErrTooManyRequests
	}
	defer leave()
	return a.hasher.Hash(plain)
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
	// 上限：测试直接设的字段优先，其次是配置，都没有就按 CPU 数（D-070 第 8 条）
	a.pwdBudget = ratelimit.NewBudget(cmp.Or(a.pwdParallel, cfg.Server.PasswordParallel, authimpl.PasswordParallel()),
		cmp.Or(a.pwdWait, authimpl.PasswordWait))
	// 算法由配置定（conf.Validate 已经保证取值合法）；参数是生产用的，测试可以调低
	params := password.ParamsFor(password.Algorithm(cfg.Server.PasswordHash))
	if a.argonMemory != 0 || a.argonTime != 0 {
		params.Memory, params.Time = a.argonMemory, a.argonTime
	}
	if a.bcryptCost != 0 {
		params.Cost = a.bcryptCost
	}
	if a.pbkdf2Iterations != 0 {
		params.Iterations = a.pbkdf2Iterations
	}
	a.hasher = password.NewHasher(params)
	if a.deps.Log == nil {
		a.deps.Log = logx.New(cfg.Log.Level, cfg.Log.Format, nil)
	}
	slog.SetDefault(a.deps.Log)

	// Redis（可选，D-074）：版本太低是配置错误，拒绝启动；连不上不拒绝，按不可用处理、后台继续探测
	if rc := cfg.Redis; rc.Enabled() {
		id, err := newInvalidationSource()
		if err != nil {
			return nil, err
		}
		a.invalidationSource = id
		client, err := redisx.Open(redisx.Options{
			Addr: rc.Addr, Username: rc.Username, Password: rc.Password, DB: rc.DB, TLS: rc.TLS,
			KeyPrefix: rc.KeyPrefix, ConnectWait: rc.ConnectWait, Log: a.deps.Log, Check: checkRedisState,
		})
		if err != nil {
			return nil, fmt.Errorf("app: %w", err)
		}
		a.redis = client
	}
	a.setupRateWindows()
	if !a.dbProvided {
		gdb, err := db.Open(cfg.Database, a.deps.Log)
		if err != nil {
			_ = a.closeRedis()
			return nil, err
		}
		a.deps.DB = gdb
	}
	if a.deps.DB != nil {
		a.deps.IPACL = ipacl.New(ipacl.Options{DB: a.deps.DB, Log: a.deps.Log, Now: func() time.Time { return a.now() },
			OnChange: func() { a.publishInvalidation("ip", "", "") },
		})
		a.deps.Orgs = org.New(org.Options{Now: func() time.Time { return a.now() }, Hash: a.hashPassword,
			OnAccountChange: a.accountChanged, OnSessionChange: a.sessionChanged, OnOrgChange: a.orgChanged,
			// 授权服务在后面才建好（Routes 阶段起可用），用到时再取
			ClearRoles: func(ctx context.Context, portal string, userID uint64) error {
				if a.deps.RBAC == nil {
					// 没有内置授权服务的装配没有角色可清（D-101）：会话照样吊销，这里留一条记录
					a.deps.Log.WarnContext(ctx, "更换主账号：没有内置授权服务，原主账号的角色没有清理", "portal", portal, "user_id", userID)
					return nil
				}
				return a.deps.RBAC.ClearUserRoles(ctx, portal, userID)
			},
		})
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
		_ = a.closeRedis()
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
	a.audit = auditimpl.New(auditimpl.Options{DB: a.deps.DB, Log: a.deps.Log, Now: func() time.Time { return a.now() }, Skip: []string{healthPath, readyPath}, KnownPortal: func(code string) bool { _, ok := a.deps.Portals.Get(code); return ok }})
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
		// IP 黑名单（D-062）：排在审计之后，被拒的请求照样记安全事件；健康检查不受影响
		a.ipDenyMiddleware(),
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

// ErrMigrationsDisabled 由不执行迁移的程序（WithoutMigrations）的 Migrate 返回。
var ErrMigrationsDisabled = errors.New("app: 这个程序不执行迁移，表结构由平台程序迁移（server migrate up）")

// Migrate 执行框架迁移和各模块的迁移。
func (a *App) Migrate(ctx context.Context) error {
	if a.migrateCheckOnly {
		return ErrMigrationsDisabled
	}
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
			Portals:        a.deps.Portals,
			OnPolicyChange: func() { a.publishInvalidation("policy", "", "") },
			OnMenuChange:   func(portal string) { a.publishInvalidation("menu", portal, "") },
			// 未使用会话锁时的兼容读取：直接查会话表，不走状态缓存。
			SessionActive: func(ctx context.Context, portal, sid string) (bool, error) {
				an, ok := a.authenticators[portal]
				if !ok {
					return false, nil
				}
				sess, err := an.Sessions().Get(ctx, sid)
				if err != nil || sess == nil {
					return false, err
				}
				if sess.Portal != portal || !sess.Active(a.now().UTC()) {
					return false, nil
				}
				if sess.LockedAt != nil {
					return false, httpx.ErrSessionLocked
				}
				return true, nil
			},
			// 本人及管理写操作锁住操作人会话，确认有效且未锁屏（D-059、D-089、D-106）：状态写同一行，两边排队。
			LockSession: func(ctx context.Context, portal, sid string) (bool, error) {
				an, ok := a.authenticators[portal]
				if !ok {
					return false, nil
				}
				err := an.Sessions().LockWritable(ctx, portal, sid)
				if errors.Is(err, session.ErrNotFound) || errors.Is(err, session.ErrInactive) {
					return false, nil
				}
				if errors.Is(err, session.ErrLocked) {
					return false, httpx.ErrSessionLocked
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
	if a.deps.IPACL != nil {
		if err := a.deps.IPACL.Load(a.Context(context.Background())); err != nil {
			return fmt.Errorf("app: %w", err)
		}
	}
	for _, m := range a.modules {
		m.Routes(a.router)
	}
	a.setupDone = true
	a.startInvalidations()
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
	svc := dict.NewService(dict.Options{Log: a.deps.Log, Now: a.now, CacheTTL: a.statusCacheTTL, PortalOK: portalOK,
		OnChange: func(code string) { a.publishInvalidation("dict", "", code) },
	})
	if err := svc.Sync(a.Context(context.Background()), decls); err != nil {
		return fmt.Errorf("app: %w", err)
	}
	a.deps.Dict = svc
	return nil
}

// CheckMigrations 只读地核对全部迁移来源都已应用（D-061），不执行任何迁移。
func (a *App) CheckMigrations(ctx context.Context) error {
	if a.deps.DB == nil {
		return errors.New("app: 没有数据库连接，无法核对迁移")
	}
	ctx = a.Context(ctx)
	for _, set := range a.MigrationSets() {
		if err := db.MigrateCheck(ctx, a.deps.DB, set.FS, set.Dir, set.Table); err != nil {
			return fmt.Errorf("%s: %w", set.Name, err)
		}
	}
	return nil
}

// Start 完成装配（含自动迁移）并启动模块的后台任务。不监听 HTTP。
// 不执行迁移的程序（WithoutMigrations）只核对迁移，落后就拒绝启动。
func (a *App) Start(ctx context.Context) error {
	switch {
	case a.migrateCheckOnly && a.deps.DB != nil:
		if err := a.CheckMigrations(ctx); err != nil {
			return err
		}
	case a.deps.Conf.Migrate.Auto && a.deps.DB != nil:
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
	a.startSessionCleanup(ctx)
	return nil
}

// Stop 按启动的逆序停止模块并关闭数据库。
func (a *App) Stop(ctx context.Context) error {
	var errs []error
	if a.readiness != nil {
		a.readiness.stop()
	}
	a.stopSessionCleanup()
	for i := len(a.started) - 1; i >= 0; i-- {
		if err := a.started[i].Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("模块 %s 停止失败: %w", a.started[i].Name(), err))
		}
	}
	a.started = nil
	// 审计记录里攒着的次数在关库前写完
	a.audit.Stop(ctx)
	if err := a.closeRedis(); err != nil {
		errs = append(errs, err)
	}
	if !a.dbProvided {
		if err := db.Close(a.deps.DB); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// closeRedis 关闭 Redis 连接（没配时什么都不做）。
func (a *App) closeRedis() error {
	if a.redis == nil {
		return nil
	}
	if a.invalidationCancel != nil {
		a.invalidationCancel()
		<-a.invalidationDone
	}
	if err := a.redis.Close(); err != nil {
		return fmt.Errorf("redis close: %w", err)
	}
	return nil
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
	if msg, ok := debugExposure(sc); ok {
		a.deps.Log.Warn(msg, "addr", sc.Addr, "mode", sc.Mode)
	}
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
	a.readiness = &readinessCheck{now: time.Now, ping: func(ctx context.Context) error {
		if a.deps.DB == nil {
			return httpx.ErrUnavailable
		}
		return db.Ping(ctx, a.deps.DB)
	}}
	a.router.Raw(http.MethodGet, healthPath, "存活探针", func(c *gin.Context) {
		httpx.OK(c, gin.H{"status": "ok"})
	})
	a.router.Raw(http.MethodGet, readyPath, "就绪探针（检查数据库）", func(c *gin.Context) {
		if err := a.readiness.check(c.Request.Context()); err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		httpx.OK(c, gin.H{"status": "ok"})
	})
}

// debugExposure 在 debug 模式监听非回环地址时给出一条启动告警（D-098）：debug 模式不做 release 的启动校验
// （JWT 密钥、数据库密码、allowedOrigins），没配 allowedOrigins 时不核对 Origin，刷新 Cookie 不带 Secure。
// 本机开发没问题；对外提供服务的实例要用 release。
func debugExposure(sc conf.Server) (string, bool) {
	if sc.IsRelease() || !sc.ListensBeyondLoopback() {
		return "", false
	}
	return "debug mode is listening on a non-loopback address: release startup checks are skipped, " +
		"Origin is not checked while server.allowedOrigins is empty, and the refresh cookie is sent without Secure; " +
		"set server.mode to release (GA_SERVER_MODE=release) for anything other than local development", true
}
