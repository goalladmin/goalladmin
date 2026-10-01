package auditimpl

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/secmark"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

// ---- 安全事件（D-032 第 4 条）----

// securityRow 对应 ga_security_event 的一行。
type securityRow struct {
	ID          uint64    `gorm:"column:id;primaryKey"`
	DedupKey    string    `gorm:"column:dedup_key"`
	WindowStart time.Time `gorm:"column:window_start"`
	Portal      string    `gorm:"column:portal"`
	Kind        string    `gorm:"column:kind"`
	Level       int       `gorm:"column:level"`
	UserID      uint64    `gorm:"column:user_id"`
	Username    string    `gorm:"column:username"`
	SessionID   string    `gorm:"column:session_id"`
	IP          string    `gorm:"column:ip"`
	UserAgent   string    `gorm:"column:user_agent"`
	Method      string    `gorm:"column:method"`
	Path        string    `gorm:"column:path"`
	Detail      string    `gorm:"column:detail"`
	RequestID   string    `gorm:"column:request_id"`
	Count       int64     `gorm:"column:count"`
	FirstAt     time.Time `gorm:"column:first_at"`
	LastAt      time.Time `gorm:"column:last_at"`
}

func (securityRow) TableName() string { return "ga_security_event" }

func (e securityRow) public() audit.SecurityEvent {
	return audit.SecurityEvent{
		ID: e.ID, Portal: e.Portal, Kind: e.Kind, Level: e.Level, UserID: e.UserID, Username: e.Username, SessionID: e.SessionID,
		IP: e.IP, UserAgent: e.UserAgent, Method: e.Method, Path: e.Path, Detail: e.Detail, RequestID: e.RequestID,
		Count: e.Count, FirstAt: e.FirstAt, LastAt: e.LastAt,
	}
}

// levelOf 是框架认识的事件类型的固定级别；不认识的返回 0。
func levelOf(kind string) int {
	switch kind {
	case secmark.TokenMismatch, secmark.RefreshReuse:
		return audit.LevelCritical
	case secmark.SessionRevoked:
		return audit.LevelInfo
	case secmark.Forbidden, secmark.CrossPortal, secmark.TokenInvalid, secmark.RefreshMismatch, secmark.BadOrigin,
		secmark.LoginLocked, secmark.LoginRateLimited, secmark.UnlockExhausted, secmark.PwdChangeThrottled, secmark.RefreshCookieDup, secmark.CLI:
		return audit.LevelWarning
	}
	return 0
}

