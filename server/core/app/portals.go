package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/authimpl"
	"github.com/goalladmin/goalladmin/server/core/internal/captcha"
	"github.com/goalladmin/goalladmin/server/core/internal/oplogimpl"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// Authorizer 由授权模块实现并通过 SetAuthorizer 安装。
// 安装前，Require 守卫的路由无法注册，Principal.Super 恒为 false，/auth/me 的 perms 和 menus 为空。
type Authorizer interface {
	authimpl.Authorizer
	// RequireMiddleware 返回判定"当前身份是否拥有该权限码"的中间件，排在认证中间件之后。
	RequireMiddleware(portalCode, perm string) gin.HandlerFunc
	// RequireSuperMiddleware 返回"必须是本端超管"的中间件（D-035）。
	RequireSuperMiddleware(portalCode string) gin.HandlerFunc
}

// SetAuthorizer 安装授权模块。必须在 Setup 之前调用。
func (a *App) SetAuthorizer(z Authorizer) {
	a.authorizer = z
	for _, an := range a.authenticators {
		an.SetAuthorizer(z)
	}
}

// setupPortals 在模块 Init 之后、Routes 之前执行：为每个已注册的端建路由组、认证器和 /auth 接口。
func (a *App) setupPortals() error {
	if a.captcha == nil {
		a.captcha = captcha.New()
	}
	for _, p := range a.deps.Portals.All() {
		cfgP, ok := a.deps.Conf.Portals[p.Code]
		if !ok {
			return fmt.Errorf("app: 端 %s 已在代码里注册，但配置里没有 portals.%s", p.Code, p.Code)
		}
		secret := []byte(cfgP.JWTSecret)
		if len(secret) == 0 {
			if a.deps.Conf.Server.IsRelease() {
				return fmt.Errorf("app: 端 %s 缺少 JWT 密钥", p.Code)
			}
			// debug 模式：生成一次性密钥，重启即失效
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			secret = []byte(hex.EncodeToString(buf))
			a.deps.Log.Warn("端未配置 JWT 密钥，debug 模式下使用随机临时密钥，重启后所有登录失效", "portal", p.Code)
		}
		p.Login, p.Password = mergeLogin(p.Login, cfgP.Login), mergePassword(p.Password, cfgP.Password)
		accessTTL, refreshTTL := p.AccessTTL, p.RefreshTTL
		if accessTTL <= 0 {
			accessTTL = cfgP.AccessTTL
		}
		if refreshTTL <= 0 {
			refreshTTL = cfgP.RefreshTTL
		}
		an, err := authimpl.New(authimpl.Options{
			Portal:         p,
			Secret:         secret,
			AccessTTL:      accessTTL,
			RefreshTTL:     refreshTTL,
			Release:        a.deps.Conf.Server.IsRelease(),
			AllowedOrigins: a.deps.Conf.Server.AllowedOrigins,
			Log:            a.deps.Log,
			Now:            a.now,
			BcryptCost:     a.bcryptCost,
			Captcha:        a.captcha,
			StatusCacheTTL: a.statusCacheTTL,
			// 只有测试会改（同包的测试直接设字段），0 用默认值
			PasswordParallel: a.pwdParallel,
		})
		if err != nil {
			return err
		}
		if a.authorizer != nil {
			an.SetAuthorizer(a.authorizer)
		}
		a.authenticators[p.Code] = an
		a.portalGroups[p.Code] = a.engine.Group(PortalPrefix(p.Code))
	}
	if a.guardResolver == nil {
		a.guardResolver = a.resolveGuard
	}
	a.deps.Auth = &authService{a: a}
	for _, p := range a.deps.Portals.All() {
		a.mountAuthRoutes(p.Code)
		a.mountDictRoutes(p.Code)
	}
	return nil
}

// maxDictCodes 是一次读取的字典数上限。
const maxDictCodes = 50

// mountDictRoutes 挂每个端的字典读取接口：登录即可，只返回本端可见的字典（D-023）。
func (a *App) mountDictRoutes(code string) {
	a.router.Portal(code).GET("/dicts", rbac.AuthOnly(), func(c *gin.Context) {
		if a.deps.Dict == nil {
			httpx.Fail(c, httpx.ErrUnavailable)
			return
		}
		var codes []string
		seen := map[string]bool{}
		for _, part := range strings.Split(c.Query("codes"), ",") {
			if part = strings.TrimSpace(part); part != "" && !seen[part] {
				seen[part] = true
				codes = append(codes, part)
			}
		}
		if len(codes) == 0 || len(codes) > maxDictCodes {
			httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("codes", "dict.readCodes", fmt.Sprintf("1-%d dictionary codes separated by commas", maxDictCodes), "max", maxDictCodes)))
			return
		}
		out, err := a.deps.Dict.Many(c.Request.Context(), code, codes, httpx.LangOf(c))
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, out)
	})
}

