package system

// 部门、岗位接口（D-033）：只做绑定和输出，规则在 OrgService。

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

type deptRequest struct {
	ParentID     uint64  `json:"parentId"`
	Name         string  `json:"name" binding:"required,max=256"`
	LeaderUserID *uint64 `json:"leaderUserId"` // 不给：改部门时保持原来的负责人（D-055）
	Phone        string  `json:"phone" binding:"max=32"`
	Email        string  `json:"email" binding:"omitempty,email,max=128"`
	Status       *int    `json:"status"`
	Sort         uint    `json:"sort" binding:"max=1000000"`
	Remark       string  `json:"remark" binding:"max=255"`
}

func (h *handlers) listDepts(c *gin.Context) {
	ctx := c.Request.Context()
	out, err := h.org.ListDepts(ctx, auth.MustFromCtx(ctx))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h *handlers) createDept(c *gin.Context) {
	var req deptRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	d, err := h.org.CreateDept(ctx, auth.MustFromCtx(ctx), DeptInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, d)
}

func (h *handlers) updateDept(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req deptRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	d, err := h.org.UpdateDept(ctx, auth.MustFromCtx(ctx), id, DeptInput(req))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, d)
}

func (h *handlers) deleteDept(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (struct{}, error) {
		return struct{}{}, h.org.DeleteDept(ctx, id)
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) deptOptions(c *gin.Context) {
	out, err := h.org.DeptOptions(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

type postRequest struct {
	Code   string `json:"code" binding:"max=64"`
	Name   string `json:"name" binding:"required,max=256"`
	Status *int   `json:"status"`
	Sort   uint   `json:"sort" binding:"max=1000000"`
	Remark string `json:"remark" binding:"max=255"`
}

func (h *handlers) listPosts(c *gin.Context) {
	q := httpx.BindPage(c)
	var status *int
	if s := c.Query("status"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			status = &v
		}
	}
	out, total, err := h.org.ListPosts(c.Request.Context(), q, status)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, out, total, q.Page, q.PageSize)
}

func (h *handlers) createPost(c *gin.Context) {
	var req postRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	p, err := withActor(h, c, func(ctx context.Context, actor auth.Principal) (*Post, error) {
		return h.org.CreatePost(ctx, actor, PostInput(req))
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, p)
}

func (h *handlers) updatePost(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req postRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	p, err := withActor(h, c, func(ctx context.Context, actor auth.Principal) (*Post, error) {
		return h.org.UpdatePost(ctx, actor, id, PostInput(req))
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, p)
}

func (h *handlers) deletePost(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (struct{}, error) {
		return struct{}{}, h.org.DeletePost(ctx, id)
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) postOptions(c *gin.Context) {
	out, err := h.org.PostOptions(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}
