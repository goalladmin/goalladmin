// Package oplogimpl 是操作日志的实现：路由级中间件、脱敏、落库、查询。
//
// 公开入口在 core/oplog（Record 路由选项）；core/app 直接用本包给 /auth/password 这类框架路由挂日志。
// 只记"通过了守卫的请求"：未登录和无权限的请求在守卫处就被拒绝，不会到达这里。
package oplogimpl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/textx"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

// 各列的长度上限（与 ga_operation_log 的定义一致）。
const (
	MaxBody      = 4096
	maxQuery     = 1024
	maxPath      = 255
	maxAction    = 64
	maxUsername  = 64
	maxIP        = 64
	maxUserAgent = 255
	maxError     = 512
	maxSession   = 32
)

// Masked 是脱敏后的占位值。
const Masked = "***"

// sensitiveKey 命中的键，其值一律替换为 Masked（规范 §10）。
var sensitiveKey = regexp.MustCompile(`(?i)password|passwd|secret|token|key|credential|sign|authorization`)

// IsSensitiveKey 报告一个键名是否属于敏感字段。
func IsSensitiveKey(k string) bool { return sensitiveKey.MatchString(k) }

// Entry 对应 ga_operation_log 的一行。只增不改。
type Entry struct {
	ID         uint64    `gorm:"column:id;primaryKey" json:"id"`
	RequestID  string    `gorm:"column:request_id" json:"requestId"`
	Portal     string    `gorm:"column:portal" json:"portal"`
	UserID     uint64    `gorm:"column:user_id" json:"userId"`
	Username   string    `gorm:"column:username" json:"username"`
	SessionID  string    `gorm:"column:session_id" json:"sessionId"` // 会话 ID（D-032）
	Action     string    `gorm:"column:action" json:"action"`
	Method     string    `gorm:"column:method" json:"method"`
	Path       string    `gorm:"column:path" json:"path"`
	Query      string    `gorm:"column:query" json:"query"`
	Body       string    `gorm:"column:body" json:"body"`
	HTTPStatus int       `gorm:"column:http_status" json:"httpStatus"`
	Code       int       `gorm:"column:code" json:"code"`
	LatencyMs  int       `gorm:"column:latency_ms" json:"latencyMs"`
	IP         string    `gorm:"column:ip" json:"ip"`
	UserAgent  string    `gorm:"column:user_agent" json:"userAgent"`
	Error      string    `gorm:"column:error" json:"error"`
	CreatedAt  time.Time `gorm:"column:created_at" json:"createdAt"`
}

// writeTimeout 是请求结束后写一条操作日志的上限（D-037）。
const writeTimeout = 3 * time.Second

// TableName 固定表名。
func (Entry) TableName() string { return "ga_operation_log" }

// Middleware 返回记录一条操作日志的中间件。它在 handler 之前读走请求体（再原样放回），
// handler 结束后把结果写库；写库失败只记错误日志，不影响已经发出的响应。
func Middleware(portal, action string) gin.HandlerFunc {
	action = truncate(action, maxAction)
	return func(c *gin.Context) {
		start := time.Now()
		// 不管走哪个分支，入库前统一截到 MaxBody、整理成合法 UTF-8（D-059）：以后加分支也不会漏
		body := truncate(captureBody(c), MaxBody)
		c.Next()

		ctx := c.Request.Context()
		e := Entry{
			RequestID:  httpx.RequestID(ctx),
			Portal:     portal,
			Action:     action,
			Method:     c.Request.Method,
			Path:       truncate(c.Request.URL.Path, maxPath),
			Query:      truncate(MaskQuery(c.Request.URL.RawQuery), maxQuery),
			Body:       body,
			HTTPStatus: c.Writer.Status(),
			LatencyMs:  int(time.Since(start).Milliseconds()),
			IP:         truncate(c.ClientIP(), maxIP),
			UserAgent:  truncate(c.Request.UserAgent(), maxUserAgent),
			CreatedAt:  start.UTC(),
		}
		if p, ok := auth.FromCtx(ctx); ok {
			e.UserID, e.Username, e.SessionID = p.UserID, truncate(p.Username, maxUsername), truncate(p.SessionID, maxSession)
		}
		if v, ok := c.Get(httpx.KeyResponseCode); ok {
			e.Code, _ = v.(int)
		}
		if e.Code != 0 || e.HTTPStatus >= 400 {
			msg, _ := c.Get(httpx.KeyResponseMsg)
			e.Error = truncate(toString(msg), maxError)
		}
		// 同时写一行审计记录到日志输出，交给外部日志系统（D-032）；请求体和查询串不写
		logx.Audit(ctx, "audit.operation", "portal", e.Portal, "action", e.Action, "user_id", e.UserID, "username", e.Username,
			"session_id", e.SessionID, "method", e.Method, "path", e.Path, "http_status", e.HTTPStatus, "code", e.Code, "ip", e.IP)
		if !db.Has(ctx) {
			logx.From(ctx).WarnContext(ctx, "operation log dropped: no database in context", "action", action)
			return
		}
		// 客户端断开、请求超时（D-037）都会取消请求 ctx；日志仍要写，所以去掉取消信号但保留 ctx 里的值。
		// 另给它一个短时限：数据库不回话时不能拖住已经写好的响应。
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
		defer cancel()
		if err := db.From(wctx).Create(&e).Error; err != nil {
			logx.From(ctx).ErrorContext(ctx, "operation log write failed", "action", action, "err", err)
		}
	}
}

