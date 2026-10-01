package system_test

// 安全设置（D-034）：只读展示生效的登录防护和密码策略。

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// policyItems 读安全设置，返回 键 → 项。
func (f *fixture) policyItems(token string) map[string]map[string]any {
	f.t.Helper()
	r := f.do(token, "GET", "/system/security-policy", nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(f.t, "platform", r.data()["portal"])
	out := map[string]map[string]any{}
	for _, v := range r.data()["items"].([]any) {
		m := v.(map[string]any)
		out[m["key"].(string)] = m
	}
	return out
}

func TestSecurityPolicy_ShowsEffectiveValuesAndSources(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) {
		p := cfg.Portals[conf.DefaultPortalCode]
		p.Login.LockAfterFailures = 5
		p.Login.LockDuration = 30 * time.Minute
		p.Password.MinLength = 12
		p.Password.RequireSymbol = true
		p.Password.MaxAgeDays = 90
		cfg.Portals[conf.DefaultPortalCode] = p
	})
	admin, _ := f.admin("root")
	r := f.do(admin, "GET", "/system/security-policy", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	secret := f.app.Deps().Conf.Portals[conf.DefaultPortalCode].JWTSecret
	require.NotEmpty(t, secret)
	require.NotContains(t, r.rec.Body.String(), secret, "端的其他配置（JWT 密钥）不能出现在返回里")
	require.NotContains(t, r.rec.Body.String(), "jwtSecret")

	items := f.policyItems(admin)
	// 策略结构体每加一个字段，页面就要多一项：漏展示的策略在这里失败
	fields := reflect.TypeFor[portal.LoginPolicy]().NumField() + reflect.TypeFor[portal.PasswordPolicy]().NumField()
	require.Len(t, items, fields, "登录防护和密码策略的每个字段都要展示")
	check := func(key, group, kind string, value any, source string) {
		t.Helper()
		it, ok := items[key]
		require.True(t, ok, key)
		require.Equal(t, group, it["group"], key)
		require.Equal(t, kind, it["kind"], key)
		require.Equal(t, value, it["value"], key)
		require.Equal(t, source, it["source"], key)
	}
	// 配置文件里写了的
	check("lockAfterFailures", system.PolicyGroupLock, system.PolicyKindTimes, float64(5), system.PolicySourceConfig)
	check("lockDuration", system.PolicyGroupLock, system.PolicyKindSeconds, float64(1800), system.PolicySourceConfig)
	check("minLength", system.PolicyGroupPassword, system.PolicyKindChars, float64(12), system.PolicySourceConfig)
	check("requireSymbol", system.PolicyGroupPassword, system.PolicyKindBool, true, system.PolicySourceConfig)
	check("maxAgeDays", system.PolicyGroupExpiry, system.PolicyKindDays, float64(90), system.PolicySourceConfig)
	// 没写的：框架默认值
	d := portal.DefaultLoginPolicy()
	check("captchaAfterFailures", system.PolicyGroupCaptcha, system.PolicyKindTimes, float64(d.CaptchaAfterFailures), system.PolicySourceDefault)
	check("accountLockAfter", system.PolicyGroupLock, system.PolicyKindTimes, float64(d.AccountLockAfter), system.PolicySourceDefault)
	check("window", system.PolicyGroupLock, system.PolicyKindSeconds, d.Window.Seconds(), system.PolicySourceDefault)
	check("ipRatePerMinute", system.PolicyGroupRate, system.PolicyKindPerMinute, float64(d.IPRatePerMinute), system.PolicySourceDefault)
	check("accountRatePerMinute", system.PolicyGroupRate, system.PolicyKindPerMinute, float64(d.AccountRatePerMinute), system.PolicySourceDefault)
	check("requireUpper", system.PolicyGroupPassword, system.PolicyKindBool, false, system.PolicySourceDefault)
	check("requireLower", system.PolicyGroupPassword, system.PolicyKindBool, false, system.PolicySourceDefault)
	check("captchaAlways", system.PolicyGroupCaptcha, system.PolicyKindBool, false, system.PolicySourceDefault)

	// 配置项路径、范围、默认值
	lock := items["lockAfterFailures"]
	require.Equal(t, "portals.platform.login.lockAfterFailures", lock["configKey"])
	require.EqualValues(t, 1, lock["min"])
	require.EqualValues(t, portal.MaxLockAfterFailures, lock["max"])
	require.EqualValues(t, d.LockAfterFailures, lock["default"])
	require.Equal(t, "portals.platform.password.maxAgeDays", items["maxAgeDays"]["configKey"])
	require.EqualValues(t, 0, items["maxAgeDays"]["default"], "默认不过期")
	require.NotContains(t, items["captchaAlways"], "min", "开关没有范围")

	// 展示的就是改密实际使用的那一份：与 /auth/me 下发的密码策略一致
	me := f.do(admin, "GET", "/auth/me", nil)
	require.EqualValues(t, 12, me.data()["pwdPolicy"].(map[string]any)["minLength"])
}

// 展示的锁定次数就是登录实际执行的锁定次数。
func TestSecurityPolicy_MatchesEnforcement(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) {
		p := cfg.Portals[conf.DefaultPortalCode]
		p.Login.LockAfterFailures = 2 // 比出验证码的次数（默认 3）小，锁定先于验证码发生
		cfg.Portals[conf.DefaultPortalCode] = p
	})
	admin, _ := f.admin("root")
	require.EqualValues(t, 2, f.policyItems(admin)["lockAfterFailures"]["value"])
	for i := 0; i < 2; i++ {
		r := f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "wrong-pass-1"})
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
	}
	r := f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "changed-pass-9"})
	require.Equal(t, httpx.CodeLocked, r.env.Code, "失败 2 次后，密码正确也被锁定")
}