// mergeLogin 把配置文件里的登录防护参数叠加到代码声明上：配置里非零的数值覆盖代码，布尔开关只能打开。
// 结果由认证器按底线校验，越界拒绝启动（D-024）。
func mergeLogin(code portal.LoginPolicy, c conf.PortalLogin) portal.LoginPolicy {
	p := code.Normalized()
	setDur := func(dst *time.Duration, v time.Duration) {
		if v != 0 {
			*dst = v
		}
	}
	setInt := func(dst *int, v int) {
		if v != 0 {
			*dst = v
		}
	}
	setDur(&p.Window, c.Window)
	setDur(&p.LockDuration, c.LockDuration)
	setInt(&p.CaptchaAfterFailures, c.CaptchaAfterFailures)
	setInt(&p.LockAfterFailures, c.LockAfterFailures)
	setInt(&p.AccountLockAfter, c.AccountLockAfter)
	setInt(&p.IPRatePerMinute, c.IPRatePerMinute)
	setInt(&p.AccountRatePerMinute, c.AccountRatePerMinute)
	p.CaptchaAlways = p.CaptchaAlways || c.CaptchaAlways
	return p
}

// mergePassword 把配置文件里的密码策略叠加到代码声明上，规则同 mergeLogin。
func mergePassword(code portal.PasswordPolicy, c conf.PortalPassword) portal.PasswordPolicy {
	p := code.Normalized()
	if c.MinLength != 0 {
		p.MinLength = c.MinLength
	}
	if c.MaxAgeDays != 0 {
		p.MaxAge = time.Duration(c.MaxAgeDays) * 24 * time.Hour
	}
	p.RequireUpper = p.RequireUpper || c.RequireUpper
	p.RequireLower = p.RequireLower || c.RequireLower
	p.RequireSymbol = p.RequireSymbol || c.RequireSymbol
	return p
}

// resolveGuard 把守卫声明翻译成中间件链。
func (a *App) resolveGuard(portalCode string, g Guard) ([]gin.HandlerFunc, error) {
	an, ok := a.authenticators[portalCode]
	if !ok {
		return nil, fmt.Errorf("端 %s 没有认证器", portalCode)
	}
	switch g.Kind() {
	case rbac.GuardPublic:
		return nil, nil
	case rbac.GuardAuthOnly:
		return []gin.HandlerFunc{an.Authenticate()}, nil
	case rbac.GuardRequire:
		if a.authorizer == nil {
			return nil, errors.New("授权模块未安装，不能注册 Require 路由")
		}
		return []gin.HandlerFunc{an.Authenticate(), a.authorizer.RequireMiddleware(portalCode, g.Perm())}, nil
	case rbac.GuardSuper:
		if a.authorizer == nil {
			return nil, errors.New("授权模块未安装，不能注册 RequireSuper 路由")
		}
		return []gin.HandlerFunc{an.Authenticate(), a.authorizer.RequireSuperMiddleware(portalCode)}, nil
	default:
		return nil, fmt.Errorf("未知的守卫档位 %q", g.Kind())
	}
}

// mountAuthRoutes 挂每个端的认证接口（规范 §5.8）。
func (a *App) mountAuthRoutes(code string) {
	an := a.authenticators[code]
	r := a.router.Portal(code).Group("/auth", an.AllowPwdChange())
	r.GET("/captcha", rbac.Public(), an.Captcha)
	r.POST("/login", rbac.Public(), an.Login)
	r.POST("/refresh", rbac.Public(), an.Refresh)
	r.POST("/logout", rbac.AuthOnly(), an.Logout)
	r.GET("/me", rbac.AuthOnly(), an.Me)
	// 锁屏（D-027）：锁定时只有查看自己、解锁、登出能用
	r.Handle(http.MethodPost, "/lock", rbac.AuthOnly(), an.Lock, WithOpName(OpLock), WithMiddleware(oplogimpl.Middleware(code, OpLock)))
	r.Handle(http.MethodPost, "/unlock", rbac.AuthOnly(), an.Unlock, WithOpName(OpUnlock), WithMiddleware(oplogimpl.Middleware(code, OpUnlock)))
	prefix := PortalPrefix(code) + "/auth"
	an.ExemptFromLock(prefix+"/me", prefix+"/unlock", prefix+"/logout")
	r.Handle(http.MethodPut, "/password", rbac.AuthOnly(), an.ChangePassword, WithOpName(OpChangePassword), WithMiddleware(oplogimpl.Middleware(code, OpChangePassword)))
}

