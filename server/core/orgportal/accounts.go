package orgportal

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// RoleRef 是账号上的一个角色。
type RoleRef struct {
	ID     uint64 `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Status int    `json:"status"`
}

// AccountView 是子账号页面上的一个账号：账号资料加它的角色。主账号的 Owner 为 true。
type AccountView struct {
	org.Account
	Roles []RoleRef `json:"roles"`
}

// views 给账号补上角色（一次查完）。
func (h *handlers) views(ctx context.Context, list []org.Account) ([]AccountView, error) {
	ids := make([]uint64, 0, len(list))
	for _, a := range list {
		ids = append(ids, a.ID)
	}
	roles, err := h.k.deps.RBAC.UserRolesBatch(ctx, h.k.code, ids)
	if err != nil {
		return nil, err
	}
	out := make([]AccountView, 0, len(list))
	for _, a := range list {
		refs := make([]RoleRef, 0, len(roles[a.ID]))
		for _, r := range roles[a.ID] {
			refs = append(refs, RoleRef{ID: r.ID, Code: r.Code, Name: r.Name, Status: r.Status})
		}
		out = append(out, AccountView{Account: a, Roles: refs})
	}
	return out, nil
}

// view 读一个账号的视图；不在本主体回 404。
func (h *handlers) view(ctx context.Context, orgID, id uint64) (*AccountView, error) {
	a, err := h.k.deps.Orgs.Member(ctx, h.k.kind, orgID, id)
	if err != nil {
		return nil, err
	}
	v, err := h.views(ctx, []org.Account{*a})
	if err != nil {
		return nil, err
	}
	return &v[0], nil
}

func (h *handlers) listAccounts(c *gin.Context) {
	q := httpx.BindPage(c)
	status := queryStatus(c)
	ctx := c.Request.Context()
	_, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var (
		out   []AccountView
		total int64
	)
	// 账号和它们的角色在同一个只读快照里读（D-052）
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		list, n, err := h.k.deps.Orgs.Accounts(ctx, h.k.kind, orgID, status, q)
		if err != nil {
			return err
		}
		out, err = h.views(ctx, list)
		total = n
		return err
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, out, total, q.Page, q.PageSize)
}

func (h *handlers) getAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	_, orgID, err := principal(ctx)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := h.view(ctx, orgID, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

type createAccountRequest struct {
	Username    string   `json:"username" binding:"required,max=64"`
	Password    string   `json:"password" binding:"max=72"` // 空则生成，只在这次响应里返回
	DisplayName string   `json:"displayName" binding:"max=64"`
	Email       string   `json:"email" binding:"omitempty,email,max=128"`
	Phone       string   `json:"phone" binding:"max=32"`
	RoleIDs     []uint64 `json:"roleIds" binding:"max=20"`
	Sort        uint     `json:"sort" binding:"max=1000000"`
	Remark      string   `json:"remark" binding:"max=255"`
}

// createAccount 建子账号：下次登录必须改密；顺手分配角色等于做了一次"分配角色"，非主账号要有那个权限码。
func (h *handlers) createAccount(c *gin.Context) {
	var req createAccountRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	deps := h.k.deps
	_, orgID, err := principal(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	username := portal.NormalizeUsername(req.Username)
	in := org.MemberInput{
		Username: username, DisplayName: req.DisplayName, Email: req.Email, Phone: req.Phone, Sort: req.Sort, Remark: req.Remark,
	}
	plain, generated := req.Password, ""
	if plain == "" {
		p, err := deps.Auth.GeneratePassword()
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		plain, generated = p, p
	} else if err := deps.Auth.ValidatePassword(h.k.code, plain, username); err != nil {
		httpx.Fail(c, err)
		return
	}
	// 便宜的检查先做（D-068）：字段不合法、登录名已被占用的请求不去算哈希。锁内照样认定
	if err := deps.Orgs.CheckMember(c.Request.Context(), h.k.kind, orgID, in); err != nil {
		httpx.Fail(c, err)
		return
	}
	// 密码哈希慢：在拿主体行锁之前算好（D-066 第 3 条），受主体的上限和进程的闸门约束（D-068）
	hash, err := h.k.hashPassword(c.Request.Context(), orgID, plain)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var created *org.Account
	err = h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		orgID = oid
		if len(req.RoleIDs) > 0 && !actor.Super {
			ok, err := deps.RBAC.AllowedLocked(ctx, actor, h.k.Perm(PermAccountAssignRole))
			if err != nil {
				return err
			}
			if !ok {
				return httpx.ErrForbidden.WithFields(httpx.NewField("roleIds", "rbac.perm.notOwned", "assigning roles needs "+h.k.Perm(PermAccountAssignRole)))
			}
		}
		a, err := deps.Orgs.CreateMember(ctx, h.k.kind, oid, in, hash, actor.UserID)
		if err != nil {
			return err
		}
		created = a
		if len(req.RoleIDs) > 0 {
			return deps.RBAC.AssignUserRoles(ctx, actor, h.k.code, a.ID, req.RoleIDs)
		}
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := h.view(c.Request.Context(), orgID, created.ID)
	if err != nil {
		// 账号已经建好：读视图失败也把初始密码交出去，不然没人知道它
		v = &AccountView{Account: *created, Roles: []RoleRef{}}
	}
	httpx.OK(c, gin.H{"account": v, "initialPassword": generated})
}

// guardOwner 挡住员工对主账号的写操作（D-067 第 4 条）。target 是锁内读到的账号，actor 是锁内认定的身份。
func guardOwner(actor auth.Principal, target *org.Account) error {
	if target.Owner && !actor.Super {
		return httpx.ErrForbidden.WithFields(httpx.NewField("id", "org.account.ownerProtected", "only the owner account can change the owner account"))
	}
	return nil
}

type updateAccountRequest struct {
	DisplayName string `json:"displayName" binding:"required,max=64"`
	Email       string `json:"email" binding:"omitempty,email,max=128"`
	Phone       string `json:"phone" binding:"max=32"`
	Sort        uint   `json:"sort" binding:"max=1000000"`
	Remark      string `json:"remark" binding:"max=255"`
}

func (h *handlers) updateAccount(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req updateAccountRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	deps := h.k.deps
	var orgID uint64
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		orgID = oid
		target, err := deps.Orgs.LockMember(ctx, h.k.kind, oid, id)
		if err != nil {
			return err
		}
		if err := guardOwner(actor, target); err != nil {
			return err
		}
		return deps.Orgs.UpdateMember(ctx, h.k.kind, oid, id, org.MemberUpdate{
			DisplayName: req.DisplayName, Email: req.Email, Phone: req.Phone, Sort: req.Sort, Remark: req.Remark,
		}, actor.UserID)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := h.view(c.Request.Context(), orgID, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}

type statusRequest struct {
	Status *int `json:"status" binding:"required"`
}

// setAccountStatus 启停账号：不能停用自己；主账号在主体内停不了；停用时同一个事务吊销它的全部会话；
// 重新启用等于把它的角色交回去，非主账号只能启用角色都是自己能分配的账号（D-058）。
func (h *handlers) setAccountStatus(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req statusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	status := *req.Status
	if status != org.StatusEnabled && status != org.StatusDisabled {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("status", "org.status", "must be 0 or 1")))
		return
	}
	deps := h.k.deps
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		if status == org.StatusDisabled && id == actor.UserID {
			return httpx.ErrValidation.WithFields(httpx.NewField("id", "org.account.disableSelf", "you cannot disable your own account"))
		}
		target, err := deps.Orgs.LockMember(ctx, h.k.kind, oid, id)
		if err != nil {
			return err
		}
		if err := guardOwner(actor, target); err != nil {
			return err
		}
		if target.Owner && status == org.StatusDisabled {
			// 到这里只能是主账号自己（上面已经挡了员工和"停用自己"）；留一道防线，主账号在主体内停不了
			return httpx.New(httpx.CodeLastSuper, "")
		}
		if status == org.StatusEnabled && target.Status != org.StatusEnabled {
			if err := deps.RBAC.CheckEnableUser(ctx, actor, h.k.code, id); err != nil {
				return err
			}
		}
		if err := deps.Orgs.SetMemberStatus(ctx, h.k.kind, oid, id, status, actor.UserID); err != nil {
			return err
		}
		if status == org.StatusDisabled {
			if err := deps.Auth.RevokeUserSessions(ctx, h.k.code, id, auth.RevokeDisabled); err != nil {
				return err
			}
		}
		db.AfterCommit(ctx, func() { deps.Auth.ForgetAccount(h.k.code, id) })
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}

// errResetOwner：主账号的密码在主体内重置不了。
var errResetOwner = httpx.ErrForbidden.WithFields(httpx.NewField("id", "org.account.resetOwner", "the owner account's password can only be reset by the platform"))

// resetAccountPassword 主账号重置员工的密码：随机密码、下次登录必须改、吊销全部会话，新密码只在这次响应里。
// 主账号自己的密码这里重置不了（自己用 /auth/password 改，忘了由平台重置）。
func (h *handlers) resetAccountPassword(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	deps := h.k.deps
	_, orgID, err := principal(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	// 便宜的检查先做（D-068）：目标不在本主体、目标是主账号的请求不去算哈希。锁内照样认定
	target, err := deps.Orgs.Member(c.Request.Context(), h.k.kind, orgID, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if target.Owner {
		httpx.Fail(c, errResetOwner)
		return
	}
	plain, err := deps.Auth.GeneratePassword()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	hash, err := h.k.hashPassword(c.Request.Context(), orgID, plain)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	err = h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		// 路由已经是 RequireSuper，锁内按主体行再认定一次：途中被换下的主账号不能再重置
		if !actor.Super {
			return httpx.ErrForbidden
		}
		target, err := deps.Orgs.LockMember(ctx, h.k.kind, oid, id)
		if err != nil {
			return err
		}
		if target.Owner {
			return errResetOwner
		}
		if err := deps.Orgs.SetMemberPassword(ctx, h.k.kind, oid, id, hash, true, actor.UserID); err != nil {
			return err
		}
		if err := deps.Auth.RevokeUserSessions(ctx, h.k.code, id, auth.RevokePwdChange); err != nil {
			return err
		}
		db.AfterCommit(ctx, func() { deps.Auth.ForgetAccount(h.k.code, id) })
		return nil
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"initialPassword": plain})
}

type assignRolesRequest struct {
	RoleIDs []uint64 `json:"roleIds" binding:"max=20"`
}

// assignRoles 整体替换账号的角色。规则在 rbac 里（D-063 第 3 条）：员工改不了主账号的角色、分配不了别的主体的角色、
// 授不出自己没有的权限码和敏感权限码。
func (h *handlers) assignRoles(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req assignRolesRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	deps := h.k.deps
	var orgID uint64
	err := h.withActor(c, func(ctx context.Context, actor auth.Principal, oid uint64) error {
		orgID = oid
		target, err := deps.Orgs.LockMember(ctx, h.k.kind, oid, id)
		if err != nil {
			return err
		}
		if err := guardOwner(actor, target); err != nil {
			return err
		}
		return deps.RBAC.AssignUserRoles(ctx, actor, h.k.code, id, req.RoleIDs)
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	v, err := h.view(c.Request.Context(), orgID, id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}
