// Package authimpl 实现一个端的认证：登录、刷新、登出、当前身份、改密，以及认证中间件。
//
// 每个端一个 Authenticator，由 core/app 在装配阶段创建；模块通过 core/auth 的公开接口使用它。
package authimpl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
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
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
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

// IPChecker 是 IP 白名单的检查（D-062），由 core/ipacl 提供。为 nil 时不检查。
type IPChecker interface {
	// AccountAllows 报告来源地址能否用这个账号：主体白名单（orgID 非 0 时）和账号白名单都要满足。
	AccountAllows(ctx context.Context, portal string, orgID, userID uint64, ip string) bool
}

// Options 是创建 Authenticator 的参数。
type Options struct {
	Portal         portal.Portal
	IPACL          IPChecker // 可选：主体、账号的 IP 白名单（D-062）
	Secret         []byte
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	Release        bool     // release 模式：Cookie 加 Secure
	AllowedOrigins []string // 登录、刷新、登出的 Origin 白名单；为空只在 debug 允许，表示不校验
	Log            *slog.Logger
	Now            func() time.Time
	Hasher         *password.Hasher // 进程共用的密码哈希器（D-070），由 core/app 传进来；nil 时用默认参数建一个
	Captcha        *captcha.Captcha
	StatusCacheTTL time.Duration  // 会话与账号状态缓存，默认 15s
	Redis          *redisx.Client // 可选共享计数与验证码（D-076、D-077）；nil 保持纯内存
	// PasswordBudget 是密码计算的位置（D-058、D-068、D-071）：一个进程里的各个端、后台里生成密码哈希的地方共用一组，
	// 由 core/app 传进来；nil 时自己建一组（PasswordParallel() 个位置，登录和解锁最多等 PasswordWait）
	PasswordBudget *ratelimit.Budget
	// 提交后的失效通知（D-075）；接收方用 Invalidate*，不会再次发通知。
	OnSessionChange func(sid string)
	OnAccountChange func(userID uint64)
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
	ip         IPChecker       // 主体、账号的 IP 白名单（D-062）；nil 不检查
	ipExempt   map[string]bool // 不查账号白名单的路由（登出）

	pwdTries    *ratelimit.Window // 本人改密核对旧密码的次数，按会话计（D-055）
	captchaRate *ratelimit.Window // 生成验证码的次数，按来源 IP 计（D-055）
	refreshRate *ratelimit.Window // 刷新访问数据库前的来源窗口（D-112）。
	captchaGate *ratelimit.Gate   // 同时在画的验证码图片数（D-055）
	budget      *ratelimit.Budget // 密码计算的位置：登录、解锁是前一类，本人改密、后台生成哈希是后一类（D-071）
	// 主体端按主体限并发（D-071）：本人改密、解锁各一个上限，键是主体 ID；平台端不用
	orgPwd          *ratelimit.KeyedGate
	orgUnlock       *ratelimit.KeyedGate
	pwdChanges      *ratelimit.Window // 一个账号成功改密的次数（D-071）
	onAccountChange func(userID uint64)
}

// 改密、验证码的限流（D-055）。改密：一个会话 15 分钟内最多核对 5 次旧密码，成功就清零——拿到访问令牌、不知道密码的人
// 不能借改密接口一直试密码（登录的失败计数管不到这里；也不套用登录的锁定，免得别人能把账号主人锁在改密之外）。
// 验证码：一个 IP 每分钟最多 30 张，同时最多画 8 张；画图和编码 PNG 不便宜，这是公开接口。
//
// 改密成功的次数另外按账号限（D-071）：15 分钟内最多 5 次。核对旧密码的次数成功就清零，两个密码来回改、每次都成功的话
// 那条限制管不到，而成功的改密是最贵的（三次密码计算）。
const (
	pwdTriesLimit    = 5
	pwdTriesWindow   = 15 * time.Minute
	pwdChangesLimit  = 5
	pwdChangesWindow = 15 * time.Minute
	captchaPerMinute = 30
	refreshPerMinute = 120
	captchaParallel  = 8
	rateMaxKeys      = 100_000
)

