// Package conf 负责配置：YAML 文件 + 环境变量覆盖 + 启动校验。
//
// 业务代码只读 Config，不直接碰 viper。
package conf

import (
	"fmt"
	"strings"
	"time"
)

// Mode 是运行模式。
const (
	ModeDebug   = "debug"
	ModeRelease = "release"
)

// Config 是整个进程的配置树。
type Config struct {
	Server   Server            `mapstructure:"server"`
	Database Database          `mapstructure:"database"`
	Log      Log               `mapstructure:"log"`
	Migrate  Migrate           `mapstructure:"migrate"`
	Monitor  Monitor           `mapstructure:"monitor"`
	Portals  map[string]Portal `mapstructure:"portals"`
}

// Monitor 是监控中心的配置（D-030、D-031）。
type Monitor struct {
	// Server 为 false 时关闭"服务器状态"：不采集请求统计，接口只回 enabled=false。安全态势不受影响。
	Server bool `mapstructure:"server"`
	// ServerCache、SecurityCache 是两类数据在进程内的缓存时长（0 表示不缓存，最长 1 分钟）：
	// 同时打开页面的人再多，每个进程每个周期也只算一次。
	ServerCache   time.Duration `mapstructure:"serverCache"`
	SecurityCache time.Duration `mapstructure:"securityCache"`
}

// Server 是 HTTP 服务配置。
type Server struct {
	Addr              string        `mapstructure:"addr"`
	Mode              string        `mapstructure:"mode"`
	TrustedProxies    []string      `mapstructure:"trustedProxies"`
	AllowedOrigins    []string      `mapstructure:"allowedOrigins"`
	MaxBodyBytes      int64         `mapstructure:"maxBodyBytes"`
	ReadHeaderTimeout time.Duration `mapstructure:"readHeaderTimeout"`
	ReadTimeout       time.Duration `mapstructure:"readTimeout"`  // 读完整个请求（含正文）的上限；防慢速正文占住连接
	WriteTimeout      time.Duration `mapstructure:"writeTimeout"` // 从读完请求头到写完响应的上限；接口没有流式响应，够用即可
	IdleTimeout       time.Duration `mapstructure:"idleTimeout"`  // keep-alive 空闲连接的上限
	// HandlerTimeout 是单个请求的处理时限（D-037）：到点后请求 ctx 被取消，经过 ctx 的查询随之中止、连接放回池子，
	// 请求回 503。必须小于 WriteTimeout，超时的响应才来得及写回去。
	HandlerTimeout  time.Duration `mapstructure:"handlerTimeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdownTimeout"`
}

// IsRelease 报告是否 release 模式。
func (s Server) IsRelease() bool { return s.Mode == ModeRelease }

// Database 是 MySQL 连接配置。
type Database struct {
	Host            string        `mapstructure:"host"`
	Port            int           `mapstructure:"port"`
	Name            string        `mapstructure:"name"`
	User            string        `mapstructure:"user"`
	Password        string        `mapstructure:"password"`
	Params          string        `mapstructure:"params"`
	MaxOpenConns    int           `mapstructure:"maxOpenConns"`
	MaxIdleConns    int           `mapstructure:"maxIdleConns"`
	ConnMaxLifetime time.Duration `mapstructure:"connMaxLifetime"`
	SlowThreshold   time.Duration `mapstructure:"slowThreshold"`
	// 下面四项见 D-037。三个超时拼进连接串；Params 里已经写了同名参数（timeout / readTimeout / writeTimeout）时以 Params 为准。
	ConnectTimeout time.Duration `mapstructure:"connectTimeout"` // 建立连接的上限
	ReadTimeout    time.Duration `mapstructure:"readTimeout"`    // 单次网络读的上限：数据库假死时最多等这么久
	WriteTimeout   time.Duration `mapstructure:"writeTimeout"`   // 单次网络写的上限
	ConnectWait    time.Duration `mapstructure:"connectWait"`    // 启动时等数据库就绪的最长时间；0 表示连不上立即退出
}

// defaultDBParams 是 Params 留空时用的连接参数。
const defaultDBParams = "charset=utf8mb4&parseTime=true&loc=UTC"

// DSN 返回 go-sql-driver/mysql 格式的连接串，带连接和读写超时。
func (d Database) DSN() string { return d.dsn(true) }

// MigrationDSN 和 DSN 一样，但不加框架默认的读写超时（D-037）：大表上的迁移可能跑好几分钟，
// 不能被单次读超时掐断在一半。Params 里自己写的读写超时照样生效。
func (d Database) MigrationDSN() string { return d.dsn(false) }

