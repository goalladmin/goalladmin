package conf

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeYAML(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestLoad_DefaultsWithoutFile(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:8080", cfg.Server.Addr)
	require.Equal(t, ModeDebug, cfg.Server.Mode)
	require.Equal(t, int64(1<<20), cfg.Server.MaxBodyBytes)
	require.Contains(t, cfg.Portals, DefaultPortalCode)
	require.Equal(t, 15*time.Minute, cfg.Portals[DefaultPortalCode].AccessTTL)
	require.NoError(t, Validate(cfg), "debug 模式的默认配置必须能通过校验")
}

func TestLoad_MissingFileIsAnError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	require.Error(t, err)
}

// 这是 viper 的经典坑：配置文件里故意不写 database.password，环境变量必须仍然生效。
func TestLoad_EnvOverridesKeyAbsentFromFile(t *testing.T) {
	p := writeYAML(t, `
server:
  addr: 127.0.0.1:9000
database:
  host: db.internal
  name: app
  user: app
`)
	t.Setenv("GA_DB_PASSWORD", "from-env")
	t.Setenv("GA_JWT_SECRET_PLATFORM", "portal-secret-from-env-0123456789abcdef")
	t.Setenv("GA_SERVER_TRUSTED_PROXIES", "10.0.0.5,10.0.1.0/24")

	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "from-env", cfg.Database.Password)
	require.Equal(t, "db.internal", cfg.Database.Host)
	require.Equal(t, "127.0.0.1:9000", cfg.Server.Addr)
	require.Equal(t, "portal-secret-from-env-0123456789abcdef", cfg.Portals[DefaultPortalCode].JWTSecret)
	require.Equal(t, []string{"10.0.0.5", "10.0.1.0/24"}, cfg.Server.TrustedProxies)
}

// 时长和整数类的键也要能从环境变量来（容器里没有配置文件时，超时和连接池参数全靠它）。
func TestLoad_EnvOverridesDurationsAndInts(t *testing.T) {
	t.Setenv("GA_SERVER_READ_TIMEOUT", "45s")
	t.Setenv("GA_SERVER_WRITE_TIMEOUT", "2m")
	t.Setenv("GA_SERVER_IDLE_TIMEOUT", "3m")
	t.Setenv("GA_SERVER_READ_HEADER_TIMEOUT", "5s")
	t.Setenv("GA_SERVER_SHUTDOWN_TIMEOUT", "20s")
	t.Setenv("GA_DB_MAX_OPEN_CONNS", "7")
	t.Setenv("GA_DB_CONN_MAX_LIFETIME", "30m")
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 45*time.Second, cfg.Server.ReadTimeout)
	require.Equal(t, 2*time.Minute, cfg.Server.WriteTimeout)
	require.Equal(t, 3*time.Minute, cfg.Server.IdleTimeout)
	require.Equal(t, 5*time.Second, cfg.Server.ReadHeaderTimeout)
	require.Equal(t, 20*time.Second, cfg.Server.ShutdownTimeout)
	require.Equal(t, 7, cfg.Database.MaxOpenConns)
	require.Equal(t, 30*time.Minute, cfg.Database.ConnMaxLifetime)
}

// Server / Database / Log / Migrate 下的每一个键都必须能从环境变量提供；新加配置项时这条测试会提醒补绑定表。
func TestEnvBindings_CoverAllStaticKeys(t *testing.T) {
	var keys []string
	var walk func(prefix string, v reflect.Value)
	walk = func(prefix string, v reflect.Value) {
		tp := v.Type()
		for i := 0; i < tp.NumField(); i++ {
			tag := tp.Field(i).Tag.Get("mapstructure")
			if tag == "" {
				continue
			}
			key := prefix + tag
			if v.Field(i).Kind() == reflect.Struct {
				walk(key+".", v.Field(i))
				continue
			}
			keys = append(keys, key)
		}
	}
	cfg := Default()
	for _, sec := range []struct {
		name string
		v    any
	}{{"server", cfg.Server}, {"database", cfg.Database}, {"log", cfg.Log}, {"migrate", cfg.Migrate}} {
		walk(sec.name+".", reflect.ValueOf(sec.v))
	}
	require.NotEmpty(t, keys)
	for _, k := range keys {
		require.Contains(t, EnvBindings, k, "配置键 %s 没有对应的环境变量，请补进 EnvBindings 和 config.example.yaml 的清单", k)
	}
}

