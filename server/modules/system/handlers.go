package system

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// handlers 只做参数绑定、调用 service、输出信封；不含业务规则。
type handlers struct {
	deps     *app.Deps
	users    *UserService
	org      *OrgService
	security securityCache // 监控中心安全统计的进程内缓存（D-031）
}

func pathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return 0, false
	}
	return id, true
}

// ---- 用户 ----

func (h *handlers) listUsers(c *gin.Context) {
	q := httpx.BindPage(c)
	var status *int
	if s := c.Query("status"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			status = &v
		}
	}
	deptID, _ := strconv.ParseUint(c.Query("deptId"), 10, 64)
	withChildren := c.Query("withChildren") != "0" && c.Query("withChildren") != "false"
	ctx := c.Request.Context()
	list, total, err := h.users.List(ctx, auth.MustFromCtx(ctx), q, status, deptID, withChildren)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, list, total, q.Page, q.PageSize)
}

func (h *handlers) getUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	v, err := h.users.GetScoped(ctx, auth.MustFromCtx(ctx), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

type createUserRequest struct {
	Username    string   `json:"username" binding:"required,max=64"`
	Password    string   `json:"password" binding:"max=72"`
	DisplayName string   `json:"displayName" binding:"max=64"`
	Email       string   `json:"email" binding:"omitempty,email,max=128"`
	Phone       string   `json:"phone" binding:"max=32"`
	RoleIDs     []uint64 `json:"roleIds"`
	DeptID      uint64   `json:"deptId"`
	PostIDs     []uint64 `json:"postIds"`
	Sort        uint     `json:"sort" binding:"max=1000000"`
	Remark      string   `json:"remark" binding:"max=255"`
}

func (h *handlers) createUser(c *gin.Context) {
	var req createUserRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	v, initial, err := h.users.Create(ctx, auth.MustFromCtx(ctx), CreateInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"user": v, "initialPassword": initial})
}

type updateUserRequest struct {
	DisplayName string    `json:"displayName" binding:"required,max=64"`
	Email       string    `json:"email" binding:"omitempty,email,max=128"`
	Phone       string    `json:"phone" binding:"max=32"`
	Bio         string    `json:"bio" binding:"max=255"`
	DeptID      *uint64   `json:"deptId"`  // 不传表示不改（D-033）
	PostIDs     *[]uint64 `json:"postIds"` // 不传表示不改；传空数组表示清空
	Sort        uint      `json:"sort" binding:"max=1000000"`
	Remark      string    `json:"remark" binding:"max=255"`
}

func (h *handlers) updateUser(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateUserRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	v, err := h.users.Update(ctx, auth.MustFromCtx(ctx), id, UpdateInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

type statusRequest struct {
	Status *int `json:"status" binding:"required"`
}

func (h *handlers) setUserStatus(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req statusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := h.users.SetStatus(ctx, auth.MustFromCtx(ctx), id, *req.Status); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) resetUserPassword(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	plain, err := h.users.ResetPassword(ctx, auth.MustFromCtx(ctx), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"initialPassword": plain})
}

type assignRolesRequest struct {
	RoleIDs []uint64 `json:"roleIds"`
}

func (h *handlers) assignUserRoles(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req assignRolesRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	v, err := h.users.AssignRoles(ctx, auth.MustFromCtx(ctx), id, req.RoleIDs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

func (h *handlers) userOptions(c *gin.Context) {
	ctx := c.Request.Context()
	opts, err := h.users.Options(ctx, auth.MustFromCtx(ctx))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, opts)
}

// ---- 角色 ----

func (h *handlers) listRoles(c *gin.Context) {
	roles, err := h.deps.RBAC.Roles(c.Request.Context(), PortalCode)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if roles == nil {
		roles = []rbac.Role{}
	}
	httpx.OK(c, roles)
}

type roleRequest struct {
	Code   string `json:"code" binding:"max=64"`
	Name   string `json:"name" binding:"required,max=64"`
	Status *int   `json:"status"`
	Sort   uint   `json:"sort" binding:"max=1000000"`
	Remark string `json:"remark" binding:"max=255"`
}

func (r roleRequest) input() rbac.RoleInput {
	status := 1
	if r.Status != nil {
		status = *r.Status
	}
	return rbac.RoleInput{Code: r.Code, Name: r.Name, Status: status, Sort: r.Sort, Remark: r.Remark}
}

func (h *handlers) createRole(c *gin.Context) {
	var req roleRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	role, err := h.deps.RBAC.CreateRole(ctx, auth.MustFromCtx(ctx), PortalCode, req.input())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, role)
}

func (h *handlers) updateRole(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req roleRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	role, err := h.deps.RBAC.UpdateRole(ctx, auth.MustFromCtx(ctx), PortalCode, id, req.input())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, role)
}