func (d Database) dsn(ioTimeouts bool) string {
	params := d.Params
	if params == "" {
		params = defaultDBParams
	}
	params = addParam(params, "timeout", d.ConnectTimeout)
	if ioTimeouts {
		params = addParam(params, "readTimeout", d.ReadTimeout)
		params = addParam(params, "writeTimeout", d.WriteTimeout)
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s", d.User, d.Password, d.Host, d.Port, d.Name, params)
}

// addParam 在 params 没有 key 时追加 key=d；d 不大于 0 时不加。
func addParam(params, key string, d time.Duration) string {
	if d <= 0 || hasParam(params, key) {
		return params
	}
	if params != "" && !strings.HasSuffix(params, "&") {
		params += "&"
	}
	return params + key + "=" + d.String()
}

// hasParam 报告 params（a=b&c=d 形式）里是否已有 key。按原样比较，不解码：驱动也是这样解析的。
func hasParam(params, key string) bool {
	for _, kv := range strings.Split(params, "&") {
		if k, _, _ := strings.Cut(kv, "="); k == key {
			return true
		}
	}
	return false
}

// Log 是日志配置。
type Log struct {
	Level  string `mapstructure:"level"`  // debug | info | warn | error
	Format string `mapstructure:"format"` // text | json
}

// Migrate 是迁移配置。
type Migrate struct {
	Auto bool `mapstructure:"auto"`
}

// Portal 是一个端的配置。端的注册在代码里（core/portal），这里只有可调参数和密钥。
type Portal struct {
	AccessTTL  time.Duration  `mapstructure:"accessTTL"`
	RefreshTTL time.Duration  `mapstructure:"refreshTTL"`
	JWTSecret  string         `mapstructure:"jwtSecret"`
	Login      PortalLogin    `mapstructure:"login"`
	Password   PortalPassword `mapstructure:"password"`
}

// PortalLogin 调整端的登录防护（规范 §5.7，D-024）。零值表示沿用代码里的值（默认 3 次出验证码、10 次锁定……）。
// 没有关闭限流和锁定的开关；取值必须在底线范围内，否则拒绝启动。
type PortalLogin struct {
	Window               time.Duration `mapstructure:"window"`               // 统计失败次数的窗口，1m–24h
	CaptchaAlways        bool          `mapstructure:"captchaAlways"`        // 每次登录都要验证码
	CaptchaAfterFailures int           `mapstructure:"captchaAfterFailures"` // 同账号+IP 失败几次后要验证码，1–5
	LockAfterFailures    int           `mapstructure:"lockAfterFailures"`    // 同账号+IP 失败几次后锁定，1–20
	AccountLockAfter     int           `mapstructure:"accountLockAfter"`     // 同账号所有 IP 累计失败几次后锁定账号，1–200
	LockDuration         time.Duration `mapstructure:"lockDuration"`         // 锁定时长，1m–24h
	IPRatePerMinute      int           `mapstructure:"ipRatePerMinute"`      // 同 IP 每分钟登录请求数，1–600
	AccountRatePerMinute int           `mapstructure:"accountRatePerMinute"` // 同账号每分钟登录请求数，1–120
}

// PortalPassword 调整端的密码策略（规范 §5.6，D-024）。"字母加数字"始终要求，这里只能往上加。
type PortalPassword struct {
	MinLength     int  `mapstructure:"minLength"`     // 最短长度，8–64；0 表示沿用代码里的值（默认 10）
	RequireUpper  bool `mapstructure:"requireUpper"`  // 必须含大写字母
	RequireLower  bool `mapstructure:"requireLower"`  // 必须含小写字母
	RequireSymbol bool `mapstructure:"requireSymbol"` // 必须含符号
	MaxAgeDays    int  `mapstructure:"maxAgeDays"`    // 密码有效期（天），0 表示不过期，最长 3650
}

// DefaultPortalCode 是 v0.1 唯一内置的端。
const DefaultPortalCode = "platform"

// Default 返回全部默认值。Load 在此基础上叠加文件和环境变量。
func Default() *Config {
	return &Config{
		Server: Server{
			Addr:              "0.0.0.0:8080",
			Mode:              ModeDebug,
			TrustedProxies:    []string{},
			AllowedOrigins:    []string{},
			MaxBodyBytes:      1 << 20,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			HandlerTimeout:    30 * time.Second,
			ShutdownTimeout:   15 * time.Second,
		},
		Database: Database{
			Host:            "127.0.0.1",
			Port:            3306,
			Name:            "goalladmin",
			User:            "goalladmin",
			Params:          defaultDBParams,
			MaxOpenConns:    50,
			MaxIdleConns:    10,
			ConnMaxLifetime: time.Hour,
			SlowThreshold:   200 * time.Millisecond,
			ConnectTimeout:  5 * time.Second,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    30 * time.Second,
		},
		Log:     Log{Level: "info", Format: "text"},
		Migrate: Migrate{Auto: true},
		Monitor: Monitor{Server: true, ServerCache: 2 * time.Second, SecurityCache: 10 * time.Second},
		Portals: map[string]Portal{
			DefaultPortalCode: {AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour},
		},
	}
}
