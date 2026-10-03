package httpx

import (
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// 分页约定（规范 §9.3）。
const (
	DefaultPageSize = 20
	MaxPageSize     = 200
	// MaxPage 是页码上限（D-055）：再往后翻是越来越深的 OFFSET，数据库要扫过前面所有行；要找很早的数据用筛选条件缩小范围。
	MaxPage = 5000
)

// PageQuery 是列表接口的通用查询参数。
type PageQuery struct {
	Page      int    `form:"page"`
	PageSize  int    `form:"pageSize"`
	Keyword   string `form:"keyword"`
	SortBy    string `form:"sortBy"`
	SortOrder string `form:"sortOrder"` // asc | desc
}

// BindPage 从查询串读取分页参数并做边界处理：page 在 [1, MaxPage]，pageSize 在 [1, MaxPageSize]，默认 DefaultPageSize。
func BindPage(c *gin.Context) PageQuery {
	q := PageQuery{
		Page:      atoi(c.Query("page"), 1),
		PageSize:  atoi(c.Query("pageSize"), DefaultPageSize),
		Keyword:   strings.TrimSpace(c.Query("keyword")),
		SortBy:    strings.TrimSpace(c.Query("sortBy")),
		SortOrder: strings.ToLower(strings.TrimSpace(c.Query("sortOrder"))),
	}
	return q.Normalize()
}

// Normalize 把越界值拉回合法范围。
func (q PageQuery) Normalize() PageQuery {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Page > MaxPage {
		q.Page = MaxPage
	}
	if q.PageSize < 1 {
		q.PageSize = DefaultPageSize
	}
	if q.PageSize > MaxPageSize {
		q.PageSize = MaxPageSize
	}
	if q.SortOrder != "asc" && q.SortOrder != "desc" {
		q.SortOrder = ""
	}
	return q
}

// Offset 返回 SQL OFFSET。
func (q PageQuery) Offset() int { return (q.Page - 1) * q.PageSize }

// Limit 返回 SQL LIMIT。
func (q PageQuery) Limit() int { return q.PageSize }

// Order 按白名单把 sortBy 翻译成 ORDER BY 子句，防止把用户输入拼进 SQL。
// allowed 的键是允许的 sortBy 参数值，值是对应的列名；不在白名单里时返回 fallback。
func (q PageQuery) Order(allowed map[string]string, fallback string) string {
	col, ok := allowed[q.SortBy]
	if !ok || q.SortBy == "" {
		return fallback
	}
	dir := "ASC"
	if q.SortOrder == "desc" {
		dir = "DESC"
	}
	return col + " " + dir
}

func atoi(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// ParseTime 解析请求里的 RFC 3339 时间（D-099）。除了格式，还要求换算成 UTC 后年份在 1–9999 之间：
// 带时区偏移的时间换算后可能超出这个范围，数据库驱动不接受这样的值。不合规时 ok 为 false。
func ParseTime(s string) (t time.Time, ok bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	if y := t.UTC().Year(); y < 1 || y > 9999 {
		return time.Time{}, false
	}
	return t, true
}
