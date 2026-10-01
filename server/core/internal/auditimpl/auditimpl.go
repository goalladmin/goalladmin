// Package auditimpl 实现安全审计（D-032）：错误日志、安全事件、调查时间线。
//
// 记录发生在框架中间件里：请求结束后看响应状态、panic 标记和安全标记决定写什么。
// 写库经过 throttle 合并和限速，攻击者刷接口时写库次数有上限。公开入口在 core/audit。
package auditimpl

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/middleware"
	"github.com/goalladmin/goalladmin/server/core/internal/secmark"
)

// 合并与限速的参数（D-032）。
const (
	flushInterval = time.Second // 同一个键每秒最多写一次库
	maxKeys       = 4096        // 内存里同时跟踪的键数
	newKeysPerSec = 20          // 新键写库的速度
	newKeysBurst  = 100
)

// Options 是创建 Recorder 的参数。
type Options struct {
	DB   *gorm.DB // 为 nil 时什么都不写（没有数据库的测试）
	Log  *slog.Logger
	Now  func() time.Time
	Skip []string // 不记录的路由模板（健康检查）
}

// Recorder 记录并查询审计数据，实现 audit.Service。
type Recorder struct {
	db       *gorm.DB
	log      *slog.Logger
	now      func() time.Time
	skip     map[string]bool
	errors   *throttle[errorRow]
	security *throttle[securityRow]

	stop   chan struct{} // 后台定时写库；Start 之后非空
	done   chan struct{}
	cancel context.CancelFunc // 关停时打断后台正在进行的一轮
}

// New 创建 Recorder。
func New(o Options) *Recorder {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Recorder{db: o.DB, log: o.Log.With("component", "audit"), now: o.Now, skip: map[string]bool{}}
	for _, p := range o.Skip {
		r.skip[p] = true
	}
	r.errors = newThrottle("error log", o.Now, r.log, flushInterval, maxKeys, newKeysPerSec, newKeysBurst, r.upsertError)
	r.security = newThrottle("security event", o.Now, r.log, flushInterval, maxKeys, newKeysPerSec, newKeysBurst, r.upsertSecurity)
	r.errors.permanent, r.security.permanent = db.IsDataError, db.IsDataError
	return r
}

// Start 启动后台定时写库：每秒把攒着的次数写掉。没有数据库时什么都不做。
func (r *Recorder) Start() {
	if r.db == nil || r.stop != nil {
		return
	}
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	go func(stop, done chan struct{}) {
		defer close(done)
		tk := time.NewTicker(flushInterval)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				r.errors.FlushDue(ctx)
				r.security.FlushDue(ctx)
			}
		}
	}(r.stop, r.done)
}

// Stop 停止后台写库，并把剩下攒着的次数写完。
func (r *Recorder) Stop(ctx context.Context) {
	if r.stop != nil {
		r.cancel()
		close(r.stop)
		<-r.done
		r.stop, r.done, r.cancel = nil, nil, nil
	}
	r.Flush(ctx)
}

// Flush 把攒着的次数写进库。关停前调用。
func (r *Recorder) Flush(ctx context.Context) {
	if r.db == nil {
		return
	}
	r.errors.Flush(ctx)
	r.security.Flush(ctx)
}

// Middleware 在请求结束后记录审计数据。必须排在 Recovery 之前（外层），这样能看到 panic 转成的 500。
func (r *Recorder) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()
		if r.skip[c.FullPath()] {
			return
		}
		pv, panicked := c.Get(middleware.KeyPanic)
		if (panicked || c.Writer.Status() >= 500) && r.db != nil {
			r.recordError(c, pv, panicked)
		}
		// 安全事件：框架各处打的标记；没有标记的 2001（业务层拒绝的越权操作）也算越权被拒
		if m, ok := secmark.Get(c); ok {
			r.recordSecurity(c, m)
		} else if code, _ := c.Get(httpx.KeyResponseCode); code == httpx.CodeForbidden {
			r.recordSecurity(c, secmark.Mark{Kind: secmark.Forbidden})
		}
	}
}

// ---- 错误日志 ----

// errorRow 对应 ga_error_log 的一行。
type errorRow struct {
	ID            uint64    `gorm:"column:id;primaryKey"`
	Fingerprint   string    `gorm:"column:fingerprint"`
	Kind          string    `gorm:"column:kind"`
	Portal        string    `gorm:"column:portal"`
	Method        string    `gorm:"column:method"`
	Route         string    `gorm:"column:route"`
	Code          int       `gorm:"column:code"`
	HTTPStatus    int       `gorm:"column:http_status"`
	Message       string    `gorm:"column:message"`
	Stack         *string   `gorm:"column:stack"`
	Count         int64     `gorm:"column:count"`
	FirstAt       time.Time `gorm:"column:first_at"`
	LastAt        time.Time `gorm:"column:last_at"`
	LastRequestID string    `gorm:"column:last_request_id"`
	LastUserID    uint64    `gorm:"column:last_user_id"`
	LastUsername  string    `gorm:"column:last_username"`
	LastSessionID string    `gorm:"column:last_session_id"`
	LastIP        string    `gorm:"column:last_ip"`
}

func (errorRow) TableName() string { return "ga_error_log" }

func (e errorRow) public() audit.ErrorEntry {
	out := audit.ErrorEntry{
		ID: e.ID, Fingerprint: e.Fingerprint, Kind: e.Kind, Portal: e.Portal, Method: e.Method, Route: e.Route,
		Code: e.Code, HTTPStatus: e.HTTPStatus, Message: e.Message, Count: e.Count, FirstAt: e.FirstAt, LastAt: e.LastAt,
		LastRequestID: e.LastRequestID, LastUserID: e.LastUserID, LastUsername: e.LastUsername, LastSessionID: e.LastSessionID, LastIP: e.LastIP,
	}
	if e.Stack != nil {
		out.Stack = *e.Stack
	}
	return out
}

