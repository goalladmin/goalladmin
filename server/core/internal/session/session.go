// Package session 管理登录会话（ga_session）和登录日志（ga_login_log）。
//
// 会话模型见规范 §5.1、§5.4：访问令牌短时，刷新凭证不透明且每次刷新轮换；
// 旧凭证在宽限期内重放视为并发刷新（409），超过宽限期视为凭证泄露（吊销整个会话）。
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/internal/textx"
)

// Session 对应 ga_session 的一行。
type Session struct {
	ID              uint64     `gorm:"column:id;primaryKey"`
	SID             string     `gorm:"column:sid"`
	Portal          string     `gorm:"column:portal"`
	UserID          uint64     `gorm:"column:user_id"`
	RefreshHash     string     `gorm:"column:refresh_hash"`
	PrevRefreshHash *string    `gorm:"column:prev_refresh_hash"`
	RotatedAt       time.Time  `gorm:"column:rotated_at"`
	ExpiresAt       time.Time  `gorm:"column:expires_at"`
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	RevokeReason    string     `gorm:"column:revoke_reason"`
	LockedAt        *time.Time `gorm:"column:locked_at"`       // 锁屏时间（D-027）；nil 表示没有锁定
	UnlockFailures  uint       `gorm:"column:unlock_failures"` // 锁定后连续解锁失败次数
	IP              string     `gorm:"column:ip"`
	UserAgent       string     `gorm:"column:user_agent"`
	LastSeenAt      time.Time  `gorm:"column:last_seen_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
}

// TableName 固定表名。
func (Session) TableName() string { return "ga_session" }

// Active 报告会话在 now 时刻是否可用。
func (s *Session) Active(now time.Time) bool {
	return s != nil && s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// LoginLog 对应 ga_login_log 的一行。
type LoginLog struct {
	ID        uint64    `gorm:"column:id;primaryKey"`
	Portal    string    `gorm:"column:portal"`
	Username  string    `gorm:"column:username"`
	UserID    uint64    `gorm:"column:user_id"`
	SessionID string    `gorm:"column:session_id"` // 登录成功时建立的会话（D-032）
	Success   bool      `gorm:"column:success"`
	Reason    string    `gorm:"column:reason"`
	IP        string    `gorm:"column:ip"`
	UserAgent string    `gorm:"column:user_agent"`
	RequestID string    `gorm:"column:request_id"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// TableName 固定表名。
func (LoginLog) TableName() string { return "ga_login_log" }

// Config 是会话参数。
type Config struct {
	RefreshTTL  time.Duration // 刷新凭证有效期（滑动）
	MaxLifetime time.Duration // 会话自创建起的最长寿命，默认 30 天
	ReuseGrace  time.Duration // 旧凭证的并发宽限期，默认 10 秒
	Now         func() time.Time
}

// Manager 读写会话。所有方法通过 db.From(ctx) 拿句柄。
type Manager struct {
	cfg Config
}

// NewManager 创建会话管理器。
func NewManager(cfg Config) *Manager {
	if cfg.MaxLifetime <= 0 {
		cfg.MaxLifetime = 30 * 24 * time.Hour
	}
	if cfg.ReuseGrace <= 0 {
		cfg.ReuseGrace = 10 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Manager{cfg: cfg}
}

// RefreshToken 是下发给客户端的不透明刷新凭证：<sid>.<secret>。
type RefreshToken struct {
	SID    string
	Secret string
}

// String 返回 Cookie 里的形式。
func (t RefreshToken) String() string { return t.SID + "." + t.Secret }

// ParseRefreshToken 解析 Cookie 值。
func ParseRefreshToken(s string) (RefreshToken, bool) {
	sid, secret, ok := strings.Cut(s, ".")
	if !ok || len(sid) != 32 || len(secret) != 64 || !isHex(sid) || !isHex(secret) {
		return RefreshToken{}, false
	}
	return RefreshToken{SID: sid, Secret: secret}, true
}

// Create 新建会话，返回会话和刷新凭证。
func (m *Manager) Create(ctx context.Context, portal string, userID uint64, ip, ua string) (*Session, RefreshToken, error) {
	now := m.cfg.Now().UTC()
	sid, err := randomHex(16)
	if err != nil {
		return nil, RefreshToken{}, err
	}
	secret, err := randomHex(32)
	if err != nil {
		return nil, RefreshToken{}, err
	}
	s := &Session{
		SID: sid, Portal: portal, UserID: userID,
		RefreshHash: hashSecret(secret),
		RotatedAt:   now, ExpiresAt: m.expiry(now, now),
		IP: truncate(ip, 64), UserAgent: truncate(ua, 255),
		LastSeenAt: now, CreatedAt: now,
	}
	if err := db.From(ctx).Create(s).Error; err != nil {
		return nil, RefreshToken{}, fmt.Errorf("session: create: %w", err)
	}
	return s, RefreshToken{SID: sid, Secret: secret}, nil
}

// Get 按 sid 读会话；不存在返回 nil, nil。
func (m *Manager) Get(ctx context.Context, sid string) (*Session, error) {
	var s Session
	err := db.From(ctx).Where("sid = ?", sid).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session: get: %w", err)
	}
	return &s, nil
}

// 轮换结果。
var (
	ErrNotFound      = errors.New("session: not found")
	ErrInactive      = errors.New("session: revoked or expired")
	ErrRetry         = errors.New("session: concurrent refresh, retry")
	ErrReuseDetected = errors.New("session: refresh token reuse detected")
	// ErrMismatch：凭证和这个会话当前、上一个值都对不上。会话不吊销（D-049）。
	ErrMismatch = errors.New("session: refresh token does not match this session")
)

// Rotate 用刷新凭证换新凭证。对会话行加锁，保证并发刷新串行化。
//
// 只有"上一个凭证在宽限期之外重放"才算凭证泄露：拿它的人确实持有过一个有效凭证。这时吊销会话并返回 ErrReuseDetected；
// 吊销本身必须提交，所以它不能作为事务错误返回。
//
// 和当前、上一个都对不上的凭证只拒绝、不吊销（ErrMismatch，D-049）：会话号出现在会话列表、登录日志、操作日志里，
// 只知道会话号、编不出 secret 的人也能发这样的请求，它证明不了合法持有人的凭证泄露；要是据此吊销，
// 能看到会话号的人就都能让任何会话下线，包括超管的。
//
// portal 是发起刷新的端：会话不属于这个端时按不存在处理（不轮换、不吊销），
// 防止拿着甲端的刷新凭证到乙端换出乙端的身份（两端的用户 ID 可能相同）。
func (m *Manager) Rotate(ctx context.Context, portal string, tok RefreshToken, ip, ua string) (*Session, RefreshToken, error) {
	var (
		out    *Session
		newTok RefreshToken
		result error
	)
	err := db.Tx(ctx, func(ctx context.Context) error {
		now := m.cfg.Now().UTC()
		var s Session
		err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("sid = ?", tok.SID).First(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result = ErrNotFound
			return nil
		}
		if err != nil {
			return fmt.Errorf("session: lock: %w", err)
		}
		if s.Portal != portal {
			result = ErrNotFound
			return nil
		}
		if !s.Active(now) {
			result = ErrInactive
			return nil
		}
		h := hashSecret(tok.Secret)
		switch {
		case constantEq(h, s.RefreshHash):
			secret, err := randomHex(32)
			if err != nil {
				return err
			}
			prev := s.RefreshHash
			s.PrevRefreshHash = &prev
			s.RefreshHash = hashSecret(secret)
			s.RotatedAt = now
			s.ExpiresAt = m.expiry(now, s.CreatedAt)
			s.LastSeenAt = now
			s.IP = truncate(ip, 64)
			s.UserAgent = truncate(ua, 255)
			if err := db.From(ctx).Model(&s).Select("prev_refresh_hash", "refresh_hash", "rotated_at", "expires_at", "last_seen_at", "ip", "user_agent").Updates(&s).Error; err != nil {
				return fmt.Errorf("session: rotate: %w", err)
			}
			out, newTok = &s, RefreshToken{SID: s.SID, Secret: secret}
			return nil
		case s.PrevRefreshHash != nil && constantEq(h, *s.PrevRefreshHash):
			if now.Sub(s.RotatedAt) <= m.cfg.ReuseGrace {
				// 宽限期内拿着上一个凭证来：多标签页并发刷新，让它带着新 Cookie 重试
				result = ErrRetry
				return nil
			}
			// 上一个凭证超过宽限期重放：视为泄露，吊销整个会话（随事务提交）
			if err := m.revokeLocked(ctx, &s, now, "reuse_detected"); err != nil {
				return err
			}
			result = ErrReuseDetected
			return nil
		default:
			// 和当前、上一个都对不上：只拒绝，不吊销（见函数注释）
			result = ErrMismatch
			return nil
		}
	})
	if err != nil {
		return nil, RefreshToken{}, err
	}
	return out, newTok, result
}

// Touch 更新最后活动时间（限频由调用方决定）。
func (m *Manager) Touch(ctx context.Context, sid string) error {
	now := m.cfg.Now().UTC()
	return db.From(ctx).Model(&Session{}).Where("sid = ?", sid).Update("last_seen_at", now).Error
}

// Revoke 吊销某端的一个会话。更新条件同时带 portal 和 sid：一个端拿到别的端的 sid 也动不了它。
// (portal, sid) 不存在时返回 ErrNotFound；已经吊销过的会话再吊销一次不算错（幂等）。
func (m *Manager) Revoke(ctx context.Context, portal, sid, reason string) error {
	now := m.cfg.Now().UTC()
	res := db.From(ctx).Model(&Session{}).
		Where("portal = ? AND sid = ? AND revoked_at IS NULL", portal, sid).
		Updates(map[string]any{"revoked_at": now, "revoke_reason": reason})
	if res.Error != nil {
		return fmt.Errorf("session: revoke: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		return nil
	}
	var n int64
	if err := db.From(ctx).Model(&Session{}).Where("portal = ? AND sid = ?", portal, sid).Count(&n).Error; err != nil {
		return fmt.Errorf("session: revoke: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LockActive 在调用方的事务里给某端的一个会话加排他锁，并确认它仍然有效（D-045）。吊销会话（改密、重置密码、
// 停用账号、管理员下线）写的也是这一行，所以两边会排队：拿到锁时看到的是对方已经提交的结果。
func (m *Manager) LockActive(ctx context.Context, portal, sid string) error {
	var s Session
	err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("revoked_at", "expires_at").Where("portal = ? AND sid = ?", portal, sid).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !s.Active(m.cfg.Now().UTC()) {
		return ErrInactive
	}
	return nil
}

// Lock 锁定某端的一个有效会话（D-027）。已经锁定的不重复计时。
func (m *Manager) Lock(ctx context.Context, portal, sid string) error {
	now := m.cfg.Now().UTC()
	return db.From(ctx).Model(&Session{}).
		Where("portal = ? AND sid = ? AND revoked_at IS NULL AND locked_at IS NULL", portal, sid).
		Updates(map[string]any{"locked_at": now, "unlock_failures": 0}).Error
}

// UnlockAttempt 是 ReserveUnlockAttempt 的结果。
type UnlockAttempt int

const (
	// AttemptReserved 占用了一次解锁机会，可以去校验密码。
	AttemptReserved UnlockAttempt = iota
	// AttemptNotLocked 会话没有锁定（例如并发的另一次尝试已经解锁）。
	AttemptNotLocked
	// AttemptExhausted 机会已用完，或会话不存在、已吊销。
	AttemptExhausted
)

// ReserveUnlockAttempt 在校验密码之前先占用一次解锁机会（D-027），返回占用后的累计次数。
// 行锁保证并发的尝试逐个计数：机会用完后，不论同时发来多少请求，都不会再有一个去校验密码。
func (m *Manager) ReserveUnlockAttempt(ctx context.Context, portal, sid string, limit uint) (UnlockAttempt, uint, error) {
	res, n := AttemptExhausted, uint(0)
	err := db.Tx(ctx, func(ctx context.Context) error {
		var s Session
		err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("revoked_at", "locked_at", "unlock_failures").
			Where("portal = ? AND sid = ?", portal, sid).First(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case s.RevokedAt != nil || s.UnlockFailures >= limit:
			return nil
		case s.LockedAt == nil:
			res = AttemptNotLocked
			return nil
		}
		if err := db.From(ctx).Model(&Session{}).Where("portal = ? AND sid = ?", portal, sid).
			Update("unlock_failures", gorm.Expr("unlock_failures + 1")).Error; err != nil {
			return err
		}
		res, n = AttemptReserved, s.UnlockFailures+1
		return nil
	})
	if err != nil {
		return AttemptExhausted, 0, fmt.Errorf("session: reserve unlock attempt: %w", err)
	}
	return res, n, nil
}

// Unlock 解除锁定并清零失败次数。会话不存在或已吊销时返回 ErrNotFound：
// 并发的错误尝试可能刚刚把它吊销，这时即使密码正确也不能解锁。
func (m *Manager) Unlock(ctx context.Context, portal, sid string) error {
	err := db.Tx(ctx, func(ctx context.Context) error {
		var s Session
		err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Select("revoked_at").
			Where("portal = ? AND sid = ?", portal, sid).First(&s).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if s.RevokedAt != nil {
			return ErrNotFound
		}
		return db.From(ctx).Model(&Session{}).Where("portal = ? AND sid = ?", portal, sid).
			Updates(map[string]any{"locked_at": nil, "unlock_failures": 0}).Error
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("session: unlock: %w", err)
	}
	return err
}

// RevokeUser 吊销某用户的全部会话；exceptSID 非空时保留该会话。返回吊销的会话数（已过期但未吊销的也算）。
func (m *Manager) RevokeUser(ctx context.Context, portal string, userID uint64, reason, exceptSID string) (int64, error) {
	now := m.cfg.Now().UTC()
	q := db.From(ctx).Model(&Session{}).Where("portal = ? AND user_id = ? AND revoked_at IS NULL", portal, userID)
	if exceptSID != "" {
		q = q.Where("sid <> ?", exceptSID)
	}
	res := q.Updates(map[string]any{"revoked_at": now, "revoke_reason": reason})
	return res.RowsAffected, res.Error
}

// ListActive 列出某用户的有效会话。
func (m *Manager) ListActive(ctx context.Context, portal string, userID uint64) ([]Session, error) {
	now := m.cfg.Now().UTC()
	var out []Session
	err := db.From(ctx).Where("portal = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", portal, userID, now).
		Order("last_seen_at DESC").Find(&out).Error
	return out, err
}

// ListActivePage 分页列出某端的有效会话；userID 为 0 表示全部用户。
func (m *Manager) ListActivePage(ctx context.Context, portal string, userID uint64, page, pageSize int) ([]Session, int64, error) {
	now := m.cfg.Now().UTC()
	q := db.From(ctx).Model(&Session{}).Where("portal = ? AND revoked_at IS NULL AND expires_at > ?", portal, now)
	if userID != 0 {
		q = q.Where("user_id = ?", userID)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []Session
	err := q.Order("last_seen_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

// ListActivePageIn 分页列出某端这些用户的有效会话；users 是只选用户 ID 的子查询（D-055）。
func (m *Manager) ListActivePageIn(ctx context.Context, portal string, users *gorm.DB, page, pageSize int) ([]Session, int64, error) {
	now := m.cfg.Now().UTC()
	q := db.From(ctx).Model(&Session{}).Where("portal = ? AND revoked_at IS NULL AND expires_at > ? AND user_id IN (?)", portal, now, users)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []Session
	err := q.Order("last_seen_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

// Cleanup 删除过期或吊销超过 keep 的会话。
func (m *Manager) Cleanup(ctx context.Context, keep time.Duration) (int64, error) {
	cut := m.cfg.Now().UTC().Add(-keep)
	res := db.From(ctx).Where("(revoked_at IS NOT NULL AND revoked_at < ?) OR expires_at < ?", cut, cut).Delete(&Session{})
	return res.RowsAffected, res.Error
}

// CountActive 统计某端当前有效（未吊销、未过期）的会话数。
func (m *Manager) CountActive(ctx context.Context, portal string) (int64, error) {
	var n int64
	err := db.From(ctx).Model(&Session{}).
		Where("portal = ? AND revoked_at IS NULL AND expires_at > ?", portal, m.cfg.Now().UTC()).Count(&n).Error
	return n, err
}

// DailyLogin 是某一天的登录次数。
type DailyLogin struct {
	Day     string `gorm:"column:day"`
	Success int64  `gorm:"column:ok"`
	Failed  int64  `gorm:"column:bad"`
}

// ReasonCount 是一种失败原因的次数。
type ReasonCount struct {
	Reason string `gorm:"column:reason"`
	Count  int64  `gorm:"column:n"`
}

// DailyLogins 按天统计某端从 since 起的登录成功、失败次数；日期按 tzOffsetMinutes（东区为正）换算成本地日期。
func (m *Manager) DailyLogins(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) ([]DailyLogin, error) {
	var out []DailyLogin
	err := db.From(ctx).Raw(`SELECT DATE_FORMAT(DATE_ADD(created_at, INTERVAL ? MINUTE), '%Y-%m-%d') AS day,
		SUM(success = 1) AS ok, SUM(success = 0) AS bad
		FROM ga_login_log WHERE portal = ? AND created_at >= ? GROUP BY day ORDER BY day`,
		tzOffsetMinutes, portal, since.UTC()).Scan(&out).Error
	return out, err
}

// FailureReasons 统计某端从 since 起登录失败的原因分布，按次数倒序。
func (m *Manager) FailureReasons(ctx context.Context, portal string, since time.Time) ([]ReasonCount, error) {
	var out []ReasonCount
	err := db.From(ctx).Raw(`SELECT reason, COUNT(*) AS n FROM ga_login_log
		WHERE portal = ? AND success = 0 AND created_at >= ? GROUP BY reason ORDER BY n DESC, reason`,
		portal, since.UTC()).Scan(&out).Error
	return out, err
}

// HourCount 是某个小时的次数。
type HourCount struct {
	Hour  int   `gorm:"column:h"`
	Count int64 `gorm:"column:n"`
}

// HourOfDayLogins 统计某端从 since 起成功登录按"本地钟点"（0–23）的分布（D-030 数据中心）。
func (m *Manager) HourOfDayLogins(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) ([]HourCount, error) {
	var out []HourCount
	err := db.From(ctx).Raw(`SELECT HOUR(DATE_ADD(created_at, INTERVAL ? MINUTE)) AS h, COUNT(*) AS n
		FROM ga_login_log WHERE portal = ? AND success = 1 AND created_at >= ? GROUP BY h ORDER BY h`,
		tzOffsetMinutes, portal, since.UTC()).Scan(&out).Error
	return out, err
}

// HourlyLogin 是某个整点小时（UTC，形如 2026-09-28 13:00）的登录成功、失败次数。
type HourlyLogin struct {
	Hour    string `gorm:"column:hr"`
	Success int64  `gorm:"column:ok"`
	Failed  int64  `gorm:"column:bad"`
}

// HourlyLogins 按 UTC 整点小时统计某端从 since 起的登录成功、失败次数（D-030 监控中心）。没有登录的小时不出现。
func (m *Manager) HourlyLogins(ctx context.Context, portal string, since time.Time) ([]HourlyLogin, error) {
	var out []HourlyLogin
	err := db.From(ctx).Raw(`SELECT DATE_FORMAT(created_at, '%Y-%m-%d %H:00') AS hr,
		SUM(success = 1) AS ok, SUM(success = 0) AS bad
		FROM ga_login_log WHERE portal = ? AND created_at >= ? GROUP BY hr ORDER BY hr`,
		portal, since.UTC()).Scan(&out).Error
	return out, err
}

// IPCount 是某个来源 IP 的次数。
type IPCount struct {
	IP    string `gorm:"column:ip"`
	Count int64  `gorm:"column:n"`
}

// TopFailedIPs 统计某端从 since 起登录失败次数最多的 n 个来源 IP。
func (m *Manager) TopFailedIPs(ctx context.Context, portal string, since time.Time, n int) ([]IPCount, error) {
	var out []IPCount
	err := db.From(ctx).Raw(`SELECT ip, COUNT(*) AS n FROM ga_login_log
		WHERE portal = ? AND success = 0 AND created_at >= ? GROUP BY ip ORDER BY n DESC, ip LIMIT ?`,
		portal, since.UTC(), n).Scan(&out).Error
	return out, err
}

// CountLocked 统计某端当前有效且处于锁屏状态的会话数。
func (m *Manager) CountLocked(ctx context.Context, portal string) (int64, error) {
	var n int64
	err := db.From(ctx).Model(&Session{}).
		Where("portal = ? AND revoked_at IS NULL AND expires_at > ? AND locked_at IS NOT NULL", portal, m.cfg.Now().UTC()).Count(&n).Error
	return n, err
}

// WriteLoginLog 写一条登录日志。
func (m *Manager) WriteLoginLog(ctx context.Context, l LoginLog) error {
	l.CreatedAt = m.cfg.Now().UTC()
	l.Username = truncate(l.Username, 64)
	l.Reason = truncate(l.Reason, 64)
	l.IP = truncate(l.IP, 64)
	l.UserAgent = truncate(l.UserAgent, 255)
	l.RequestID = truncate(l.RequestID, 64)
	l.SessionID = truncate(l.SessionID, 32)
	return db.From(ctx).Create(&l).Error
}

// LoginLogFilter 是登录日志查询条件。
type LoginLogFilter struct {
	Portal    string
	UserID    uint64
	Username  string
	IP        string
	SessionID string
	Success   *bool
	From, To  time.Time
}

// ListLoginLogs 分页查询登录日志，按时间倒序。
func (m *Manager) ListLoginLogs(ctx context.Context, f LoginLogFilter, page, pageSize int) ([]LoginLog, int64, error) {
	q := db.From(ctx).Model(&LoginLog{})
	if f.Portal != "" {
		q = q.Where("portal = ?", f.Portal)
	}
	if f.UserID != 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.Username != "" {
		q = q.Where("username LIKE ?", escapeLike(f.Username)+"%")
	}
	if f.IP != "" {
		q = q.Where("ip = ?", f.IP)
	}
	if f.SessionID != "" {
		q = q.Where("session_id = ?", f.SessionID)
	}
	if f.Success != nil {
		q = q.Where("success = ?", *f.Success)
	}
	if !f.From.IsZero() {
		q = q.Where("created_at >= ?", f.From.UTC())
	}
	if !f.To.IsZero() {
		q = q.Where("created_at < ?", f.To.UTC())
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var out []LoginLog
	err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func (m *Manager) revokeLocked(ctx context.Context, s *Session, now time.Time, reason string) error {
	return db.From(ctx).Model(s).Updates(map[string]any{"revoked_at": now, "revoke_reason": reason}).Error
}

func (m *Manager) expiry(now, createdAt time.Time) time.Time {
	e := now.Add(m.cfg.RefreshTTL)
	if maxE := createdAt.Add(m.cfg.MaxLifetime); e.After(maxE) {
		return maxE
	}
	return e
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func constantEq(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// truncate 截到最多 n 字节，保证是合法的 UTF-8（D-058）。
func truncate(s string, n int) string { return textx.Clip(s, n) }
