// Package oplogimpl 是操作日志的实现：路由级中间件、脱敏、落库、查询。
//
// 公开入口在 core/oplog（Record 路由选项）；core/app 直接用本包给 /auth/password 这类框架路由挂日志。
// 只记"通过了守卫的请求"：未登录和无权限的请求在守卫处就被拒绝，不会到达这里。
package oplogimpl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	OrgID      uint64    `gorm:"column:org_id" json:"orgId"` // 操作人所属的主体（主体端，D-061）；平台端为 0
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
		body, readErr := captureBody(c)
		body = clipBody(body)
		var tooLarge *http.MaxBytesError
		if errors.As(readErr, &tooLarge) {
			// 预读已经发现超限，后续处理函数不再执行；仍记录本次失败操作（D-083）。
			httpx.Fail(c, httpx.ErrBodyTooLarge.WithCause(readErr))
		} else {
			c.Next()
		}

		ctx := c.Request.Context()
		e := Entry{
			RequestID:  httpx.RequestID(ctx),
			Portal:     portal,
			Action:     action,
			Method:     c.Request.Method,
			Path:       truncate(c.Request.URL.Path, maxPath),
			Query:      clipQuery(MaskQuery(c.Request.URL.RawQuery)),
			Body:       body,
			HTTPStatus: c.Writer.Status(),
			LatencyMs:  int(time.Since(start).Milliseconds()),
			IP:         truncate(c.ClientIP(), maxIP),
			UserAgent:  truncate(c.Request.UserAgent(), maxUserAgent),
			CreatedAt:  start.UTC(),
		}
		if p, ok := auth.FromCtx(ctx); ok {
			e.OrgID, e.UserID, e.Username, e.SessionID = p.OrgID, p.UserID, truncate(p.Username, maxUsername), truncate(p.SessionID, maxSession)
		}
		if v, ok := c.Get(httpx.KeyResponseCode); ok {
			e.Code, _ = v.(int)
		}
		if e.Code != 0 || e.HTTPStatus >= 400 {
			msg, _ := c.Get(httpx.KeyResponseMsg)
			e.Error = truncate(toString(msg), maxError)
		}
		// 同时写一行审计记录到日志输出，交给外部日志系统（D-032）；请求体和查询串不写
		logx.Audit(ctx, "audit.operation", "portal", e.Portal, "org_id", e.OrgID, "action", e.Action, "user_id", e.UserID, "username", e.Username,
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

// captureBody 读走请求体、放回一份可重读的副本，并返回脱敏、截断后的文本和读取错误。
// 只解析 JSON 和表单；其他类型（如文件上传）不存内容，只存类型和大小。
func captureBody(c *gin.Context) (string, error) {
	if c.Request.Body == nil || c.Request.Body == http.NoBody {
		return "", nil
	}
	raw, err := io.ReadAll(c.Request.Body)
	_ = c.Request.Body.Close()
	// 正常内容原样回放；读取失败只回放错误，不能把不完整的请求体当成有效输入。
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		c.Request.Body = io.NopCloser(errReader{err: err})
		return placeholder("unreadable", len(raw)), err
	}
	if len(raw) == 0 {
		return "", nil
	}
	ct, _, _ := mime.ParseMediaType(c.ContentType())
	switch {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		return clipBody(MaskJSON(raw)), nil
	case ct == "application/x-www-form-urlencoded":
		// 声明成表单、内容却是 JSON 的请求体按 JSON 脱敏（D-095）：当成表单解析的话，整段 JSON 会变成一个"键"原样写出来
		if looksLikeJSON(raw) {
			return clipBody(maskWholeJSON(raw)), nil
		}
		return clipBody(MaskQuery(string(raw))), nil
	default:
		return placeholder(ct, len(raw)), nil
	}
}

// looksLikeJSON 报告请求体是不是以 { 或 [ 开头（前面可以有空白）。
func looksLikeJSON(raw []byte) bool {
	t := bytes.TrimLeft(raw, " \t\r\n")
	return len(t) > 0 && (t[0] == '{' || t[0] == '[')
}

