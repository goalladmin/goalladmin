package system

// 安全设置（D-034）：只读展示本端生效的登录防护和密码策略。
// 改动只能走配置文件、重启生效（D-024），这里没有、也不能有写接口。

import (
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// 安全设置页的分组（对应前端的页签）。
const (
	PolicyGroupCaptcha  = "captcha"
	PolicyGroupPassword = "password"
	PolicyGroupRate     = "rate"
	PolicyGroupLock     = "lock"
	PolicyGroupExpiry   = "expiry"
)

// 取值的种类，前端据此格式化。
const (
	PolicyKindBool      = "bool"      // 开关：true / false
	PolicyKindTimes     = "times"     // 次数
	PolicyKindPerMinute = "perMinute" // 每分钟次数
	PolicyKindSeconds   = "seconds"   // 时长，单位秒
	PolicyKindDays      = "days"      // 天数，0 表示不过期
	PolicyKindChars     = "chars"     // 字符数
)

// 值的来源。
const (
	PolicySourceConfig  = "config"  // 配置文件里写了
	PolicySourceCode    = "code"    // 配置文件没写，代码注册端时声明了
	PolicySourceDefault = "default" // 都没写，用框架默认值
)

// PolicyItem 是安全设置页上的一项。Value、Default 是布尔值（种类 bool）或整数（其余种类）。
// Min、Max 是底线允许的范围；布尔项没有范围（只能打开，D-024）。
type PolicyItem struct {
	Group     string `json:"group"`
	Key       string `json:"key"`
	ConfigKey string `json:"configKey"`
	Kind      string `json:"kind"`
	Value     any    `json:"value"`
	Default   any    `json:"default"`
	Min       int64  `json:"min,omitempty"`
	Max       int64  `json:"max,omitempty"`
	Source    string `json:"source"`
}

// SecurityPolicyView 是 GET /system/security-policy 的返回。
type SecurityPolicyView struct {
	Portal string       `json:"portal"`
	Items  []PolicyItem `json:"items"`
}

// BuildSecurityPolicy 把生效的策略整理成页面上的各项。
// login、pwd 是认证器实际使用的生效值；code 是代码注册端时的声明；cl、cp 是配置文件里这个端的原始值，只用来判断来源。
// 只传登录防护和密码两段配置进来，端的其他配置（其中有 JWT 密钥）碰都不碰。
func BuildSecurityPolicy(portalCode string, login portal.LoginPolicy, pwd portal.PasswordPolicy, code portal.Portal, cl conf.PortalLogin, cp conf.PortalPassword) SecurityPolicyView {
	defLogin, defPwd := portal.DefaultLoginPolicy(), portal.DefaultPasswordPolicy()
	// 数值项：配置文件写了就以配置为准，否则看代码声明
	source := func(inConfig, inCode bool) string {
		switch {
		case inConfig:
			return PolicySourceConfig
		case inCode:
			return PolicySourceCode
		default:
			return PolicySourceDefault
		}
	}
	// 开关项：生效值是"代码或配置任一打开"。代码已经打开时配置写不写都一样，来源算代码
	boolSource := func(inConfig, inCode bool) string { return source(inConfig && !inCode, inCode) }
	loginKey := func(k string) string { return fmt.Sprintf("portals.%s.login.%s", portalCode, k) }
	pwdKey := func(k string) string { return fmt.Sprintf("portals.%s.password.%s", portalCode, k) }
	secs := func(d time.Duration) int64 { return int64(d / time.Second) }
	days := func(d time.Duration) int64 { return int64(d / (24 * time.Hour)) }

	dl, dp := code.Login, code.Password
	items := []PolicyItem{
		// 验证码
		{Group: PolicyGroupCaptcha, Key: "captchaAlways", ConfigKey: loginKey("captchaAlways"), Kind: PolicyKindBool,
			Value: login.CaptchaAlways, Default: defLogin.CaptchaAlways, Source: boolSource(cl.CaptchaAlways, dl.CaptchaAlways)},
		{Group: PolicyGroupCaptcha, Key: "captchaAfterFailures", ConfigKey: loginKey("captchaAfterFailures"), Kind: PolicyKindTimes,
			Value: int64(login.CaptchaAfterFailures), Default: int64(defLogin.CaptchaAfterFailures), Min: 1, Max: portal.MaxCaptchaAfterFailures,
			Source: source(cl.CaptchaAfterFailures != 0, dl.CaptchaAfterFailures != 0)},
		// 密码复杂度（字母加数字等始终生效的规则由前端列出）
		{Group: PolicyGroupPassword, Key: "minLength", ConfigKey: pwdKey("minLength"), Kind: PolicyKindChars,
			Value: int64(pwd.MinLength), Default: int64(defPwd.MinLength), Min: portal.MinPasswordLength, Max: portal.MaxPasswordLength,
			Source: source(cp.MinLength != 0, dp.MinLength != 0)},
		{Group: PolicyGroupPassword, Key: "requireUpper", ConfigKey: pwdKey("requireUpper"), Kind: PolicyKindBool,
			Value: pwd.RequireUpper, Default: defPwd.RequireUpper, Source: boolSource(cp.RequireUpper, dp.RequireUpper)},
		{Group: PolicyGroupPassword, Key: "requireLower", ConfigKey: pwdKey("requireLower"), Kind: PolicyKindBool,
			Value: pwd.RequireLower, Default: defPwd.RequireLower, Source: boolSource(cp.RequireLower, dp.RequireLower)},
		{Group: PolicyGroupPassword, Key: "requireSymbol", ConfigKey: pwdKey("requireSymbol"), Kind: PolicyKindBool,
			Value: pwd.RequireSymbol, Default: defPwd.RequireSymbol, Source: boolSource(cp.RequireSymbol, dp.RequireSymbol)},
		// 限流
		{Group: PolicyGroupRate, Key: "ipRatePerMinute", ConfigKey: loginKey("ipRatePerMinute"), Kind: PolicyKindPerMinute,
			Value: int64(login.IPRatePerMinute), Default: int64(defLogin.IPRatePerMinute), Min: 1, Max: portal.MaxIPRatePerMinute,
			Source: source(cl.IPRatePerMinute != 0, dl.IPRatePerMinute != 0)},
		{Group: PolicyGroupRate, Key: "accountRatePerMinute", ConfigKey: loginKey("accountRatePerMinute"), Kind: PolicyKindPerMinute,
			Value: int64(login.AccountRatePerMinute), Default: int64(defLogin.AccountRatePerMinute), Min: 1, Max: portal.MaxAccountRatePerMinute,
			Source: source(cl.AccountRatePerMinute != 0, dl.AccountRatePerMinute != 0)},
		// 失败锁定（统计窗口也决定验证码的计数）
		{Group: PolicyGroupLock, Key: "window", ConfigKey: loginKey("window"), Kind: PolicyKindSeconds,
			Value: secs(login.Window), Default: secs(defLogin.Window), Min: secs(portal.MinWindow), Max: secs(portal.MaxPolicyDuration),
			Source: source(cl.Window != 0, dl.Window != 0)},
		{Group: PolicyGroupLock, Key: "lockAfterFailures", ConfigKey: loginKey("lockAfterFailures"), Kind: PolicyKindTimes,
			Value: int64(login.LockAfterFailures), Default: int64(defLogin.LockAfterFailures), Min: 1, Max: portal.MaxLockAfterFailures,
			Source: source(cl.LockAfterFailures != 0, dl.LockAfterFailures != 0)},
		{Group: PolicyGroupLock, Key: "accountLockAfter", ConfigKey: loginKey("accountLockAfter"), Kind: PolicyKindTimes,
			Value: int64(login.AccountLockAfter), Default: int64(defLogin.AccountLockAfter), Min: 1, Max: portal.MaxAccountLockAfter,
			Source: source(cl.AccountLockAfter != 0, dl.AccountLockAfter != 0)},
		{Group: PolicyGroupLock, Key: "lockDuration", ConfigKey: loginKey("lockDuration"), Kind: PolicyKindSeconds,
			Value: secs(login.LockDuration), Default: secs(defLogin.LockDuration), Min: secs(portal.MinLockDuration), Max: secs(portal.MaxPolicyDuration),
			Source: source(cl.LockDuration != 0, dl.LockDuration != 0)},
		// 密码有效期：0 表示不过期
		{Group: PolicyGroupExpiry, Key: "maxAgeDays", ConfigKey: pwdKey("maxAgeDays"), Kind: PolicyKindDays,
			Value: days(pwd.MaxAge), Default: days(defPwd.MaxAge), Min: days(portal.MinPasswordMaxAge), Max: days(portal.MaxPasswordMaxAge),
			Source: source(cp.MaxAgeDays != 0, dp.MaxAge != 0)},
	}
	return SecurityPolicyView{Portal: portalCode, Items: items}
}

// securityPolicy 返回操作者所在端的生效策略（D-034）。
func (h *handlers) securityPolicy(c *gin.Context) {
	actor := auth.MustFromCtx(c.Request.Context())
	login, pwd, err := h.deps.Auth.Policies(actor.Portal)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	code, _ := h.deps.Portals.Get(actor.Portal)
	cfg := h.deps.Conf.Portals[actor.Portal]
	httpx.OK(c, BuildSecurityPolicy(actor.Portal, login, pwd, code, cfg.Login, cfg.Password))
}