// PasswordParallel 是同时做密码计算的默认上限（D-058）：密码计算又吃 CPU 又占内存（Argon2id 每次 19 MiB，D-070），公开的登录接口不设上限的话，
// 大量来源同时登录能把 CPU 和内存占满、拖垮整个服务；满了回 429（登录、解锁先等一小会儿，D-071）。按 CPU 数给，至少 8。
func PasswordParallel() int { return max(8, 4*runtime.GOMAXPROCS(0)) }

// PasswordWait 是登录、解锁在密码计算的位置占满时最多等多久（D-071）：等到了照常做，等不到才回 429。
const PasswordWait = 2 * time.Second

// 主体端一个主体同时在做的本人改密、解锁的上限（D-071），满了直接回 429。位置数最少的默认配置是 8 个：一个主体
// 解锁占 2 个、改密和套件里的建账号重置占后一类的 4 个，最多 6 个，别的主体的登录总还有位置。
const (
	orgPwdParallel    = 2
	orgUnlockParallel = 2
)

// MaxUnlockFailures 是锁屏后允许连续输错密码的次数，到了就吊销会话（D-027）。
const MaxUnlockFailures = 5

// ExemptFromAccountIP 登记不查主体、账号 IP 白名单的路由（完整路径模板）。装配时调用。只用于登出这类只会缩小权限的接口。
func (a *Authenticator) ExemptFromAccountIP(paths ...string) {
	if a.ipExempt == nil {
		a.ipExempt = map[string]bool{}
	}
	for _, p := range paths {
		a.ipExempt[p] = true
	}
}

// accountIPAllowed 报告来源地址能否用这个账号（D-062）。主体端带上账号所属的主体，平台端主体为 0。
func (a *Authenticator) accountIPAllowed(ctx context.Context, orgID, userID uint64, ip string) bool {
	if a.ip == nil {
		return true
	}
	if !a.p.Scoped {
		orgID = 0
	}
	return a.ip.AccountAllows(ctx, a.p.Code, orgID, userID, ip)
}

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
	orgID     uint64 // 主体端：会话所属的主体（D-061）
	expiresAt time.Time
	touchedAt time.Time
	locked    bool
}

