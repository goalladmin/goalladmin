package orgportal

import (
	"context"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/org"
)

// OverviewView 是概览：本主体的资料，以及调用者有查看权限的几项计数（没有权限的不在 Counts 里）。
type OverviewView struct {
	Org    org.Summary      `json:"org"`
	Owner  bool             `json:"owner"` // 调用者是不是主账号（概览身份标记）
	Counts map[string]int64 `json:"counts"`
}

// builtinCounts 是套件自带的计数：账号、在线会话、角色。
func (h *handlers) builtinCounts(orgID uint64) []Count {
	k := h.k
	return []Count{
		{Key: "accounts", Perm: k.Perm(PermAccountList), Fn: func(ctx context.Context) (int64, error) {
			_, n, err := k.deps.Orgs.Accounts(ctx, k.kind, orgID, nil, httpx.PageQuery{Page: 1, PageSize: 1})
			return n, err
		}},
		{Key: "sessions", Perm: k.Perm(PermSessionList), Fn: func(ctx context.Context) (int64, error) {
			_, n, err := k.deps.Orgs.Sessions(ctx, k.kind, orgID, httpx.PageQuery{Page: 1, PageSize: 1})
			return n, err
		}},
		{Key: "roles", Perm: k.Perm(PermRoleList), Fn: func(ctx context.Context) (int64, error) {
			roles, err := k.deps.RBAC.Roles(ctx, k.code)
			return int64(len(roles)), err
		}},
	}
}

