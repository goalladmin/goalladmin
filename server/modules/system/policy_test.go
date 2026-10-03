package system_test

// 安全策略进配置文件（docs/decisions.md D-024）的反向测试：越过底线拒绝启动、只能调严、
// 复杂度要求对改密和建用户都生效、生成的密码满足策略、密码过期后必须先改密。

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// setupWith 用给定配置建应用并返回 Setup 的错误（越过底线时应当拒绝启动）。
func setupWith(t *testing.T, tweak func(p *conf.Portal)) error {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	tweak(&p)
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(system.Module())
	return a.Setup()
}

func TestPolicy_FloorsRefuseStartup(t *testing.T) {
	cases := map[string]func(p *conf.Portal){
		"失败 21 次才锁（上限 20）":        func(p *conf.Portal) { p.Login.LockAfterFailures = 21 },
		"失败 6 次才要验证码（上限 5）":       func(p *conf.Portal) { p.Login.CaptchaAfterFailures = 6 },
		"账号累计 500 次才锁（上限 200）":    func(p *conf.Portal) { p.Login.AccountLockAfter = 500 },
		"只锁 30 秒（下限 1 分钟）":        func(p *conf.Portal) { p.Login.LockDuration = 30 * time.Second },
		"每 IP 每分钟 1000 次（上限 600）": func(p *conf.Portal) { p.Login.IPRatePerMinute = 1000 },
		"每账号每分钟 500 次（上限 120）":    func(p *conf.Portal) { p.Login.AccountRatePerMinute = 500 },
		"负数当作关闭":                  func(p *conf.Portal) { p.Login.LockAfterFailures = -1 },
		"密码最短 6 位（下限 8）":          func(p *conf.Portal) { p.Password.MinLength = 6 },
		"密码有效期为负":                 func(p *conf.Portal) { p.Password.MaxAgeDays = -1 },
		"密码有效期 5000 天（上限 3650）":   func(p *conf.Portal) { p.Password.MaxAgeDays = 5000 },
	}
	for name, tweak := range cases {
		t.Run(name, func(t *testing.T) {
			require.Error(t, setupWith(t, tweak), "越过底线必须拒绝启动")
		})
	}
	// 底线之内的严格配置可以启动
	require.NoError(t, setupWith(t, func(p *conf.Portal) {
		p.Login = conf.PortalLogin{CaptchaAlways: true, LockAfterFailures: 3, AccountLockAfter: 20, LockDuration: time.Hour, IPRatePerMinute: 5, AccountRatePerMinute: 3}
		p.Password = conf.PortalPassword{MinLength: 16, RequireUpper: true, RequireLower: true, RequireSymbol: true, MaxAgeDays: 90}
	}))
}

func TestPolicy_CaptchaAlways(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) {
		p := cfg.Portals[conf.DefaultPortalCode]
		p.Login.CaptchaAlways = true
		cfg.Portals[conf.DefaultPortalCode] = p
	})
	_, err := system.CreateAdmin(f.app.Context(context.Background()), f.app.Deps(), "root")
	require.NoError(t, err)
	r := f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "whatever-123"})
	require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code, "第一次登录就要验证码")
}

func TestPolicy_PasswordComplexityAndGeneratedPasswords(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) {
		p := cfg.Portals[conf.DefaultPortalCode]
		p.Password = conf.PortalPassword{MinLength: 14, RequireUpper: true, RequireSymbol: true}
		cfg.Portals[conf.DefaultPortalCode] = p
	})
	ctx := f.app.Context(context.Background())
	pwd, err := system.CreateAdmin(ctx, f.app.Deps(), "root")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(pwd), 20, "生成的初始密码满足策略")
	tok := f.login("root", pwd)

	// 策略下发给前端
	me := f.do(tok, "GET", "/auth/me", nil)
	pp := me.env.Data.(map[string]any)["pwdPolicy"].(map[string]any)
	require.EqualValues(t, 14, pp["minLength"])
	require.Equal(t, true, pp["requireUpper"])
	require.Equal(t, false, pp["requireLower"])
	require.Equal(t, true, pp["requireSymbol"])

	for _, bad := range []string{"abcdefgh1234567", "Abcdefgh1234567", "Abc!1234"} {
		r := f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": pwd, "newPassword": bad})
		require.Equal(t, httpx.CodeValidation, r.env.Code, bad)
	}
	r := f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": pwd, "newPassword": "Abcdefgh!12345"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 建用户时给的密码同样按策略校验；不给就生成，生成的满足策略、能直接登录
	r = f.do(tok, "POST", "/system/users", gin.H{"username": "weak", "password": "abcdefgh12"})
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	r = f.do(tok, "POST", "/system/users", gin.H{"username": "bob"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	initial := r.data()["initialPassword"].(string)
	f.login("bob", initial)
}

func TestPolicy_PasswordExpiry(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) {
		p := cfg.Portals[conf.DefaultPortalCode]
		p.Password.MaxAgeDays = 30
		cfg.Portals[conf.DefaultPortalCode] = p
	})
	admin, _ := f.admin("root") // 已完成首次改密，pwd_changed_at 是现在
	uid, bob := f.createUser(admin, "bob", nil)
	r := f.do(bob, "GET", "/system/options/users", nil)
	require.Equal(t, 0, r.env.Code, "密码没过期，正常访问")

	// 把 bob 的改密时间拨到 31 天前
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET pwd_changed_at = ? WHERE id = ?", time.Now().UTC().Add(-31*24*time.Hour), uid).Error)
	f.app.Deps().Auth.ForgetAccount("platform", uid)

	r = f.do("", "POST", "/auth/login", gin.H{"username": "bob", "password": "user-pass-456"})
	require.Equal(t, 0, r.env.Code, "过期后仍然能登录")
	require.Equal(t, true, r.data()["mustChangePwd"])
	require.Equal(t, true, r.data()["pwdExpired"])
	tok := r.data()["accessToken"].(string)
	r = f.do(tok, "GET", "/system/options/users", nil)
	require.Equal(t, 403, r.rec.Code)
	require.Equal(t, httpx.CodePwdChangeRequired, r.env.Code, "改密之前只能访问 /auth/*")
	r = f.do(bob, "GET", "/system/options/users", nil)
	require.Equal(t, httpx.CodePwdChangeRequired, r.env.Code, "已经登录的会话同样被拦下")
	me := f.do(tok, "GET", "/auth/me", nil)
	require.Equal(t, true, me.env.Data.(map[string]any)["user"].(map[string]any)["pwdExpired"])

	r = f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-456", "newPassword": "user-pass-789"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(tok, "GET", "/system/options/users", nil)
	require.Equal(t, 0, r.env.Code, "改完密码恢复正常")
}