type acctState struct {
	username      string
	displayName   string
	avatar        string
	enabled       bool // 主体端：账号启用并且所属主体启用（D-061）
	mustChangePwd bool
	orgID         uint64 // 主体端：账号所属的主体
	owner         bool   // 主体端：账号是所属主体的主账号
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
		o.Captcha = captcha.NewShared(o.Now, o.Redis)
	}
	if o.StatusCacheTTL <= 0 {
		o.StatusCacheTTL = 15 * time.Second
	}
	if o.Hasher == nil {
		o.Hasher = password.NewHasher(password.DefaultParams())
	}
	if o.PasswordBudget == nil {
		o.PasswordBudget = ratelimit.NewBudget(PasswordParallel(), PasswordWait)
	}
	origins := map[string]struct{}{}
	for _, or := range o.AllowedOrigins {
		origins[strings.ToLower(strings.TrimRight(or, "/"))] = struct{}{}
	}
	a := &Authenticator{
		p:         o.Portal,
		policy:    login,
		signer:    token.New(o.Portal.Code, o.Secret, o.AccessTTL, o.Now),
		guard:     loginguard.NewShared(login, o.Now, o.Redis, o.Portal.Code),
		hasher:    o.Hasher,
		pwdPolicy: password.Policy{MinLength: pwd.MinLength, RequireUpper: pwd.RequireUpper, RequireLower: pwd.RequireLower, RequireSymbol: pwd.RequireSymbol},
		pwdRules:  pwd,
		captcha:   o.Captcha,
		sessCache: ttlcache.New[sessState](o.StatusCacheTTL, o.Now),
		acctCache: ttlcache.New[acctState](o.StatusCacheTTL, o.Now),
		release:   o.Release,
		origins:   origins,
		ip:        o.IPACL,
		now:       o.Now,
		log:       o.Log.With("component", "auth", "portal", o.Portal.Code),

		pwdTries:        ratelimit.NewShared(pwdTriesLimit, pwdTriesWindow, rateMaxKeys, o.Now, o.Redis, "pwd-tries:"+o.Portal.Code),
		captchaRate:     ratelimit.NewShared(captchaPerMinute, time.Minute, rateMaxKeys, o.Now, o.Redis, "captcha:"+o.Portal.Code),
		refreshRate:     ratelimit.NewShared(refreshPerMinute, time.Minute, rateMaxKeys, o.Now, o.Redis, "refresh:"+o.Portal.Code),
		captchaGate:     ratelimit.NewGate(captchaParallel),
		budget:          o.PasswordBudget,
		orgPwd:          ratelimit.NewKeyedGate(orgPwdParallel),
		orgUnlock:       ratelimit.NewKeyedGate(orgUnlockParallel),
		pwdChanges:      ratelimit.NewShared(pwdChangesLimit, pwdChangesWindow, rateMaxKeys, o.Now, o.Redis, "pwd-changes:"+o.Portal.Code),
		onAccountChange: o.OnAccountChange,
	}
	a.sessions = session.NewManager(session.Config{RefreshTTL: o.RefreshTTL, Now: o.Now, OnChange: func(_ string, sid string) {
		a.InvalidateSession(sid)
		if o.OnSessionChange != nil {
			o.OnSessionChange(sid)
		}
	}})
	return a, nil
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
	a.InvalidateAccount(userID)
	if a.onAccountChange != nil {
		a.onAccountChange(userID)
	}
}

// InvalidateAccount 只清本地账号缓存；0 表示清这个端的全部账号状态（D-075）。
func (a *Authenticator) InvalidateAccount(userID uint64) {
	if userID == 0 {
		a.acctCache.Flush()
	} else {
		a.acctCache.Delete(acctKey(userID))
	}
}

// InvalidateSession 只清本地会话缓存；空串表示清这个端的全部会话状态（D-075）。
func (a *Authenticator) InvalidateSession(sid string) {
	if sid == "" {
		a.sessCache.Flush()
	} else {
		a.sessCache.Delete(sid)
	}
}

// ForgetSession 清掉会话状态缓存。
func (a *Authenticator) ForgetSession(sid string) { a.sessCache.Delete(sid) }

// HashPassword 生成密码哈希（后台建账号、重置密码用）。占的是密码计算位置里的后一类（D-068、D-071）：最多占一半，
// 满了回 429，不排队。
func (a *Authenticator) HashPassword(plain string) (string, error) {
	leave, ok := a.budget.EnterBackground()
	if !ok {
		return "", httpx.ErrTooManyRequests
	}
	defer leave()
	return a.hasher.Hash(plain)
}

// orgKey 返回按主体限并发用的键：主体端的身份才有（D-071）。
func (a *Authenticator) orgKey(p auth.Principal) (string, bool) {
	if !a.p.Scoped || p.OrgID == 0 {
		return "", false
	}
	return strconv.FormatUint(p.OrgID, 10), true
}

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
// live 为真时不用缓存、直接读库（D-073）；读到会话已经无效，把缓存里那一条也删掉，走缓存的路由下一个请求就跟上。
func (a *Authenticator) sessionState(ctx context.Context, sid string, gen uint64, live bool) (sessState, sessStatus, error) {
	if !live {
		if st, ok := a.sessCache.Get(sid); ok {
			// 缓存里的会话也可能在这个缓存周期内到期：到期时间随状态一起缓存，命中时再比一次（D-043）
			if !st.expiresAt.After(a.now().UTC()) {
				a.sessCache.Delete(sid)
				return sessState{}, sessExpired, nil
			}
			return st, sessActive, nil
		}
	}
	st, status, err := a.readSession(ctx, sid)
	if err != nil {
		return sessState{}, sessMissing, err
	}
	if status != sessActive {
		// 缓存里有才删（Drop）：删除会让缓存的代数加一（别的请求这一轮读到的状态就不写回去了），拿着已吊销的会话
		// 反复请求不该能一直触发它
		if live {
			a.sessCache.Drop(sid)
		}
		return sessState{}, status, nil
	}
	a.sessCache.SetIfGen(sid, st, gen)
	return st, sessActive, nil
}

