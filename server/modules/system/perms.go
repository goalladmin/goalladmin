package system

import "github.com/goalladmin/goalladmin/server/core/rbac"

// 权限码（规范 §9.5）。敏感的只有超管能授予。
const (
	PermUserList       = "system:user:list"
	PermUserCreate     = "system:user:create"
	PermUserUpdate     = "system:user:update"
	PermUserStatus     = "system:user:status"
	PermUserAssignRole = "system:user:assign-role"
	PermRoleList       = "system:role:list"
	PermRoleCreate     = "system:role:create"
	PermRoleUpdate     = "system:role:update"
	PermRoleDelete     = "system:role:delete"
	PermRoleGrant      = "system:role:grant"
	PermDeptList       = "system:dept:list" // 部门、岗位只是组织资料，不影响权限判断（D-033）
	PermDeptCreate     = "system:dept:create"
	PermDeptUpdate     = "system:dept:update"
	PermDeptDelete     = "system:dept:delete"
	PermPostList       = "system:post:list"
	PermPostCreate     = "system:post:create"
	PermPostUpdate     = "system:post:update"
	PermPostDelete     = "system:post:delete"
	PermSessionList    = "system:session:list"
	PermSessionRevoke  = "system:session:revoke"
	PermOplogList      = "system:oplog:list"
	PermLoginLogList   = "system:loginlog:list"
	PermDictList       = "system:dict:list"
	PermDictCreate     = "system:dict:create"
	PermDictUpdate     = "system:dict:update"
	PermDictDelete     = "system:dict:delete"
	PermMenuList       = "system:menu:list"
	PermMenuUpdate     = "system:menu:update"
	PermDashboardView  = "system:dashboard:view"
	PermMonitorView    = "system:monitor:view"    // 监控中心：含运行环境信息，敏感（D-030）
	PermWorkspaceView  = "system:workspace:view"  // 工作台：只看本人数据（D-030）
	PermErrorLogList   = "system:errorlog:list"   // 错误日志列表（D-032）：含内部错误文本，敏感
	PermErrorLogDetail = "system:errorlog:detail" // 错误详情含调用栈（D-032），敏感
	PermSecEventList   = "system:secevent:list"   // 安全事件（D-032），敏感
	PermAuditTimeline  = "system:audit:timeline"  // 调查时间线（D-032）：能看到某人、某 IP 的全部动作，敏感
	PermSecurityView   = "system:security:view"   // 安全设置（D-034）：只读，但含锁定和限流阈值，敏感
)

// DataUser 是用户数据资源（D-039）：数据所属部门是 ga_user.dept_id，所属的人是用户自己。
const DataUser = "system:user"

// 操作日志动作名（写进 ga_operation_log.action，前端按语言翻译）。
const (
	OpUserCreate          = "user.create"
	OpUserUpdate          = "user.update"
	OpUserStatus          = "user.status"
	OpUserResetPassword   = "user.reset-password"
	OpUserAssignRole      = "user.assign-role"
	OpRoleCreate          = "role.create"
	OpRoleUpdate          = "role.update"
	OpRoleDelete          = "role.delete"
	OpRoleGrant           = "role.grant" // 功能权限和数据范围一起保存（D-039）
	OpSessionRevoke       = "session.revoke"
	OpDeptCreate          = "dept.create"
	OpDeptUpdate          = "dept.update"
	OpDeptDelete          = "dept.delete"
	OpPostCreate          = "post.create"
	OpPostUpdate          = "post.update"
	OpPostDelete          = "post.delete"
	OpDictCreate          = "dict.create"
	OpDictUpdate          = "dict.update"
	OpDictDelete          = "dict.delete"
	OpDictItemCreate      = "dict.item-create"
	OpDictItemUpdate      = "dict.item-update"
	OpDictItemDelete      = "dict.item-delete"
	OpDictItemReset       = "dict.item-reset"
	OpMenuUpdate          = "menu.update"
	OpMenuReset           = "menu.reset"
	OpMenuLayout          = "menu.layout"
	OpMenuGroupCreate     = "menu.group-create"
	OpMenuGroupDelete     = "menu.group-delete"
	OpProfileUpdate       = "profile.update"        // 个人中心：本人改资料（D-038）
	OpProfileRevokeOthers = "profile.revoke-others" // 个人中心：本人让其他设备下线（D-038）
	OpProfileAvatar       = "profile.avatar"        // 个人中心：本人上传、选择或清除头像（D-040）
	OpUserAvatarClear     = "user.avatar-clear"     // 管理员清除别人的头像（D-040）

	// 查看审计数据本身也留痕（D-032 第 7 条）：谁在什么时候、按什么条件翻看过
	OpViewOplog          = "audit.view.oplog"
	OpViewLoginLog       = "audit.view.loginlog"
	OpViewErrorLog       = "audit.view.errorlog"
	OpViewErrorLogDetail = "audit.view.errorlog-detail"
	OpViewSecEvent       = "audit.view.secevent"
	OpViewTimeline       = "audit.view.timeline"
)

