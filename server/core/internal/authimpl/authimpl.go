// Package authimpl 实现一个端的认证：登录、刷新、登出、当前身份、改密，以及认证中间件。
//
// 每个端一个 Authenticator，由 core/app 在装配阶段创建；模块通过 core/auth 的公开接口使用它。
package authimpl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/captcha"
	"github.com/goalladmin/goalladmin/server/core/internal/loginguard"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/internal/secmark"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/internal/token"
	"github.com/goalladmin/goalladmin/server/core/internal/ttlcache"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// HeaderClient 是登录、刷新、登出接口要求的客户端标识头（规范 §5.2）。
const HeaderClient = "X-GA-Client"

// HeaderSession 是刷新时前端声明"我要续的是哪个会话"的请求头（D-048）。同一浏览器的标签页共用一个刷新 Cookie，
// 另一个标签页换了人登录之后，Cookie 里就是别人的会话：带了这个头而 Cookie 不是这个会话的，刷新被拒（401），
// Cookie 原样留着——那是另一个标签页的登录。不带这个头（页面刚打开，还不知道自己是谁）时照旧按 Cookie 刷新。
const HeaderSession = "X-GA-Session"

// Authorizer 由授权模块提供，用来判定超管和填充 /auth/me 的权限与菜单。未安装时为 nil。
type Authorizer interface {
	IsSuper(ctx context.Context, portalCode string, userID uint64) (bool, error)
	Perms(ctx context.Context, p auth.Principal) ([]string, error)
	Menus(ctx context.Context, p auth.Principal) (any, error)
}

// Options 是创建 Authenticator 的参数。
type Options struct {
	Portal         portal.Portal
	Secret         []byte
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	Release        bool     // release 模式：Cookie 加 Secure
	AllowedOrigins []string // 登录、刷新、登出的 Origin 白名单；为空只在 debug 允许，表示不校验
	Log            *slog.Logger
	Now            func() time.Time
	BcryptCost     int
	Captcha        *captcha.Captcha
	StatusCacheTTL time.Duration // 会话与账号状态缓存，默认 15s
	// PasswordParallel 是同时核对密码的上限（D-058）；0 用默认值（按 CPU 数，至少 8）
	PasswordParallel int
}

// Authenticator 是一个端的认证器。
type Authenticator struct {
	p          portal.Portal
	policy     portal.LoginPolicy
	signer     *token.Signer
	sessions   *session.Manager
	guard      *loginguard.Guard
	hasher     *password.Hasher
	pwdPolicy  password.Policy
	pwdRules   portal.PasswordPolicy // 生效的密码策略（含有效期）
	captcha    *captcha.Captcha
	sessCache  *ttlcache.Cache[sessState]
	acctCache  *ttlcache.Cache[acctState]
	release    bool
	origins    map[string]struct{}
	now        func() time.Time
	log        *slog.Logger
	authorizer Authorizer
	lockExempt map[string]bool // 锁屏时仍能访问的路由（完整路径模板）

	pwdTries    *ratelimit.Window // 本人改密核对旧密码的次数，按会话计（D-055）
	captchaRate *ratelimit.Window // 生成验证码的次数，按来源 IP 计（D-055）
	captchaGate *ratelimit.Gate   // 同时在画的验证码图片数（D-055）
	pwdGate     *ratelimit.Gate   // 同时在做的密码核对（bcrypt）数：登录、解锁、本人改密（D-058）
}

// 改密、验证码的限流（D-055）。改密：一个会话 15 分钟内最多核对 5 次旧密码，成功就清零——拿到访问令牌、不知道密码的人
// 不能借改密接口一直试密码（登录的失败计数管不到这里；也不套用登录的锁定，免得别人能把账号主人锁在改密之外）。
// 验证码：一个 IP 每分钟最多 30 张，同时最多画 8 张；画图和编码 PNG 不便宜，这是公开接口。
const (
	pwdTriesLimit    = 5
	pwdTriesWindow   = 15 * time.Minute
	captchaPerMinute = 30
	captchaParallel  = 8
	rateMaxKeys      = 100_000
)

// pwdParallel 是同时核对密码的上限（D-058）：bcrypt 每次要几百毫秒 CPU，公开的登录接口不设上限的话，
// 大量来源同时登录能把 CPU 占满、拖垮整个服务；满了直接回 429，不排队。按 CPU 数给，至少 8。
func pwdParallel() int { return max(8, 4*runtime.GOMAXPROCS(0)) }

// MaxUnlockFailures 是锁屏后允许连续输错密码的次数，到了就吊销会话（D-027）。
const MaxUnlockFailures = 5

