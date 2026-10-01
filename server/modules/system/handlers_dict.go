package system

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/dict"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 字典管理（docs/decisions.md D-023）。规则都在 core/dict 的服务里，这里只做绑定和输出。

func (h *handlers) dictSvc(c *gin.Context) (*dict.Service, bool) {
	if h.deps.Dict == nil {
		httpx.Fail(c, httpx.ErrUnavailable)
		return nil, false
	}
	return h.deps.Dict, true
}

func pathItemID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("itemId"), 10, 64)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return 0, false
	}
	return id, true
}

func (h *handlers) listDicts(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	q := httpx.BindPage(c)
	list, total, err := svc.ListDicts(c.Request.Context(), q, dict.DictFilter{Portal: c.Query("portal"), Source: c.Query("source")})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, list, total, q.Page, q.PageSize)
}

func (h *handlers) getDict(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	v, err := svc.GetDict(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

type dictRequest struct {
	Code      string            `json:"code" binding:"max=64"`
	Portal    string            `json:"portal" binding:"max=32"`
	Name      string            `json:"name" binding:"required,max=64"`
	NameI18n  map[string]string `json:"nameI18n"`
	ValueType string            `json:"valueType" binding:"omitempty,oneof=string int"`
	Status    *int              `json:"status"`
	Sort      int               `json:"sort" binding:"min=0,max=1000000"`
	Remark    string            `json:"remark" binding:"max=255"`
}

func (h *handlers) createDict(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	var req dictRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (*dict.DictInfo, error) {
		return svc.CreateDict(ctx, dict.DictInput{
			Code: req.Code, Portal: req.Portal, Name: req.Name, NameI18n: req.NameI18n, ValueType: dict.ValueType(req.ValueType),
			Status: req.Status, Sort: req.Sort, Remark: req.Remark,
		})
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

func (h *handlers) updateDict(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req dictRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (*dict.DictInfo, error) {
		return svc.UpdateDict(ctx, id, dict.DictUpdate{
			Portal: req.Portal, Name: req.Name, NameI18n: req.NameI18n, Status: req.Status, Sort: req.Sort, Remark: req.Remark,
		})
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

func (h *handlers) deleteDict(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (struct{}, error) {
		return struct{}{}, svc.DeleteDict(ctx, id)
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

type dictItemRequest struct {
	ParentID  uint64            `json:"parentId"`
	Value     string            `json:"value" binding:"required,max=64"`
	Label     string            `json:"label" binding:"required,max=128"`
	LabelI18n map[string]string `json:"labelI18n"`
	Color     string            `json:"color" binding:"max=16"`
	Extra     string            `json:"extra" binding:"max=255"`
	Status    *int              `json:"status"`
	Sort      int               `json:"sort" binding:"min=0,max=1000000"`
	Remark    string            `json:"remark" binding:"max=255"`
}

func (h *handlers) createDictItem(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req dictItemRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (*dict.ItemInfo, error) {
		return svc.CreateItem(ctx, id, dict.ItemInput{
			ParentID: req.ParentID, Value: req.Value, Label: req.Label, LabelI18n: req.LabelI18n, Color: req.Color,
			Extra: req.Extra, Status: req.Status, Sort: req.Sort, Remark: req.Remark,
		})
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

func (h *handlers) updateDictItem(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	itemID, ok := pathItemID(c)
	if !ok {
		return
	}
	var req dictItemRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (*dict.ItemInfo, error) {
		return svc.UpdateItem(ctx, id, itemID, dict.ItemUpdate{
			Value: req.Value, Label: req.Label, LabelI18n: req.LabelI18n, Color: req.Color, Extra: req.Extra,
			Status: req.Status, Sort: req.Sort, Remark: req.Remark,
		})
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

func (h *handlers) deleteDictItem(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	itemID, ok := pathItemID(c)
	if !ok {
		return
	}
	if _, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (struct{}, error) {
		return struct{}{}, svc.DeleteItem(ctx, id, itemID)
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func (h *handlers) resetDictItem(c *gin.Context) {
	svc, ok := h.dictSvc(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	itemID, ok := pathItemID(c)
	if !ok {
		return
	}
	v, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (*dict.ItemInfo, error) {
		return svc.ResetItem(ctx, id, itemID)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

// portalOptions 返回已注册的端代号，供字典的"所属端"下拉使用。
func (h *handlers) portalOptions(c *gin.Context) {
	all := h.deps.Portals.All()
	out := make([]string, 0, len(all))
	for _, p := range all {
		out = append(out, p.Code)
	}
	httpx.OK(c, out)
}
