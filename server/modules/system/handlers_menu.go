package system

import (
	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// 菜单管理（docs/decisions.md D-025）。规则都在 core/rbac 里，这里只做绑定和输出。
// 请求体一律严格解析：字段白名单之外的字段（例如 path、component、perm）直接回 3002，不静默忽略。
// 只能管理操作者所在端的菜单：服务端取身份里的端，不从请求里读。

func (h *handlers) listMenus(c *gin.Context) {
	ctx := c.Request.Context()
	v, err := h.deps.RBAC.MenuAdmin(ctx, auth.MustFromCtx(ctx))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

type menuDisplayRequest struct {
	Titles map[string]string `json:"titles"`
	Icon   string            `json:"icon" binding:"max=64"`
	Hidden bool              `json:"hidden"`
	Sort   *int              `json:"sort"`
}

func (h *handlers) updateMenu(c *gin.Context) {
	var req menuDisplayRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	err := h.deps.RBAC.UpdateMenu(ctx, auth.MustFromCtx(ctx), c.Param("name"), rbac.MenuDisplayInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) resetMenu(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.deps.RBAC.ResetMenu(ctx, auth.MustFromCtx(ctx), c.Param("name")); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

type menuGroupRequest struct {
	Titles map[string]string `json:"titles" binding:"required"`
	Icon   string            `json:"icon" binding:"max=64"`
	Hidden bool              `json:"hidden"`
	Parent string            `json:"parent" binding:"max=64"`
	Sort   int               `json:"sort"`
}

func (h *handlers) createMenuGroup(c *gin.Context) {
	var req menuGroupRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	name, err := h.deps.RBAC.CreateMenuGroup(ctx, auth.MustFromCtx(ctx), rbac.MenuGroupInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"name": name})
}

func (h *handlers) deleteMenuGroup(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.deps.RBAC.DeleteMenuGroup(ctx, auth.MustFromCtx(ctx), c.Param("name")); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

type menuPositionRequest struct {
	Name   string `json:"name" binding:"required,max=64"`
	Parent string `json:"parent" binding:"max=64"`
	Sort   int    `json:"sort"`
}

type menuLayoutRequest struct {
	Items []menuPositionRequest `json:"items" binding:"required,max=2000,dive"`
}

func (h *handlers) saveMenuLayout(c *gin.Context) {
	var req menuLayoutRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]rbac.MenuPosition, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, rbac.MenuPosition(it))
	}
	ctx := c.Request.Context()
	if err := h.deps.RBAC.SaveMenuLayout(ctx, auth.MustFromCtx(ctx), items); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}
