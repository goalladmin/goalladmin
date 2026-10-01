// Package audit 是安全审计的公开入口（D-032）：错误日志、安全事件、调查时间线。
//
// 记录由框架完成：服务端故障（5xx、panic）和攻击迹象（越权被拒、令牌异常、凭证重放等）在请求结束时
// 自动写入；模块通过 app.Deps.Audit 查询，也可以自己记录安全事件。所有日志都不能删除、不能修改。
package audit

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound 表示要查看的记录不存在。
var ErrNotFound = errors.New("audit: not found")

// ErrorEntry 是错误日志的一行：同一类错误（相同指纹）只占一行，累加次数。
type ErrorEntry struct {
	ID            uint64    `json:"id"`
	Fingerprint   string    `json:"fingerprint"`
	Kind          string    `json:"kind"` // panic 或 error
	Portal        string    `json:"portal"`
	Method        string    `json:"method"`
	Route         string    `json:"route"` // 路由模板；没匹配到路由时为空
	Code          int       `json:"code"`
	HTTPStatus    int       `json:"httpStatus"`
	Message       string    `json:"message"`         // 已去掉具体值并截断
	Stack         string    `json:"stack,omitempty"` // 只有查看详情时返回；只有 panic 才有
	Count         int64     `json:"count"`
	FirstAt       time.Time `json:"firstAt"`
	LastAt        time.Time `json:"lastAt"`
	LastRequestID string    `json:"lastRequestId"`
	LastUserID    uint64    `json:"lastUserId"`
	LastUsername  string    `json:"lastUsername"`
	LastSessionID string    `json:"lastSessionId"`
	LastIP        string    `json:"lastIp"`
}

// ErrorFilter 是错误日志的查询条件；零值字段不参与过滤。From、To 按最近一次出现的时间过滤。
type ErrorFilter struct {
	// Portal 非空时只看这个端的错误（D-050）；IncludeGlobal 为 true 时再包括不属于任何端的（没匹配到端下路由的请求）
	Portal        string
	IncludeGlobal bool
	Kind          string
	Route         string // 前缀匹配
	From, To      time.Time
}

// 安全事件的级别。
const (
	LevelInfo     = 1 // 提示：多半是正常现象，出现得多时值得看一眼
	LevelWarning  = 2 // 警告：可能是试探或误操作
	LevelCritical = 3 // 严重：凭证可能已经泄露
)

// SecurityEvent 是安全事件的一行：同一来源在同一分钟内合并，Count 是次数。
type SecurityEvent struct {
	ID        uint64    `json:"id"`
	Portal    string    `json:"portal"`
	Kind      string    `json:"kind"`
	Level     int       `json:"level"`
	UserID    uint64    `json:"userId"`
	Username  string    `json:"username"`
	SessionID string    `json:"sessionId"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Detail    string    `json:"detail"`
	RequestID string    `json:"requestId"`
	Count     int64     `json:"count"`
	FirstAt   time.Time `json:"firstAt"`
	LastAt    time.Time `json:"lastAt"`
}

// NewSecurityEvent 是模块、命令行自己记录的安全事件。
// Kind 用小写字母、数字、下划线和点（最长 32）；框架认识的类型按固定级别记录，其他类型 Level 为 0 时按警告。
type NewSecurityEvent struct {
	Portal    string
	Kind      string
	Level     int
	UserID    uint64
	Username  string
	SessionID string
	IP        string
	Detail    string
}

// SecurityFilter 是安全事件的查询条件；零值字段不参与过滤。From、To 按第一次出现的时间过滤。
type SecurityFilter struct {
	Portal string
	// IncludeGlobal 为 true 时，Portal 之外再包括不属于任何端的事件（例如命令行清理策略）。
	IncludeGlobal bool
	Kind          string
	MinLevel      int
	UserID        uint64
	Username      string // 前缀匹配
	IP            string
	SessionID     string
	From, To      time.Time
}

// Service 是审计服务。
type Service interface {
	// RecordSecurity 记录一个安全事件（同样合并、限速，并写一行审计记录到日志输出）。
	RecordSecurity(ctx context.Context, e NewSecurityEvent) error
	// ListSecurityEvents 分页查询安全事件，按第一次出现倒序。
	ListSecurityEvents(ctx context.Context, f SecurityFilter, page, pageSize int) ([]SecurityEvent, int64, error)
	// ListErrors 分页查询错误日志，按最近一次出现倒序；不含调用栈。
	ListErrors(ctx context.Context, f ErrorFilter, page, pageSize int) ([]ErrorEntry, int64, error)
	// GetError 返回一行错误日志，含调用栈；不存在时返回 ErrNotFound。
	GetError(ctx context.Context, id uint64) (ErrorEntry, error)
	// Timeline 把某个用户、IP 或会话的登录日志、操作日志、安全事件合成一条按时间倒序的线。
	Timeline(ctx context.Context, q TimelineQuery) (Timeline, error)
}

// 调查时间线里的记录类型（D-032 第 6 条）。
const (
	TimelineLogin     = "login"
	TimelineOperation = "operation"
	TimelineSecurity  = "security"
)

// TimelineQuery 是调查时间线的条件：UserID、IP、SessionID 恰好给一个。
// Cursor 是上一页返回的 Next，只取排在它后面（更早）的记录；Limit 取 1–200，默认 100。
// Portal 非空时只看这个端的记录，不属于任何端的安全事件也包括在内。
type TimelineQuery struct {
	Portal    string
	UserID    uint64
	IP        string
	SessionID string
	Cursor    string
	Limit     int
}

// TimelineItem 是时间线上的一条：登录、操作或安全事件。只有对应类型的字段有值。
type TimelineItem struct {
	Type      string    `json:"type"`
	ID        uint64    `json:"id"`
	At        time.Time `json:"at"`
	UserID    uint64    `json:"userId"`
	Username  string    `json:"username"`
	SessionID string    `json:"sessionId"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"userAgent,omitempty"`
	RequestID string    `json:"requestId,omitempty"`
	// 登录
	Success *bool  `json:"success,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// 操作
	Action     string `json:"action,omitempty"`
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Code       *int   `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
	// 安全事件
	Kind   string     `json:"kind,omitempty"`
	Level  int        `json:"level,omitempty"`
	Detail string     `json:"detail,omitempty"`
	Count  int64      `json:"count,omitempty"`
	LastAt *time.Time `json:"lastAt,omitempty"`
}

// Timeline 是一页时间线，按时间倒序（同一时刻按安全事件、操作、登录，再按 ID 倒序）。
// More 为 true 时还有更早的记录，把 Next 作为下一页的 Cursor。
type Timeline struct {
	Items []TimelineItem `json:"items"`
	More  bool           `json:"more"`
	Next  string         `json:"next,omitempty"`
}

// ErrBadTimelineQuery 表示时间线的条件不对（没有给、或者给了不止一个查询对象）。
var ErrBadTimelineQuery = errors.New("audit: timeline needs exactly one of userId, ip, sessionId")

// ErrBadCursor 表示翻页游标不对。
var ErrBadCursor = errors.New("audit: bad timeline cursor")
