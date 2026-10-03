package system

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// UserService 是用户管理的业务逻辑。所有安全规则在这一层，handler 只做绑定和输出。
type UserService struct {
	deps *app.Deps
	repo *UserRepo
	org  *OrgService
	// avatarSlots 限制同时处理的头像上传数（D-040）：解码一张大图要几十 MB 内存
	avatarSlots chan struct{}
	avatarMu    sync.Mutex
	avatarUsers map[uint64]bool
}

// 同时处理的头像上传数上限。
const avatarConcurrency = 2

// NewUserService 创建服务。
func NewUserService(deps *app.Deps, repo *UserRepo, org *OrgService) *UserService {
	return &UserService{deps: deps, repo: repo, org: org, avatarSlots: make(chan struct{}, avatarConcurrency), avatarUsers: map[uint64]bool{}}
}

// UserView 是对外的用户视图（不含密码哈希）。
type UserView struct {
	ID            uint64     `json:"id"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"displayName"`
	Email         string     `json:"email"`
	Phone         string     `json:"phone"`
	Avatar        string     `json:"avatar"`
	Bio           string     `json:"bio"` // 个人简介，本人在个人中心维护（D-038）
	Status        int        `json:"status"`
	MustChangePwd bool       `json:"mustChangePwd"`
	LastLoginAt   *time.Time `json:"lastLoginAt"`
	LastLoginIP   string     `json:"lastLoginIp"`
	Sort          uint       `json:"sort"`
	Remark        string     `json:"remark"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	Roles         []RoleRef  `json:"roles"`
	DeptID        uint64     `json:"deptId"`   // 所属部门（D-033），0 表示未分配
	DeptName      string     `json:"deptName"` // 部门已不存在时为空
	Posts         []PostRef  `json:"posts"`
}

// RoleRef 是用户视图里的角色引用。
type RoleRef struct {
	ID      uint64 `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	IsSuper bool   `json:"isSuper"`
}

func toView(u *User, roles []rbac.Role, deptName string, posts []PostRef) UserView {
	if posts == nil {
		posts = []PostRef{}
	}
	refs := make([]RoleRef, 0, len(roles))
	for _, r := range roles {
		refs = append(refs, RoleRef{ID: r.ID, Code: r.Code, Name: r.Name, IsSuper: r.IsSuper})
	}
	return UserView{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone, Avatar: u.Avatar, Bio: u.Bio,
		Status: u.Status, MustChangePwd: u.MustChangePwd, LastLoginAt: u.LastLoginAt, LastLoginIP: u.LastLoginIP,
		Sort: u.Sort, Remark: u.Remark, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, Roles: refs,
		DeptID: u.DeptID, DeptName: deptName, Posts: posts,
	}
}

// scope 返回操作人对用户资源做某件事（perm）时的数据范围（D-039）。调用方把它和读数据放进同一个 db.Snapshot（D-054）。
func (s *UserService) scope(ctx context.Context, actor auth.Principal, perm string) (rbac.DataFilter, error) {
	return s.deps.RBAC.DataFilter(ctx, actor, DataUser, perm)
}

// checkTarget 确认目标用户在操作人做这件事的数据范围内；不在就当作不存在（404），不让人试探出这个人在不在。
func (s *UserService) checkTarget(ctx context.Context, actor auth.Principal, perm string, u *User) error {
	f, err := s.scope(ctx, actor, perm)
	if err != nil {
		return err
	}
	if !f.Allows(u.DeptID, u.ID) {
		return httpx.ErrNotFound
	}
	return nil
}

// checkWriteTarget 是写操作用的 checkTarget：范围在锁内按已提交的授权算（D-047），必须在 WithActor 里调用。
func (s *UserService) checkWriteTarget(ctx context.Context, actor auth.Principal, perm string, u *User) error {
	f, err := s.deps.RBAC.DataFilterLocked(ctx, actor, DataUser, perm)
	if err != nil {
		return err
	}
	if !f.Allows(u.DeptID, u.ID) {
		return httpx.ErrNotFound
	}
	return nil
}

// checkNewDept 确认新部门在操作人做这件事的数据范围内（新建用户、改部门时，D-039）。
// 范围在锁内按已提交的授权算（D-047），必须在 WithActor 里调用。
func (s *UserService) checkNewDept(ctx context.Context, actor auth.Principal, perm string, deptID uint64) error {
	f, err := s.deps.RBAC.DataFilterLocked(ctx, actor, DataUser, perm)
	if err != nil {
		return err
	}
	if !f.AllowsDept(deptID) {
		return httpx.ErrForbidden.WithFields(httpx.NewField("deptId", "system.user.deptOutOfScope", "the department is outside your data scope"))
	}
	return nil
}

