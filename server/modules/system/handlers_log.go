package system

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
)

// ---- 操作日志、登录日志（只读；规范 §10：只增不改，不提供更新和删除接口）----

// queryTime 解析 RFC 3339 时间参数；空串或格式错误都当作没传。
func queryTime(c *gin.Context, key string) time.Time {
	s := c.Query(key)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// queryUserID 解析 userId 参数；格式错误当作没传。
func queryUserID(c *gin.Context) uint64 {
	v, _ := strconv.ParseUint(c.Query("userId"), 10, 64)
	return v
}

func queryBool(c *gin.Context, key string) *bool {
	switch c.Query(key) {
	case "1", "true":
		v := true
		return &v
	case "0", "false":
		v := false
		return &v
	}
	return nil
}

func (h *handlers) listOperationLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	f := oplog.Filter{
		Portal:    PortalCode,
		UserID:    queryUserID(c),
		Username:  c.Query("username"),
		Action:    c.Query("action"),
		Path:      c.Query("path"),
		Method:    c.Query("method"),
		IP:        c.Query("ip"),
		SessionID: c.Query("sessionId"),
		Failed:    queryBool(c, "failed"),
		From:      queryTime(c, "from"),
		To:        queryTime(c, "to"),
	}
	rows, total, err := oplog.List(c.Request.Context(), f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, total, q.Page, q.PageSize)
}

func (h *handlers) listLoginLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	f := auth.LoginLogFilter{
		Portal:    PortalCode,
		UserID:    queryUserID(c),
		Username:  c.Query("username"),
		IP:        c.Query("ip"),
		SessionID: c.Query("sessionId"),
		Success:   queryBool(c, "success"),
		From:      queryTime(c, "from"),
		To:        queryTime(c, "to"),
	}
	rows, total, err := h.deps.Auth.ListLoginLogs(c.Request.Context(), f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, total, q.Page, q.PageSize)
}

// ---- 错误日志（D-032，只读）----

func (h *handlers) listErrorLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	// 只看本端的错误，外加不属于任何端的（D-050）：别的端的用户名、会话、调用栈不给本端的管理员看
	f := audit.ErrorFilter{Portal: PortalCode, IncludeGlobal: true, Kind: c.Query("kind"), Route: c.Query("route"), From: queryTime(c, "from"), To: queryTime(c, "to")}
	rows, total, err := h.deps.Audit.ListErrors(c.Request.Context(), f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, total, q.Page, q.PageSize)
}

func (h *handlers) getErrorLog(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	row, err := h.deps.Audit.GetError(c.Request.Context(), id)
	if err == nil && row.Portal != "" && row.Portal != PortalCode {
		err = audit.ErrNotFound // 别的端的错误：当作不存在（D-050）
	}
	if errors.Is(err, audit.ErrNotFound) {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, row)
}

// ---- 安全事件（D-032，只读）----

func (h *handlers) listSecurityEvents(c *gin.Context) {
	q := httpx.BindPage(c)
	level, _ := strconv.Atoi(c.Query("level"))
	f := audit.SecurityFilter{
		Portal:        PortalCode,
		IncludeGlobal: true, // 命令行清理策略这类不属于任何端的事件也要看得到
		Kind:          c.Query("kind"),
		MinLevel:      level,
		UserID:        queryUserID(c),
		Username:      c.Query("username"),
		IP:            c.Query("ip"),
		SessionID:     c.Query("sessionId"),
		From:          queryTime(c, "from"),
		To:            queryTime(c, "to"),
	}
	rows, total, err := h.deps.Audit.ListSecurityEvents(c.Request.Context(), f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, total, q.Page, q.PageSize)
}

// ---- 调查时间线（D-032）----

func (h *handlers) auditTimeline(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	q := audit.TimelineQuery{
		Portal:    PortalCode,
		UserID:    queryUserID(c),
		IP:        c.Query("ip"),
		SessionID: c.Query("sessionId"),
		Cursor:    c.Query("cursor"),
		Limit:     limit,
	}
	out, err := h.deps.Audit.Timeline(c.Request.Context(), q)
	if errors.Is(err, audit.ErrBadCursor) {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("cursor", "audit.timelineCursor", "invalid cursor")))
		return
	}
	if errors.Is(err, audit.ErrBadTimelineQuery) {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("subject", "audit.timelineSubject", "exactly one of userId, ip, sessionId is required")))
		return
	}
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}