var kindRe = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,31}$`)

// recordSecurity 记录请求上的安全标记。身份字段为空时用请求的当前身份补上。
func (r *Recorder) recordSecurity(c *gin.Context, m secmark.Mark) {
	ctx := c.Request.Context()
	ev := securityRow{
		Kind: m.Kind, Level: levelOf(m.Kind), UserID: m.UserID, Username: m.Username, SessionID: m.SessionID,
		IP: c.ClientIP(), UserAgent: c.Request.UserAgent(), Method: c.Request.Method, Path: c.FullPath(), Detail: m.Detail,
		Portal: portalOf(c.Request.URL.Path),
	}
	if ev.Path == "" {
		ev.Path = c.Request.URL.Path
	}
	if p, ok := auth.FromCtx(ctx); ok {
		ev.Portal = p.Portal
		if ev.UserID == 0 {
			ev.UserID, ev.SessionID = p.UserID, p.SessionID
		}
		if ev.Username == "" && ev.UserID == p.UserID {
			ev.Username = p.Username
		}
	}
	if ev.Level == 0 {
		ev.Level = audit.LevelWarning
	}
	r.addSecurity(ctx, ev)
}

// portalOf 从 /api/<端>/v1/... 里取端代号。
func portalOf(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/")
	if !ok {
		return ""
	}
	code, _, _ := strings.Cut(rest, "/")
	return code
}

// RecordSecurity 实现 audit.Service：模块和命令行自己记录的安全事件。
func (r *Recorder) RecordSecurity(ctx context.Context, e audit.NewSecurityEvent) error {
	if !kindRe.MatchString(e.Kind) {
		return fmt.Errorf("audit: 安全事件类型 %q 不合规（小写字母开头，只含小写字母、数字、下划线和点，最长 32）", e.Kind)
	}
	level := levelOf(e.Kind)
	if level == 0 {
		level = e.Level
		if level < audit.LevelInfo || level > audit.LevelCritical {
			level = audit.LevelWarning
		}
	}
	r.addSecurity(ctx, securityRow{
		Portal: e.Portal, Kind: e.Kind, Level: level, UserID: e.UserID, Username: e.Username, SessionID: e.SessionID,
		IP: e.IP, Detail: e.Detail,
	})
	return nil
}

// addSecurity 补齐时间、截断各列，写一行审计记录到日志输出，再交给 throttle 合并写库。
func (r *Recorder) addSecurity(ctx context.Context, ev securityRow) {
	now := r.now().UTC()
	ev.Portal, ev.Username, ev.SessionID = truncate(ev.Portal, 32), truncate(ev.Username, 64), truncate(ev.SessionID, 32)
	ev.IP, ev.UserAgent, ev.Method = truncate(ev.IP, 64), truncate(ev.UserAgent, 255), truncate(ev.Method, 10)
	ev.Path, ev.Detail = truncate(ev.Path, 255), truncate(ev.Detail, 255)
	ev.RequestID = truncate(httpx.RequestID(ctx), 64)
	ev.WindowStart = now.Truncate(time.Minute)
	ev.FirstAt, ev.LastAt = now, now
	// 登录类事件的账号是攻击者随便填的：不参与合并，否则换着账号刷就能造出无数个新来源（只保留第一次的账号）
	keyUser := ev.Username
	if strings.HasPrefix(ev.Kind, "login_") {
		keyUser = ""
	}
	ev.DedupKey = fingerprint(ev.Portal, ev.Kind, strconv.FormatUint(ev.UserID, 10), keyUser, ev.SessionID, ev.IP, ev.Method, ev.Path, ev.Detail)
	logx.Audit(ctx, "audit.security", "kind", ev.Kind, "level", ev.Level, "portal", ev.Portal, "user_id", ev.UserID, "username", ev.Username,
		"session_id", ev.SessionID, "ip", ev.IP, "method", ev.Method, "path", ev.Path, "detail", ev.Detail)
	if r.db == nil {
		return
	}
	// 严重事件不受新来源写入速度的限制：攻击者刷别的事件也挤不掉它们
	r.security.Add(ctx, ev.DedupKey+"|"+ev.WindowStart.Format(time.RFC3339), ev, ev.Level >= audit.LevelCritical)
}

// upsertSecurity 插入这一分钟的第一行，或给已有的行累加次数。
func (r *Recorder) upsertSecurity(ctx context.Context, e securityRow, n int64) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO ga_security_event
		(dedup_key, window_start, portal, kind, level, user_id, username, session_id, ip, user_agent, method, path, detail, request_id, count, first_at, last_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE count = count + VALUES(count), last_at = GREATEST(last_at, VALUES(last_at))`,
		e.DedupKey, e.WindowStart, e.Portal, e.Kind, e.Level, e.UserID, e.Username, e.SessionID, e.IP, e.UserAgent, e.Method, e.Path,
		e.Detail, e.RequestID, n, e.FirstAt, e.LastAt).Error
}

// ListSecurityEvents 实现 audit.Service。
func (r *Recorder) ListSecurityEvents(ctx context.Context, f audit.SecurityFilter, page, pageSize int) ([]audit.SecurityEvent, int64, error) {
	if r.db == nil {
		return nil, 0, errors.New("audit: no database")
	}
	q := r.db.WithContext(ctx).Model(&securityRow{})
	switch {
	case f.Portal != "" && f.IncludeGlobal:
		q = q.Where("portal IN (?, '')", f.Portal)
	case f.Portal != "":
		q = q.Where("portal = ?", f.Portal)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.MinLevel > 0 {
		q = q.Where("level >= ?", f.MinLevel)
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
	if !f.From.IsZero() {
		q = q.Where("first_at >= ?", f.From.UTC())
	}
	if !f.To.IsZero() {
		q = q.Where("first_at < ?", f.To.UTC())
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []securityRow
	if err := q.Order("first_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]audit.SecurityEvent, 0, len(rows))
	for _, e := range rows {
		out = append(out, e.public())
	}
	return out, total, nil
}