// List 分页列出操作人数据范围内的用户（D-039）。deptID 非 0 时只列这个部门的用户，withChildren 为 true 时连下级部门一起。
// 范围、用户行、角色、部门名、岗位在同一个只读快照里读（D-052），列表里每一行的资料都是同一时刻的。
func (s *UserService) List(ctx context.Context, actor auth.Principal, q httpx.PageQuery, status *int, deptID uint64, withChildren bool) ([]UserView, int64, error) {
	var (
		out   []UserView
		total int64
	)
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		var err error
		out, total, err = s.list(ctx, actor, q, status, deptID, withChildren)
		return err
	})
	return out, total, err
}

func (s *UserService) list(ctx context.Context, actor auth.Principal, q httpx.PageQuery, status *int, deptID uint64, withChildren bool) ([]UserView, int64, error) {
	f, err := s.scope(ctx, actor, PermUserList)
	if err != nil {
		return nil, 0, err
	}
	var depts []uint64
	if deptID != 0 {
		depts = []uint64{deptID}
		if withChildren {
			var err error
			if depts, err = s.org.deptSubtree(ctx, deptID); err != nil {
				return nil, 0, err
			}
		}
	}
	users, total, err := s.repo.List(ctx, q, status, depts, f)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]uint64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	roles, err := s.deps.RBAC.UserRolesBatch(ctx, PortalCode, ids)
	if err != nil {
		return nil, 0, err
	}
	deptNames, posts, err := s.org.orgOf(ctx, users)
	if err != nil {
		return nil, 0, err
	}
	out := make([]UserView, 0, len(users))
	for i := range users {
		u := &users[i]
		out = append(out, toView(u, roles[u.ID], deptNames[u.DeptID], posts[u.ID]))
	}
	return out, total, nil
}

// GetScoped 取操作人数据范围内的单个用户；范围外的当作不存在（D-039）。
// 检查范围用的那一行就是返回的那一行，账号、角色、部门、岗位在同一个只读快照里读（D-051）：
// 检查之后再按 ID 重读一遍的话，两次读之间有人把他调出了范围，返回的就是范围外的资料。
func (s *UserService) GetScoped(ctx context.Context, actor auth.Principal, id uint64) (*UserView, error) {
	var v *UserView
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		u, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkTarget(ctx, actor, PermUserList, u); err != nil {
			return err
		}
		v, err = s.viewOf(ctx, u)
		return err
	})
	return v, err
}

// viewAfterWrite 写操作成功后的回显：只有目标在操作人"查看用户"的范围内才给完整资料（D-039）——
// 修改范围可能比查看范围宽，不能借写操作把看不到的人的资料带出来。看不到时返回 nil。
//
// 范围和目标在同一个只读快照里读（D-054）：判断范围的那一行就是返回的那一行（D-051），算范围用的角色、部门也和它是
// 同一时刻的。超管身份也按库认定（D-048），不沿用请求开始时的 actor.Super：超管在途中被降级，旧身份的"全部"会把
// 范围外的人的资料带出来。服务器上的命令（UserID 为 0）没有可以重读的角色，身份照调用方给的算。
func (s *UserService) viewAfterWrite(ctx context.Context, actor auth.Principal, id uint64) (*UserView, error) {
	p := actor
	if p.UserID != 0 {
		p.Super = false
	}
	var v *UserView
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		u, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return notFound(err)
		}
		f, err := s.scope(ctx, p, PermUserList)
		if err != nil {
			return err
		}
		if !f.Allows(u.DeptID, u.ID) {
			return nil
		}
		v, err = s.viewOf(ctx, u)
		return err
	})
	return v, err
}