// ExemptFromLock 登记锁屏时仍能访问的路由（完整路径模板，如 /api/platform/v1/auth/me）。装配时调用。
func (a *Authenticator) ExemptFromLock(paths ...string) {
	if a.lockExempt == nil {
		a.lockExempt = map[string]bool{}
	}
	for _, p := range paths {
		a.lockExempt[p] = true
	}
}

type sessState struct {
	userID    uint64
	expiresAt time.Time
	touchedAt time.Time
	locked    bool
}

type acctState struct {
	username      string
	displayName   string
	avatar        string
	enabled       bool
	mustChangePwd bool
}

// New 创建认证器。
func New(o Options) (*Authenticator, error) {
	if err := o.Portal.Validate(); err != nil {
		return nil, err
	}
	// 登录防护和密码策略只能在底线范围内取值，越界拒绝启动（D-024）
	login, pwd := o.Portal.Login.Normalized(), o.Portal.Password.Normalized()
	if err := login.Validate(); err != nil {
		return nil, fmt.Errorf("authimpl: 端 %s 的登录防护策略：%w", o.Portal.Code, err)
	}
	if err := pwd.Validate(); err != nil {
		return nil, fmt.Errorf("authimpl: 端 %s 的密码策略：%w", o.Portal.Code, err)
	}
	if len(o.Secret) == 0 {
		return nil, fmt.Errorf("authimpl: 端 %s 没有密钥", o.Portal.Code)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Captcha == nil {
		o.Captcha = captcha.New()
	}
	if o.StatusCacheTTL <= 0 {
		o.StatusCacheTTL = 15 * time.Second
	}
	if o.BcryptCost <= 0 {
		o.BcryptCost = password.DefaultCost
	}
	origins := map[string]struct{}{}
	for _, or := range o.AllowedOrigins {
		origins[strings.ToLower(strings.TrimRight(or, "/"))] = struct{}{}
	}
	return &Authenticator{
		p:         o.Portal,
		policy:    login,
		signer:    token.New(o.Portal.Code, o.Secret, o.AccessTTL, o.Now),
		sessions:  session.NewManager(session.Config{RefreshTTL: o.RefreshTTL, Now: o.Now}),
		guard:     loginguard.New(login, o.Now),
		hasher:    password.NewHasher(o.BcryptCost),
		pwdPolicy: password.Policy{MinLength: pwd.MinLength, RequireUpper: pwd.RequireUpper, RequireLower: pwd.RequireLower, RequireSymbol: pwd.RequireSymbol},
		pwdRules:  pwd,
		captcha:   o.Captcha,
		sessCache: ttlcache.New[sessState](o.StatusCacheTTL, o.Now),
		acctCache: ttlcache.New[acctState](o.StatusCacheTTL, o.Now),
		release:   o.Release,
		origins:   origins,
		now:       o.Now,
		log:       o.Log.With("component", "auth", "portal", o.Portal.Code),

		pwdTries:    ratelimit.New(pwdTriesLimit, pwdTriesWindow, rateMaxKeys, o.Now),
		captchaRate: ratelimit.New(captchaPerMinute, time.Minute, rateMaxKeys, o.Now),
		captchaGate: ratelimit.NewGate(captchaParallel),
		pwdGate:     ratelimit.NewGate(cmp.Or(o.PasswordParallel, pwdParallel())),
	}, nil
}

// Portal 返回端代号。
func (a *Authenticator) Portal() string { return a.p.Code }

// SetAuthorizer 安装授权模块。
func (a *Authenticator) SetAuthorizer(z Authorizer) { a.authorizer = z }

// Hasher 返回密码哈希器，供创建账号时使用。
func (a *Authenticator) Hasher() *password.Hasher { return a.hasher }

// Sessions 返回会话管理器（系统管理模块的会话列表用）。
func (a *Authenticator) Sessions() *session.Manager { return a.sessions }

// ---- auth.Service ----

// RevokeUserSessions 吊销某用户全部会话并清缓存。可以在调用方的事务里调用：缓存在事务提交后才清。
func (a *Authenticator) RevokeUserSessions(ctx context.Context, userID uint64, reason string) error {
	if _, err := a.sessions.RevokeUser(ctx, a.p.Code, userID, reason, ""); err != nil {
		return err
	}
	db.AfterCommit(ctx, a.sessCache.Flush)
	return nil
}

// RevokeOtherSessions 吊销某用户除 keepSID 外的全部会话并清缓存，返回吊销的会话数。一条语句完成，
// 不按会话逐个吊销：会话再多也没有"分页取不完"的边界（D-043）。
func (a *Authenticator) RevokeOtherSessions(ctx context.Context, userID uint64, keepSID, reason string) (int64, error) {
	n, err := a.sessions.RevokeUser(ctx, a.p.Code, userID, reason, keepSID)
	if err != nil {
		return 0, err
	}
	db.AfterCommit(ctx, a.sessCache.Flush)
	return n, nil
}

// RevokeSession 吊销本端的一个会话并清缓存；sid 不属于本端时返回 session.ErrNotFound。
func (a *Authenticator) RevokeSession(ctx context.Context, sid, reason string) error {
	if err := a.sessions.Revoke(ctx, a.p.Code, sid, reason); err != nil {
		return err
	}
	db.AfterCommit(ctx, func() { a.sessCache.Delete(sid) })
	return nil
}

// ForgetAccount 清掉账号状态缓存。
func (a *Authenticator) ForgetAccount(userID uint64) {
	a.acctCache.Delete(acctKey(userID))
}

// ForgetSession 清掉会话状态缓存。
func (a *Authenticator) ForgetSession(sid string) { a.sessCache.Delete(sid) }

// HashPassword 生成密码哈希。
func (a *Authenticator) HashPassword(plain string) (string, error) { return a.hasher.Hash(plain) }

// ValidatePassword 按策略校验新密码。
func (a *Authenticator) ValidatePassword(plain, username string) error {
	if err := a.pwdPolicy.Check(a.hasher, plain, username, ""); err != nil {
		return httpx.ErrValidation.WithFields(a.policyField("password", err)).WithCause(err)
	}
	return nil
}

// PasswordPolicy 返回生效的密码策略。
func (a *Authenticator) PasswordPolicy() portal.PasswordPolicy { return a.pwdRules }

// LoginPolicy 返回生效的登录防护策略。
func (a *Authenticator) LoginPolicy() portal.LoginPolicy { return a.policy }

// GeneratePassword 生成一个满足任何合法密码策略、且不短于 minLength 的随机密码。
func GeneratePassword(minLength int) (string, error) { return password.Generate(minLength) }

// mustChange 报告账号是否必须先改密：被要求改密，或者密码已过有效期。
func (a *Authenticator) mustChange(acc *portal.Account) (must, expired bool) {
	expired = a.pwdRules.Expired(acc.PwdChangedAt, a.now().UTC())
	return acc.MustChangePwd || expired, expired
}

func acctKey(userID uint64) string { return fmt.Sprintf("u:%d", userID) }

// ---- 查状态（带缓存） ----

// sessStatus 是会话状态的查询结果。
type sessStatus int

const (
	sessActive  sessStatus = iota
	sessMissing            // 不存在或属于别的端：令牌签名有效时意味着密钥可能泄露
	sessRevoked            // 已吊销
	sessExpired            // 已过期（会话有最长寿命，访问令牌可能比它晚到期）：正常现象
)

// gen 是请求开始时取的缓存代数：读库期间会话被锁定或吊销时不把读到的旧状态写回缓存。
func (a *Authenticator) sessionState(ctx context.Context, sid string, gen uint64) (sessState, sessStatus, error) {
	if st, ok := a.sessCache.Get(sid); ok {
		// 缓存里的会话也可能在这个缓存周期内到期：到期时间随状态一起缓存，命中时再比一次（D-043）
		if !st.expiresAt.After(a.now().UTC()) {
			a.sessCache.Delete(sid)
			return sessState{}, sessExpired, nil
		}
		return st, sessActive, nil
	}
	s, err := a.sessions.Get(ctx, sid)
	if err != nil {
		return sessState{}, sessMissing, err
	}
	// 会话不存在（被清理、或令牌里的 sid 是编的）、属于别的端、已吊销或已过期：一律当作无效，回 401 而不是 500
	if s == nil || s.Portal != a.p.Code {
		return sessState{}, sessMissing, nil
	}
	if s.RevokedAt != nil {
		return sessState{}, sessRevoked, nil
	}
	if !s.Active(a.now().UTC()) {
		return sessState{}, sessExpired, nil
	}
	st := sessState{userID: s.UserID, expiresAt: s.ExpiresAt, touchedAt: s.LastSeenAt, locked: s.LockedAt != nil}
	a.sessCache.SetIfGen(sid, st, gen)
	return st, sessActive, nil
}

func (a *Authenticator) accountState(ctx context.Context, userID uint64) (acctState, bool, error) {
	if st, ok := a.acctCache.Get(acctKey(userID)); ok {
		return st, true, nil
	}
	gen := a.acctCache.Gen()
	acc, err := a.p.Users.FindByID(ctx, userID)
	if errors.Is(err, portal.ErrAccountNotFound) {
		return acctState{}, false, nil
	}
	if err != nil {
		return acctState{}, false, err
	}
	must, _ := a.mustChange(acc)
	st := acctState{username: acc.Username, displayName: acc.DisplayName, avatar: acc.Avatar, enabled: acc.Enabled(), mustChangePwd: must}
	a.acctCache.SetIfGen(acctKey(userID), st, gen)
	return st, true, nil
}

// ---- 中间件 ----

const keyAllowPwdChange = "ga.allow_pwd_change" //nolint:gosec // gin 上下文键名，不是凭据

// AllowPwdChange 标记这条路由允许"必须改密"状态的用户访问（挂在 /auth 分组上）。
func (a *Authenticator) AllowPwdChange() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(keyAllowPwdChange, true)
		c.Next()
	}
}