func (h *handlers) deleteRole(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := h.deps.RBAC.DeleteRole(ctx, auth.MustFromCtx(ctx), PortalCode, id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) rolePerms(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	codes, err := h.deps.RBAC.RolePerms(c.Request.Context(), PortalCode, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if codes == nil {
		codes = []string{}
	}
	httpx.OK(c, codes)
}

type grantRequest struct {
	Codes []string `json:"codes"`
	// DataScopes 是数据资源 → 范围（D-039）；不传或不含某个资源时，该资源的范围不变
	DataScopes map[string]rbac.DataScope `json:"dataScopes"`
}

func (h *handlers) grantRolePerms(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req grantRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := h.deps.RBAC.GrantRole(ctx, auth.MustFromCtx(ctx), PortalCode, id, req.Codes, req.DataScopes); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// roleDataScopes 返回角色在每个数据资源上的范围（D-039）。
func (h *handlers) roleDataScopes(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	scopes, err := h.deps.RBAC.RoleDataScopes(c.Request.Context(), PortalCode, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, scopes)
}

// dataResourceView 是授权界面上的一个数据资源。
type dataResourceView struct {
	Code    string           `json:"code"`
	Name    string           `json:"name"`
	Perms   []string         `json:"perms"`
	Default rbac.DataScope   `json:"default"`
	Scopes  []rbac.DataScope `json:"scopes"`
}

// dataResources 列出本端声明的数据资源和可选范围（D-039）。
func (h *handlers) dataResources(c *gin.Context) {
	list := h.deps.RBAC.DataResources(PortalCode)
	out := make([]dataResourceView, 0, len(list))
	for _, d := range list {
		out = append(out, dataResourceView{Code: d.Code, Name: d.Name, Perms: d.Perms, Default: d.Default, Scopes: d.Choices()})
	}
	httpx.OK(c, out)
}

func (h *handlers) permTree(c *gin.Context) {
	tree := h.deps.RBAC.PermTree(PortalCode)
	if tree == nil {
		tree = []rbac.PermGroup{}
	}
	httpx.OK(c, tree)
}

// ---- 会话 ----

type sessionView struct {
	auth.SessionInfo
	Username string `json:"username"`
	// SuperAccount 表示这是超管账号的会话：非超管不能让它下线（D-035），前端据此置灰按钮
	SuperAccount bool `json:"superAccount"`
}

func (h *handlers) listSessions(c *gin.Context) {
	q := httpx.BindPage(c)
	var userID uint64
	if s := c.Query("userId"); s != "" {
		userID, _ = strconv.ParseUint(s, 10, 64)
	}
	ctx := c.Request.Context()
	var (
		out   []sessionView
		total int64
	)
	// 范围内有哪些人、这些人的会话、账号名和角色在同一个只读快照里读（D-052）：先按范围取 ID、再查会话的话，
	// 两次查询之间有人被调出范围，列出来的就是他调走之后的新会话
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		rows, n, err := h.scopedSessions(ctx, auth.MustFromCtx(ctx), userID, q)
		if err != nil {
			return err
		}
		ids := make([]uint64, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.UserID)
		}
		users, err := h.users.repo.FindByIDs(ctx, ids)
		if err != nil {
			return err
		}
		roles, err := h.deps.RBAC.UserRolesBatch(ctx, PortalCode, ids)
		if err != nil {
			return err
		}
		out = make([]sessionView, 0, len(rows))
		for _, r := range rows {
			v := sessionView{SessionInfo: r}
			if u, ok := users[r.UserID]; ok {
				v.Username = u.Username
			}
			for _, role := range roles[r.UserID] {
				if role.IsSuper {
					v.SuperAccount = true
				}
			}
			out = append(out, v)
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

// scopedSessions 只列"查看会话"范围内的用户的会话（D-039）。
func (h *handlers) scopedSessions(ctx context.Context, actor auth.Principal, userID uint64, q httpx.PageQuery) ([]auth.SessionInfo, int64, error) {
	f, err := h.deps.RBAC.DataFilter(ctx, actor, DataUser, PermSessionList)
	if err != nil {
		return nil, 0, err
	}
	if f.All() {
		return h.deps.Auth.ListSessions(ctx, PortalCode, userID, q.Page, q.PageSize)
	}
	users := h.users.repo.IDsInScopeQuery(ctx, f)
	if userID != 0 {
		users = users.Where("id = ?", userID)
	}
	return h.deps.Auth.ListSessionsIn(ctx, PortalCode, users, q.Page, q.PageSize)
}

func (h *handlers) revokeSession(c *gin.Context) {
	sid := c.Param("sid")
	if len(sid) != 32 {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	ctx := c.Request.Context()
	if err := h.users.RevokeSession(ctx, auth.MustFromCtx(ctx), sid); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}
