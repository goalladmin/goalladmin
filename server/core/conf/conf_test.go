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

// 规范 §13.2 第 153 条（D-069）：连接池上限必须是正数——database/sql 把 0 和负数当成"不限"；空闲连接数不能是负数。
// 环境变量把它改成 0 同样被拒绝；1 是合法的最小值，空闲连接数比上限大也合法（database/sql 自己会压下来）。
func TestValidate_153_DatabasePoolLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(c *Config)
		want string
	}{
		{"上限 0 等于不限", func(c *Config) { c.Database.MaxOpenConns = 0 }, "database.maxOpenConns"},
		{"上限负数", func(c *Config) { c.Database.MaxOpenConns = -1 }, "database.maxOpenConns"},
		{"空闲连接数负数", func(c *Config) { c.Database.MaxIdleConns = -1 }, "database.maxIdleConns"},
	} {
		cfg := Default()
		tc.mod(cfg)
		err := Validate(cfg)
		require.ErrorIs(t, err, ErrInvalidConfig, tc.name)
		require.Contains(t, err.Error(), tc.want, tc.name)
	}
	for _, ok := range []struct{ open, idle int }{{1, 0}, {1, 10}, {50, 10}, {5, 5}} {
		cfg := Default()
		cfg.Database.MaxOpenConns, cfg.Database.MaxIdleConns = ok.open, ok.idle
		require.NoError(t, Validate(cfg), "%+v", ok)
	}
	require.NoError(t, Validate(Default()), "默认值合法")

	t.Setenv("GA_DB_MAX_OPEN_CONNS", "0")
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 0, cfg.Database.MaxOpenConns)
	err = Validate(cfg)
	require.ErrorIs(t, err, ErrInvalidConfig, "环境变量改成 0 也拒绝")
	require.Contains(t, err.Error(), "database.maxOpenConns")
}

// 规范 §13.2 第 156 条（D-070）：密码计算的并发上限 server.passwordParallel——默认 0（按 CPU 数），可以用环境变量设；
// 负数和大得离谱的值拒绝启动。
func TestValidate_156_PasswordParallel(t *testing.T) {
	require.Zero(t, Default().Server.PasswordParallel)
	for _, bad := range []int{-1, MaxPasswordParallel + 1} {
		cfg := Default()
		cfg.Server.PasswordParallel = bad
		err := Validate(cfg)
		require.ErrorIs(t, err, ErrInvalidConfig, "%d", bad)
		require.Contains(t, err.Error(), "server.passwordParallel")
	}
	for _, ok := range []int{0, 1, 8, MaxPasswordParallel} {
		cfg := Default()
		cfg.Server.PasswordParallel = ok
		require.NoError(t, Validate(cfg), "%d", ok)
	}
	t.Setenv("GA_SERVER_PASSWORD_PARALLEL", "6")
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, 6, cfg.Server.PasswordParallel)
	require.NoError(t, Validate(cfg))
}

// 规范 §13.2 第 160 条（D-070）：密码哈希算法 server.passwordHash——默认 argon2id，可以选 bcrypt、可以用环境变量设；
// 别的值（包括空、大小写不对的）拒绝启动，不悄悄用默认的。
func TestValidate_160_PasswordHashAlgorithm(t *testing.T) {
	require.Equal(t, PasswordHashArgon2id, Default().Server.PasswordHash)
	for _, bad := range []string{"", "scrypt", "ARGON2ID", "Bcrypt", "argon2", "argon2id ", "pbkdf2", "pbkdf2-sha1", "PBKDF2-SHA256"} {
		cfg := Default()
		cfg.Server.PasswordHash = bad
		err := Validate(cfg)
		require.ErrorIs(t, err, ErrInvalidConfig, "%q", bad)
		require.Contains(t, err.Error(), "server.passwordHash")
	}
	for _, ok := range []string{PasswordHashArgon2id, PasswordHashBcrypt} {
		cfg := Default()
		cfg.Server.PasswordHash = ok
		require.NoError(t, Validate(cfg), ok)
	}
	t.Setenv("GA_SERVER_PASSWORD_HASH", "bcrypt")
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, PasswordHashBcrypt, cfg.Server.PasswordHash)
	require.NoError(t, Validate(cfg))
}