// captureBody 读走请求体、放回一份可重读的副本，并返回脱敏、截断后的文本。
// 只解析 JSON 和表单；其他类型（如文件上传）不存内容，只存类型和大小。
func captureBody(c *gin.Context) string {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return ""
	}
	raw, err := io.ReadAll(c.Request.Body)
	_ = c.Request.Body.Close()
	// 无论读成功与否，都把已读到的部分放回去；超限的错误由 BodyLimit 中间件在 handler 读取时统一处理
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		// http.MaxBytesReader 报错时需要让 handler 再读一次才能得到同样的错误；这里用一个总是报错的 reader
		c.Request.Body = io.NopCloser(errReader{err: err})
		return placeholder("unreadable", len(raw))
	}
	if len(raw) == 0 {
		return ""
	}
	ct, _, _ := mime.ParseMediaType(c.ContentType())
	switch {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		return truncate(MaskJSON(raw), MaxBody)
	case ct == "application/x-www-form-urlencoded":
		return truncate(MaskQuery(string(raw)), MaxBody)
	default:
		return placeholder(ct, len(raw))
	}
}

// maxKind 是占位符里媒体类型的最大长度：类型来自请求头 Content-Type，长度由请求方决定（D-059）
const maxKind = 64

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func placeholder(kind string, n int) string {
	if kind == "" {
		kind = "unknown"
	}
	kind = truncate(kind, maxKind)
	return "<" + kind + ", " + strconv.Itoa(n) + " bytes>"
}

// MaskJSON 把 JSON 文本里敏感键的值替换为 Masked，嵌套的对象和数组一并处理；
// 不是合法 JSON 时不存原文，只存占位符（原文可能含明文密码）。
func MaskJSON(raw []byte) string {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return placeholder("invalid json", len(raw))
	}
	out, err := json.Marshal(maskValue(v))
	if err != nil {
		return placeholder("invalid json", len(raw))
	}
	return string(out)
}

func maskValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if IsSensitiveKey(k) {
				t[k] = Masked
			} else {
				t[k] = maskValue(val)
			}
		}
		return t
	case []any:
		for i := range t {
			t[i] = maskValue(t[i])
		}
		return t
	default:
		return v
	}
}

// MaskQuery 对查询串或表单做同样的脱敏；键按字母序重排。无法解析的原样返回空串。
func MaskQuery(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return placeholder("invalid query", len(raw))
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range vals[k] {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(url.QueryEscape(k))
			b.WriteByte('=')
			if IsSensitiveKey(k) {
				b.WriteString(Masked)
			} else {
				b.WriteString(url.QueryEscape(v))
			}
		}
	}
	return b.String()
}

// Filter 是查询条件。零值字段不参与过滤。
type Filter struct {
	Portal    string
	UserID    uint64
	Username  string // 前缀匹配
	Action    string // 精确
	Path      string // 前缀匹配
	Method    string
	IP        string
	SessionID string
	Failed    *bool // true 只看失败（code != 0），false 只看成功
	From, To  time.Time
}