// readSession 按库读会话的状态。
func (a *Authenticator) readSession(ctx context.Context, sid string) (sessState, sessStatus, error) {
	s, err := a.sessions.Get(ctx, sid)
	if err != nil {
		return sessState{}, sessMissing, err
	}
	// 会话不存在（被清理、或令牌里的 sid 是编的）、属于别的端、已吊销或已过期：一律当作无效，回 401 而不是 500
	if s == nil || s.Portal != a.p.Code {
		return sessState{}, sessMissing, nil
	}
	// 主体端的会话必须带主体（D-061）：建会话时一定写了，缺了说明这一行不是登录建出来的
	if a.p.Scoped && s.OrgID == 0 {
		return sessState{}, sessMissing, nil
	}
	if s.RevokedAt != nil {
		return sessState{}, sessRevoked, nil
	}
	if !s.Active(a.now().UTC()) {
		return sessState{}, sessExpired, nil
	}
	return sessState{userID: s.UserID, orgID: s.OrgID, expiresAt: s.ExpiresAt, touchedAt: s.LastSeenAt, locked: s.LockedAt != nil}, sessActive, nil
}

// accountState 返回账号的状态。live 为真时不用缓存、直接读库（D-073）；读到的和缓存里的不一样（别的程序改过），
// 把缓存里那一条删掉——删除让代数加一，这次读到的也就不写回去，走缓存的路由下一个请求重新读库。
// 这只是尽力而为：别的程序的改动不带失效通知，两个请求一前一后读库、后读的先写缓存时，旧状态仍可能留到缓存到期。
// 按库核对的路由自己不受影响，它不读缓存。
func (a *Authenticator) accountState(ctx context.Context, userID uint64, live bool) (acctState, bool, error) {
	key := acctKey(userID)
	if !live {
		if st, ok := a.acctCache.Get(key); ok {
			return st, true, nil
		}
	}
	gen := a.acctCache.Gen()
	acc, err := a.p.Users.FindByID(ctx, userID)
	if errors.Is(err, portal.ErrAccountNotFound) {
		if live {
			a.acctCache.Drop(key)
		}
		return acctState{}, false, nil
	}
	if err != nil {
		return acctState{}, false, err
	}
	must, _ := a.mustChange(acc)
	st := acctState{username: acc.Username, displayName: acc.DisplayName, avatar: acc.Avatar, enabled: acc.Enabled(), mustChangePwd: must}
	if a.p.Scoped {
		// 主体端：主体的状态和账号的状态一起查、一起缓存（D-061）。主体停用、不存在，它下面的账号都按停用处理
		org, err := a.accountOrg(ctx, acc)
		if err != nil {
			return acctState{}, false, err
		}
		st.orgID = acc.OrgID
		st.enabled = st.enabled && org.Enabled()
		st.owner = org != nil && org.OwnerUserID == acc.ID
	}
	if live {
		if old, ok := a.acctCache.Get(key); ok && old != st {
			a.acctCache.Drop(key)
			return st, true, nil
		}
	}
	a.acctCache.SetIfGen(key, st, gen)
	return st, true, nil
}

// orgUsers 返回主体端的用户来源（注册时已校验它实现了 OrgUserProvider）。
func (a *Authenticator) orgUsers() portal.OrgUserProvider {
	ou, _ := a.p.Users.(portal.OrgUserProvider)
	return ou
}