func (h *handlers) overview(c *gin.Context) {
	ctx := c.Request.Context()
	p, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	perms, err := h.k.deps.RBAC.Perms(ctx, p)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	allowed := func(perm string) bool { return perm == "" || p.Super || slices.Contains(perms, perm) }
	out := OverviewView{Owner: p.Super, Counts: map[string]int64{}}
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		s, err := h.k.deps.Orgs.Summary(ctx, h.k.kind, orgID)
		if err != nil {
			return err
		}
		out.Org = *s
		for _, cnt := range append(h.builtinCounts(orgID), h.k.counts...) {
			if !allowed(cnt.Perm) {
				continue
			}
			n, err := cnt.Fn(ctx)
			if err != nil {
				return err
			}
			out.Counts[cnt.Key] = n
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// ---- 主体的 IP 白名单（D-062 第 6 条）：只有主账号 ----

// allowView 是一份白名单和调用者当前的地址（页面提示"你现在的 IP"，设名单时不要漏了它）。
type allowView struct {
	Items  []ipacl.Rule `json:"items"`
	YourIP string       `json:"yourIp"`
}

type allowRequest struct {
	Items []ipacl.Entry `json:"items"`
}

func (h *handlers) getIPAllow(c *gin.Context) {
	ctx := c.Request.Context()
	_, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	items, err := h.k.deps.IPACL.ListAllow(ctx, ipacl.OrgTarget(h.k.code, orgID))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if items == nil {
		items = []ipacl.Rule{}
	}
	httpx.OK(c, allowView{Items: items, YourIP: c.ClientIP()})
}

// setIPAllow 整份替换本主体的白名单。名单对主账号自己也生效：不为空时必须包含主账号当前的地址（D-062 第 5 条）。
func (h *handlers) setIPAllow(c *gin.Context) {
	var req allowRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ip := c.ClientIP()
	var items []ipacl.Rule
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		if !actor.Super {
			return httpx.ErrForbidden
		}
		var err error
		items, err = h.k.deps.IPACL.SetAllow(ctx, ipacl.OrgTarget(h.k.code, oid), req.Items, actor.UserID, ip)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if items == nil {
		items = []ipacl.Rule{}
	}
	httpx.OK(c, allowView{Items: items, YourIP: ip})
}

// ---- 个人中心：只读写调用者本人（D-038、D-058）----

// ProfileView 是个人中心的本人视图。只有本人的资料：管理者写在这个账号上的备注、排序和状态不给本人看（D-099，
// 和平台端的个人中心一样，D-038）。
type ProfileView struct {
	ID            uint64     `json:"id"`
	OrgID         uint64     `json:"orgId"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"displayName"`
	Email         string     `json:"email"`
	Phone         string     `json:"phone"`
	Avatar        string     `json:"avatar"`
	Owner         bool       `json:"owner"`
	MustChangePwd bool       `json:"mustChangePwd"`
	LastLoginAt   *time.Time `json:"lastLoginAt"`
	LastLoginIP   string     `json:"lastLoginIp"`
	CreatedAt     time.Time  `json:"createdAt"`
	Roles         []RoleRef  `json:"roles"`
	OrgCode       string     `json:"orgCode"`
	OrgName       string     `json:"orgName"`
	Sessions      int64      `json:"sessions"` // 当前有效的会话数，含本次
}

func (h *handlers) profile(c *gin.Context) {
	ctx := c.Request.Context()
	p, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var out ProfileView
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		v, err := h.view(ctx, orgID, p.UserID)
		if err != nil {
			return err
		}
		s, err := h.k.deps.Orgs.Summary(ctx, h.k.kind, orgID)
		if err != nil {
			return err
		}
		_, n, err := h.k.deps.Auth.ListSessions(ctx, h.k.code, p.UserID, 1, 1)
		if err != nil {
			return err
		}
		a := v.Account
		out = ProfileView{
			ID: a.ID, OrgID: a.OrgID, Username: a.Username, DisplayName: a.DisplayName, Email: a.Email, Phone: a.Phone, Avatar: a.Avatar,
			Owner: a.Owner, MustChangePwd: a.MustChangePwd, LastLoginAt: a.LastLoginAt, LastLoginIP: a.LastLoginIP, CreatedAt: a.CreatedAt,
			Roles: v.Roles, OrgCode: s.Code, OrgName: s.Name, Sessions: n,
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

type profileRequest struct {
	DisplayName string `json:"displayName" binding:"required,max=64"`
	Email       string `json:"email" binding:"omitempty,email,max=128"`
	Phone       string `json:"phone" binding:"max=32"`
}

func (h *handlers) updateProfile(c *gin.Context) {
	var req profileRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	p, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	err = h.k.deps.RBAC.WithSelf(ctx, p, func(ctx context.Context) error {
		if err := h.k.deps.Orgs.UpdateProfile(ctx, h.k.kind, orgID, p.UserID, org.ProfileInput{DisplayName: req.DisplayName, Email: req.Email, Phone: req.Phone}); err != nil {
			return err
		}
		db.AfterCommit(ctx, func() { h.k.deps.Auth.ForgetAccount(h.k.code, p.UserID) })
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

type avatarRequest struct {
	Preset string `json:"preset" binding:"max=32"` // 内置头像的名字；空表示清除
}

// setAvatar 本人选内置头像或清除。这一版主体端不能上传（D-067）。
func (h *handlers) setAvatar(c *gin.Context) {
	var req avatarRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	p, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var value string
	err = h.k.deps.RBAC.WithSelf(ctx, p, func(ctx context.Context) error {
		var err error
		if value, err = h.k.deps.Orgs.SetAvatar(ctx, h.k.kind, orgID, p.UserID, req.Preset); err != nil {
			return err
		}
		db.AfterCommit(ctx, func() { h.k.deps.Auth.ForgetAccount(h.k.code, p.UserID) })
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"avatar": value})
}

// revokeOtherSessions 让本人除当前会话外的全部会话下线，返回下线的会话数。
func (h *handlers) revokeOtherSessions(c *gin.Context) {
	ctx := c.Request.Context()
	p, _, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var n int64
	err = h.k.deps.RBAC.WithSelf(ctx, p, func(ctx context.Context) error {
		var err error
		n, err = h.k.deps.Auth.RevokeOtherSessions(ctx, h.k.code, p.UserID, p.SessionID, auth.RevokeLogout)
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"revoked": n})
}