// List 分页查询操作日志，按时间倒序。
func List(ctx context.Context, f Filter, page, pageSize int) ([]Entry, int64, error) {
	q := db.From(ctx).Model(&Entry{})
	if f.Portal != "" {
		q = q.Where("portal = ?", f.Portal)
	}
	if f.UserID != 0 {
		q = q.Where("user_id = ?", f.UserID)
	}
	if f.Username != "" {
		q = q.Where("username LIKE ?", escapeLike(f.Username)+"%")
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.Path != "" {
		q = q.Where("path LIKE ?", escapeLike(f.Path)+"%")
	}
	if f.Method != "" {
		q = q.Where("method = ?", strings.ToUpper(f.Method))
	}
	if f.IP != "" {
		q = q.Where("ip = ?", f.IP)
	}
	if f.SessionID != "" {
		q = q.Where("session_id = ?", f.SessionID)
	}
	if f.Failed != nil {
		if *f.Failed {
			q = q.Where("code <> 0")
		} else {
			q = q.Where("code = 0")
		}
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
	var out []Entry
	err := q.Order("created_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&out).Error
	return out, total, err
}

// DailyCount 是某一天的操作次数。
type DailyCount struct {
	Day   string `gorm:"column:day" json:"day"`
	Count int64  `gorm:"column:n" json:"count"`
}

// ActionCount 是一种动作的次数。
type ActionCount struct {
	Action string `gorm:"column:action" json:"action"`
	Count  int64  `gorm:"column:n" json:"count"`
}

// DailyCounts 按天统计某端从 since 起的操作次数；日期按 tzOffsetMinutes（东区为正）换算成本地日期。
func DailyCounts(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) ([]DailyCount, error) {
	var out []DailyCount
	err := db.From(ctx).Raw(`SELECT DATE_FORMAT(DATE_ADD(created_at, INTERVAL ? MINUTE), '%Y-%m-%d') AS day, COUNT(*) AS n
		FROM ga_operation_log WHERE portal = ? AND created_at >= ? GROUP BY day ORDER BY day`,
		tzOffsetMinutes, portal, since.UTC()).Scan(&out).Error
	return out, err
}

// TopActions 统计某端从 since 起次数最多的 n 种动作。
func TopActions(ctx context.Context, portal string, since time.Time, n int) ([]ActionCount, error) {
	var out []ActionCount
	err := db.From(ctx).Raw(`SELECT action, COUNT(*) AS n FROM ga_operation_log
		WHERE portal = ? AND created_at >= ? GROUP BY action ORDER BY n DESC, action LIMIT ?`,
		portal, since.UTC(), n).Scan(&out).Error
	return out, err
}

// HourCount 是某个本地钟点（0–23）的操作次数。
type HourCount struct {
	Hour  int   `gorm:"column:h" json:"hour"`
	Count int64 `gorm:"column:n" json:"count"`
}

// HourOfDayCounts 统计某端从 since 起按本地钟点的操作次数分布（D-030）。
func HourOfDayCounts(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) ([]HourCount, error) {
	var out []HourCount
	err := db.From(ctx).Raw(`SELECT HOUR(DATE_ADD(created_at, INTERVAL ? MINUTE)) AS h, COUNT(*) AS n
		FROM ga_operation_log WHERE portal = ? AND created_at >= ? GROUP BY h ORDER BY h`,
		tzOffsetMinutes, portal, since.UTC()).Scan(&out).Error
	return out, err
}

// UserCount 是某个操作人的操作次数。
type UserCount struct {
	UserID   uint64 `gorm:"column:user_id" json:"userId"`
	Username string `gorm:"column:username" json:"username"`
	Count    int64  `gorm:"column:n" json:"count"`
}

// TopUsers 统计某端从 since 起操作次数最多的 n 个人（D-030 活跃用户排名）。
func TopUsers(ctx context.Context, portal string, since time.Time, n int) ([]UserCount, error) {
	var out []UserCount
	err := db.From(ctx).Raw(`SELECT user_id, MAX(username) AS username, COUNT(*) AS n FROM ga_operation_log
		WHERE portal = ? AND created_at >= ? AND user_id > 0 GROUP BY user_id ORDER BY n DESC, user_id LIMIT ?`,
		portal, since.UTC(), n).Scan(&out).Error
	return out, err
}

// TopUsersIn 只在给定的用户里统计操作次数最多的 n 个人（按部门的数据权限用，D-039）；users 是只选用户 ID 的子查询（D-055）。
func TopUsersIn(ctx context.Context, portal string, users *gorm.DB, since time.Time, n int) ([]UserCount, error) {
	var out []UserCount
	err := db.From(ctx).Raw(`SELECT user_id, MAX(username) AS username, COUNT(*) AS n FROM ga_operation_log
		WHERE portal = ? AND created_at >= ? AND user_id IN (?) GROUP BY user_id ORDER BY n DESC, user_id LIMIT ?`,
		portal, since.UTC(), users, n).Scan(&out).Error
	return out, err
}

// UserTopActions 统计某人从 since 起次数最多的 n 种动作（D-030 工作台）。
func UserTopActions(ctx context.Context, portal string, userID uint64, since time.Time, n int) ([]ActionCount, error) {
	var out []ActionCount
	err := db.From(ctx).Raw(`SELECT action, COUNT(*) AS n FROM ga_operation_log
		WHERE portal = ? AND user_id = ? AND created_at >= ? GROUP BY action ORDER BY n DESC, action LIMIT ?`,
		portal, userID, since.UTC(), n).Scan(&out).Error
	return out, err
}

func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// truncate 截到最多 n 字节，保证是合法的 UTF-8（D-058）。
func truncate(s string, n int) string { return textx.Clip(s, n) }

func toString(v any) string {
	s, _ := v.(string)
	return s
}