func (m *module) Perms() []rbac.Perm {
	p := func(code, name, group string, sensitive bool) rbac.Perm {
		return rbac.Perm{Code: code, Name: name, Portal: PortalCode, Group: group, Sensitive: sensitive}
	}
	return []rbac.Perm{
		p(PermUserList, "perm.system.user.list", "system.user", false),
		p(PermUserCreate, "perm.system.user.create", "system.user", true),
		p(PermUserUpdate, "perm.system.user.update", "system.user", false),
		p(PermUserStatus, "perm.system.user.status", "system.user", false),
		p(PermUserAssignRole, "perm.system.user.assign-role", "system.user", true),
		p(PermRoleList, "perm.system.role.list", "system.role", false),
		p(PermRoleCreate, "perm.system.role.create", "system.role", false),
		p(PermRoleUpdate, "perm.system.role.update", "system.role", false),
		p(PermRoleDelete, "perm.system.role.delete", "system.role", false),
		p(PermRoleGrant, "perm.system.role.grant", "system.role", true),
		p(PermDeptList, "perm.system.dept.list", "system.dept", false),
		p(PermDeptCreate, "perm.system.dept.create", "system.dept", false),
		p(PermDeptUpdate, "perm.system.dept.update", "system.dept", false),
		p(PermDeptDelete, "perm.system.dept.delete", "system.dept", false),
		p(PermPostList, "perm.system.post.list", "system.post", false),
		p(PermPostCreate, "perm.system.post.create", "system.post", false),
		p(PermPostUpdate, "perm.system.post.update", "system.post", false),
		p(PermPostDelete, "perm.system.post.delete", "system.post", false),
		p(PermSessionList, "perm.system.session.list", "system.session", false),
		p(PermSessionRevoke, "perm.system.session.revoke", "system.session", false),
		p(PermOplogList, "perm.system.oplog.list", "system.log", false),
		p(PermLoginLogList, "perm.system.loginlog.list", "system.log", false),
		p(PermDictList, "perm.system.dict.list", "system.dict", false),
		p(PermDictCreate, "perm.system.dict.create", "system.dict", false),
		p(PermDictUpdate, "perm.system.dict.update", "system.dict", false),
		p(PermDictDelete, "perm.system.dict.delete", "system.dict", false),
		p(PermMenuList, "perm.system.menu.list", "system.menu", false),
		// 改菜单只影响显示，但改名、挪位置也能误导别的管理员，所以只能由超管授出（D-025）
		p(PermMenuUpdate, "perm.system.menu.update", "system.menu", true),
		p(PermDashboardView, "perm.system.dashboard.view", "system.dashboard", false),
		p(PermMonitorView, "perm.system.monitor.view", "system.dashboard", true),
		p(PermWorkspaceView, "perm.system.workspace.view", "system.dashboard", false),
		p(PermErrorLogList, "perm.system.errorlog.list", "system.audit", true),
		p(PermErrorLogDetail, "perm.system.errorlog.detail", "system.audit", true),
		p(PermSecEventList, "perm.system.secevent.list", "system.audit", true),
		p(PermAuditTimeline, "perm.system.audit.timeline", "system.audit", true),
		// 知道确切的锁定次数和限流阈值，就能把尝试速度压在阈值以下，所以只读也只能由超管授出（D-034）
		p(PermSecurityView, "perm.system.security.view", "system.security", true),
	}
}

