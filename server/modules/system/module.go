// Package system 是平台端的系统管理模块：用户、角色、会话、操作日志、登录日志、字典、菜单。
//
// 它也注册 platform 端本身（用户来源是 ga_user），并提供 admin create 命令用到的 CreateAdmin。
package system

import (
	"context"
	"errors"
	"fmt"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// PortalCode 是本模块负责的端。
const PortalCode = conf.DefaultPortalCode

type module struct {
	deps *app.Deps
	repo *UserRepo
	h    *handlers
}

// Module 返回系统管理模块。在 main.go 里显式注册。
func Module() app.Module { return &module{} }

func (m *module) Name() string { return "system" }

func (m *module) Init(deps *app.Deps) error {
	m.deps = deps
	m.repo = NewUserRepo()
	org := NewOrgService(deps, m.repo)
	m.h = &handlers{deps: deps, users: NewUserService(deps, m.repo, org), org: org}
	return deps.Portals.Register(portal.Portal{
		Code:  PortalCode,
		Users: NewUserProvider(m.repo),
		Org:   orgProvider{s: org},
	})
}

// DataResources 声明按部门的数据权限约束的资源（D-039）。
func (m *module) DataResources() []rbac.DataResource {
	return []rbac.DataResource{{
		Code: DataUser, Name: "data.system.user", Portal: PortalCode,
		// 用户的查看、新建、修改、启停、分配角色，和会话的查看、下线（会话属于某个用户）
		Perms:   []string{PermUserList, PermUserCreate, PermUserUpdate, PermUserStatus, PermUserAssignRole, PermSessionList, PermSessionRevoke},
		Default: rbac.ScopeSelf, // 新建的角色要在授权对话框里明确选范围；升级时现有角色写成全部（迁移 00011）
	}}
}

// Routes 注册系统管理接口（规范 §9.5）。前缀 /api/platform/v1/system。
func (m *module) Routes(r *app.Router) {
	g := r.Portal(PortalCode).Group("/system")
	h := m.h

	g.GET("/users", rbac.Require(PermUserList), h.listUsers)
	g.GET("/users/:id", rbac.Require(PermUserList), h.getUser)
	g.POST("/users", rbac.Require(PermUserCreate), h.createUser, oplog.Record(OpUserCreate))
	g.PUT("/users/:id", rbac.Require(PermUserUpdate), h.updateUser, oplog.Record(OpUserUpdate))
	g.POST("/users/:id/status", rbac.Require(PermUserStatus), h.setUserStatus, oplog.Record(OpUserStatus))
	// 重置别人的密码等于能用对方的账号登录：只有超管能做，超管自己的只能用命令行（D-035）
	g.POST("/users/:id/reset-password", rbac.RequireSuper(), h.resetUserPassword, oplog.Record(OpUserResetPassword))
	g.PUT("/users/:id/roles", rbac.Require(PermUserAssignRole), h.assignUserRoles, oplog.Record(OpUserAssignRole))
	g.GET("/options/users", rbac.AuthOnly(), h.userOptions)
	// 用户表单里选部门、岗位（D-033）：只给能看用户的人
	g.GET("/options/depts", rbac.Require(PermUserList), h.deptOptions)
	g.GET("/options/posts", rbac.Require(PermUserList), h.postOptions)

	g.GET("/depts", rbac.Require(PermDeptList), h.listDepts)
	g.POST("/depts", rbac.Require(PermDeptCreate), h.createDept, oplog.Record(OpDeptCreate))
	g.PUT("/depts/:id", rbac.Require(PermDeptUpdate), h.updateDept, oplog.Record(OpDeptUpdate))
	g.DELETE("/depts/:id", rbac.Require(PermDeptDelete), h.deleteDept, oplog.Record(OpDeptDelete))
	g.GET("/posts", rbac.Require(PermPostList), h.listPosts)
	g.POST("/posts", rbac.Require(PermPostCreate), h.createPost, oplog.Record(OpPostCreate))
	g.PUT("/posts/:id", rbac.Require(PermPostUpdate), h.updatePost, oplog.Record(OpPostUpdate))
	g.DELETE("/posts/:id", rbac.Require(PermPostDelete), h.deletePost, oplog.Record(OpPostDelete))

	g.GET("/roles", rbac.Require(PermRoleList), h.listRoles)
	g.POST("/roles", rbac.Require(PermRoleCreate), h.createRole, oplog.Record(OpRoleCreate))
	g.PUT("/roles/:id", rbac.Require(PermRoleUpdate), h.updateRole, oplog.Record(OpRoleUpdate))
	g.DELETE("/roles/:id", rbac.Require(PermRoleDelete), h.deleteRole, oplog.Record(OpRoleDelete))
	g.GET("/roles/:id/perms", rbac.Require(PermRoleList), h.rolePerms)
	g.PUT("/roles/:id/perms", rbac.Require(PermRoleGrant), h.grantRolePerms, oplog.Record(OpRoleGrant))
	g.GET("/perms/tree", rbac.Require(PermRoleList), h.permTree)
	g.GET("/roles/:id/data-scopes", rbac.Require(PermRoleList), h.roleDataScopes)
	g.GET("/data-resources", rbac.Require(PermRoleList), h.dataResources)

	g.GET("/sessions", rbac.Require(PermSessionList), h.listSessions)
	g.POST("/sessions/:sid/revoke", rbac.Require(PermSessionRevoke), h.revokeSession, oplog.Record(OpSessionRevoke))

	// 审计数据只读、不能删改；每次查看都记一条操作日志（D-032）
	g.GET("/operation-logs", rbac.Require(PermOplogList), h.listOperationLogs, oplog.Record(OpViewOplog))
	g.GET("/login-logs", rbac.Require(PermLoginLogList), h.listLoginLogs, oplog.Record(OpViewLoginLog))
	g.GET("/error-logs", rbac.Require(PermErrorLogList), h.listErrorLogs, oplog.Record(OpViewErrorLog))
	g.GET("/error-logs/:id", rbac.Require(PermErrorLogDetail), h.getErrorLog, oplog.Record(OpViewErrorLogDetail))
	g.GET("/security-events", rbac.Require(PermSecEventList), h.listSecurityEvents, oplog.Record(OpViewSecEvent))
	g.GET("/audit/timeline", rbac.Require(PermAuditTimeline), h.auditTimeline, oplog.Record(OpViewTimeline))

	g.GET("/dicts", rbac.Require(PermDictList), h.listDicts)
	g.GET("/dicts/:id", rbac.Require(PermDictList), h.getDict)
	g.POST("/dicts", rbac.Require(PermDictCreate), h.createDict, oplog.Record(OpDictCreate))
	g.PUT("/dicts/:id", rbac.Require(PermDictUpdate), h.updateDict, oplog.Record(OpDictUpdate))
	g.DELETE("/dicts/:id", rbac.Require(PermDictDelete), h.deleteDict, oplog.Record(OpDictDelete))
	g.POST("/dicts/:id/items", rbac.Require(PermDictUpdate), h.createDictItem, oplog.Record(OpDictItemCreate))
	g.PUT("/dicts/:id/items/:itemId", rbac.Require(PermDictUpdate), h.updateDictItem, oplog.Record(OpDictItemUpdate))
	g.DELETE("/dicts/:id/items/:itemId", rbac.Require(PermDictUpdate), h.deleteDictItem, oplog.Record(OpDictItemDelete))
	g.POST("/dicts/:id/items/:itemId/reset", rbac.Require(PermDictUpdate), h.resetDictItem, oplog.Record(OpDictItemReset))
	g.GET("/options/portals", rbac.AuthOnly(), h.portalOptions)

	g.GET("/dashboard", rbac.Require(PermDashboardView), h.dashboard)
	g.GET("/monitor/security", rbac.Require(PermMonitorView), h.monitorSecurity)
	g.GET("/monitor/server", rbac.Require(PermMonitorView), h.monitorServer)
	g.GET("/workspace", rbac.Require(PermWorkspaceView), h.workspace)

	// 个人中心（D-038）：只读写调用者本人，登录即可；能改别人资料的仍然只有管理员（PUT /users/:id）
	g.GET("/profile", rbac.AuthOnly(), h.profile)
	g.PUT("/profile", rbac.AuthOnly(), h.updateProfile, oplog.Record(OpProfileUpdate))
	g.POST("/profile/revoke-other-sessions", rbac.AuthOnly(), h.revokeOtherSessions, oplog.Record(OpProfileRevokeOthers))
	// 头像（D-040）：本人上传、选内置、清除都只改调用者本人；读头像按随机键、要登录；管理员只能清除别人的
	g.POST("/avatar", rbac.AuthOnly(), h.uploadAvatar, app.WithMiddleware(requireImageBody), oplog.Record(OpProfileAvatar))
	g.PUT("/avatar", rbac.AuthOnly(), h.setPresetAvatar, oplog.Record(OpProfileAvatar))
	g.DELETE("/avatar", rbac.AuthOnly(), h.clearOwnAvatar, oplog.Record(OpProfileAvatar))
	g.GET("/avatars/:key", rbac.AuthOnly(), h.avatarImage)
	g.DELETE("/users/:id/avatar", rbac.Require(PermUserUpdate), h.clearUserAvatar, oplog.Record(OpUserAvatarClear))

	// 安全设置只读（D-034）：策略只能在配置文件里改（D-024），这里没有写接口
	g.GET("/security-policy", rbac.Require(PermSecurityView), h.securityPolicy)

	g.GET("/menus", rbac.Require(PermMenuList), h.listMenus)
	g.PUT("/menu-layout", rbac.Require(PermMenuUpdate), h.saveMenuLayout, oplog.Record(OpMenuLayout))
	g.PUT("/menus/:name", rbac.Require(PermMenuUpdate), h.updateMenu, oplog.Record(OpMenuUpdate))
	g.POST("/menus/:name/reset", rbac.Require(PermMenuUpdate), h.resetMenu, oplog.Record(OpMenuReset))
	g.POST("/menu-groups", rbac.Require(PermMenuUpdate), h.createMenuGroup, oplog.Record(OpMenuGroupCreate))
	g.DELETE("/menu-groups/:name", rbac.Require(PermMenuUpdate), h.deleteMenuGroup, oplog.Record(OpMenuGroupDelete))
}

func (m *module) Start(ctx context.Context) error { return nil }
func (m *module) Stop(ctx context.Context) error  { return nil }

// CreateAdmin 创建一个超级管理员账号：随机密码、下次登录必须改密、分配 super 角色。
// 返回明文密码，调用方只打印一次。需要在 app.Setup 之后调用（依赖 deps.Auth）。
func CreateAdmin(ctx context.Context, deps *app.Deps, username string) (string, error) {
	username = portal.NormalizeUsername(username)
	if !ValidUsername(username) {
		return "", errors.New("用户名须为 3–64 位：小写字母开头，只含小写字母、数字、'_'、'.'、'-'")
	}
	if deps.Auth == nil || deps.RBAC == nil {
		return "", errors.New("认证/授权服务未就绪（需要先 app.Setup）")
	}
	plain, err := deps.Auth.GeneratePassword()
	if err != nil {
		return "", err
	}
	hash, err := deps.Auth.HashPassword(PortalCode, plain)
	if err != nil {
		return "", err
	}
	repo := NewUserRepo()
	var userID uint64
	err = db.Tx(ctx, func(ctx context.Context) error {
		if _, err := repo.FindByUsername(ctx, username); err == nil {
			return fmt.Errorf("用户 %s 已存在", username)
		} else if !errors.Is(err, portal.ErrAccountNotFound) {
			return err
		}
		role, err := deps.RBAC.RoleByCode(ctx, PortalCode, rbac.SuperRoleCode)
		if err != nil {
			return err
		}
		u := &User{
			Username: username, PasswordHash: hash, DisplayName: username,
			MustChangePwd: true, Status: StatusEnabled, Remark: "created by cli",
		}
		if err := repo.Create(ctx, u); err != nil {
			return err
		}
		userID = u.ID
		cli := auth.Principal{Portal: PortalCode, Username: "cli", Super: true}
		return deps.RBAC.AssignUserRoles(ctx, cli, PortalCode, u.ID, []uint64{role.ID})
	})
	if err != nil {
		return "", err
	}
	// 能登上服务器的人一条命令就能造出超管：记一个安全事件，后台看得到（D-032）
	if deps.Audit != nil {
		if err := deps.Audit.RecordSecurity(ctx, audit.NewSecurityEvent{
			Portal: PortalCode, Kind: KindCLI, UserID: userID, Username: username, Detail: "admin create",
		}); err != nil {
			return "", err
		}
	}
	return plain, nil
}

// CLIReset 是命令行重置密码的结果。
type CLIReset struct {
	Password string // 新密码，调用方只打印一次
	Enabled  bool   // 账号是否处于启用状态（停用的账号重置后仍然不能登录）
	Super    bool   // 是否超管
	// AuditErr 是记安全事件失败的原因。密码已经换掉，所以这不算命令失败：调用方照样把新密码打印出来，再提示这个错误
	AuditErr error
}

// ResetPasswordByCLI 在服务器命令行上重置一个账号的密码（D-035）：随机密码、下次登录必须改密、吊销全部会话，
// 并记一条 cli 安全事件（operator 是执行命令的系统用户和主机，写进事件说明）。网页上超管的密码不能重置，
// 这是唯一的办法。需要在 app.Setup 之后调用。
func ResetPasswordByCLI(ctx context.Context, deps *app.Deps, username, operator string) (*CLIReset, error) {
	if deps.Auth == nil || deps.RBAC == nil {
		return nil, errors.New("认证/授权服务未就绪（需要先 app.Setup）")
	}
	username = portal.NormalizeUsername(username)
	repo := NewUserRepo()
	u, err := repo.FindByUsername(ctx, username)
	if errors.Is(err, portal.ErrAccountNotFound) {
		return nil, fmt.Errorf("用户 %s 不存在", username)
	}
	if err != nil {
		return nil, err
	}
	super, err := deps.RBAC.HoldsSuperRole(ctx, PortalCode, u.ID)
	if err != nil {
		return nil, err
	}
	plain, err := resetPassword(ctx, deps, repo, u.ID)
	if err != nil {
		return nil, err
	}
	res := &CLIReset{Password: plain, Enabled: u.Status == StatusEnabled, Super: super}
	if deps.Audit != nil {
		detail := "admin reset-password"
		if operator != "" {
			detail += " (" + operator + ")"
		}
		res.AuditErr = deps.Audit.RecordSecurity(ctx, audit.NewSecurityEvent{
			Portal: PortalCode, Kind: KindCLI, UserID: u.ID, Username: u.Username, Detail: detail,
		})
	}
	return res, nil
}

// KindCLI 是命令行操作的安全事件类型（D-032）。
const KindCLI = "cli"