// 认证接口的操作日志动作名。
const (
	OpChangePassword = "auth.password"
	OpLock           = "auth.lock"
	OpUnlock         = "auth.unlock"
)

// authService 把各端的认证器聚合成 auth.Service。
type authService struct{ a *App }

func (s *authService) portal(code string) (*authimpl.Authenticator, error) {
	an, ok := s.a.authenticators[code]
	if !ok {
		return nil, fmt.Errorf("auth: 端 %q 不存在", code)
	}
	return an, nil
}

func (s *authService) RevokeUserSessions(ctx context.Context, portalCode string, userID uint64, reason string) error {
	an, err := s.portal(portalCode)
	if err != nil {
		return err
	}
	return an.RevokeUserSessions(ctx, userID, reason)
}

func (s *authService) RevokeOtherSessions(ctx context.Context, portalCode string, userID uint64, keepSID, reason string) (int64, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return 0, err
	}
	return an.RevokeOtherSessions(ctx, userID, keepSID, reason)
}

func (s *authService) RevokeSession(ctx context.Context, portalCode, sid, reason string) error {
	an, err := s.portal(portalCode)
	if err != nil {
		return err
	}
	if err := an.RevokeSession(ctx, sid, reason); err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return httpx.ErrNotFound.WithCause(err)
		}
		return err
	}
	return nil
}

func (s *authService) SessionOwner(ctx context.Context, portalCode, sid string) (uint64, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return 0, err
	}
	sess, err := an.Sessions().Get(ctx, sid)
	if err != nil {
		return 0, err
	}
	// 别的端的会话对本端来说就是不存在（规范 §5.4）
	if sess == nil || sess.Portal != portalCode {
		return 0, httpx.ErrNotFound
	}
	return sess.UserID, nil
}

func (s *authService) ForgetAccount(portalCode string, userID uint64) {
	if an, ok := s.a.authenticators[portalCode]; ok {
		an.ForgetAccount(userID)
	}
}

func (s *authService) HashPassword(portalCode, plain string) (string, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return "", err
	}
	return an.HashPassword(plain)
}

func (s *authService) ValidatePassword(portalCode, plain, username string) error {
	an, err := s.portal(portalCode)
	if err != nil {
		return err
	}
	return an.ValidatePassword(plain, username)
}

func (s *authService) Policies(portalCode string) (portal.LoginPolicy, portal.PasswordPolicy, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return portal.LoginPolicy{}, portal.PasswordPolicy{}, err
	}
	return an.LoginPolicy(), an.PasswordPolicy(), nil
}

// GeneratePassword 生成随机密码，长度不短于所有端里最严的最短长度，并且各类字符都有，满足任何端的策略。
func (s *authService) GeneratePassword() (string, error) {
	minLen := 0
	for _, an := range s.a.authenticators {
		if n := an.PasswordPolicy().MinLength; n > minLen {
			minLen = n
		}
	}
	return authimpl.GeneratePassword(minLen)
}

func (s *authService) ListSessions(ctx context.Context, portalCode string, userID uint64, page, pageSize int) ([]auth.SessionInfo, int64, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := an.Sessions().ListActivePage(ctx, portalCode, userID, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	return sessionInfos(rows), total, nil
}

func (s *authService) ListSessionsIn(ctx context.Context, portalCode string, users *gorm.DB, page, pageSize int) ([]auth.SessionInfo, int64, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return nil, 0, err
	}
	rows, total, err := an.Sessions().ListActivePageIn(ctx, portalCode, users, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	return sessionInfos(rows), total, nil
}

func sessionInfos(rows []session.Session) []auth.SessionInfo {
	out := make([]auth.SessionInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, auth.SessionInfo{SID: r.SID, Portal: r.Portal, UserID: r.UserID, IP: r.IP, UserAgent: r.UserAgent, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt})
	}
	return out
}