func TestLoad_EnvOverridesKeyPresentInFile(t *testing.T) {
	p := writeYAML(t, "server:\n  addr: 127.0.0.1:9000\n")
	t.Setenv("GA_SERVER_ADDR", "127.0.0.1:9100")
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:9100", cfg.Server.Addr)
}

func TestLoad_SecretsFromFileWhenEnvUnset(t *testing.T) {
	t.Setenv("GA_JWT_SECRET_PLATFORM", "")
	t.Setenv("GA_DB_PASSWORD", "")
	p := writeYAML(t, "database:\n  password: file-pw\nportals:\n  platform:\n    jwtSecret: file-secret-0123456789abcdef0123456789\n")
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "file-pw", cfg.Database.Password)
	require.Equal(t, "file-secret-0123456789abcdef0123456789", cfg.Portals["platform"].JWTSecret)
}

func TestLoad_SecretInFileIsOverriddenByEnv(t *testing.T) {
	p := writeYAML(t, "portals:\n  platform:\n    jwtSecret: in-file\n")
	t.Setenv("GA_JWT_SECRET_PLATFORM", "in-env-0123456789abcdef0123456789abcdef")
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, "in-env-0123456789abcdef0123456789abcdef", cfg.Portals["platform"].JWTSecret)
}

func TestLoad_ExtraPortalGetsSecretFromEnvAndDefaults(t *testing.T) {
	p := writeYAML(t, "portals:\n  merchant:\n    accessTTL: 10m\n")
	t.Setenv("GA_JWT_SECRET_MERCHANT", "merchant-secret-0123456789abcdef0123456789")
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Contains(t, cfg.Portals, "platform", "内置端始终存在")
	require.Equal(t, 10*time.Minute, cfg.Portals["merchant"].AccessTTL)
	require.Equal(t, 168*time.Hour, cfg.Portals["merchant"].RefreshTTL, "未写的字段取默认值")
	require.Equal(t, "merchant-secret-0123456789abcdef0123456789", cfg.Portals["merchant"].JWTSecret)
}

func TestEnvBindings_CoverExampleFile(t *testing.T) {
	// config.example.yaml 注释里列出的环境变量都必须在绑定表里，避免文档和代码脱节。
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "config.example.yaml"))
	require.NoError(t, err)
	bound := map[string]bool{}
	for _, env := range EnvBindings {
		bound[env] = true
	}
	for _, line := range strings.Split(string(raw), "\n") {
		for _, tok := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == '（' || r == '）' }) {
			if strings.HasPrefix(tok, "GA_") && !strings.HasPrefix(tok, JWTSecretEnvPrefix) {
				require.True(t, bound[tok], "config.example.yaml 提到的 %s 没有在 EnvBindings 里绑定", tok)
			}
		}
	}
}

func releaseConfig() *Config {
	cfg := Default()
	cfg.Server.Mode = ModeRelease
	cfg.Server.AllowedOrigins = []string{"https://admin.example.com"}
	cfg.Database.Password = "db-pw"
	cfg.Portals["platform"] = Portal{
		AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour,
		JWTSecret: "a-perfectly-fine-random-secret-of-40-bytes!!",
	}
	return cfg
}

func TestValidate_ReleaseHappyPath(t *testing.T) {
	require.NoError(t, Validate(releaseConfig()))
}

func TestValidate_ReleaseRejectsWeakSecret(t *testing.T) {
	cfg := releaseConfig()
	p := cfg.Portals["platform"]
	p.JWTSecret = "short"
	cfg.Portals["platform"] = p
	err := Validate(cfg)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Contains(t, err.Error(), "不足 32 字节")
}

func TestValidate_ReleaseRejectsExampleSecret(t *testing.T) {
	cfg := releaseConfig()
	p := cfg.Portals["platform"]
	p.JWTSecret = "please-change-me-to-a-random-secret!"
	cfg.Portals["platform"] = p
	err := Validate(cfg)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Contains(t, err.Error(), "示例值")
}