func (m *module) Menus() []rbac.MenuNode {
	return []rbac.MenuNode{
		// 数据中心、工作台放在根目录（D-036）：排在最前，登录后默认落到数据中心。菜单名、路由、权限码沿用 D-030，
		// 书签和菜单管理里的调整照常有效；原来的"控制台"目录去掉，挂在它下面的调整回到代码位置
		{Portal: PortalCode, Name: "dashboard-data", Path: "/dashboard/data-center", Component: "dashboard/data-center", TitleKey: "menu.dashboard.data", Icon: "DataAnalysis", Perm: PermDashboardView, Sort: 10, KeepAlive: true},
		// 安全监控（原"监控中心"）：D-036 挪进权限管理，D-041 回到根目录、紧跟数据中心；菜单名、路由、权限码不变
		{Portal: PortalCode, Name: "dashboard-monitor", Path: "/dashboard/monitor", Component: "dashboard/monitor", TitleKey: "menu.dashboard.monitor", Icon: "DataLine", Perm: PermMonitorView, Sort: 20, KeepAlive: true},
		{Portal: PortalCode, Name: "dashboard-workspace", Path: "/dashboard/workspace", Component: "dashboard/workspace", TitleKey: "menu.dashboard.workspace", Icon: "Briefcase", Perm: PermWorkspaceView, Sort: 30, KeepAlive: true},
		// 运维中心（D-032）：日志与安全审计。操作日志、登录日志从系统管理移过来，菜单名不变，后台对它们的调整照常生效
		{Portal: PortalCode, Name: "ops", Path: "/ops", TitleKey: "menu.ops", Icon: "Operation", Sort: 500},
		{Portal: PortalCode, Parent: "ops", Name: "system-oplog", Path: "/ops/operation-logs", Component: "system/oplog/index", TitleKey: "menu.system.oplog", Icon: "Document", Perm: PermOplogList, Sort: 10},
		{Portal: PortalCode, Parent: "ops", Name: "system-loginlog", Path: "/ops/login-logs", Component: "system/loginlog/index", TitleKey: "menu.system.loginlog", Icon: "Finished", Perm: PermLoginLogList, Sort: 20},
		{Portal: PortalCode, Parent: "ops", Name: "ops-errorlog", Path: "/ops/error-logs", Component: "ops/errorlog/index", TitleKey: "menu.ops.errorlog", Icon: "Warning", Perm: PermErrorLogList, Sort: 30},
		{Portal: PortalCode, Parent: "ops", Name: "ops-security", Path: "/ops/security-events", Component: "ops/security/index", TitleKey: "menu.ops.security", Icon: "Lock", Perm: PermSecEventList, Sort: 50},
		{Portal: PortalCode, Parent: "ops", Name: "ops-timeline", Path: "/ops/timeline", Component: "ops/timeline/index", TitleKey: "menu.ops.timeline", Icon: "Clock", Perm: PermAuditTimeline, Sort: 60, KeepAlive: true},
		// 会话管理（D-041）：从权限管理挪到运维中心；D-042 起排在运维中心最后。菜单名、路由、权限码不变
		{Portal: PortalCode, Parent: "ops", Name: "system-session", Path: "/system/sessions", Component: "system/session/index", TitleKey: "menu.system.session", Icon: "Monitor", Perm: PermSessionList, Sort: 70},
		{Portal: PortalCode, Name: "system", Path: "/system", TitleKey: "menu.system", Icon: "Setting", Sort: 600},
		{Portal: PortalCode, Parent: "system", Name: "system-user", Path: "/system/users", Component: "system/user/index", TitleKey: "menu.system.user", Icon: "User", Perm: PermUserList, Sort: 10, KeepAlive: true},
		{Portal: PortalCode, Parent: "system", Name: "system-role", Path: "/system/roles", Component: "system/role/index", TitleKey: "menu.system.role", Icon: "Key", Perm: PermRoleList, Sort: 20, KeepAlive: true},
		{Portal: PortalCode, Parent: "system", Name: "system-dept", Path: "/system/depts", Component: "system/dept/index", TitleKey: "menu.system.dept", Icon: "OfficeBuilding", Perm: PermDeptList, Sort: 24, KeepAlive: true},
		{Portal: PortalCode, Parent: "system", Name: "system-post", Path: "/system/posts", Component: "system/post/index", TitleKey: "menu.system.post", Icon: "Postcard", Perm: PermPostList, Sort: 26, KeepAlive: true},
		{Portal: PortalCode, Parent: "system", Name: "system-menu", Path: "/system/menus", Component: "system/menu/index", TitleKey: "menu.system.menu", Icon: "Menu", Perm: PermMenuList, Sort: 70, KeepAlive: true},
		// 系统设置（D-034）：字典挪到这里，菜单名、路由、权限码都不变，只换上级
		{Portal: PortalCode, Name: "settings", Path: "/settings", TitleKey: "menu.settings", Icon: "Tools", Sort: 800},
		{Portal: PortalCode, Parent: "settings", Name: "system-dict", Path: "/system/dicts", Component: "system/dict/index", TitleKey: "menu.system.dict", Icon: "Collection", Perm: PermDictList, Sort: 10, KeepAlive: true},
		{Portal: PortalCode, Parent: "settings", Name: "settings-security", Path: "/settings/security", Component: "settings/security/index", TitleKey: "menu.settings.security", Icon: "Lock", Perm: PermSecurityView, Sort: 20}, // 不缓存：每次打开都读最新的生效值
	}
}