func (s *authService) ListLoginLogs(ctx context.Context, f auth.LoginLogFilter, page, pageSize int) ([]auth.LoginLogInfo, int64, error) {
	var first *authimpl.Authenticator
	for _, an := range s.a.authenticators {
		first = an
		break
	}
	if first == nil {
		return nil, 0, errors.New("auth: 没有任何端")
	}
	rows, total, err := first.Sessions().ListLoginLogs(ctx, session.LoginLogFilter{
		Portal: f.Portal, UserID: f.UserID, Username: f.Username, IP: f.IP, SessionID: f.SessionID, Success: f.Success, From: f.From, To: f.To,
	}, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	out := make([]auth.LoginLogInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, auth.LoginLogInfo{
			ID: r.ID, Portal: r.Portal, Username: r.Username, UserID: r.UserID, SessionID: r.SessionID, Success: r.Success, Reason: r.Reason,
			IP: r.IP, UserAgent: r.UserAgent, RequestID: r.RequestID, CreatedAt: r.CreatedAt,
		})
	}
	return out, total, nil
}

func (s *authService) CountActiveSessions(ctx context.Context, portalCode string) (int64, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return 0, err
	}
	return an.Sessions().CountActive(ctx, portalCode)
}

func (s *authService) LoginStats(ctx context.Context, portalCode string, since time.Time, tzOffsetMinutes int) (auth.LoginStats, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return auth.LoginStats{}, err
	}
	days, err := an.Sessions().DailyLogins(ctx, portalCode, since, tzOffsetMinutes)
	if err != nil {
		return auth.LoginStats{}, err
	}
	reasons, err := an.Sessions().FailureReasons(ctx, portalCode, since)
	if err != nil {
		return auth.LoginStats{}, err
	}
	out := auth.LoginStats{Days: make([]auth.DailyLogin, 0, len(days)), Reasons: make([]auth.ReasonCount, 0, len(reasons))}
	for _, d := range days {
		out.Days = append(out.Days, auth.DailyLogin{Day: d.Day, Success: d.Success, Failed: d.Failed})
	}
	for _, r := range reasons {
		out.Reasons = append(out.Reasons, auth.ReasonCount{Reason: r.Reason, Count: r.Count})
	}
	hours, err := an.Sessions().HourOfDayLogins(ctx, portalCode, since, tzOffsetMinutes)
	if err != nil {
		return auth.LoginStats{}, err
	}
	out.Hours = make([]int64, 24)
	for _, h := range hours {
		if h.Hour >= 0 && h.Hour < 24 {
			out.Hours[h.Hour] = h.Count
		}
	}
	return out, nil
}

// securityTopIPs 是安全统计里列出的失败来源 IP 数量。
const securityTopIPs = 8

func (s *authService) SecurityStats(ctx context.Context, portalCode string, since time.Time) (auth.SecurityStats, error) {
	an, err := s.portal(portalCode)
	if err != nil {
		return auth.SecurityStats{}, err
	}
	sm := an.Sessions()
	hours, err := sm.HourlyLogins(ctx, portalCode, since)
	if err != nil {
		return auth.SecurityStats{}, err
	}
	ips, err := sm.TopFailedIPs(ctx, portalCode, since, securityTopIPs)
	if err != nil {
		return auth.SecurityStats{}, err
	}
	locked, err := sm.CountLocked(ctx, portalCode)
	if err != nil {
		return auth.SecurityStats{}, err
	}
	out := auth.SecurityStats{Hours: make([]auth.HourlyLogin, 0, len(hours)), FailedIPs: make([]auth.IPCount, 0, len(ips)), LockedSessions: locked}
	for _, h := range hours {
		t, err := time.ParseInLocation("2006-01-02 15:04", h.Hour, time.UTC)
		if err != nil {
			continue
		}
		out.Hours = append(out.Hours, auth.HourlyLogin{Hour: t, Success: h.Success, Failed: h.Failed})
	}
	for _, ip := range ips {
		out.FailedIPs = append(out.FailedIPs, auth.IPCount{IP: ip.IP, Count: ip.Count})
	}
	return out, nil
}

// 供测试与授权模块使用的访问器。

// Portals 返回端注册表。
func (a *App) Portals() *portal.Registry { return a.deps.Portals }