func TestValidate_ReleaseRejectsMissingSecretDbPasswordAndOrigins(t *testing.T) {
	cfg := releaseConfig()
	cfg.Database.Password = ""
	cfg.Server.AllowedOrigins = nil
	p := cfg.Portals["platform"]
	p.JWTSecret = ""
	cfg.Portals["platform"] = p
	err := Validate(cfg)
	require.ErrorIs(t, err, ErrInvalidConfig)
	msg := err.Error()
	require.Contains(t, msg, "database.password")
	require.Contains(t, msg, "allowedOrigins")
	require.Contains(t, msg, "缺少 JWT 密钥")
}

func TestValidate_ReleaseRejectsSharedSecretAcrossPortals(t *testing.T) {
	cfg := releaseConfig()
	cfg.Portals["merchant"] = cfg.Portals["platform"]
	err := Validate(cfg)
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Contains(t, err.Error(), "相同的 JWT 密钥")
}

func TestValidate_DebugAllowsMissingSecrets(t *testing.T) {
	cfg := Default()
	require.NoError(t, Validate(cfg))
}

func TestValidate_CommonRules(t *testing.T) {
	cfg := Default()
	cfg.Server.Mode = "prod"
	cfg.Server.TrustedProxies = []string{"not-an-ip"}
	cfg.Server.AllowedOrigins = []string{"admin.example.com"}
	cfg.Server.WriteTimeout = 0
	cfg.Portals["Bad Code"] = Portal{AccessTTL: time.Hour, RefreshTTL: time.Minute}
	err := Validate(cfg)
	require.ErrorIs(t, err, ErrInvalidConfig)
	msg := err.Error()
	require.Contains(t, msg, "server.mode")
	require.Contains(t, msg, "trustedProxies")
	require.Contains(t, msg, "allowedOrigins")
	require.Contains(t, msg, "writeTimeout")
	require.Contains(t, msg, "端代号")
	require.Contains(t, msg, "accessTTL 必须小于 refreshTTL")
}

func TestDatabase_DSN(t *testing.T) {
	d := Database{Host: "h", Port: 3306, Name: "n", User: "u", Password: "p"}
	require.Equal(t, "u:p@tcp(h:3306)/n?charset=utf8mb4&parseTime=true&loc=UTC", d.DSN())
}

func TestLoad_PortalSecurityPolicy(t *testing.T) {
	p := writeYAML(t, `portals:
  platform:
    login:
      captchaAlways: true
      lockAfterFailures: 5
      lockDuration: 30m
    password:
      minLength: 12
      requireUpper: true
      requireSymbol: true
      maxAgeDays: 90
`)
	cfg, err := Load(p)
	require.NoError(t, err)
	pl := cfg.Portals["platform"]
	require.True(t, pl.Login.CaptchaAlways)
	require.Equal(t, 5, pl.Login.LockAfterFailures)
	require.Equal(t, 30*time.Minute, pl.Login.LockDuration)
	require.Zero(t, pl.Login.IPRatePerMinute, "没写的字段为零，表示沿用代码里的值")
	require.Equal(t, 12, pl.Password.MinLength)
	require.True(t, pl.Password.RequireUpper)
	require.False(t, pl.Password.RequireLower)
	require.True(t, pl.Password.RequireSymbol)
	require.Equal(t, 90, pl.Password.MaxAgeDays)
	require.Equal(t, 15*time.Minute, pl.AccessTTL, "同一个端的其他字段仍取默认值")
}

// 监控中心（D-031）：默认开启、缓存 2s / 10s；环境变量能关掉服务器状态；缓存时长越界拒绝启动。
func TestMonitorConfig(t *testing.T) {
	d := Default()
	require.True(t, d.Monitor.Server)
	require.Equal(t, 2*time.Second, d.Monitor.ServerCache)
	require.Equal(t, 10*time.Second, d.Monitor.SecurityCache)
	require.NoError(t, Validate(d))

	t.Setenv("GA_MONITOR_SERVER", "false")
	t.Setenv("GA_MONITOR_SECURITY_CACHE", "0s")
	cfg, err := Load("")
	require.NoError(t, err)
	require.False(t, cfg.Monitor.Server)
	require.Zero(t, cfg.Monitor.SecurityCache)
	require.NoError(t, Validate(cfg), "0 表示不缓存，是合法值")

	for _, bad := range []time.Duration{-time.Second, 2 * time.Minute} {
		c := Default()
		c.Monitor.ServerCache = bad
		require.ErrorContains(t, Validate(c), "monitor.serverCache")
		c = Default()
		c.Monitor.SecurityCache = bad
		require.ErrorContains(t, Validate(c), "monitor.securityCache")
	}
}

