package authimpl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/internal/secmark"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// CookieName 返回刷新凭证的 Cookie 名。release 模式下带 __Host- 前缀（D-058）：浏览器只接受本主机经 https 写的、
// 不带 Domain、Path 为 / 的这个 Cookie，同一可注册域下的其他子域（或明文 http 的中间人）写不进同名 Cookie。
// debug 模式在本机 http 上开发，不能满足这个前缀的要求，用不带前缀的名字。
func (a *Authenticator) CookieName() string {
	if a.release {
		return "__Host-ga_rt_" + a.p.Code
	}
	return "ga_rt_" + a.p.Code
}

// CookiePath 返回刷新凭证 Cookie 的作用路径。__Host- 前缀要求 Path=/（release 模式）；debug 模式只发给本端的认证接口。
func (a *Authenticator) CookiePath() string {
	if a.release {
		return "/"
	}
	return "/api/" + a.p.Code + "/v1/auth"
}

// setRefreshCookie 写刷新 Cookie。这是服务端唯一写这个 Cookie 的地方，只在登录和轮换成功时调用；
// 服务端从不删除它（D-049）：同一浏览器的标签页共用一个 Cookie，退出、失败的刷新这类响应迟到时，
// 浏览器里的 Cookie 可能已经是别人新登录的，一条"删除 Cookie"的迟到响应会把那个登录清掉。
// 吊销过的会话的 Cookie 留在浏览器里没有用处：下次刷新回 401，下次登录会把它换掉。
func (a *Authenticator) setRefreshCookie(c *gin.Context, tok session.RefreshToken, maxAge int) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(a.CookieName(), tok.String(), maxAge, a.CookiePath(), "", a.release, true)
}

func (a *Authenticator) cookieMaxAge(expiresAt time.Time) int {
	d := expiresAt.Sub(a.now().UTC())
	if d <= 0 {
		return -1
	}
	return int(d.Seconds())
}

// tokenResponse 是登录和刷新的出参。
type tokenResponse struct {
	AccessToken   string `json:"accessToken"`
	TokenType     string `json:"tokenType"`
	ExpiresIn     int    `json:"expiresIn"` // 秒
	MustChangePwd bool   `json:"mustChangePwd"`
	PwdExpired    bool   `json:"pwdExpired"` // 必须改密的原因是密码过期（而不是首次登录或被重置）
	SessionID     string `json:"sessionId"`  // 这个令牌所属的会话；前端刷新时放进 X-GA-Session（D-048）
}

func (a *Authenticator) issue(c *gin.Context, acc *portal.Account, sid string) error {
	must, expired := a.mustChange(acc)
	access, claims, err := a.signer.Sign(acc.ID, sid)
	if err != nil {
		return err
	}
	httpx.OK(c, tokenResponse{
		AccessToken:   access,
		TokenType:     "Bearer",
		ExpiresIn:     int(claims.ExpiresAt.Sub(claims.IssuedAt).Seconds()),
		MustChangePwd: must,
		PwdExpired:    expired,
		SessionID:     sid,
	})
	return nil
}

// Captcha 处理 GET /auth/captcha。
func (a *Authenticator) Captcha(c *gin.Context) {
	if c.GetHeader(HeaderClient) != "web" || c.GetHeader("Sec-Fetch-Site") == "cross-site" || (c.GetHeader("Origin") != "" && !a.checkClient(c)) {
		httpx.Fail(c, httpx.ErrForbidden)
		return
	}
	// 公开接口、每次都要画图编码：按来源 IP 限速，同时画的数量也有上限，满了直接拒绝（D-055）
	if _, ok := a.captchaRate.Reserve(c.Request.Context(), ratelimit.IPKey(c.ClientIP())); !ok {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	if !a.captchaGate.TryEnter() {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	defer a.captchaGate.Leave()
	id, img, err := a.captcha.GenerateFor(c.Request.Context(), a.p.Code)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"captchaId": id, "image": img})
}

type loginRequest struct {
	Org         string `json:"org" binding:"max=32"` // 主体编号：主体端必填（D-061），平台端不看
	Username    string `json:"username" binding:"required,max=64"`
	Password    string `json:"password" binding:"required,max=72"`
	CaptchaID   string `json:"captchaId" binding:"max=64"`
	CaptchaCode string `json:"captchaCode" binding:"max=16"`
}

// loginLogTimeout 是写一条登录日志的上限（D-037）。
const loginLogTimeout = 3 * time.Second

