package system

// IP 黑名单与白名单的平台端管理（D-062）：黑名单（对三个程序都生效）、平台端白名单、平台账号的白名单。
// 名单的匹配和存储在内核 core/ipacl；这里只管谁能改、改的时候不把自己锁在外面。

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
)

// 黑名单的有效期上限：一年。永久封禁传 0。
const maxDenyMinutes = 365 * 24 * 60

type denyRequest struct {
	CIDR      string `json:"cidr"`
	ExpiresIn int    `json:"expiresIn"` // 分钟；0 表示永久
	Remark    string `json:"remark"`
}

type allowRequest struct {
	Items []ipacl.Entry `json:"items"`
}

// allowView 是一份白名单和调用者当前的地址（页面提示"你现在的 IP"，设名单时不要漏了它）。
type allowView struct {
	Items  []ipacl.Rule `json:"items"`
	YourIP string       `json:"yourIp"`
}

func (h *handlers) acl(c *gin.Context) (*ipacl.Service, bool) {
	if h.deps.IPACL == nil {
		httpx.Fail(c, httpx.ErrUnavailable)
		return nil, false
	}
	return h.deps.IPACL, true
}

// listIPDeny 分页列出黑名单；includeExpired=1 时连已过期的一起。
func (h *handlers) listIPDeny(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	q := httpx.BindPage(c)
	f := ipacl.DenyFilter{Keyword: q.Keyword, IncludeExpired: c.Query("includeExpired") == "1"}
	out, total, err := acl.ListDeny(c.Request.Context(), f, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, out, total, q.Page, q.PageSize)
}

// addIPDeny 加一条黑名单（同一网段再加一次是更新有效期）。网段包含操作人自己当前的地址时拒绝。
func (h *handlers) addIPDeny(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	var req denyRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.ExpiresIn < 0 || req.ExpiresIn > maxDenyMinutes {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("expiresIn", "ipacl.expiresIn", "0 (permanent) or 1-525600 minutes", "max", maxDenyMinutes)))
		return
	}
	ip := c.ClientIP()
	r, err := withActor(h, c, func(ctx context.Context, actor auth.Principal) (ipacl.Rule, error) {
		in := ipacl.DenyInput{CIDR: req.CIDR, ExpiresIn: time.Duration(req.ExpiresIn) * time.Minute, Remark: req.Remark}
		return acl.AddDeny(ctx, in, actor.UserID, ip)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, r)
}

// removeIPDeny 删除一条黑名单。
func (h *handlers) removeIPDeny(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	if _, err := withActor(h, c, func(ctx context.Context, _ auth.Principal) (ipacl.Rule, error) {
		return acl.RemoveDeny(ctx, id)
	}); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// getIPAllow 返回平台端白名单。
func (h *handlers) getIPAllow(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	items, err := acl.ListAllow(c.Request.Context(), ipacl.PortalTarget(PortalCode))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, allowView{Items: items, YourIP: c.ClientIP()})
}

// setIPAllow 整份替换平台端白名单；为空表示不限制。名单不为空时必须包含操作人当前的地址（这份名单对他自己生效）。
func (h *handlers) setIPAllow(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	var req allowRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ip := c.ClientIP()
	items, err := withActor(h, c, func(ctx context.Context, actor auth.Principal) ([]ipacl.Rule, error) {
		return acl.SetAllow(ctx, ipacl.PortalTarget(PortalCode), req.Items, actor.UserID, ip)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, allowView{Items: items, YourIP: ip})
}

// getUserIPAllow 返回某个平台账号的白名单：目标要在操作人"查看用户"的数据范围内（D-039）。
// 账号白名单不单独做数据资源：它是用户资料的一部分，按查看、修改用户的范围约束，不改变"挪部门要每个用户权限码
// 都是全部范围"的规则。
func (h *handlers) getUserIPAllow(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	actor := auth.MustFromCtx(ctx)
	var items []ipacl.Rule
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		u, err := h.users.repo.FindByID(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := h.users.checkTarget(ctx, actor, PermUserList, u); err != nil {
			return err
		}
		items, err = acl.ListAllow(ctx, ipacl.UserTarget(PortalCode, 0, id))
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, allowView{Items: items, YourIP: c.ClientIP()})
}

// setUserIPAllow 整份替换某个平台账号的白名单：目标要在操作人"修改用户"的数据范围内（D-039），非超管不能改超管账号（D-035）；
// 改的是自己的账号时，名单必须包含自己当前的地址。
func (h *handlers) setUserIPAllow(c *gin.Context) {
	acl, ok := h.acl(c)
	if !ok {
		return
	}
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req allowRequest
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ip := c.ClientIP()
	items, err := withActor(h, c, func(ctx context.Context, actor auth.Principal) ([]ipacl.Rule, error) {
		u, err := h.users.repo.FindByIDForUpdate(ctx, id)
		if err != nil {
			return nil, notFound(err)
		}
		if err := h.users.checkWriteTarget(ctx, actor, PermUserUpdate, u); err != nil {
			return nil, err
		}
		if err := h.users.guardSuperTarget(ctx, actor, id); err != nil {
			return nil, err
		}
		guard := ""
		if id == actor.UserID {
			guard = ip
		}
		return acl.SetAllow(ctx, ipacl.UserTarget(PortalCode, 0, id), req.Items, actor.UserID, guard)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, allowView{Items: items, YourIP: ip})
}