// accountOrg 读账号所属的主体：账号没有主体或主体不存在时返回 nil（按停用处理），读库出错时返回错误。
func (a *Authenticator) accountOrg(ctx context.Context, acc *portal.Account) (*portal.Org, error) {
	if acc.OrgID == 0 {
		return nil, nil
	}
	org, err := a.orgUsers().FindOrgByID(ctx, acc.OrgID)
	if errors.Is(err, portal.ErrOrgNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if org.ID != acc.OrgID {
		return nil, nil
	}
	return org, nil
}

// ---- 中间件 ----

const (
	keyAllowPwdChange = "ga.allow_pwd_change" //nolint:gosec // gin 上下文键名，不是凭据
	keyLiveAuth       = "ga.live_auth"
)

// AllowPwdChange 标记这条路由允许"必须改密"状态的用户访问（挂在 /auth 分组上）。
func (a *Authenticator) AllowPwdChange() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(keyAllowPwdChange, true)
		c.Next()
	}
}

// LiveAuth 标记这条路由"按库核对"（D-073）：排在 Authenticate 前面，Authenticate 看到标记就不用会话状态、账号状态
// 两个缓存，每个请求读库。别的程序（平台程序停用主体、换主账号、吊销会话）改了库，这条路由在下一个请求就看得到。
// 标记跟着这条路由自己的处理链走，不靠路径去对。
func (a *Authenticator) LiveAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(keyLiveAuth, true)
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
		// 标了"按库核对"的路由不用状态缓存（D-073）
		live := c.GetBool(keyLiveAuth)
		gen := a.sessCache.Gen()
		sess, status, err := a.sessionState(ctx, claims.SessionID, gen, live)
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
		acct, ok, err := a.accountState(ctx, claims.UserID, live)
		if err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		if !ok || !acct.enabled {
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		// 主体端：账号所属的主体必须就是会话的主体（D-061）。账号不能换主体，对不上说明数据被改过
		if a.p.Scoped && (acct.orgID == 0 || acct.orgID != sess.orgID) {
			secmark.Set(c, secmark.Mark{Kind: secmark.TokenMismatch, Detail: "org mismatch", UserID: claims.UserID, SessionID: claims.SessionID})
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		// 主体、账号的 IP 白名单（D-062）：每个已登录的请求都查，换到名单外的 IP 回 2003（登出除外）
		if !a.ipExempt[c.FullPath()] && !a.accountIPAllowed(ctx, sess.orgID, claims.UserID, c.ClientIP()) {
			secmark.Set(c, secmark.Mark{Kind: secmark.IPDenied, Detail: "account", OrgID: acct.orgID, UserID: claims.UserID, SessionID: claims.SessionID})
			httpx.Fail(c, httpx.ErrIPDenied)
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
		super, orgID := false, uint64(0)
		switch {
		case a.p.Scoped:
			orgID = sess.orgID // 平台端的身份永远不带主体，哪怕会话行上被写了值
			// 主体端没有超管角色：本主体的主账号就是主体内的超管（D-061）
			super = acct.owner
		case a.authorizer != nil:
			super, err = a.authorizer.IsSuper(ctx, a.p.Code, claims.UserID)
			if err != nil {
				httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
				return
			}
		}
		p := auth.Principal{
			Portal: a.p.Code, UserID: claims.UserID, SessionID: claims.SessionID, OrgID: orgID,
			Username: acct.username, DisplayName: acct.displayName, Super: super, Locked: sess.locked,
		}
		ctx = auth.WithPrincipal(ctx, p)
		ctx = logx.With(ctx, "user_id", p.UserID, "portal", p.Portal)
		if p.OrgID != 0 {
			ctx = logx.With(ctx, "org_id", p.OrgID)
		}
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
	// 只换缓存里的值，不动过期时间（D-097）：重新算的话，别的程序在库里吊销的会话会带着旧状态多活一个缓存周期
	a.sessCache.UpdateIfGen(sid, st, gen)
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