// Login 处理 POST /auth/login（规范 §5.7）。
//
// 和刷新、退出一样要求 X-GA-Client 与 Origin 白名单，并且只收 application/json（D-051）：登录成功会写刷新 Cookie，
// 跨站页面用表单（text/plain 也能拼出 JSON）顶层提交过来时，SameSite=Strict 挡不住这个 Cookie 被写下，
// 受害者的浏览器就登进了攻击者指定的账号。表单设不了自定义头，跨站的 fetch 带自定义头或 JSON 类型要先过预检，
// 这两条在算密码、建会话之前拦住它。
func (a *Authenticator) Login(c *gin.Context) {
	if !a.checkClient(c) || c.ContentType() != "application/json" {
		secmark.Set(c, secmark.Mark{Kind: secmark.BadOrigin, Detail: "login"})
		httpx.Fail(c, httpx.ErrForbidden)
		return
	}
	var req loginRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	ip, ua := c.ClientIP(), c.Request.UserAgent()
	// 归一化之后再做一切：限流、锁定、查账号、写日志都用同一个键，大小写变体不能各算一份配额
	username := portal.NormalizeUsername(req.Username)
	var orgCode string
	if a.p.Scoped {
		// 主体端：账号名只在主体内唯一，编号必填（D-061）
		if orgCode = portal.NormalizeOrgCode(req.Org); orgCode == "" {
			httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("org", "validation.required", "required")))
			return
		}
	}
	// 登录防护里账号这一维的键：主体端是"编号/账号名"，商户 A 的 admin 被锁不影响商户 B 的 admin
	guardKey := a.guardKey(orgCode, username)
	var orgID uint64 // 编号对应的主体（查到了才有），写进登录日志
	logAttempt := func(userID uint64, sid string, success bool, reason string) {
		// 客户端断开或请求超时（D-037）都会取消请求 ctx；登录日志仍要写，所以去掉取消信号但保留 ctx 里的值，
		// 另给它一个短时限：数据库不回话时不能拖住响应（限流、锁定的请求也要尽快回）。
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginLogTimeout)
		defer cancel()
		err := a.sessions.WriteLoginLog(wctx, session.LoginLog{
			Portal: a.p.Code, OrgID: orgID, OrgCode: orgCode, Username: username, UserID: userID, SessionID: sid,
			Success: success, Reason: reason, IP: ip, UserAgent: ua, RequestID: httpx.RequestID(ctx),
		})
		if err != nil {
			a.log.ErrorContext(ctx, "write login log failed", "err", err)
		}
		// 同时写一行审计记录到日志输出，交给外部日志系统（D-032）
		logx.Audit(ctx, "audit.login", "portal", a.p.Code, "org_id", orgID, "org_code", orgCode, "username", username, "user_id", userID, "session_id", sid,
			"success", success, "reason", reason, "ip", ip, "user_agent", ua)
	}

	d := a.guard.CheckContext(ctx, guardKey, ip)
	// 被接纳的尝试钉住了它的失败记录，不管怎么结束都要放开（D-057）
	defer db.AfterEnd(ctx, func(bool) { d.Attempt.Done() })
	switch {
	// 账号的请求次数超限时 d.CaptchaRequired 为真（D-103），走下面验证码那一支。
	// 被限流、被锁定的请求没有核对密码，只记安全事件（同一来源一分钟合并成一行、次数不丢），不逐条写登录日志：
	// 这类请求不用登录就能无限发，逐条写库等于让人随意灌表（D-058）
	case d.RateLimited:
		secmark.Set(c, secmark.Mark{Kind: secmark.LoginRateLimited, Username: guardKey})
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	case d.Locked:
		secmark.Set(c, secmark.Mark{Kind: secmark.LoginLocked, Username: guardKey})
		httpx.Fail(c, httpx.New(httpx.CodeLocked, "").WithData(gin.H{"lockedUntil": d.LockedUntil.UTC().Format(time.RFC3339)}))
		return
	case d.CaptchaRequired:
		if !a.captcha.VerifyFor(ctx, a.p.Code, req.CaptchaID, req.CaptchaCode) {
			d.Attempt.CaptchaFailed() // 没过验证码的请求不占这个账号的请求次数（D-103）
			logAttempt(0, "", false, "captcha")
			httpx.Fail(c, httpx.New(httpx.CodeCaptchaRequired, "").WithData(gin.H{"captchaRequired": true}))
			return
		}
	}

	// 核对密码的并发有上限（D-058）。登录可以用全部位置，满了等一小会儿，等不到才回 429、不算一次失败（D-071）
	leave, admitted := a.budget.EnterFront(ctx)
	if !admitted {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	defer leave()
	acc, org, missReason, err := a.findLoginAccount(ctx, orgCode, username)
	if err != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	if org != nil {
		orgID = org.ID
	}
	var (
		ok     bool
		reason string
		userID uint64
	)
	// 主体、账号的 IP 白名单（D-062）：不在名单里的来源按"账号不存在"处理——不核对密码、响应和失败计数都一样，
	// 名单外的人试不出账号在不在、密码对不对；登录日志记原因 ip_denied，另记安全事件
	if acc != nil && !a.accountIPAllowed(ctx, acc.OrgID, acc.ID, ip) {
		secmark.Set(c, secmark.Mark{Kind: secmark.IPDenied, Detail: "login", OrgID: orgID, UserID: acc.ID, Username: guardKey})
		userID, acc, missReason = acc.ID, nil, "ip_denied"
	}
	switch {
	case acc == nil:
		// 账号不存在，主体端还有编号不存在、主体停用：响应、耗时、失败计数都一样，试不出哪一样不对（D-061）
		a.hasher.VerifyDummy(req.Password) // 抹平账号不存在时的耗时差异
		reason = missReason
	case !a.hasher.Verify(acc.PasswordHash, req.Password):
		userID, reason = acc.ID, "bad_password"
	case !acc.Enabled():
		userID, reason = acc.ID, "disabled"
	default:
		ok, userID = true, acc.ID
	}
	if !ok {
		leave() // 核对完了：记失败、写登录日志都是数据库操作，不带着位置做（D-071）
		captchaRequired, _ := d.Attempt.Fail()
		logAttempt(userID, "", false, reason)
		httpx.Fail(c, httpx.New(httpx.CodeLoginFailed, "").WithData(gin.H{"captchaRequired": captchaRequired}))
		return
	}

	// 存的不是"选的算法 + 当前参数"的哈希：密码刚核对通过，明文就在手里，按当前的重算一份，下面建会话的事务里换上
	// （D-070）。算在事务外面，和核对一样不拿着行锁算；用户来源没实现 PasswordRehasher 的不升级
	var upgraded string
	rehasher, canRehash := a.p.Users.(portal.PasswordRehasher)
	if canRehash && a.hasher.NeedsRehash(acc.PasswordHash) {
		if upgraded, err = a.hasher.Hash(req.Password); err != nil {
			// 重算这一步出错不挡住登录：这次不升级，下次登录再试。（往库里换的那一步在事务里，出错和事务里别的
			// 数据库错误一样让这次登录失败。）
			a.log.WarnContext(ctx, "rehash password failed", "err", err)
			upgraded = ""
		}
	}
	// 算完了，位置让出来：下面建会话的事务要等行锁，不带着位置等（D-071）
	leave()

	// 校验密码是在事务外做的（哈希很慢，不能拿着行锁算）。建会话的事务里先锁住账号行、确认密码和状态还是刚才校验的那样：
	// 这之间管理员重置了密码或停用了账号的，重置、停用吊销的是当时已有的会话，这里新建的会话会漏网，所以整个登录作废（D-047）。
	// 加锁顺序和重置、停用相同（用户行 → 会话行）；主体端在最前面先锁主体行（主体行 → 用户行 → 会话行，D-063）：
	// 后台写操作先锁主体行、再锁目标账号，登录反过来就会和它们互相等待。
	var (
		sess *session.Session
		tok  session.RefreshToken
	)
	err = db.Tx(ctx, func(ctx context.Context) error {
		var sessOrg uint64
		if a.p.Scoped {
			// 主体在核对密码之后被停用的，这次登录同样作废（D-061）：停用主体吊销的是当时已有的会话
			o, err := a.lockOrg(ctx, acc.OrgID)
			if err != nil && !errors.Is(err, portal.ErrOrgNotFound) {
				return err
			}
			if !o.Enabled() || o.ID != acc.OrgID {
				return errLoginDisabled
			}
			sessOrg = acc.OrgID
		}
		cur, err := a.lockAccount(ctx, acc.ID)
		if err != nil {
			return err
		}
		switch {
		case !a.samePassword(acc.PasswordHash, cur.PasswordHash, req.Password):
			return errLoginChanged // 密码已经换了：按密码错误处理
		case !cur.Enabled():
			return errLoginDisabled
		case a.p.Scoped && cur.OrgID != acc.OrgID:
			return errLoginChanged // 账号不能换主体，对不上说明数据被改过
		}
		if upgraded != "" {
			// 账号行锁着，哈希就是刚才核对的那个：只换这一列，必须改密、改密时间都不动
			if err := rehasher.RehashPassword(ctx, acc.ID, acc.PasswordHash, upgraded); err != nil {
				return err
			}
		}
		sess, tok, err = a.sessions.Create(ctx, a.p.Code, sessOrg, acc.ID, ip, ua)
		return err
	})
	if errors.Is(err, errLoginChanged) || errors.Is(err, errLoginDisabled) || errors.Is(err, portal.ErrAccountNotFound) {
		reason := "bad_password"
		if errors.Is(err, errLoginDisabled) {
			reason = "disabled"
		}
		captchaRequired, _ := d.Attempt.Fail()
		logAttempt(acc.ID, "", false, reason)
		httpx.Fail(c, httpx.New(httpx.CodeLoginFailed, "").WithData(gin.H{"captchaRequired": captchaRequired}))
		return
	}
	if err != nil {
		// 密码已经核对通过、会话没建成：照样留一条登录日志，不能让"验证了一次密码"没有任何记录（D-058）
		logAttempt(acc.ID, "", false, "error")
		httpx.Fail(c, err)
		return
	}
	db.AfterCommit(ctx, d.Attempt.Succeed)
	if err := a.p.Users.TouchLogin(ctx, acc.ID, ip, a.now().UTC()); err != nil {
		a.log.WarnContext(ctx, "touch login failed", "err", err)
	}
	db.AfterCommit(ctx, func() { a.ForgetAccount(acc.ID) })
	logAttempt(acc.ID, sess.SID, true, "")
	a.setRefreshCookie(c, tok, a.cookieMaxAge(sess.ExpiresAt))
	if err := a.issue(c, acc, sess.SID); err != nil {
		httpx.Fail(c, err)
	}
}

