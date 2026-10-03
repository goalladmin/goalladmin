// Package auth 是业务代码读取"当前是谁"的入口，以及框架提供的会话管理能力。
//
// 身份只能从 ctx 里取（认证中间件放进去的），禁止从请求参数取。
package auth

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/portal"
)

// Principal 是已认证的当前身份。
type Principal struct {
	Portal      string
	UserID      uint64
	SessionID   string
	Username    string
	DisplayName string
	OrgID       uint64 // 所属主体（D-061）：只在主体端（portal.Portal.Scoped）非 0，来自会话行，不来自令牌和请求参数
	Super       bool   // 平台端：超级管理员，由角色决定；主体端：本主体的主账号（主体的 owner_user_id）
	Locked      bool   // 当前会话处于锁屏状态（D-027）；只有 /auth/me、/auth/unlock、/auth/logout 能走到这里
}

type ctxKey struct{}

// ErrNoPrincipal 表示 ctx 里没有身份：路由是 Public 的，或者 ctx 不是来自请求。
var ErrNoPrincipal = errors.New("auth: ctx 里没有当前身份")

// WithPrincipal 把身份放进 ctx。只有认证中间件应该调用它。
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromCtx 返回 ctx 里的身份。
func FromCtx(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustFromCtx 返回 ctx 里的身份，没有时 panic。用于 AuthOnly / Require 路由的 handler 和 service。
func MustFromCtx(ctx context.Context) Principal {
	p, ok := FromCtx(ctx)
	if !ok {
		panic(ErrNoPrincipal)
	}
	return p
}

// 会话吊销原因。
const (
	RevokeLogout        = "logout"
	RevokePwdChange     = "pwd_change"
	RevokeDisabled      = "disabled"
	RevokeReuseDetected = "reuse_detected"
	RevokeAdmin         = "admin"
	RevokeUnlockFailed  = "unlock_failed" // 锁屏后连续输错密码（D-027）
)

// Service 是框架暴露给模块的会话管理能力，通过 app.Deps.Auth 注入。
type Service interface {
	// RevokeUserSessions 吊销某用户的全部会话（停用账号、管理员重置密码时调用）。
	RevokeUserSessions(ctx context.Context, portal string, userID uint64, reason string) error
	// RevokeOtherSessions 吊销某用户除 keepSID 外的全部会话（个人中心"下线其他设备"），返回吊销的会话数。
	RevokeOtherSessions(ctx context.Context, portal string, userID uint64, keepSID, reason string) (int64, error)
	// RevokeSession 吊销某端的一个会话。sid 不属于该端时返回 httpx.ErrNotFound：会话 ID 是全局随机的，
	// 但一个端的管理员只能下线自己端的会话（规范 §5.4）。
	RevokeSession(ctx context.Context, portal string, sid string, reason string) error
	// SessionOwner 返回某端一个会话所属的用户 ID；sid 不属于该端时返回 httpx.ErrNotFound。
	// 让会话下线前用它判断是不是超管的会话（D-035）。
	SessionOwner(ctx context.Context, portalCode, sid string) (uint64, error)
	// ForgetAccount 让账号状态缓存立即失效（改了 status、must_change_pwd 之后调用）。
	ForgetAccount(portal string, userID uint64)
	// HashPassword 用该端的参数生成密码哈希（创建账号、重置密码时用）。算哈希慢：先做完便宜的检查再调、在拿锁之前调。
	// 和登录核对密码占同一个并发上限（D-068），满了返回 httpx.ErrTooManyRequests（429），不排队。
	HashPassword(portal string, plain string) (string, error)
	// ValidatePassword 按密码策略校验；不通过时返回带字段明细的 httpx 错误。
	ValidatePassword(portal string, plain, username string) error
	// GeneratePassword 生成一个符合策略的随机密码。
	GeneratePassword() (string, error)
	// ListSessions 分页列出某端的有效会话（userID 为 0 表示全部用户）。
	ListSessions(ctx context.Context, portal string, userID uint64, page, pageSize int) ([]SessionInfo, int64, error)
	// ListSessionsIn 分页列出某端这些用户的有效会话（按部门的数据权限用，D-039）。users 是只选用户 ID 一列的子查询，
	// 过滤在数据库里做，不把范围内的全部 ID 先取出来（D-055）；和调用方的其他读取用同一个 ctx（同一个快照）。
	ListSessionsIn(ctx context.Context, portal string, users *gorm.DB, page, pageSize int) ([]SessionInfo, int64, error)
	// ListLoginLogs 分页查询登录日志，按时间倒序。
	ListLoginLogs(ctx context.Context, f LoginLogFilter, page, pageSize int) ([]LoginLogInfo, int64, error)
	// CountActiveSessions 统计某端当前有效的会话数（首页概览用，D-027）。
	CountActiveSessions(ctx context.Context, portal string) (int64, error)
	// LoginStats 统计某端从 since 起按天的登录成功、失败次数和失败原因分布；tzOffsetMinutes 是东区为正的时区偏移。
	LoginStats(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) (LoginStats, error)
	// SecurityStats 统计某端从 since 起按小时的登录情况、失败最多的来源 IP，以及当前锁屏中的会话数（D-030 监控中心）。
	SecurityStats(ctx context.Context, portal string, since time.Time) (SecurityStats, error)
	// Policies 返回某端生效的登录防护和密码策略：代码声明叠加配置文件、通过底线校验之后的结果，
	// 与登录、改密时实际使用的是同一份（D-024）。只读，安全设置页用（D-034）。
	Policies(portalCode string) (portal.LoginPolicy, portal.PasswordPolicy, error)
}

// LoginStats 是登录统计。
type LoginStats struct {
	Days    []DailyLogin  `json:"days"`
	Reasons []ReasonCount `json:"reasons"`
	// Hours 是成功登录按本地钟点（下标 0–23）的分布（D-030）。
	Hours []int64 `json:"hours"`
}

// SecurityStats 是安全监控用的统计（D-030）。
type SecurityStats struct {
	Hours          []HourlyLogin `json:"hours"`     // 按 UTC 整点小时，没有登录的小时不出现
	FailedIPs      []IPCount     `json:"failedIps"` // 失败次数最多的来源 IP
	LockedSessions int64         `json:"lockedSessions"`
}

// HourlyLogin 是某个整点小时的登录成功、失败次数。
type HourlyLogin struct {
	Hour    time.Time `json:"hour"`
	Success int64     `json:"success"`
	Failed  int64     `json:"failed"`
}

// IPCount 是某个来源 IP 的次数。
type IPCount struct {
	IP    string `json:"ip"`
	Count int64  `json:"count"`
}

// DailyLogin 是某一天的登录次数（Day 形如 2026-09-28，没有登录的日子不出现）。
type DailyLogin struct {
	Day     string `json:"day"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// ReasonCount 是一种登录失败原因的次数。
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

// LoginLogFilter 是登录日志的查询条件；零值字段不参与过滤。
type LoginLogFilter struct {
	Portal    string
	OrgID     uint64 // 非 0 时只看这个主体的（主体端查本主体的日志，D-061）
	UserID    uint64
	Username  string // 前缀匹配
	IP        string
	SessionID string // 会话 ID（D-032）
	Success   *bool
	From, To  time.Time
}

// LoginLogInfo 是登录日志的一行（ga_login_log）。
type LoginLogInfo struct {
	ID        uint64    `json:"id"`
	Portal    string    `json:"portal"`
	OrgID     uint64    `json:"orgId"`   // 主体端：编号对应的主体（D-061）
	OrgCode   string    `json:"orgCode"` // 主体端：登录时输入的编号
	Username  string    `json:"username"`
	UserID    uint64    `json:"userId"`
	SessionID string    `json:"sessionId"` // 登录成功时建立的会话（D-032）
	Success   bool      `json:"success"`
	Reason    string    `json:"reason"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	RequestID string    `json:"requestId"`
	CreatedAt time.Time `json:"createdAt"`
}

// SessionInfo 是会话的管理视图。
type SessionInfo struct {
	SID        string    `json:"sid"`
	Portal     string    `json:"portal"`
	OrgID      uint64    `json:"orgId"` // 主体端：会话所属的主体（D-061）
	UserID     uint64    `json:"userId"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}