// 规范 §13.2 第 161 条（D-072）：server.passwordHash 还可以是 pbkdf2-sha256、pbkdf2-sha512（给有 FIPS 140 要求的部署），
// 可以用环境变量设；不带摘要算法的 pbkdf2、SHA-1 的、SHA-384 的、大小写不对的拒绝启动，报错里列出可以取的值。
func TestValidate_161_PBKDF2Algorithms(t *testing.T) {
	require.Equal(t, []string{"argon2id", "bcrypt", "pbkdf2-sha256", "pbkdf2-sha512"}, PasswordHashes())
	require.Equal(t, PasswordHashes()[0], Default().Server.PasswordHash, "默认的排在最前")
	for _, ok := range PasswordHashes() {
		cfg := Default()
		cfg.Server.PasswordHash = ok
		require.NoError(t, Validate(cfg), ok)
	}
	for _, bad := range []string{"pbkdf2", "pbkdf2-sha1", "pbkdf2-sha384", "pbkdf2_sha256", "PBKDF2-SHA512", "pbkdf2-sha256 ", "sha256"} {
		cfg := Default()
		cfg.Server.PasswordHash = bad
		err := Validate(cfg)
		require.ErrorIs(t, err, ErrInvalidConfig, "%q", bad)
		for _, ok := range PasswordHashes() {
			require.Contains(t, err.Error(), ok, "报错里列出可以取的值")
		}
	}
	for _, name := range []string{PasswordHashPBKDF2SHA256, PasswordHashPBKDF2SHA512} {
		t.Setenv("GA_SERVER_PASSWORD_HASH", name)
		cfg, err := Load("")
		require.NoError(t, err)
		require.Equal(t, name, cfg.Server.PasswordHash)
		require.NoError(t, Validate(cfg))
	}
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

// D-061：代理商、商户程序只加载自己的端。配置文件里别的端（含密钥）被丢掉，release 模式也不要求别的端的密钥。
func TestLoadFor_KeepsOnlyListedPortals(t *testing.T) {
	t.Setenv("GA_JWT_SECRET_PLATFORM", "")
	t.Setenv("GA_JWT_SECRET_MERCHANT", "merchant-secret-from-env-0123456789abcdef")
	p := writeYAML(t, `
server:
  mode: release
  allowedOrigins: ["https://merchant.example.com"]
database:
  password: db-secret
portals:
  platform:
    jwtSecret: platform-secret-in-file-0123456789abcdef
  merchant:
    accessTTL: 10m
    jwtSecret: merchant-secret-in-file-0123456789abcdef
`)
	cfg, err := LoadFor(p, "merchant")
	require.NoError(t, err)
	require.Len(t, cfg.Portals, 1)
	require.Contains(t, cfg.Portals, "merchant")
	require.NotContains(t, cfg.Portals, DefaultPortalCode, "平台端的段落和密钥不能进商户程序")
	require.Equal(t, "merchant-secret-from-env-0123456789abcdef", cfg.Portals["merchant"].JWTSecret, "环境变量优先")
	require.Equal(t, 10*time.Minute, cfg.Portals["merchant"].AccessTTL)
	require.Equal(t, 168*time.Hour, cfg.Portals["merchant"].RefreshTTL, "没写的有效期取默认值")
	require.NoError(t, Validate(cfg), "release 模式只要求本程序的端有密钥")

	// 没有配置文件、只有环境变量：列出的端补上默认值
	cfg, err = LoadFor("", "agent")
	require.NoError(t, err)
	require.Len(t, cfg.Portals, 1)
	require.Equal(t, 15*time.Minute, cfg.Portals["agent"].AccessTTL)

	// 本程序的端没有密钥：release 模式拒绝启动（别的端有也不算）
	t.Setenv("GA_JWT_SECRET_MERCHANT", "")
	p = writeYAML(t, `
server:
  mode: release
  allowedOrigins: ["https://merchant.example.com"]
database:
  password: db-secret
portals:
  platform:
    jwtSecret: platform-secret-in-file-0123456789abcdef
`)
	cfg, err = LoadFor(p, "merchant")
	require.NoError(t, err)
	require.ErrorContains(t, Validate(cfg), "端 merchant 缺少 JWT 密钥")

	_, err = LoadFor(p)
	require.Error(t, err)
}

// 规范 §13.2 第 163 条（D-074）：Redis 是可选的——默认不配（addr 为空），这时别的 redis.* 写什么都不检查；
// 配了 addr 才校验：地址是 主机:端口，库号 0–255，键前缀 1–32 个字符且只有字母、数字和 : _ -（不能有花括号、
// 通配符、空白），设了用户名就得有密码，启动等待 0–10 分钟。每一项都能用环境变量设。
func TestValidate_163_Redis(t *testing.T) {
	def := Default()
	require.False(t, def.Redis.Enabled(), "默认不用 Redis")
	require.Equal(t, "", def.Redis.Addr)
	require.Equal(t, "ga:", def.Redis.KeyPrefix)
	require.Equal(t, 5*time.Second, def.Redis.ConnectWait)
	require.NoError(t, Validate(def))

	// 没配地址：别的写错了也不管
	off := Default()
	off.Redis = Redis{DB: -1, KeyPrefix: "{bad}", Username: "u", ConnectWait: -time.Second}
	require.NoError(t, Validate(off))

	on := func(tweak func(r *Redis)) error {
		cfg := Default()
		cfg.Redis.Addr = "127.0.0.1:6379"
		tweak(&cfg.Redis)
		return Validate(cfg)
	}
	require.NoError(t, on(func(*Redis) {}))
	for _, addr := range []string{"redis.internal:6380", "[::1]:6379", "r-abc.redis.example.com:6379", "10.0.0.5:1", "h:65535"} {
		require.NoError(t, on(func(r *Redis) { r.Addr = addr }), addr)
	}
	for _, addr := range []string{"127.0.0.1", "localhost:", ":6379", "host:0", "host:65536", "host:abc", "host:06379", "redis://host:6379", "a:1:2", "host: 6379"} {
		err := on(func(r *Redis) { r.Addr = addr })
		require.ErrorIs(t, err, ErrInvalidConfig, addr)
		require.Contains(t, err.Error(), "redis.addr", addr)
	}
	for _, db := range []int{0, 1, 15, 255} {
		require.NoError(t, on(func(r *Redis) { r.DB = db }), db)
	}
	for _, db := range []int{-1, 256, 1000} {
		require.ErrorContains(t, on(func(r *Redis) { r.DB = db }), "redis.db", db)
	}
	for _, p := range []string{"ga:", "a", "prod_1:", "team-a:ga:", strings.Repeat("x", 32)} {
		require.NoError(t, on(func(r *Redis) { r.KeyPrefix = p }), p)
	}
	for _, p := range []string{"", "ga {x}:", "{ga}:", "ga*", "ga ?", "带中文:", "a b", "a\n", strings.Repeat("x", 33)} {
		require.ErrorContains(t, on(func(r *Redis) { r.KeyPrefix = p }), "redis.keyPrefix", "%q", p)
	}
	require.NoError(t, on(func(r *Redis) { r.Password = "only-password" }))
	require.NoError(t, on(func(r *Redis) { r.Username, r.Password = "app", "pw" }))
	require.ErrorContains(t, on(func(r *Redis) { r.Username = "app" }), "redis.password")
	for _, w := range []time.Duration{0, time.Second, 10 * time.Minute} {
		require.NoError(t, on(func(r *Redis) { r.ConnectWait = w }), w)
	}
	for _, w := range []time.Duration{-time.Second, 10*time.Minute + time.Second} {
		require.ErrorContains(t, on(func(r *Redis) { r.ConnectWait = w }), "redis.connectWait", w)
	}

	t.Setenv("GA_REDIS_ADDR", "cache.internal:6380")
	t.Setenv("GA_REDIS_USERNAME", "app")
	t.Setenv("GA_REDIS_PASSWORD", "from-env")
	t.Setenv("GA_REDIS_DB", "3")
	t.Setenv("GA_REDIS_TLS", "true")
	t.Setenv("GA_REDIS_KEY_PREFIX", "prod:")
	t.Setenv("GA_REDIS_CONNECT_WAIT", "30s")
	cfg, err := Load("")
	require.NoError(t, err)
	require.Equal(t, Redis{Addr: "cache.internal:6380", Username: "app", Password: "from-env", DB: 3, TLS: true, KeyPrefix: "prod:", ConnectWait: 30 * time.Second}, cfg.Redis)
	require.True(t, cfg.Redis.Enabled())
	require.NoError(t, Validate(cfg))
}

// 182（D-098）：release 模式不接受全网段的可信代理；debug 模式不限制。
func TestValidate_182_ReleaseRejectsTrustEveryoneProxies(t *testing.T) {
	release := func(proxies ...string) error {
		cfg := Default()
		cfg.Server.Mode = ModeRelease
		cfg.Server.AllowedOrigins = []string{"https://admin.example.com"}
		cfg.Database.Password = "db-password"
		cfg.Portals["platform"] = Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: strings.Repeat("k", 16) + strings.Repeat("7", 16)}
		cfg.Server.TrustedProxies = proxies
		return Validate(cfg)
	}
	require.NoError(t, release())
	require.NoError(t, release("10.0.0.5", "10.0.1.0/24", "172.16.0.0/12", "fd00::/8", "::ffff:10.0.0.0/104", "::1/128"))
	for _, all := range []string{"0.0.0.0/0", "::/0", "10.1.2.3/0", "::ffff:0:0/96", "::ffff:0.0.0.0/96"} {
		err := release("10.0.0.5", all)
		require.ErrorIs(t, err, ErrInvalidConfig, all)
		require.Contains(t, err.Error(), "trustedProxies", all)
	}
	cfg := Default() // debug
	cfg.Server.TrustedProxies = []string{"0.0.0.0/0"}
	require.NoError(t, Validate(cfg))
}

// 182（D-098）：哪些监听地址会接受本机之外的连接。
func TestServer_182_ListensBeyondLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"0.0.0.0:8080": true, ":8080": true, "[::]:8080": true, "192.168.1.5:8080": true, "example.internal:8080": true, "bad": true,
		"127.0.0.1:8080": false, "localhost:8080": false, "[::1]:8080": false, "127.0.0.9:80": false,
	} {
		require.Equal(t, want, Server{Addr: addr}.ListensBeyondLoopback(), addr)
	}
}
