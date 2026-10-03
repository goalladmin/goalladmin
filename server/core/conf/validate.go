package conf

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// MinJWTSecretBytes 是端密钥的最小长度（规范 §11.2）。
const MinJWTSecretBytes = 32

// exampleSecrets 是文档、示例里出现过的占位密钥，release 模式下一律拒绝。
var exampleSecrets = map[string]struct{}{
	"change-me":                            {},
	"changeme":                             {},
	"secret":                               {},
	"example":                              {},
	"please-change-me-to-a-random-secret!": {},
	"0123456789abcdef0123456789abcdef":     {},
	// Makefile 里 make run / make admin（以及代理商、商户程序的 make run-agent / run-merchant）的本地开发默认值
	"dev-only-jwt-secret-not-for-production-0123456789":  {},
	"dev-only-agent-jwt-secret-not-for-production-01234": {},
	"dev-only-merchant-jwt-secret-not-for-production-0":  {},
}

// Validate 校验配置。任何模式下都检查基本合法性；release 模式额外检查机密和安全项。
// 返回的错误包装了 ErrInvalidConfig，并把所有问题一次列全，方便一次改完。
// MaxPasswordParallel 是 server.passwordParallel 允许的最大值：每个在算的请求占 19 MiB，256 个就是十个 G 上下的峰值。
const MaxPasswordParallel = 256