// maskWholeJSON 和 MaskJSON 一样脱敏，但要求整段请求体就是一个 JSON 值：后面还有别的内容时只存占位符。
// 用在声明成表单的请求体上（D-095）：JSON 值后面再跟表单字段的话，只记前半段就漏了后半段。
func maskWholeJSON(raw []byte) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var v any
	if err := dec.Decode(&v); err != nil {
		return placeholder("invalid json", len(raw))
	}
	if _, err := dec.Token(); err != io.EOF {
		return placeholder("invalid json", len(raw))
	}
	return MaskJSON(raw)
}

// clipBody 把要入库的请求体整理成合法 UTF-8 并限制在 MaxBody 以内；超出时结尾标明被截断和完整长度（D-095），
// 看日志的人知道这条记录不完整。
func clipBody(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if len(s) <= MaxBody {
		return s
	}
	mark := "…(truncated, " + strconv.Itoa(len(s)) + " bytes)"
	return textx.Clip(s, MaxBody-len(mark)) + mark
}

// maxValue 是日志里单个字符串值的上限（D-095）：再长的只留开头并标上原长度。备注、简介这类最长的字段是 255 个字符，
// 留得下；一个很长的值不能把同一个请求里别的参数挤出日志。
const maxValue = 512

func clipValue(s string) string {
	if len(s) <= maxValue {
		return s
	}
	return textx.Clip(s, maxValue) + "…(" + strconv.Itoa(len(s)) + " bytes)"
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
		long := 0
		for k, val := range t {
			switch {
			case len(k) > maxKey:
				// 过长的键不写原文（D-095）：它和很长的值一样能把别的参数挤出日志，原文里也可能就有要脱敏的内容
				delete(t, k)
				long++
			case IsSensitiveKey(k):
				t[k] = Masked
			default:
				t[k] = maskValue(val)
			}
		}
		if long > 0 {
			t[keyPlaceholder] = strconv.Itoa(long) + " long keys omitted"
		}
		return t
	case []any:
		for i := range t {
			t[i] = maskValue(t[i])
		}
		return t
	case string:
		return clipValue(t)
	default:
		return v
	}
}

// MaskQuery 对查询串或表单做同样的脱敏；键按字母序重排。无法解析的只存占位符；
// 不像字段名的键只写 keyPlaceholder，连它的值一起遮掉（D-095）。
func MaskQuery(raw string) string {
	if raw == "" {
		return ""
	}
	vals, err := url.ParseQuery(raw)
	if err != nil && len(vals) == 0 {
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
			if plainKey(k) {
				b.WriteString(url.QueryEscape(k))
			} else {
				// 键不像字段名：多半是别的格式的内容被当成了表单，原文可能就含要脱敏的值，不写出来（D-095）
				b.WriteString(keyPlaceholder)
				b.WriteByte('=')
				b.WriteString(Masked)
				continue
			}
			b.WriteByte('=')
			if IsSensitiveKey(k) {
				b.WriteString(Masked)
			} else {
				b.WriteString(url.QueryEscape(clipValue(v)))
			}
		}
	}
	if err != nil {
		b.WriteString(" [partial query, " + strconv.Itoa(len(raw)) + " bytes]")
	}
	return b.String()
}

func clipQuery(s string) string {
	if len(s) <= maxQuery {
		return s
	}
	mark := "…(truncated, " + strconv.Itoa(len(s)) + " bytes)"
	return textx.Clip(s, maxQuery-len(mark)) + mark
}

// keyPlaceholder 代替查询串、表单里不像字段名的键。
const keyPlaceholder = "(key)"

// maxKey 是查询串、表单里一个键的最大长度。
const maxKey = 128

// plainKey 报告 k 像不像一个字段名：不超过 maxKey 字节，只含字母、数字、'_'、'-'、'.' 和方括号。
func plainKey(k string) bool {
	if k == "" || len(k) > maxKey {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == '[' || c == ']':
		default:
			return false
		}
	}
	return true
}

// Filter 是查询条件。零值字段不参与过滤。
type Filter struct {
	Portal    string
	OrgID     uint64 // 非 0 时只看这个主体的（主体端查本主体的日志，D-061）
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
	if f.OrgID != 0 {
		q = q.Where("org_id = ?", f.OrgID)
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