// 密码校验通过之后、建会话之前，账号的密码被换了或账号被停用了（D-047）。
var (
	errLoginChanged  = errors.New("authimpl: password changed during login")
	errLoginDisabled = errors.New("authimpl: account disabled during login")
)

// guardKey 是登录防护里账号这一维的键：平台端是登录名，主体端是"编号/登录名"（D-061）。
func (a *Authenticator) guardKey(orgCode, username string) string {
	if !a.p.Scoped {
		return username
	}
	return orgCode + "/" + username
}

// findLoginAccount 按登录名（主体端还有编号）查登录的账号。账号不存在、是别名（D-043），主体端还有编号不存在、
// 主体停用（D-061），一律返回 nil 账号和登录日志里的失败原因；org 是编号对应的主体（查到了才有）。只有读库出错才返回 error。
func (a *Authenticator) findLoginAccount(ctx context.Context, orgCode, username string) (acc *portal.Account, org *portal.Org, reason string, err error) {
	if !a.p.Scoped {
		acc, err = a.p.Users.FindByUsername(ctx, username)
	} else {
		ou := a.orgUsers()
		org, err = ou.FindOrgByCode(ctx, orgCode)
		switch {
		case errors.Is(err, portal.ErrOrgNotFound):
			return nil, nil, "no_org", nil
		case err != nil:
			return nil, nil, "", err
		case portal.NormalizeOrgCode(org.Code) != orgCode:
			// 编号的别名（排序规则忽略的差异）同样按不存在处理，理由同账号名（D-043）
			return nil, nil, "no_org", nil
		case !org.Enabled():
			return nil, org, "org_disabled", nil
		}
		acc, err = ou.FindByOrgUsername(ctx, org.ID, username)
		if err == nil && acc.OrgID != org.ID {
			acc = nil
		}
	}
	switch {
	case errors.Is(err, portal.ErrAccountNotFound):
		return nil, org, "no_account", nil
	case err != nil:
		return nil, nil, "", err
	case acc == nil:
		return nil, org, "no_account", nil
	}
	// 查到的账号必须和输入的登录名逐字相同：用户表的排序规则可能忽略重音等差异（ádmin 查到 admin），
	// 而限流、锁定、日志都按输入的登录名计数，别名会给同一个账号开出另一份尝试配额；
	// 所以别名一律按"账号不存在"处理，既不校验密码，也不在响应上区分（D-043）。
	if portal.NormalizeUsername(acc.Username) != username {
		return nil, org, "no_account", nil
	}
	return acc, org, "", nil
}