func Validate(cfg *Config) error {
	var problems []string
	add := func(format string, a ...any) { problems = append(problems, fmt.Sprintf(format, a...)) }

	if o := cfg.Onboarding; o.MerchantEnabled || o.MerchantOrigin != "" {
		u, err := url.Parse(o.MerchantOrigin)
		if err != nil || !validOrigin(o.MerchantOrigin) || u.User != nil || u.Hostname() == "" || (cfg.Server.IsRelease() && !strings.HasPrefix(o.MerchantOrigin, "https://")) {
			add("onboarding.merchantOrigin 需要有效来源，release 模式必须为 HTTPS")
		}
	}
	// ---- 任何模式 ----
	switch cfg.Server.Mode {
	case ModeDebug, ModeRelease:
	default:
		add("server.mode 必须是 debug 或 release，当前是 %q", cfg.Server.Mode)
	}
	if strings.TrimSpace(cfg.Server.Addr) == "" {
		add("server.addr 不能为空")
	}
	if cfg.Server.MaxBodyBytes <= 0 {
		add("server.maxBodyBytes 必须大于 0")
	}
	// 0 在 net/http 里表示"不限时"，慢速请求就能一直占着连接，所以四个超时一个都不能省
	if cfg.Server.ReadHeaderTimeout <= 0 || cfg.Server.ReadTimeout <= 0 || cfg.Server.WriteTimeout <= 0 || cfg.Server.IdleTimeout <= 0 {
		add("server.readHeaderTimeout / readTimeout / writeTimeout / idleTimeout 都必须大于 0")
	}
	if cfg.Server.ShutdownTimeout <= 0 {
		add("server.shutdownTimeout 必须大于 0")
	}
	// 请求时限（D-037）：必须小于 writeTimeout，超时的请求才来得及把 503 写回去
	if cfg.Server.HandlerTimeout <= 0 || (cfg.Server.WriteTimeout > 0 && cfg.Server.HandlerTimeout >= cfg.Server.WriteTimeout) {
		add("server.handlerTimeout 必须大于 0 且小于 server.writeTimeout（当前 %s / %s）；调小 writeTimeout 时要同时调小 handlerTimeout（GA_SERVER_HANDLER_TIMEOUT）",
			cfg.Server.HandlerTimeout, cfg.Server.WriteTimeout)
	}
	for name, d := range map[string]time.Duration{"monitor.serverCache": cfg.Monitor.ServerCache, "monitor.securityCache": cfg.Monitor.SecurityCache} {
		if d < 0 || d > time.Minute {
			add("%s 必须在 0 到 1 分钟之间，当前是 %s", name, d)
		}
	}
	for _, p := range cfg.Server.TrustedProxies {
		if !validProxy(p) {
			add("server.trustedProxies 含无效地址 %q（需要 IP 或 CIDR）", p)
		}
	}
	// 密码计算的并发上限（D-070）：0 表示按 CPU 数；每个在算的请求占 19 MiB，上限太大等于不设
	if cfg.Server.PasswordParallel < 0 || cfg.Server.PasswordParallel > MaxPasswordParallel {
		add("server.passwordParallel 必须在 0（按 CPU 数）到 %d 之间，当前是 %d", MaxPasswordParallel, cfg.Server.PasswordParallel)
	}
	// 密码哈希算法（D-070、D-072）：只有这几种。写错了不悄悄用默认的——不用默认值的人多半有理由
	// （要和旧版本并存、内存紧、有 FIPS 140 要求），悄悄换成 Argon2id 正好违背他的要求
	if !slices.Contains(PasswordHashes(), cfg.Server.PasswordHash) {
		add("server.passwordHash 必须是 %s 之一，当前是 %q", strings.Join(PasswordHashes(), "、"), cfg.Server.PasswordHash)
	}
	for _, o := range cfg.Server.AllowedOrigins {
		if !validOrigin(o) {
			add("server.allowedOrigins 含无效来源 %q（需要 scheme://host[:port]）", o)
		}
	}
	if cfg.Database.Name == "" || cfg.Database.User == "" || cfg.Database.Host == "" {
		add("database.host / name / user 不能为空")
	}
	if cfg.Database.Port <= 0 || cfg.Database.Port > 65535 {
		add("database.port 无效: %d", cfg.Database.Port)
	}
	// 连接池上限（D-069）：database/sql 把 0 和负数当成"不限"，高峰时连接数没有上限、能把 MySQL 的连接数占满
	// （三个程序共用一个库，一个占满了另外两个也连不上），所以必须是正数。空闲连接数 0 表示用 database/sql 的默认值；
	// 比上限大没关系，database/sql 自己会压到上限以内
	if cfg.Database.MaxOpenConns < 1 {
		add("database.maxOpenConns 必须不小于 1（0 和负数等于不限制连接数），当前是 %d", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns < 0 {
		add("database.maxIdleConns 不能是负数，当前是 %d", cfg.Database.MaxIdleConns)
	}
	// 数据库超时（D-037）：0 等于不限时，数据库假死时连接会一直卡着，所以不允许
	for _, b := range []struct {
		name     string
		v        time.Duration
		min, max time.Duration
	}{
		{"database.connectTimeout", cfg.Database.ConnectTimeout, time.Second, time.Minute},
		{"database.readTimeout", cfg.Database.ReadTimeout, time.Second, 10 * time.Minute},
		{"database.writeTimeout", cfg.Database.WriteTimeout, time.Second, 10 * time.Minute},
		{"database.connectWait", cfg.Database.ConnectWait, 0, 10 * time.Minute},
	} {
		if b.v < b.min || b.v > b.max {
			add("%s 必须在 %s 到 %s 之间，当前是 %s", b.name, b.min, b.max, b.v)
		}
	}
	// 请求里的查询要先被请求时限取消（回 503），而不是先被读超时掐断连接（回 500）
	if cfg.Database.ReadTimeout > 0 && cfg.Database.ReadTimeout < cfg.Server.HandlerTimeout {
		add("database.readTimeout（%s）不能小于 server.handlerTimeout（%s）", cfg.Database.ReadTimeout, cfg.Server.HandlerTimeout)
	}
	// Redis（D-074）：只在配了地址时检查。写错了不悄悄当成"没配"——配它的人是要跑多个实例的，
	// 悄悄退回单机的内存状态正好违背他的要求
	if r := cfg.Redis; r.Enabled() {
		if host, port, err := net.SplitHostPort(r.Addr); err != nil || host == "" || !validPort(port) {
			add("redis.addr 必须是 主机:端口，当前是 %q", r.Addr)
		}
		if r.DB < 0 || r.DB > 255 {
			add("redis.db 必须在 0 到 255 之间，当前是 %d", r.DB)
		}
		if !redisKeyPrefixRe.MatchString(r.KeyPrefix) {
			add("redis.keyPrefix 必须是 1 到 32 个字符，只能有字母、数字和 : _ -，当前是 %q", r.KeyPrefix)
		}
		if r.Username != "" && r.Password == "" {
			add("redis.username 设了的时候 redis.password 不能为空")
		}
		if r.ConnectWait < 0 || r.ConnectWait > 10*time.Minute {
			add("redis.connectWait 必须在 0 到 10m 之间，当前是 %s", r.ConnectWait)
		}
	}
	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level 必须是 debug/info/warn/error，当前是 %q", cfg.Log.Level)
	}
	switch cfg.Log.Format {
	case "text", "json":
	default:
		add("log.format 必须是 text 或 json，当前是 %q", cfg.Log.Format)
	}
	if len(cfg.Portals) == 0 {
		add("portals 至少要有一个端")
	}
	for code, p := range cfg.Portals {
		if !validPortalCode(code) {
			add("端代号 %q 无效（只能是小写字母、数字和连字符，字母开头）", code)
		}
		if p.AccessTTL <= 0 || p.RefreshTTL <= 0 {
			add("portals.%s 的 accessTTL / refreshTTL 必须大于 0", code)
		}
		if p.AccessTTL >= p.RefreshTTL {
			add("portals.%s 的 accessTTL 必须小于 refreshTTL", code)
		}
	}

	// ---- release 模式 ----
	if cfg.Server.IsRelease() {
		if cfg.Database.Password == "" {
			add("release 模式下 database.password 不能为空（通过 GA_DB_PASSWORD 提供）")
		}
		if len(cfg.Server.AllowedOrigins) == 0 {
			add("release 模式下 server.allowedOrigins 不能为空")
		}
		// 信任所有来源等于让请求方自己决定客户端 IP：按 IP 的限流、锁定、黑白名单都失去意义（D-098）
		for _, p := range cfg.Server.TrustedProxies {
			if trustsEveryone(p) {
				add("release 模式下 server.trustedProxies 不能是全网段（%q）：只填反向代理自己的地址或网段", p)
			}
		}
		seen := map[string]string{}
		for code, p := range cfg.Portals {
			env := JWTSecretEnv(code)
			switch {
			case p.JWTSecret == "":
				add("端 %s 缺少 JWT 密钥（通过 %s 提供）", code, env)
			case len(p.JWTSecret) < MinJWTSecretBytes:
				add("端 %s 的 JWT 密钥不足 %d 字节（%s）", code, MinJWTSecretBytes, env)
			default:
				if _, bad := exampleSecrets[strings.ToLower(p.JWTSecret)]; bad {
					add("端 %s 的 JWT 密钥是示例值，必须更换（%s）", code, env)
				}
				if other, dup := seen[p.JWTSecret]; dup {
					add("端 %s 与端 %s 使用了相同的 JWT 密钥，各端密钥必须不同", code, other)
				}
				seen[p.JWTSecret] = code
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  - %s", ErrInvalidConfig, strings.Join(problems, "\n  - "))
}

func validProxy(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// trustsEveryone 报告一条可信代理是不是覆盖全部地址的网段：0.0.0.0/0、::/0，以及 IPv4 映射写法的 ::ffff:0:0/96。
// 只认这种一眼看得出的写法；把全网段拆成几段来写不在检查范围内。
func trustsEveryone(s string) bool {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return false
	}
	ones, bits := n.Mask.Size()
	if bits == 128 && n.IP.To4() != nil {
		ones -= 96 // IPv4 映射地址：前 96 位是固定前缀
	}
	return ones <= 0
}

func validOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func validPortalCode(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// redisKeyPrefixRe 限定 Redis 键前缀的写法：不能有空白、通配符和花括号（花括号留给框架自己标 hash tag）。
var redisKeyPrefixRe = regexp.MustCompile(`^[A-Za-z0-9:_-]{1,32}$`)

// validPort 报告 s 是不是 1–65535 的十进制端口号。
func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535 && strconv.Itoa(n) == s
}
