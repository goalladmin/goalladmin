package orgportal

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/org"
)

// SessionView 是会话页面上的一个会话。Owner 表示这是主账号的会话：员工不能让它下线，前端据此置灰按钮。
type SessionView struct {
	org.Session
	Owner   bool `json:"owner"`
	Current bool `json:"current"` // 调用者自己的这个会话
}

func (h *handlers) listSessions(c *gin.Context) {
	q := httpx.BindPage(c)
	ctx := c.Request.Context()
	p, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var (
		out   []SessionView
		total int64
	)
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		info, err := h.k.deps.Orgs.Get(ctx, h.k.kind, orgID)
		if err != nil {
			return err
		}
		rows, n, err := h.k.deps.Orgs.Sessions(ctx, h.k.kind, orgID, q)
		if err != nil {
			return err
		}
		out = make([]SessionView, 0, len(rows))
		for _, r := range rows {
			out = append(out, SessionView{Session: r, Owner: info.OwnerUserID != 0 && r.UserID == info.OwnerUserID, Current: r.SID == p.SessionID})
		}
		total = n
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, out, total, q.Page, q.PageSize)
}

// revokeSession 让本主体的一个会话下线：别的主体、别的端的会话是 404；员工不能让主账号的会话下线。
func (h *handlers) revokeSession(c *gin.Context) {
	sid := c.Param("sid")
	if len(sid) != 32 {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	deps := h.k.deps
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		userID, err := deps.Auth.SessionOwner(ctx, h.k.code, sid)
		if err != nil {
			return err
		}
		target, err := deps.Orgs.Member(ctx, h.k.kind, oid, userID)
		if err != nil {
			return err // 别的主体的会话：和不存在一样
		}
		if target.Owner && !actor.Super {
			return httpx.ErrForbidden.WithFields(httpx.NewField("sid", "org.session.ownerProtected", "only the owner account can end the owner account's sessions"))
		}
		return deps.Auth.RevokeSession(ctx, h.k.code, sid, auth.RevokeAdmin)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// ---- 日志：只看本主体的，只读 ----

func (h *handlers) listLoginLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	ctx := c.Request.Context()
	_, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	f := auth.LoginLogFilter{
		Portal:    h.k.code,
		OrgID:     orgID,
		UserID:    queryUserID(c),
		Username:  c.Query("username"),
		IP:        c.Query("ip"),
		SessionID: c.Query("sessionId"),
		Success:   queryBool(c, "success"),
		From:      queryTime(c, "from"),
		To:        queryTime(c, "to"),
	}
	rows, total, err := h.k.deps.Auth.ListLoginLogs(ctx, f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, total, q.Page, q.PageSize)
}

func (h *handlers) listOperationLogs(c *gin.Context) {
	q := httpx.BindPage(c)
	ctx := c.Request.Context()
	p, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	f := oplog.Filter{
		Portal:    h.k.code,
		OrgID:     orgID,
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
	rows, total, err := oplog.List(ctx, f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// IP 白名单、黑名单只有主账号能看（D-090）：改名单的请求体里就是名单的内容，员工在日志里只看得到
	// 谁、什么时候改过、结果如何，请求体不给（D-097）。这组路由按库核对，p.Super 是这次请求读到的主账号
	if !p.Super {
		for i := range rows {
			if ownerOnlyBody(rows[i].Action) && rows[i].Body != "" {
				rows[i].Body = oplog.Masked
			}
		}
	}
	httpx.OKPage(c, rows, total, q.Page, q.PageSize)
}

// ownerOnlyBody 报告一种操作的请求体是不是只给主账号看（D-097）：改 IP 白名单、黑名单。
func ownerOnlyBody(action string) bool { return action == OpIPAllow || action == OpIPDeny }