// lockOrg 在当前事务里读主体的最新提交状态并加排他锁（portal.OrgLocker，主体端注册时已要求实现，D-061、D-063）。
func (a *Authenticator) lockOrg(ctx context.Context, id uint64) (*portal.Org, error) {
	if l, ok := a.p.Users.(portal.OrgLocker); ok {
		return l.LockOrgByID(ctx, id)
	}
	return nil, fmt.Errorf("authimpl: 主体端 %s 的用户来源没有实现 portal.OrgLocker", a.p.Code)
}

// lockAccount 在当前事务里读账号的最新提交状态：用户来源实现了 portal.UserLocker 的加排他锁，否则退回普通读（D-047）。
func (a *Authenticator) lockAccount(ctx context.Context, id uint64) (*portal.Account, error) {
	if l, ok := a.p.Users.(portal.UserLocker); ok {
		return l.LockByID(ctx, id)
	}
	return a.p.Users.FindByID(ctx, id)
}

// sessionEndedErr：刷新失败，且服务端确定 Cookie 里的会话已经结束（不存在、已吊销、已过期，或这次刚因重放、账号停用被吊销）。
// 带翻译键，前端只有看到它才能把"退出没被服务端确认"算作已确认（D-052）；其他 401（没有或看不懂 Cookie、
// 凭证对不上、Cookie 属于别的会话）说明的只是这次刷新不能完成，会话本身可能还活着。
func sessionEndedErr() *httpx.Error {
	return httpx.NewKey(httpx.CodeTokenInvalid, "auth.sessionEnded", "the session has ended")
}

