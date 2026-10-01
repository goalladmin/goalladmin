package conf

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// EnvBindings 是"环境变量 → 配置键"的显式对应表。
//
// viper 的 AutomaticEnv 对"配置文件和默认值里都不存在的键"不生效，Unmarshal 会读到空值，
// 所以这里每个可由环境变量提供的键都显式 BindEnv（规范 §11.1）。
// 端的 JWT 密钥（GA_JWT_SECRET_<CODE>）是动态键，在 Load 里单独处理。
var EnvBindings = map[string]string{
	"server.addr":              "GA_SERVER_ADDR",
	"server.mode":              "GA_SERVER_MODE",
	"server.trustedProxies":    "GA_SERVER_TRUSTED_PROXIES",
	"server.allowedOrigins":    "GA_SERVER_ALLOWED_ORIGINS",
	"server.maxBodyBytes":      "GA_SERVER_MAX_BODY_BYTES",
	"server.readHeaderTimeout": "GA_SERVER_READ_HEADER_TIMEOUT",
	"server.readTimeout":       "GA_SERVER_READ_TIMEOUT",
	"server.writeTimeout":      "GA_SERVER_WRITE_TIMEOUT",
	"server.idleTimeout":       "GA_SERVER_IDLE_TIMEOUT",
	"server.shutdownTimeout":   "GA_SERVER_SHUTDOWN_TIMEOUT",
	"server.handlerTimeout":    "GA_SERVER_HANDLER_TIMEOUT",
	"database.host":            "GA_DB_HOST",
	"database.port":            "GA_DB_PORT",
	"database.name":            "GA_DB_NAME",
	"database.user":            "GA_DB_USER",
	"database.password":        "GA_DB_PASSWORD",
	"database.params":          "GA_DB_PARAMS",
	"database.maxOpenConns":    "GA_DB_MAX_OPEN_CONNS",
	"database.maxIdleConns":    "GA_DB_MAX_IDLE_CONNS",
	"database.connMaxLifetime": "GA_DB_CONN_MAX_LIFETIME",
	"database.slowThreshold":   "GA_DB_SLOW_THRESHOLD",
	"database.connectTimeout":  "GA_DB_CONNECT_TIMEOUT",
	"database.readTimeout":     "GA_DB_READ_TIMEOUT",
	"database.writeTimeout":    "GA_DB_WRITE_TIMEOUT",
	"database.connectWait":     "GA_DB_CONNECT_WAIT",
	"log.level":                "GA_LOG_LEVEL",
	"log.format":               "GA_LOG_FORMAT",
	"migrate.auto":             "GA_MIGRATE_AUTO",
	"monitor.server":           "GA_MONITOR_SERVER",
	"monitor.serverCache":      "GA_MONITOR_SERVER_CACHE",
	"monitor.securityCache":    "GA_MONITOR_SECURITY_CACHE",
}

// JWTSecretEnvPrefix 是端密钥环境变量的前缀：GA_JWT_SECRET_PLATFORM。
const JWTSecretEnvPrefix = "GA_JWT_SECRET_" //nolint:gosec // 这是环境变量名，不是凭据

// JWTSecretEnv 返回某个端的密钥环境变量名。
func JWTSecretEnv(portalCode string) string {
	return JWTSecretEnvPrefix + strings.ToUpper(portalCode)
}

// Load 读取配置：默认值 ← YAML 文件 ← 环境变量。
//
// path 为空时不读文件（适合容器里只用环境变量的场景）；path 非空但文件不存在时返回错误。
// Load 不做 release 校验，调用方在需要时调用 Validate。
func Load(path string) (*Config, error) {
	v := viper.New()
	setDefaults(v, "", reflect.ValueOf(*Default()))

	for key, env := range EnvBindings {
		if err := v.BindEnv(key, env); err != nil {
			return nil, fmt.Errorf("conf: bind env %s: %w", env, err)
		}
	}

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("conf: read %s: %w", path, err)
		}
	}

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("conf: unmarshal: %w", err)
	}
	if cfg.Portals == nil {
		cfg.Portals = map[string]Portal{}
	}
	if _, ok := cfg.Portals[DefaultPortalCode]; !ok {
		cfg.Portals[DefaultPortalCode] = Default().Portals[DefaultPortalCode]
	}
	// 端密钥：配置文件里可以写（config.yaml 不进 git），环境变量 GA_JWT_SECRET_<CODE> 优先。
	for code, p := range cfg.Portals {
		if s := os.Getenv(JWTSecretEnv(code)); s != "" {
			p.JWTSecret = s
		}
		if p.AccessTTL == 0 {
			p.AccessTTL = 15 * time.Minute
		}
		if p.RefreshTTL == 0 {
			p.RefreshTTL = 168 * time.Hour
		}
		cfg.Portals[code] = p
	}
	if cfg.Server.TrustedProxies == nil {
		cfg.Server.TrustedProxies = []string{}
	}
	if cfg.Server.AllowedOrigins == nil {
		cfg.Server.AllowedOrigins = []string{}
	}
	return cfg, nil
}

// MustLoad 是 Load 的 panic 版本，供 main 使用。
func MustLoad(path string) *Config {
	cfg, err := Load(path)
	if err != nil {
		panic(err)
	}
	return cfg
}

// setDefaults 把 Default() 展平成 viper 的默认值，保证每个键在 Unmarshal 时都存在。
func setDefaults(v *viper.Viper, prefix string, rv reflect.Value) {
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := f.Tag.Get("mapstructure")
		if tag == "" || tag == "-" {
			continue
		}
		key := tag
		if prefix != "" {
			key = prefix + "." + tag
		}
		fv := rv.Field(i)
		switch fv.Kind() {
		case reflect.Struct:
			setDefaults(v, key, fv)
		case reflect.Map:
			for _, mk := range fv.MapKeys() {
				setDefaults(v, key+"."+mk.String(), fv.MapIndex(mk))
			}
		default:
			v.SetDefault(key, fv.Interface())
		}
	}
}

// ErrInvalidConfig 是 Validate 返回错误的基类，便于调用方判断。
var ErrInvalidConfig = errors.New("invalid config")