// 错误日志的种类。
const (
	KindPanic = "panic"
	KindError = "error"
)

func (r *Recorder) recordError(c *gin.Context, pv any, panicked bool) {
	ctx := c.Request.Context()
	now := r.now().UTC()
	code, _ := c.Get(httpx.KeyResponseCode)
	codeInt, _ := code.(int)
	row := errorRow{
		Method: truncate(c.Request.Method, 10), Route: truncate(c.FullPath(), 255), Code: codeInt, HTTPStatus: c.Writer.Status(),
		FirstAt: now, LastAt: now, LastRequestID: truncate(httpx.RequestID(ctx), 64), LastIP: truncate(c.ClientIP(), 64),
	}
	if p, ok := auth.FromCtx(ctx); ok {
		row.Portal, row.LastUserID, row.LastUsername, row.LastSessionID = p.Portal, p.UserID, truncate(p.Username, 64), truncate(p.SessionID, 32)
	}
	if row.Portal == "" {
		// 没登录的请求（登录、刷新、公开接口）按路由模板归到端（D-050）：错误日志按端隔离，不能因为没带身份就谁都看得到
		row.Portal = truncate(portalOf(row.Route), 32)
	}
	if info, ok := pv.(middleware.PanicInfo); ok && panicked {
		stack, origin := cleanStack(info.Stack)
		row.Kind, row.Message, row.Stack = KindPanic, cleanMessage("panic: "+info.Value), &stack
		row.Fingerprint = fingerprint(KindPanic, row.Method, row.Route, info.Type, origin)
	} else {
		row.Kind, row.Message = KindError, cleanMessage(errorText(c, codeInt))
		row.Fingerprint = fingerprint(KindError, row.Method, row.Route, strconv.Itoa(codeInt), normalizeForFingerprint(row.Message))
	}
	r.errors.Add(ctx, row.Fingerprint, row, row.Kind == KindPanic)
}

// errorText 取失败响应背后的原因；没有原因时用错误码的英文文案。
func errorText(c *gin.Context, code int) string {
	v, _ := c.Get(httpx.KeyResponseErr)
	if err, ok := v.(error); ok && err != nil {
		if cause := errors.Unwrap(httpx.AsError(err)); cause != nil {
			return cause.Error()
		}
		var he *httpx.Error
		if !errors.As(err, &he) {
			return err.Error()
		}
	}
	if code == 0 {
		// 没经过 httpx.Fail 的 5xx（例如 Raw 路由直接写状态码）：用 HTTP 状态文案
		return http.StatusText(c.Writer.Status())
	}
	return httpx.Message(code, httpx.LangEN)
}

// upsertError 插入一类新错误，或给已有的那一行累加次数、更新"最近一次"。
func (r *Recorder) upsertError(ctx context.Context, e errorRow, n int64) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO ga_error_log
		(fingerprint, kind, portal, method, route, code, http_status, message, stack, count, first_at, last_at,
		 last_request_id, last_user_id, last_username, last_session_id, last_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		  count = count + VALUES(count),
		  last_at = GREATEST(last_at, VALUES(last_at)),
		  http_status = VALUES(http_status),
		  message = VALUES(message),
		  stack = COALESCE(VALUES(stack), stack),
		  portal = IF(VALUES(portal) = '', portal, VALUES(portal)),
		  last_request_id = VALUES(last_request_id),
		  last_user_id = VALUES(last_user_id),
		  last_username = VALUES(last_username),
		  last_session_id = VALUES(last_session_id),
		  last_ip = VALUES(last_ip)`,
		e.Fingerprint, e.Kind, e.Portal, e.Method, e.Route, e.Code, e.HTTPStatus, e.Message, e.Stack, n, e.FirstAt, e.LastAt,
		e.LastRequestID, e.LastUserID, e.LastUsername, e.LastSessionID, e.LastIP).Error
}

// ListErrors 实现 audit.Service。
func (r *Recorder) ListErrors(ctx context.Context, f audit.ErrorFilter, page, pageSize int) ([]audit.ErrorEntry, int64, error) {
	if r.db == nil {
		return nil, 0, errors.New("audit: no database")
	}
	q := r.db.WithContext(ctx).Model(&errorRow{})
	switch {
	case f.Portal != "" && f.IncludeGlobal:
		q = q.Where("portal IN (?, '')", f.Portal)
	case f.Portal != "":
		q = q.Where("portal = ?", f.Portal)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Route != "" {
		q = q.Where("route LIKE ?", escapeLike(f.Route)+"%")
	}
	if !f.From.IsZero() {
		q = q.Where("last_at >= ?", f.From.UTC())
	}
	if !f.To.IsZero() {
		q = q.Where("last_at < ?", f.To.UTC())
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []errorRow
	if err := q.Omit("stack").Order("last_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]audit.ErrorEntry, 0, len(rows))
	for _, e := range rows {
		e.Stack = nil
		out = append(out, e.public())
	}
	return out, total, nil
}

// GetError 实现 audit.Service。
func (r *Recorder) GetError(ctx context.Context, id uint64) (audit.ErrorEntry, error) {
	if r.db == nil {
		return audit.ErrorEntry{}, errors.New("audit: no database")
	}
	var e errorRow
	err := r.db.WithContext(ctx).Where("id = ?", id).Take(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return audit.ErrorEntry{}, audit.ErrNotFound
	}
	if err != nil {
		return audit.ErrorEntry{}, err
	}
	return e.public(), nil
}

func escapeLike(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' || s[i] == '%' || s[i] == '_' {
			out = append(out, '\\')
		}
		out = append(out, s[i])
	}
	return string(out)
}