// viewOf 用已经读到（并检查过）的账号行拼出完整资料；角色、部门名、岗位按这一行去读，不再按 ID 重读账号。
func (s *UserService) viewOf(ctx context.Context, u *User) (*UserView, error) {
	roles, err := s.deps.RBAC.UserRoles(ctx, PortalCode, u.ID)
	if err != nil {
		return nil, err
	}
	deptNames, posts, err := s.org.orgOf(ctx, []User{*u})
	if err != nil {
		return nil, err
	}
	v := toView(u, roles, deptNames[u.DeptID], posts[u.ID])
	return &v, nil
}

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,63}$`)

// ValidUsername 报告归一化后的登录名是否合法：3–64 位，小写字母开头，只含小写字母、数字、'_'、'.'、'-'。
// 创建用户的接口和 admin create 命令用同一条规则。
func ValidUsername(s string) bool { return usernameRe.MatchString(s) }

// CreateInput 是创建用户的入参。
type CreateInput struct {
	Username    string
	Password    string // 空则生成
	DisplayName string
	Email       string
	Phone       string
	RoleIDs     []uint64
	DeptID      uint64   // 所属部门（D-033），0 表示不分配
	PostIDs     []uint64 // 岗位（D-033）
	Sort        uint
	Remark      string
}

// Create 创建用户：密码由管理员提供或系统生成，下次登录必须改密。返回生成的初始密码（管理员提供时为空）。
func (s *UserService) Create(ctx context.Context, actor auth.Principal, in CreateInput) (*UserView, string, error) {
	if len(in.PostIDs) > maxUserPosts {
		return nil, "", httpx.ErrValidation.WithFields(httpx.NewField("postIds", "system.user.posts", "at most 20 posts", "max", maxUserPosts))
	}
	if len(in.RoleIDs) > rbac.MaxRolesPerUser {
		return nil, "", httpx.ErrValidation.WithFields(httpx.NewField("roleIds", "validation.max", "too many roles", "param", rbac.MaxRolesPerUser))
	}
	in.Username = portal.NormalizeUsername(in.Username)
	if !ValidUsername(in.Username) {
		return nil, "", httpx.ErrValidation.WithFields(httpx.NewField("username", "system.user.username", "3-64 chars: lowercase letters, digits, '_', '.', '-', starting with a letter"))
	}
	generated := ""
	plain := in.Password
	if plain == "" {
		p, err := s.deps.Auth.GeneratePassword()
		if err != nil {
			return nil, "", err
		}
		plain, generated = p, p
	} else if err := s.deps.Auth.ValidatePassword(PortalCode, plain, in.Username); err != nil {
		return nil, "", err
	}
	if in.DisplayName == "" {
		in.DisplayName = in.Username
	}
	// 显示名和本人在个人中心改的同一套字符校验（D-058）：不能有控制字符和不可见字符
	name, err := cleanName("displayName", in.DisplayName)
	if err != nil {
		return nil, "", err
	}
	in.DisplayName = name
	// 字段校验都过了再算哈希（D-068）：密码哈希慢，和登录核对密码占同一个并发上限，满了回 429
	hash, err := s.deps.Auth.HashPassword(PortalCode, plain)
	if err != nil {
		return nil, "", err
	}
	u := &User{
		Username: in.Username, PasswordHash: hash, DisplayName: in.DisplayName, Email: in.Email, Phone: in.Phone,
		MustChangePwd: true, Status: StatusEnabled, Sort: in.Sort, Remark: in.Remark,
		CreatedBy: actor.UserID, UpdatedBy: actor.UserID,
	}
	// 整个建用户放在超管锁里：顺手分配角色时要拿这把锁，而写部门会先给部门行加共享锁；先拿超管锁，
	// 加锁顺序就和改部门（超管锁 → 部门表）一致，不会互相等待（D-045）
	err = s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		// 新用户的部门必须在操作人的范围内（D-039）：不能把人建到自己看不见的地方。
		// 这些判断都在锁内、按认定后的身份和已提交的授权做（D-047）
		if err := s.checkNewDept(ctx, actor, PermUserCreate, in.DeptID); err != nil {
			return err
		}
		// 建用户时顺手分配角色，等于做了一次"分配角色"：非超管要有这个权限码，新部门也要在它的范围内
		if len(in.RoleIDs) > 0 && !actor.Super {
			ok, err := s.deps.RBAC.AllowedLocked(ctx, actor, PermUserAssignRole)
			if err != nil {
				return err
			}
			if !ok {
				return httpx.ErrForbidden.WithFields(httpx.NewField("roleIds", "rbac.perm.notOwned", "assigning roles needs "+PermUserAssignRole))
			}
			if err := s.checkNewDept(ctx, actor, PermUserAssignRole, in.DeptID); err != nil {
				return err
			}
		}
		if err := s.repo.Create(ctx, u); err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return httpx.NewKey(httpx.CodeConflict, "system.user.usernameTaken", "username already exists").WithCause(err)
			}
			return err
		}
		if err := s.org.assignOrg(ctx, u, &in.DeptID, &in.PostIDs); err != nil {
			return err
		}
		if len(in.RoleIDs) > 0 {
			return s.deps.RBAC.AssignUserRoles(ctx, actor, PortalCode, u.ID, in.RoleIDs)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	v, err := s.viewAfterWrite(ctx, actor, u.ID)
	if err == nil && v == nil {
		// 建在了"查看用户"的范围外：只回显登录名（是操作人自己填的），好让他把初始密码交出去
		v = &UserView{ID: u.ID, Username: u.Username, Roles: []RoleRef{}, Posts: []PostRef{}}
	}
	return v, generated, err
}

// UpdateInput 是更新用户资料的入参。DeptID、PostIDs 为 nil 时不改（D-033）。
type UpdateInput struct {
	DisplayName string
	Email       string
	Phone       string
	Bio         string
	DeptID      *uint64
	PostIDs     *[]uint64
	Sort        uint
	Remark      string
}

// Update 更新资料和部门、岗位（不含密码、状态、角色）。
func (s *UserService) Update(ctx context.Context, actor auth.Principal, id uint64, in UpdateInput) (*UserView, error) {
	if in.PostIDs != nil && len(*in.PostIDs) > maxUserPosts {
		return nil, httpx.ErrValidation.WithFields(httpx.NewField("postIds", "system.user.posts", "at most 20 posts", "max", maxUserPosts))
	}
	if in.DisplayName == "" {
		return nil, httpx.ErrValidation.WithFields(httpx.NewField("displayName", "validation.required", "required"))
	}
	// 显示名、简介和本人在个人中心改的同一套字符校验（D-058）
	name, err := cleanName("displayName", in.DisplayName)
	if err != nil {
		return nil, err
	}
	bio, err := cleanBio(in.Bio)
	if err != nil {
		return nil, err
	}
	in.DisplayName, in.Bio = name, bio
	// 先拿超管锁（D-035）：和"提升为超管"串行，判断"是不是超管账号"时看到的是已提交的最新结果；
	// 再锁用户行：同一用户的并发编辑排队，岗位的"原来就有"按最新提交的数据判断
	err = s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		u, err := s.repo.FindByIDForUpdate(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkWriteTarget(ctx, actor, PermUserUpdate, u); err != nil {
			return err
		}
		if err := s.guardSuperTarget(ctx, actor, id); err != nil {
			return err
		}
		if in.DeptID != nil && *in.DeptID != u.DeptID {
			// 非超管不能改自己的部门：改了就能扩大自己的范围（D-039）
			if !actor.Super && id == actor.UserID {
				return httpx.ErrForbidden.WithFields(httpx.NewField("deptId", "system.user.ownDept", "you cannot change your own department"))
			}
			if err := s.checkNewDept(ctx, actor, PermUserUpdate, *in.DeptID); err != nil {
				return err
			}
			for _, d := range s.deps.RBAC.DataResources(PortalCode) {
				if d.Code != DataUser {
					continue
				}
				for _, perm := range d.Perms {
					f, err := s.deps.RBAC.DataFilterLocked(ctx, actor, DataUser, perm)
					if err != nil {
						return err
					}
					if !f.All() && f.Allows(u.DeptID, id) != f.Allows(*in.DeptID, id) {
						return httpx.ErrForbidden.WithFields(httpx.NewField("deptId", "system.user.deptOutOfScope", "moving the account would cross a data scope boundary"))
					}
				}
			}
			if err := s.deps.RBAC.CheckUserDepartment(ctx, actor, id, *in.DeptID); err != nil {
				return err
			}
		}
		if err := s.repo.Update(ctx, id, map[string]any{
			"display_name": in.DisplayName, "email": in.Email, "phone": in.Phone, "bio": in.Bio, // 头像不在这里改（D-040）
			"sort": in.Sort, "remark": in.Remark, "updated_by": actor.UserID,
		}); err != nil {
			return err
		}
		if err := s.org.assignOrg(ctx, u, in.DeptID, in.PostIDs); err != nil {
			return err
		}
		db.AfterCommit(ctx, func() { s.deps.Auth.ForgetAccount(PortalCode, id) })
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.viewAfterWrite(ctx, actor, id)
}

// SetStatus 启用或停用账号。停用会吊销全部会话；不能停用自己，不能停用最后一个超管。
func (s *UserService) SetStatus(ctx context.Context, actor auth.Principal, id uint64, status int) error {
	if status != StatusEnabled && status != StatusDisabled {
		return httpx.ErrValidation.WithFields(httpx.NewField("status", "common.status", "must be 0 or 1"))
	}
	if status == StatusDisabled && id == actor.UserID {
		return httpx.ErrValidation.WithFields(httpx.NewField("id", "system.user.disableSelf", "cannot disable your own account"))
	}
	// 判断"最后一个可用超管"、改状态、吊销会话放在同一个事务里，并先锁住超管角色行，
	// 两个超管并发互相停用时后到的那个会看到前一个已经提交的结果（规范 §6.4）。
	return s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		u, err := s.repo.FindByIDForUpdate(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkWriteTarget(ctx, actor, PermUserStatus, u); err != nil {
			return err
		}
		if err := s.guardSuperTarget(ctx, actor, id); err != nil {
			return err
		}
		// 重新启用等于把账号角色的权限交回去：非超管只能启用角色都是自己能分配的账号（D-058）
		if status == StatusEnabled && u.Status != StatusEnabled {
			if err := s.deps.RBAC.CheckEnableUser(ctx, actor, PortalCode, id); err != nil {
				return err
			}
		}
		if status == StatusDisabled {
			last, err := s.isLastActiveSuper(ctx, u)
			if err != nil {
				return err
			}
			if last {
				return httpx.New(httpx.CodeLastSuper, "")
			}
		}
		if err := s.repo.Update(ctx, id, map[string]any{"status": status, "updated_by": actor.UserID}); err != nil {
			return err
		}
		if status == StatusDisabled {
			if err := s.deps.Auth.RevokeUserSessions(ctx, PortalCode, id, auth.RevokeDisabled); err != nil {
				return err
			}
		}
		db.AfterCommit(ctx, func() { s.deps.Auth.ForgetAccount(PortalCode, id) })
		return nil
	})
}

// isLastActiveSuper 报告该用户是否最后一个"启用且拥有启用超管角色"的账号。
// 判定本身在 rbac.Service.IsLastSuper（会查其他超管的账号状态），这里只补一条：停用中的账号不可能是"最后一个可用超管"。
func (s *UserService) isLastActiveSuper(ctx context.Context, u *User) (bool, error) {
	if u.Status != StatusEnabled {
		return false, nil
	}
	return s.deps.RBAC.IsLastSuper(ctx, PortalCode, u.ID)
}

// guardSuperTarget 挡住非超管对超管账号的写操作（D-035）：改资料、启停、重置密码、让会话下线。
// 分配角色的同一条规则在 rbac.Service.AssignUserRoles 里。是不是超管直接读库，只要拥有超管角色就算。
func (s *UserService) guardSuperTarget(ctx context.Context, actor auth.Principal, userID uint64) error {
	if actor.Super {
		return nil
	}
	super, err := s.deps.RBAC.HoldsSuperRole(ctx, PortalCode, userID)
	if err != nil {
		return err
	}
	if super {
		return httpx.ErrForbidden.WithFields(httpx.NewField("id", "system.user.superProtected", "only a super administrator can change a super administrator's account"))
	}
	return nil
}

// ResetPassword 超管重置别人的密码（D-035）：生成随机密码、必须改密、吊销全部会话，返回新密码（只显示一次）。
// 路由已经是 RequireSuper，这里再挡一次；超管的密码（包括自己的）不能在这里重置，只能用命令行。
func (s *UserService) ResetPassword(ctx context.Context, actor auth.Principal, id uint64) (string, error) {
	if !actor.Super {
		return "", httpx.ErrForbidden
	}
	// 目标不存在的请求不去算哈希；哈希在拿超管锁之前算好，不在锁里等它（D-068）。锁内照样认定
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return "", notFound(err)
	}
	plain, hash, err := newPassword(s.deps)
	if err != nil {
		return "", err
	}
	// 判断"不是超管"和换密码在同一把超管锁里：同时被提升为超管的账号不会在网页上被重置
	err = s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		u, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if !actor.Super {
			return httpx.ErrForbidden
		}
		super, err := s.deps.RBAC.HoldsSuperRole(ctx, PortalCode, u.ID)
		if err != nil {
			return err
		}
		if super {
			return httpx.ErrForbidden.WithFields(httpx.NewField("id", "system.user.resetSuper", "a super administrator's password can only be reset on the server command line (admin reset-password)"))
		}
		return setPassword(ctx, s.deps, s.repo, u.ID, hash)
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

// newPassword 生成随机密码并算好哈希（慢：调用方在拿锁之前调；和登录核对密码占同一个并发上限，满了回 429，D-068）。
func newPassword(deps *app.Deps) (plain, hash string, err error) {
	if plain, err = deps.Auth.GeneratePassword(); err != nil {
		return "", "", err
	}
	if hash, err = deps.Auth.HashPassword(PortalCode, plain); err != nil {
		return "", "", err
	}
	return plain, hash, nil
}

// setPassword 把密码换成 hash 并要求下次登录改密；写密码和吊销会话是一个事务：吊销失败时密码也不会换掉，
// 不会出现"密码已改、没人知道新密码、旧会话还在"的状态。网页上的重置和命令行共用。
func setPassword(ctx context.Context, deps *app.Deps, repo *UserRepo, id uint64, hash string) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		if err := repo.UpdatePasswordHash(ctx, id, hash, true); err != nil {
			return err
		}
		if err := deps.Auth.RevokeUserSessions(ctx, PortalCode, id, auth.RevokePwdChange); err != nil {
			return err
		}
		// 调用方可能还在外层事务里（超管锁）：清缓存挂到最外层提交之后，免得提交前被并发请求填回旧状态
		db.AfterCommit(ctx, func() { deps.Auth.ForgetAccount(PortalCode, id) })
		return nil
	})
}

// RevokeSession 让本端的一个会话下线；非超管不能让超管的会话下线（D-035）。
func (s *UserService) RevokeSession(ctx context.Context, actor auth.Principal, sid string) error {
	return s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		if actor.Super {
			return s.deps.Auth.RevokeSession(ctx, PortalCode, sid, auth.RevokeAdmin)
		}
		owner, err := s.deps.Auth.SessionOwner(ctx, PortalCode, sid)
		if err != nil {
			return err
		}
		// 会话属于某个用户：用户不在"下线会话"的范围内时，当作会话不存在（D-039）
		u, err := s.repo.FindByID(ctx, owner)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkWriteTarget(ctx, actor, PermSessionRevoke, u); err != nil {
			return err
		}
		super, err := s.deps.RBAC.HoldsSuperRole(ctx, PortalCode, owner)
		if err != nil {
			return err
		}
		if super {
			return httpx.ErrForbidden.WithFields(httpx.NewField("sid", "system.session.superProtected", "only a super administrator can end a super administrator's session"))
		}
		return s.deps.Auth.RevokeSession(ctx, PortalCode, sid, auth.RevokeAdmin)
	})
}

// AssignRoles 整体替换用户角色（规则在 rbac.Service 里）。
func (s *UserService) AssignRoles(ctx context.Context, actor auth.Principal, id uint64, roleIDs []uint64) (*UserView, error) {
	// 目标在范围内的判断和写入在同一个事务里，锁住用户行：判断之后部门被并发改掉也不会漏过（D-039）
	err := s.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, actor auth.Principal) error {
		u, err := s.repo.FindByIDForUpdate(ctx, id)
		if err != nil {
			return notFound(err)
		}
		if err := s.checkWriteTarget(ctx, actor, PermUserAssignRole, u); err != nil {
			return err
		}
		return s.deps.RBAC.AssignUserRoles(ctx, actor, PortalCode, u.ID, roleIDs)
	})
	if err != nil {
		return nil, err
	}
	return s.viewAfterWrite(ctx, actor, id)
}

// Option 是下拉选项。
type Option struct {
	ID          uint64 `json:"id"`
	DisplayName string `json:"displayName"`
}

// Options 返回启用用户的下拉选项（最多 500 个），只含"查看用户"范围内的人（D-039）：
// 没有查看用户权限的人只看得到自己。
func (s *UserService) Options(ctx context.Context, actor auth.Principal) ([]Option, error) {
	// 过滤条件和被过滤的数据在同一个只读快照里读（D-054）
	var users []User
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		f, err := s.scope(ctx, actor, PermUserList)
		if err != nil {
			return err
		}
		users, err = s.repo.ListEnabled(ctx, 500, f)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Option, 0, len(users))
	for _, u := range users {
		out = append(out, Option{ID: u.ID, DisplayName: u.DisplayName})
	}
	return out, nil
}

func notFound(err error) error {
	if errors.Is(err, portal.ErrAccountNotFound) {
		return httpx.ErrNotFound.WithCause(err)
	}
	return fmt.Errorf("system: %w", err)
}