// Authenticate 是 AuthOnly / Require 路由的认证中间件（规范 §5.3 的校验顺序）。
func (a *Authenticator) Authenticate() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := bearer(c)
		if raw == "" {
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		claims, err := a.signer.Verify(raw)
		if err != nil {
			// 过期是正常现象（前端会去刷新）；签名或格式不对才是安全事件；签名有效但内容不对是严重事件（D-032）
			switch {
			case errors.Is(err, token.ErrExpired):
			case errors.Is(err, token.ErrBadClaims):
				secmark.Set(c, secmark.Mark{Kind: secmark.TokenMismatch, Detail: "bad claims"})
			default:
				secmark.Set(c, secmark.Mark{Kind: secmark.TokenInvalid})
			}
			httpx.Fail(c, httpx.ErrTokenInvalid.WithCause(err))
			return
		}
		ctx := c.Request.Context()
		gen := a.sessCache.Gen()
		sess, status, err := a.sessionState(ctx, claims.SessionID, gen)
		if err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		who := secmark.Mark{UserID: claims.UserID, SessionID: claims.SessionID}
		switch {
		case status == sessMissing:
			who.Kind, who.Detail = secmark.TokenMismatch, "unknown session"
		case status == sessRevoked:
			who.Kind = secmark.SessionRevoked
		case status == sessExpired:
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		case sess.userID != claims.UserID:
			who.Kind, who.Detail = secmark.TokenMismatch, "user mismatch"
		}
		if who.Kind != "" {
			secmark.Set(c, who)
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		acct, ok, err := a.accountState(ctx, claims.UserID)
		if err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		if !ok || !acct.enabled {
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		// 锁屏在服务端判定（D-027）：锁定的会话只能查看自己、解锁、登出
		if sess.locked && !a.lockExempt[c.FullPath()] {
			httpx.Fail(c, httpx.ErrSessionLocked)
			return
		}
		if acct.mustChangePwd && !c.GetBool(keyAllowPwdChange) {
			httpx.Fail(c, httpx.ErrPwdChangeRequired)
			return
		}
		super := false
		if a.authorizer != nil {
			super, err = a.authorizer.IsSuper(ctx, a.p.Code, claims.UserID)
			if err != nil {
				httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
				return
			}
		}
		p := auth.Principal{
			Portal: a.p.Code, UserID: claims.UserID, SessionID: claims.SessionID,
			Username: acct.username, DisplayName: acct.displayName, Super: super, Locked: sess.locked,
		}
		ctx = auth.WithPrincipal(ctx, p)
		ctx = logx.With(ctx, "user_id", p.UserID, "portal", p.Portal)
		c.Request = c.Request.WithContext(ctx)
		a.maybeTouch(ctx, claims.SessionID, sess, gen)
		c.Next()
	}
}

// maybeTouch 每分钟最多更新一次 last_seen_at。st 是请求开始时的状态，期间被失效过就不写回缓存。
func (a *Authenticator) maybeTouch(ctx context.Context, sid string, st sessState, gen uint64) {
	now := a.now().UTC()
	if now.Sub(st.touchedAt) < time.Minute {
		return
	}
	st.touchedAt = now
	a.sessCache.SetIfGen(sid, st, gen)
	if err := a.sessions.Touch(ctx, sid); err != nil {
		a.log.WarnContext(ctx, "touch session failed", "err", err)
	}
}

func bearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// checkClient 校验登录、刷新、登出请求的客户端标识和 Origin（D-051 起登录也要）。
func (a *Authenticator) checkClient(c *gin.Context) bool {
	if c.GetHeader(HeaderClient) != "web" {
		return false
	}
	if len(a.origins) == 0 {
		return !a.release // release 模式下配置校验已保证非空，这里只是兜底
	}
	origin := strings.ToLower(strings.TrimRight(c.GetHeader("Origin"), "/"))
	if origin == "" {
		return false
	}
	_, ok := a.origins[origin]
	return ok
}