// Refresh 处理 POST /auth/refresh（规范 §5.4）。
func (a *Authenticator) Refresh(c *gin.Context) {
	if !a.checkClient(c) {
		secmark.Set(c, secmark.Mark{Kind: secmark.BadOrigin, Detail: "refresh"})
		httpx.Fail(c, httpx.ErrForbidden)
		return
	}
	// 同名的刷新 Cookie 不止一个：有别的主机（同一可注册域下的子域）往浏览器里塞了一个（D-058）。
	// 不猜哪个是真的——浏览器先发路径更长的，塞进来的那个可以排在前面，拿它刷新就会登录成别人的账号。
	// 拒绝刷新、记安全事件；不带"会话已结束"的键（本来的会话可能还活着），也不动 Cookie
	if len(c.Request.CookiesNamed(a.CookieName())) > 1 {
		secmark.Set(c, secmark.Mark{Kind: secmark.RefreshCookieDup})
		httpx.Fail(c, httpx.ErrTokenInvalid)
		return
	}
	raw, err := c.Cookie(a.CookieName())
	if err != nil || raw == "" {
		httpx.Fail(c, httpx.ErrTokenInvalid)
		return
	}
	tok, ok := session.ParseRefreshToken(raw)
	if !ok {
		httpx.Fail(c, httpx.ErrTokenInvalid)
		return
	}
	if _, ok := a.refreshRate.Reserve(c.Request.Context(), ratelimit.IPKey(c.ClientIP())); !ok {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	if want := c.GetHeader(HeaderSession); want != "" && want != tok.SID {
		// Cookie 已经是另一个会话的（别的标签页换了人登录）：这个标签页的登录到此为止，不轮换、不动 Cookie（D-048）。
		// 带上翻译键，前端据此告诉用户是换了账号，而不只是登录过期（D-050）
		httpx.Fail(c, httpx.NewKey(httpx.CodeTokenInvalid, "auth.sessionSwitched", "the refresh cookie belongs to another session"))
		return
	}
	ctx := c.Request.Context()
	// 先查账号、再轮换（D-050）：轮换一旦提交，浏览器手里的凭证就旧了。查账号时数据库临时出错，要在轮换之前回 503，
	// 会话和凭证都不动，浏览器下次照样能刷新；轮换之后才出错的话，响应里没有新 Cookie，旧凭证在宽限期后再用就被当成重放。
	//
	// 凭证对得上（当前值、上一个值，或者家族密钥对得上，D-104）才查账号、主体和 IP 名单（D-097）：会话号出现在会话列表和各类日志里，
	// 只知道会话号、编不出凭证的请求不该走到这一步——它会留下带着那个账号的"IP 不允许"记录，
	// 回答也因账号、名单的状态而不同。对不上的直接交给 Rotate，按原来的规则回 401
	var (
		acc      *portal.Account
		resolved bool // 账号、主体、IP 名单已经按这个会话核对过
	)
	s, gerr := a.sessions.Get(ctx, tok.SID)
	if gerr != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(gerr))
		return
	}
	if s == nil || s.Portal != a.p.Code {
		httpx.Fail(c, sessionEndedErr())
		return
	}
	if s != nil && s.Portal == a.p.Code && s.HoldsToken(tok) {
		found, denied, ferr := a.refreshAccount(ctx, s, c.ClientIP())
		if ferr != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(ferr))
			return
		}
		if denied {
			// 主体、账号的 IP 白名单（D-062）：名单外的来源不轮换、不吊销、不动 Cookie，回 2003
			secmark.Set(c, secmark.Mark{Kind: secmark.IPDenied, Detail: "refresh", OrgID: s.OrgID, UserID: found.ID, SessionID: s.SID})
			httpx.Fail(c, httpx.ErrIPDenied)
			return
		}
		acc, resolved = found, true
	}
	// 下面所有失败分支都不动 Cookie（D-049，见 setRefreshCookie 的注释）
	sess, newTok, err := a.sessions.Rotate(ctx, a.p.Code, tok, c.ClientIP(), c.Request.UserAgent())
	switch {
	case errors.Is(err, session.ErrRetry):
		httpx.Fail(c, httpx.New(httpx.CodeRefreshRetry, ""))
		return
	case errors.Is(err, session.ErrReuseDetected):
		secmark.Set(c, a.refreshMark(ctx, secmark.RefreshReuse, tok.SID))
		a.sessCache.Delete(tok.SID)
		a.log.WarnContext(ctx, "refresh token reuse detected", "sid", tok.SID, "ip", c.ClientIP())
		httpx.Fail(c, sessionEndedErr())
		return
	case errors.Is(err, session.ErrMismatch):
		// 凭证和会话对不上：会话没动，只记安全事件（有人拿着会话号在试，或很旧的凭证副本）
		secmark.Set(c, a.refreshMark(ctx, secmark.RefreshMismatch, tok.SID))
		a.log.WarnContext(ctx, "refresh token does not match session", "sid", tok.SID, "ip", c.ClientIP())
		httpx.Fail(c, httpx.ErrTokenInvalid)
		return
	case errors.Is(err, session.ErrNotFound), errors.Is(err, session.ErrInactive):
		httpx.Fail(c, sessionEndedErr())
		return
	case err != nil:
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	if !resolved {
		// 读会话时凭证还对不上、轮换时却对上了：只可能是两步之间会话被并发轮换过，正常情况下走不到。
		// 轮换已经提交，这里补做同样的核对，不能跳过账号状态和 IP 名单
		a.log.WarnContext(ctx, "refresh credential matched only at rotation; account checked after rotating", "sid", sess.SID)
		found, denied, ferr := a.refreshAccount(ctx, sess, c.ClientIP())
		if ferr != nil || denied {
			// 新凭证已经生效：把它交给浏览器再回错误，不然浏览器手里的旧凭证过了宽限期再用会被当成重放
			a.setRefreshCookie(c, newTok, a.cookieMaxAge(sess.ExpiresAt))
		}
		if ferr != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(ferr))
			return
		}
		if denied {
			secmark.Set(c, secmark.Mark{Kind: secmark.IPDenied, Detail: "refresh", OrgID: sess.OrgID, UserID: found.ID, SessionID: sess.SID})
			httpx.Fail(c, httpx.ErrIPDenied)
			return
		}
		acc = found
	}
	if acc == nil || acc.ID != sess.UserID || !acc.Enabled() {
		// 吊销失败时不能说"会话已结束"（D-053）：前端会据此把退出算作已确认、删掉待补退出的记录，而会话其实还在
		if err := a.sessions.Revoke(ctx, a.p.Code, sess.SID, auth.RevokeDisabled); err != nil && !errors.Is(err, session.ErrNotFound) {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		a.sessCache.Delete(sess.SID)
		httpx.Fail(c, sessionEndedErr())
		return
	}
	a.sessCache.Delete(sess.SID)
	a.setRefreshCookie(c, newTok, a.cookieMaxAge(sess.ExpiresAt))
	if err := a.issue(c, acc, sess.SID); err != nil {
		httpx.Fail(c, err)
	}
}

// refreshAccount 读刷新所属会话的账号，并核对主体和 IP 名单。
// 账号不存在、主体停用或不存在、账号的主体不是会话的主体时返回 nil 账号：按账号停用处理，轮换之后吊销（D-061）。
// denied 为真表示来源不在主体或账号的 IP 白名单里（D-062），这时一并返回账号，供调用方记安全事件。
// 读库出错时返回错误，调用方回 503。
func (a *Authenticator) refreshAccount(ctx context.Context, s *session.Session, ip string) (acc *portal.Account, denied bool, err error) {
	found, ferr := a.p.Users.FindByID(ctx, s.UserID)
	if errors.Is(ferr, portal.ErrAccountNotFound) {
		return nil, false, nil // 账号没了：和停用一样，轮换之后吊销
	}
	if ferr != nil {
		return nil, false, ferr
	}
	if a.p.Scoped {
		org, oerr := a.accountOrg(ctx, found)
		if oerr != nil {
			return nil, false, oerr
		}
		if !org.Enabled() || found.OrgID != s.OrgID {
			return nil, false, nil
		}
	}
	if !a.accountIPAllowed(ctx, s.OrgID, found.ID, ip) {
		return found, true, nil
	}
	return found, false, nil
}

// refreshMark 组装刷新失败的安全标记：带会话号，会话还在时补上它的用户。
func (a *Authenticator) refreshMark(ctx context.Context, kind, sid string) secmark.Mark {
	mark := secmark.Mark{Kind: kind, SessionID: sid}
	if s, err := a.sessions.Get(ctx, sid); err == nil && s != nil {
		mark.UserID = s.UserID
	}
	return mark
}

