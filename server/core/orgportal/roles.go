package orgportal

import (
	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// 角色与权限：规则都在 rbac 里（D-063）——角色按主体、只看本主体的，主账号能授敏感权限码，员工授不出自己没有的。
// 主体端没有部门，不声明数据资源，所以没有数据范围。

func (h *handlers) listRoles(c *gin.Context) {
	ctx := c.Request.Context()
	if _, _, err := principal(ctx); err != nil {
		httpx.Fail(c, err)
		return
	}
	roles, err := h.k.deps.RBAC.Roles(ctx, h.k.code)
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
	return rbac.RoleInput{Code: r.Code, Name: r.Name, Status: status, Sort: r.Sort, Remark: r.Remark, KeepStatus: r.Status == nil}
}

func (h *handlers) createRole(c *gin.Context) {
	var req roleRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	role, err := h.k.deps.RBAC.CreateRole(ctx, auth.MustFromCtx(ctx), h.k.code, req.input())
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
	role, err := h.k.deps.RBAC.UpdateRole(ctx, auth.MustFromCtx(ctx), h.k.code, id, req.input())
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
	if err := h.k.deps.RBAC.DeleteRole(ctx, auth.MustFromCtx(ctx), h.k.code, id); err != nil {
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
	// 别的主体的角色是 404（RolePerms 按身份所属的主体查，D-063）
	codes, err := h.k.deps.RBAC.RolePerms(c.Request.Context(), h.k.code, id)
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
	Codes []string `json:"codes" binding:"max=1024"`
}

func (h *handlers) grantRole(c *gin.Context) {
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
	if err := h.k.deps.RBAC.GrantRolePerms(ctx, auth.MustFromCtx(ctx), h.k.code, id, req.Codes); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) permTree(c *gin.Context) {
	tree := h.k.deps.RBAC.PermTree(h.k.code)
	if tree == nil {
		tree = []rbac.PermGroup{}
	}
	httpx.OK(c, tree)
}
