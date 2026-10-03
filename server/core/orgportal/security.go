package orgportal

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/org"
)

func (h *handlers) listIPDeny(c *gin.Context) {
	ctx := c.Request.Context()
	_, oid, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	q := httpx.BindPage(c)
	items, total, err := h.k.deps.IPACL.ListOrgDeny(ctx, ipacl.OrgTarget(h.k.code, oid), ipacl.DenyFilter{Keyword: q.Keyword, IncludeExpired: c.Query("includeExpired") == "1"}, q.Page, q.PageSize)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, items, total, q.Page, q.PageSize)
}

func (h *handlers) addIPDeny(c *gin.Context) {
	var req struct {
		CIDR      string `json:"cidr"`
		ExpiresIn int    `json:"expiresIn"`
		Remark    string `json:"remark"`
	}
	if err := httpx.BindJSONStrict(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	const maxMinutes = 365 * 24 * 60
	if req.ExpiresIn < 0 || req.ExpiresIn > maxMinutes {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("expiresIn", "ipacl.expiresIn", "0 (permanent) or 1-525600 minutes", "max", maxMinutes)))
		return
	}
	var out ipacl.Rule
	err := h.withActor(c, func(ctx context.Context, p auth.Principal, oid uint64) error {
		if !p.Super {
			return httpx.ErrForbidden
		}
		var err error
		out, err = h.k.deps.IPACL.AddOrgDeny(ctx, ipacl.OrgTarget(h.k.code, oid), ipacl.DenyInput{CIDR: req.CIDR, Remark: req.Remark, ExpiresIn: time.Duration(req.ExpiresIn) * time.Minute}, p.UserID, c.ClientIP())
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h *handlers) removeIPDeny(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	err := h.withActor(c, func(ctx context.Context, p auth.Principal, oid uint64) error {
		if !p.Super {
			return httpx.ErrForbidden
		}
		_, err := h.k.deps.IPACL.RemoveOrgDeny(ctx, ipacl.OrgTarget(h.k.code, oid), id)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

func dashboardInt(raw string, def, lo, hi int) int {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}

func (h *handlers) dashboard(c *gin.Context) {
	ctx := c.Request.Context()
	p, oid, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var out org.Dashboard
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		allowed, err := h.k.deps.RBAC.AllowedInSnapshot(ctx, p, h.k.Perm(PermDashboardView))
		if err != nil {
			return err
		}
		if !allowed {
			return httpx.ErrForbidden
		}
		all, err := h.k.deps.RBAC.AllowedInSnapshot(ctx, p, h.k.Perm(PermAccountList))
		if err != nil {
			return err
		}
		rankID := p.UserID
		if all {
			rankID = 0
		}
		out, err = h.k.deps.Orgs.DataCenter(ctx, h.k.kind, oid, dashboardInt(c.Query("days"), 30, 1, 90), dashboardInt(c.Query("tz"), 0, -840, 840), rankID)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}
