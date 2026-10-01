package system

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
)

// 工作台（docs/decisions.md D-030）：只返回调用者本人的数据。用户 ID 和端都取自身份，
// 接口不接受任何指定用户的参数，所以谁也看不到别人的记录。要有 system:workspace:view（普通权限）。

const (
	workspaceRecentOps    = 10
	workspaceRecentLogins = 8
	workspaceMaxSessions  = 20
	workspaceTopActions   = 6
)

type workspaceResponse struct {
	Roles      []string            `json:"roles"`
	Sessions   []workspaceSession  `json:"sessions"`
	OpsToday   int64               `json:"opsToday"`
	Ops30d     int64               `json:"ops30d"`
	Recent     []workspaceOp       `json:"recent"`
	Logins     []workspaceLogin    `json:"logins"`
	LastLogin  *workspaceLogin     `json:"lastLogin"` // 本次会话之前的最近一次成功登录
	TopActions []oplog.ActionCount `json:"topActions"`
}

type workspaceSession struct {
	Current    bool      `json:"current"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

type workspaceOp struct {
	Action    string    `json:"action"`
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Code      int       `json:"code"`
	CreatedAt time.Time `json:"createdAt"`
}

type workspaceLogin struct {
	Success   bool      `json:"success"`
	Reason    string    `json:"reason"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"createdAt"`
}

// workspace 处理 GET /system/workspace?tz=480（tz 只用来算"今天"从哪一刻开始）。
// lastLoginScan 是找"上次登录"时往回取的条数：够跳过同一毫秒里的几次登录。
const lastLoginScan = 20

func (h *handlers) workspace(c *gin.Context) {
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	tz := clampInt(c.Query("tz"), 0, -840, 840)
	now := time.Now().UTC()
	offset := time.Duration(tz) * time.Minute
	today := now.Add(offset).Truncate(24 * time.Hour).Add(-offset)
	month := now.AddDate(0, 0, -30)

	resp := workspaceResponse{Roles: []string{}}
	roles, err := h.deps.RBAC.UserRoles(ctx, p.Portal, p.UserID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	for _, r := range roles {
		resp.Roles = append(resp.Roles, r.Name)
	}

	sessions, _, err := h.deps.Auth.ListSessions(ctx, p.Portal, p.UserID, 1, workspaceMaxSessions)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp.Sessions = make([]workspaceSession, 0, len(sessions))
	for _, s := range sessions {
		resp.Sessions = append(resp.Sessions, workspaceSession{Current: s.SID == p.SessionID, IP: s.IP, UserAgent: s.UserAgent, CreatedAt: s.CreatedAt, LastSeenAt: s.LastSeenAt})
	}

	mine := oplog.Filter{Portal: p.Portal, UserID: p.UserID}
	todayF, monthF := mine, mine
	todayF.From, monthF.From = today, month
	if _, resp.OpsToday, err = oplog.List(ctx, todayF, 1, 1); err != nil {
		httpx.Fail(c, err)
		return
	}
	if _, resp.Ops30d, err = oplog.List(ctx, monthF, 1, 1); err != nil {
		httpx.Fail(c, err)
		return
	}
	ops, _, err := oplog.List(ctx, mine, 1, workspaceRecentOps)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp.Recent = make([]workspaceOp, 0, len(ops))
	for _, o := range ops {
		resp.Recent = append(resp.Recent, workspaceOp{Action: o.Action, Method: o.Method, Path: o.Path, Code: o.Code, CreatedAt: o.CreatedAt})
	}
	if resp.TopActions, err = oplog.UserTopActions(ctx, p.Portal, p.UserID, month, workspaceTopActions); err != nil {
		httpx.Fail(c, err)
		return
	}
	if resp.TopActions == nil {
		resp.TopActions = []oplog.ActionCount{}
	}

	logins, _, err := h.deps.Auth.ListLoginLogs(ctx, auth.LoginLogFilter{Portal: p.Portal, UserID: p.UserID}, 1, workspaceRecentLogins)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	resp.Logins = make([]workspaceLogin, 0, len(logins))
	for _, l := range logins {
		resp.Logins = append(resp.Logins, workspaceLogin{Success: l.Success, Reason: l.Reason, IP: l.IP, CreatedAt: l.CreatedAt})
	}
	// 上次登录：本次登录的日志（按会话 ID 找，D-032）之前最近的一次成功登录。本次登录按会话 ID 单独查，
	// 不从上面的会话列表里找：会话超过一页时本次会话可能不在列表里；上次登录也单独查一条，
	// 不从最近几条登录里找：本次登录之后又登录了很多次时，真正的上次登录已经不在那几条里了。
	cur, _, err := h.deps.Auth.ListLoginLogs(ctx, auth.LoginLogFilter{Portal: p.Portal, UserID: p.UserID, SessionID: p.SessionID}, 1, 1)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(cur) > 0 {
		ok := true
		// 时间只精确到毫秒：和本次登录同一毫秒的上一次登录，按"严格早于"会被漏掉。所以取到本次这一毫秒为止的
		// 几条（按时间、ID 倒序），跳过本次和它之后的，第一条就是上一次
		cutoff := cur[0].CreatedAt.Add(time.Millisecond)
		prev, _, err := h.deps.Auth.ListLoginLogs(ctx, auth.LoginLogFilter{Portal: p.Portal, UserID: p.UserID, Success: &ok, To: cutoff}, 1, lastLoginScan)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		for _, l := range prev {
			if l.CreatedAt.Equal(cur[0].CreatedAt) && l.ID >= cur[0].ID {
				continue // 本次登录，或同一毫秒里在它之后的登录
			}
			resp.LastLogin = &workspaceLogin{Success: l.Success, Reason: l.Reason, IP: l.IP, CreatedAt: l.CreatedAt}
			break
		}
	}
	httpx.OK(c, resp)
}