func TestDatabase_DSNTimeouts(t *testing.T) {
	d := Default().Database
	d.Password = "p"
	require.Equal(t, "goalladmin:p@tcp(127.0.0.1:3306)/goalladmin?charset=utf8mb4&parseTime=true&loc=UTC&timeout=5s&readTimeout=30s&writeTimeout=30s", d.DSN())
	// 迁移连接不加默认的读写超时（D-037），连接超时照加
	require.Equal(t, "goalladmin:p@tcp(127.0.0.1:3306)/goalladmin?charset=utf8mb4&parseTime=true&loc=UTC&timeout=5s", d.MigrationDSN())

	// params 里已写同名参数时以它为准，不重复追加；名字只是后缀相同的参数不算
	d.Params = "charset=utf8mb4&readTimeout=5m&xwriteTimeout=1s"
	require.Equal(t, "goalladmin:p@tcp(127.0.0.1:3306)/goalladmin?charset=utf8mb4&readTimeout=5m&xwriteTimeout=1s&timeout=5s&writeTimeout=30s", d.DSN())
	require.Equal(t, "goalladmin:p@tcp(127.0.0.1:3306)/goalladmin?charset=utf8mb4&readTimeout=5m&xwriteTimeout=1s&timeout=5s", d.MigrationDSN(),
		"params 里自己写的读超时，迁移连接也照样生效")
}

func TestValidate_DatabaseTimeoutsAndHandlerTimeout(t *testing.T) {
	cases := []struct {
		name string
		mod  func(c *Config)
		want string
	}{
		{"readTimeout 0 不限时", func(c *Config) { c.Database.ReadTimeout = 0 }, "database.readTimeout"},
		{"writeTimeout 太长", func(c *Config) { c.Database.WriteTimeout = 11 * time.Minute }, "database.writeTimeout"},
		{"connectTimeout 太短", func(c *Config) { c.Database.ConnectTimeout = 500 * time.Millisecond }, "database.connectTimeout"},
		{"connectWait 负数", func(c *Config) { c.Database.ConnectWait = -time.Second }, "database.connectWait"},
		{"connectWait 太长", func(c *Config) { c.Database.ConnectWait = 11 * time.Minute }, "database.connectWait"},
		{"handlerTimeout 0", func(c *Config) { c.Server.HandlerTimeout = 0 }, "server.handlerTimeout"},
		{"handlerTimeout 不小于 writeTimeout", func(c *Config) { c.Server.WriteTimeout = 30 * time.Second }, "server.handlerTimeout"},
		{"readTimeout 小于 handlerTimeout", func(c *Config) { c.Database.ReadTimeout = 10 * time.Second }, "不能小于 server.handlerTimeout"},
	}
	for _, tc := range cases {
		cfg := Default()
		tc.mod(cfg)
		err := Validate(cfg)
		require.ErrorIs(t, err, ErrInvalidConfig, tc.name)
		require.Contains(t, err.Error(), tc.want, tc.name)
	}
	cfg := Default()
	cfg.Database.ConnectWait = 10 * time.Minute
	cfg.Server.HandlerTimeout = cfg.Server.WriteTimeout - time.Second
	cfg.Database.ReadTimeout = cfg.Server.HandlerTimeout
	require.NoError(t, Validate(cfg), "边界值合法")
}

func TestLoad_DatabaseProtectionFromEnv(t *testing.T) {
	t.Setenv("GA_DB_CONNECT_TIMEOUT", "3s")
	t.Setenv("GA_DB_READ_TIMEOUT", "2m")
	t.Setenv("GA_DB_WRITE_TIMEOUT", "45s")
	t.Setenv("GA_DB_CONNECT_WAIT", "90s")
	t.Setenv("GA_SERVER_HANDLER_TIMEOUT", "20s")
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 3*time.Second, cfg.Database.ConnectTimeout)
	require.Equal(t, 2*time.Minute, cfg.Database.ReadTimeout)
	require.Equal(t, 45*time.Second, cfg.Database.WriteTimeout)
	require.Equal(t, 90*time.Second, cfg.Database.ConnectWait)
	require.Equal(t, 20*time.Second, cfg.Server.HandlerTimeout)
}