// Logout 处理 POST /auth/logout：吊销当前会话。
//
// 不动刷新 Cookie（D-049）：吊销后 Cookie 已经没有用处，而退出的响应可能迟到——同一浏览器里这时已经有人重新登录了，
// 一条"删除 Cookie"的响应会把新登录清掉。请求发出时比对 Cookie 也不行，登录可能发生在比对之后、响应到达之前。
func (a *Authenticator) Logout(c *gin.Context) {
	if !a.checkClient(c) {
		secmark.Set(c, secmark.Mark{Kind: secmark.BadOrigin, Detail: "logout"})
		httpx.Fail(c, httpx.ErrForbidden)
		return
	}
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	if err := a.RevokeSession(ctx, p.SessionID, auth.RevokeLogout); err != nil && !errors.Is(err, session.ErrNotFound) {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// meResponse 是 /auth/me 的出参。
type meResponse struct {
	User      meUser     `json:"user"`
	Org       *meOrg     `json:"org,omitempty"` // 主体端：当前账号所属的主体（D-061）
	Locked    bool       `json:"locked"`
	Perms     []string   `json:"perms"`
	Menus     any        `json:"menus"`
	PwdPolicy pwdPolicyV `json:"pwdPolicy"`
}

// pwdPolicyV 是下发给前端的密码策略：改密页据此做前端校验和提示，安全仍以后端校验为准。
type pwdPolicyV struct {
	MinLength     int  `json:"minLength"`
	RequireUpper  bool `json:"requireUpper"`
	RequireLower  bool `json:"requireLower"`
	RequireSymbol bool `json:"requireSymbol"`
	MaxAgeDays    int  `json:"maxAgeDays"`
}

// meOrg 是 /auth/me 里的主体：顶栏显示"主体名 · 账号"。
type meOrg struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type meUser struct {
	ID            uint64 `json:"id"`
	Username      string `json:"username"`
	DisplayName   string `json:"displayName"`
	Avatar        string `json:"avatar"`
	MustChangePwd bool   `json:"mustChangePwd"`
	PwdExpired    bool   `json:"pwdExpired"`
	Super         bool   `json:"super"`
}

// Me 处理 GET /auth/me。
func (a *Authenticator) Me(c *gin.Context) {
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	acc, err := a.p.Users.FindByID(ctx, p.UserID)
	if err != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	must, expired := a.mustChange(acc)
	pp := a.pwdRules
	resp := meResponse{
		User:   meUser{ID: acc.ID, Username: acc.Username, DisplayName: acc.DisplayName, Avatar: acc.Avatar, MustChangePwd: must, PwdExpired: expired, Super: p.Super},
		Locked: p.Locked,
		Perms:  []string{},
		Menus:  []any{},
		PwdPolicy: pwdPolicyV{
			MinLength: pp.MinLength, RequireUpper: pp.RequireUpper, RequireLower: pp.RequireLower, RequireSymbol: pp.RequireSymbol,
			MaxAgeDays: int(pp.MaxAge / (24 * time.Hour)),
		},
	}
	if a.p.Scoped {
		org, err := a.accountOrg(ctx, acc)
		if err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		if org != nil {
			resp.Org = &meOrg{Code: org.Code, Name: org.Name}
		}
	}
	// 锁屏时只给锁屏界面要用的用户名和头像，不给权限和菜单（D-027）
	if a.authorizer != nil && !p.Locked {
		if resp.Perms, err = a.authorizer.Perms(ctx, p); err != nil {
			httpx.Fail(c, err)
			return
		}
		if resp.Menus, err = a.authorizer.Menus(ctx, p); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	httpx.OK(c, resp)
}

// Lock 处理 POST /auth/lock：锁定当前会话（D-027）。之后除查看自己、解锁、登出外的接口都回 1006。
func (a *Authenticator) Lock(c *gin.Context) {
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	if err := a.sessions.Lock(ctx, a.p.Code, p.SessionID); err != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	a.sessCache.Delete(p.SessionID)
	httpx.OK(c, nil)
}

type unlockRequest struct {
	Password string `json:"password" binding:"required,max=72"`
}

// Unlock 处理 POST /auth/unlock：校验当前账号的密码后解除锁定。
// 每次尝试先在数据库里占用一次机会再校验密码，并发请求也最多只有 MaxUnlockFailures 次机会；
// 用完即吊销这个会话，只能重新登录。输错也计入登录防护的失败次数（按账号和 IP），
// 所以换到登录页也不会多出猜测的机会。
func (a *Authenticator) Unlock(c *gin.Context) {
	var req unlockRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	if !p.Locked {
		httpx.OK(c, nil)
		return
	}
	acc, err := a.p.Users.FindByID(ctx, p.UserID)
	if err != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	// 输错要计入登录防护：先在登录防护里给这次核对留好位置，放不下（计数表满了）就在占用解锁机会、核对密码之前拒绝（D-057）
	username := portal.NormalizeUsername(acc.Username)
	var orgCode string
	if a.p.Scoped {
		org, err := a.accountOrg(ctx, acc)
		if err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		if org != nil {
			orgCode = portal.NormalizeOrgCode(org.Code)
		}
	}
	key := a.guardKey(orgCode, username) // 和登录同一个键（D-061）
	att := a.guard.AdmitContext(ctx, key, c.ClientIP())
	if att == nil {
		secmark.Set(c, secmark.Mark{Kind: secmark.LoginRateLimited, Username: key})
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	defer att.Done()
	// 主体端：一个主体同时在做的解锁有上限，满了直接回 429（D-071）。和下面的位置一样，满了不占解锁次数
	if key, ok := a.orgKey(p); ok {
		if !a.orgUnlock.TryEnter(key) {
			httpx.Fail(c, httpx.ErrTooManyRequests)
			return
		}
		defer a.orgUnlock.Leave(key)
	}
	// 核对密码的并发上限，满了不占解锁次数（D-058）。解锁和登录一样可以用全部位置，满了等一小会儿（D-071）
	leave, admitted := a.budget.EnterFront(ctx)
	if !admitted {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	defer leave()
	res, n, err := a.sessions.ReserveUnlockAttempt(ctx, a.p.Code, p.SessionID, MaxUnlockFailures)
	if err != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	switch res {
	case session.AttemptNotLocked:
		a.sessCache.Delete(p.SessionID)
		httpx.OK(c, nil)
		return
	case session.AttemptExhausted:
		a.revokeLocked(c, p.SessionID)
		return
	}
	incorrect := func() {
		if n >= MaxUnlockFailures {
			a.revokeLocked(c, p.SessionID)
			return
		}
		left := MaxUnlockFailures - int(n)
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("password", "password.unlockIncorrect", fmt.Sprintf("incorrect password, %d attempts left", left), "left", left)))
	}
	matched := a.hasher.Verify(acc.PasswordHash, req.Password)
	leave() // 核对完了，位置让出来：下面记失败、吊销会话、解除锁定都是数据库操作，不带着位置做（D-071）
	if !matched {
		att.Fail()
		incorrect()
		return
	}
	// 密码是在事务外核对的；解锁时再锁住账号行确认密码没变（D-053），和本人改密、重置密码同一个加锁顺序（用户行 → 会话行）：
	// 核对之后、解锁之前密码被改了（同一会话在途的改密、管理员重置），刚才输入的已经不是当前密码，按输错处理
	var pwdChanged bool
	err = db.Tx(ctx, func(ctx context.Context) error {
		cur, err := a.lockAccount(ctx, acc.ID)
		if err != nil {
			return err
		}
		if !a.samePassword(acc.PasswordHash, cur.PasswordHash, req.Password) {
			pwdChanged = true
			return nil
		}
		return a.sessions.Unlock(ctx, a.p.Code, p.SessionID)
	})
	if err != nil {
		a.sessCache.Delete(p.SessionID)
		if errors.Is(err, session.ErrNotFound) || errors.Is(err, portal.ErrAccountNotFound) {
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	if pwdChanged {
		incorrect()
		return
	}
	a.sessCache.Delete(p.SessionID)
	httpx.OK(c, nil)
}

// revokeLocked 解锁机会用完：吊销会话（已吊销的不受影响），回 1004。刷新 Cookie 不动（D-049），吊销后它已经没有用处。
func (a *Authenticator) revokeLocked(c *gin.Context, sid string) {
	secmark.Set(c, secmark.Mark{Kind: secmark.UnlockExhausted})
	if err := a.RevokeSession(c.Request.Context(), sid, auth.RevokeUnlockFailed); err != nil && !errors.Is(err, session.ErrNotFound) {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	httpx.Fail(c, httpx.ErrTokenInvalid)
}

type changePasswordRequest struct {
	OldPassword string `json:"oldPassword" binding:"required,max=72"`
	NewPassword string `json:"newPassword" binding:"required,max=72"`
}

// ChangePassword 处理 PUT /auth/password。
func (a *Authenticator) ChangePassword(c *gin.Context) {
	var req changePasswordRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	// 主体端：一个主体同时在做的本人改密有上限，满了直接回 429（D-071）
	if key, ok := a.orgKey(p); ok {
		if !a.orgPwd.TryEnter(key) {
			httpx.Fail(c, httpx.ErrTooManyRequests)
			return
		}
		defer a.orgPwd.Leave(key)
	}
	// 密码计算的位置，满了不占改密次数（D-058）。改密是后一类：最多占一半位置，满了直接回 429，不等（D-071）
	leave, admitted := a.budget.EnterBackground()
	if !admitted {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	defer leave()
	// 核对旧密码之前先占一次机会（D-055）：只拿到访问令牌、不知道密码的人不能借这里一直试密码
	triesKey := "sid:" + p.SessionID
	tries, ok := a.pwdTries.Reserve(ctx, triesKey)
	if !ok {
		secmark.Set(c, secmark.Mark{Kind: secmark.PwdChangeThrottled})
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	acc, err := a.p.Users.FindByID(ctx, p.UserID)
	if err != nil {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
		return
	}
	// 一个账号 15 分钟内成功改密的次数（D-071）：先占一次，这次没改成再退回去，并发的请求不能一起挤过去。
	// 被要求改密的账号（新建的、被重置的）不计也不挡：那一次是系统要他改的，挡住了他什么都做不了
	changesKey := strconv.FormatUint(p.UserID, 10)
	counted, changed := !acc.MustChangePwd, false
	if counted {
		changes, ok := a.pwdChanges.Reserve(ctx, changesKey)
		if !ok {
			tries.Undo() // 没有核对，不占核对旧密码的次数
			secmark.Set(c, secmark.Mark{Kind: secmark.PwdChangeThrottled})
			httpx.Fail(c, httpx.ErrTooManyRequests)
			return
		}
		defer func() {
			if !changed {
				changes.Undo()
				return
			}
			db.AfterEnd(ctx, func(committed bool) {
				if !committed {
					changes.Undo()
				}
			})
		}()
	}
	if !a.hasher.Verify(acc.PasswordHash, req.OldPassword) {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("oldPassword", "password.oldIncorrect", "incorrect")))
		return
	}
	hash, err := a.newPasswordHash(acc, req.NewPassword)
	leave() // 算完了，位置让出来：下面写库的事务要等行锁，不带着位置等（D-071）
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := a.storePassword(ctx, acc, req.OldPassword, hash, p.SessionID); err != nil {
		httpx.Fail(c, err)
		return
	}
	changed = true
	db.AfterCommit(ctx, tries.Reset)
	httpx.OK(c, nil)
}

// samePassword 报告事务里读到的哈希 cur 对应的是不是"刚才核对过的那个密码"（D-047、D-070）。哈希没变当然是。
// 刚才核对的是旧算法的哈希、这期间被另一个登录升级成了当前参数的：哈希变了，密码没变——拿这次输入的明文按新哈希
// 再核对一次，核对得上就照常继续；不然同一个账号升级的那一刻，同时在途的登录、解锁、改密都会被当成"密码已经换了"。
// 别的情况（改密、重置）都是密码真的换了，不用算就知道。
//
// 这是唯一一处拿着账号行锁做密码计算的地方：只在"旧哈希刚被升级"的那一次发生，一个账号一辈子一回。
func (a *Authenticator) samePassword(checked, cur, plain string) bool {
	if cur == checked {
		return true
	}
	return a.hasher.NeedsRehash(checked) && !a.hasher.NeedsRehash(cur) && a.hasher.Verify(cur, plain)
}

// newPasswordHash 按策略校验新密码（包括不能和现在的一样，这一条要做一次密码比较）并算好哈希。都是计算，不碰库。
func (a *Authenticator) newPasswordHash(acc *portal.Account, plain string) (string, error) {
	if err := a.pwdPolicy.Check(a.hasher, plain, acc.Username, acc.PasswordHash); err != nil {
		return "", httpx.ErrValidation.WithFields(a.policyField("newPassword", err)).WithCause(err)
	}
	return a.hasher.Hash(plain)
}

// storePassword 把本人改密算好的新哈希写进去，吊销该用户除 keepSID 外的全部会话，清缓存。
// oldPlain 是刚才核对通过的旧密码：事务里发现哈希变了的时候用它分清"密码真的换了"和"只是被升级了算法"（samePassword）。
func (a *Authenticator) storePassword(ctx context.Context, acc *portal.Account, oldPlain, hash, keepSID string) error {
	// 写新密码和吊销其他会话是一个事务：任何一步失败都整体回滚，不会出现"密码已换、旧会话还活着"的中间状态
	err := db.Tx(ctx, func(ctx context.Context) error {
		if keepSID != "" {
			// 本人改密（D-045、D-047）：旧密码是在事务外验证的，这之后管理员可能已经重置了密码、吊销了会话，
			// 同一个会话的另一个改密请求也可能已经把密码换掉了。先锁用户行，再锁自己的会话行：
			// 重置密码、停用账号也是先写用户行、再吊销会话，加锁顺序相同（用户行 → 会话行，D-046），排队而不会死锁。
			// 会话已被吊销的回 401，管理员重置的密码留着；密码已经不是刚才验证的那个，按旧密码不对处理。
			cur, err := a.lockAccount(ctx, acc.ID)
			if err != nil {
				return err
			}
			if err := a.sessions.LockWritable(ctx, a.p.Code, keepSID); err != nil {
				if errors.Is(err, session.ErrInactive) || errors.Is(err, session.ErrNotFound) {
					return httpx.ErrTokenInvalid
				}
				if errors.Is(err, session.ErrLocked) {
					return httpx.ErrSessionLocked
				}
				return err
			}
			if !a.samePassword(acc.PasswordHash, cur.PasswordHash, oldPlain) {
				return httpx.ErrValidation.WithFields(httpx.NewField("oldPassword", "password.oldIncorrect", "incorrect"))
			}
		}
		if err := a.p.Users.UpdatePasswordHash(ctx, acc.ID, hash, false); err != nil {
			return err
		}
		if _, err := a.sessions.RevokeUser(ctx, a.p.Code, acc.ID, auth.RevokePwdChange, keepSID); err != nil {
			return err
		}
		db.AfterCommit(ctx, func() { a.ForgetAccount(acc.ID) })
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// policyField 把密码策略的错误翻成带翻译键的字段错误（D-026）：键 password.<规则>，最小长度作为参数。
func (a *Authenticator) policyField(field string, err error) httpx.FieldError {
	switch {
	case errors.Is(err, password.ErrTooShort):
		return httpx.NewField(field, "password.tooShort", fmt.Sprintf("at least %d characters", a.pwdRules.MinLength), "min", a.pwdRules.MinLength)
	case errors.Is(err, password.ErrNoUpper):
		return httpx.NewField(field, "password.noUpper", "must contain an uppercase letter")
	case errors.Is(err, password.ErrNoLower):
		return httpx.NewField(field, "password.noLower", "must contain a lowercase letter")
	case errors.Is(err, password.ErrNoSymbol):
		return httpx.NewField(field, "password.noSymbol", "must contain a symbol")
	case errors.Is(err, password.ErrNoLetter):
		return httpx.NewField(field, "password.noLetter", "must contain a letter")
	case errors.Is(err, password.ErrNoDigit):
		return httpx.NewField(field, "password.noDigit", "must contain a digit")
	case errors.Is(err, password.ErrSameAsUsername):
		return httpx.NewField(field, "password.sameAsUsername", "must not equal the username")
	case errors.Is(err, password.ErrSameAsPrevious):
		return httpx.NewField(field, "password.sameAsPrevious", "must differ from the previous password")
	case errors.Is(err, password.ErrContainsSpaces):
		return httpx.NewField(field, "password.spaces", "must not start or end with whitespace")
	case errors.Is(err, password.ErrTooLong):
		return httpx.NewField(field, "password.tooLong", "at most 72 bytes", "max", 72)
	default:
		return httpx.NewField(field, "password.invalid", "invalid")
	}
}