// 代码注册端时声明的值，来源显示为"代码"；配置文件再写就以配置为准。
func TestBuildSecurityPolicy_SourceCode(t *testing.T) {
	code := portal.Portal{Code: "merchant", Login: portal.LoginPolicy{LockAfterFailures: 7}, Password: portal.PasswordPolicy{RequireUpper: true, MaxAge: 30 * 24 * time.Hour}}
	login := code.Login.Normalized()
	pwd := code.Password.Normalized()
	find := func(v system.SecurityPolicyView, key string) system.PolicyItem {
		for _, it := range v.Items {
			if it.Key == key {
				return it
			}
		}
		t.Fatalf("没有 %s", key)
		return system.PolicyItem{}
	}
	v := system.BuildSecurityPolicy("merchant", login, pwd, code, conf.PortalLogin{}, conf.PortalPassword{})
	require.Equal(t, "merchant", v.Portal)
	require.Equal(t, system.PolicySourceCode, find(v, "lockAfterFailures").Source)
	require.EqualValues(t, 7, find(v, "lockAfterFailures").Value)
	require.Equal(t, "portals.merchant.login.lockAfterFailures", find(v, "lockAfterFailures").ConfigKey)
	require.Equal(t, system.PolicySourceCode, find(v, "requireUpper").Source)
	require.Equal(t, system.PolicySourceCode, find(v, "maxAgeDays").Source)
	require.EqualValues(t, 30, find(v, "maxAgeDays").Value)
	require.Equal(t, system.PolicySourceDefault, find(v, "lockDuration").Source)

	login.LockAfterFailures = 4
	v = system.BuildSecurityPolicy("merchant", login, pwd, code, conf.PortalLogin{LockAfterFailures: 4}, conf.PortalPassword{})
	require.Equal(t, system.PolicySourceConfig, find(v, "lockAfterFailures").Source)
	require.EqualValues(t, 4, find(v, "lockAfterFailures").Value)

	// 开关项的生效值是"代码或配置任一打开"：代码已经打开时，删掉配置里的这一行也不会变，来源算代码
	for _, tc := range []struct {
		code, cfg bool
		want      string
	}{
		{false, false, system.PolicySourceDefault},
		{false, true, system.PolicySourceConfig},
		{true, false, system.PolicySourceCode},
		{true, true, system.PolicySourceCode},
	} {
		c := portal.Portal{Code: "merchant", Password: portal.PasswordPolicy{RequireSymbol: tc.code}}
		eff := c.Password.Normalized()
		eff.RequireSymbol = tc.code || tc.cfg
		v := system.BuildSecurityPolicy("merchant", portal.DefaultLoginPolicy(), eff, c, conf.PortalLogin{}, conf.PortalPassword{RequireSymbol: tc.cfg})
		require.Equal(t, tc.want, find(v, "requireSymbol").Source, "code=%v cfg=%v", tc.code, tc.cfg)
		require.Equal(t, tc.code || tc.cfg, find(v, "requireSymbol").Value)
	}
}

// 反向：没有权限码 403；敏感权限只有超管能授出；没有任何写接口。
func TestSecurityPolicy_PermissionAndReadOnly(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")

	plain := f.createRole(admin, "plain", []string{system.PermUserList})
	_, bob := f.createUser(admin, "bob", []uint64{plain})
	r := f.do(bob, "GET", "/system/security-policy", nil)
	require.Equal(t, 403, r.rec.Code)
	require.Equal(t, httpx.CodeForbidden, r.env.Code)

	// 超管授给角色后可以看
	viewer := f.createRole(admin, "viewer", []string{system.PermSecurityView, system.PermRoleList, system.PermRoleGrant})
	_, carol := f.createUser(admin, "carol", []uint64{viewer})
	require.Equal(t, 200, f.do(carol, "GET", "/system/security-policy", nil).rec.Code)

	// 自己有这个权限码的非超管也不能把它授给别的角色：它是敏感权限
	other := f.createRole(admin, "other", nil)
	g := f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", other), gin.H{"codes": []string{system.PermSecurityView}})
	require.Equal(t, 403, g.rec.Code, g.rec.Body.String())
	require.Contains(t, g.rec.Body.String(), "sensitive", "被拒的原因是敏感权限，而不是自己没有")

	// 没有写接口：策略只能在配置文件里改（D-024）
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		st, _ := f.try(admin, m, "/system/security-policy", gin.H{"login": gin.H{"lockAfterFailures": 20}})
		require.Contains(t, []int{404, 405}, st, m)
	}
}

// 字典挪进"系统设置"（D-034）：菜单名、路由、权限码都不变。
func TestSecurityPolicy_SettingsMenuGroup(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	me := f.meMenus(admin)
	require.Equal(t, "", me["settings"].parent)
	require.Equal(t, "settings", me["system-dict"].parent)
	require.Equal(t, "/system/dicts", me["system-dict"].node["path"])
	require.Equal(t, "settings", me["settings-security"].parent)
	require.Equal(t, "/settings/security", me["settings-security"].node["path"])

	// 只有字典权限的人：看到"系统设置"和字典，看不到安全设置
	role := f.createRole(admin, "dict-only", []string{system.PermDictList})
	_, bob := f.createUser(admin, "bob", []uint64{role})
	bm := f.meMenus(bob)
	require.Contains(t, bm, "settings")
	require.Contains(t, bm, "system-dict")
	require.NotContains(t, bm, "settings-security")
	require.NotContains(t, bm, "system", "系统管理目录下没有他能看的菜单")
}
