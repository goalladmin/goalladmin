// Package orgportal 是主体端（代理商端、商户端）自己的后台（D-067）：概览、子账号、角色与权限、会话、登录日志、操作日志、
// 主体的数据中心、IP 黑白名单、个人中心。两种主体共用一份，端的模块（server/modules/agentportal、merchantportal）只是把它挂上：
//
//	func (m *module) Init(deps *app.Deps) error {
//		kit, err := orgportal.New(deps, org.Merchant())
//		if err != nil {
//			return err
//		}
//		m.kit = kit
//		return deps.Portals.Register(kit.Portal())
//	}
//	func (m *module) Perms() []rbac.Perm      { return m.kit.Perms() }
//	func (m *module) Menus() []rbac.MenuNode  { return m.kit.Menus() }
//	func (m *module) Routes(r *app.Router)    { m.kit.Routes(r) }
//
// 规则：
//   - 主体只从身份来（Principal.OrgID，来自会话行），从不读请求；请求里带了主体 ID 由内核的参数守卫回 3002（D-067 第 5 条）。
//   - 写操作都在 rbac.WithActor 里（锁主体行，D-063），目标账号在锁内按主体读、锁，不在本主体一律 404。
//   - 主账号就是本主体的超管（Principal.Super）：员工不能对主账号做写操作；主体内没有人能在后台重置主账号的密码，
//     主账号也停不了（只能由平台换主账号或停用主体）。重置别人的密码只有主账号能做（和平台端 D-035 一样）。
//   - 本人的写操作（个人中心）在 rbac.WithSelf 里，不拿主体行锁（D-058）。
//   - 建子账号、重置员工密码要算密码哈希（慢）：先做便宜的检查，再过主体自己的上限和进程的密码计算闸门，
//     都在拿主体行锁之前（D-068）。
package orgportal

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/core/scope"
)

// 权限码的后两段；完整的权限码是"<端代号>:" + 这一段（Kit.Perm），比如 merchant:account:list。
const (
	PermDashboardView     = "dashboard:view"
	PermAccountList       = "account:list"
	PermAccountCreate     = "account:create" // 敏感：新账号的初始密码在建的人手里
	PermAccountUpdate     = "account:update"
	PermAccountStatus     = "account:status"
	PermAccountAssignRole = "account:assign-role" // 敏感
	PermRoleList          = "role:list"
	PermRoleCreate        = "role:create"
	PermRoleUpdate        = "role:update"
	PermRoleDelete        = "role:delete"
	PermRoleGrant         = "role:grant" // 敏感
	PermSessionList       = "session:list"
	PermSessionRevoke     = "session:revoke"
	PermLoginLogList      = "loginlog:list"
	PermOplogList         = "oplog:list"
)

// 操作日志的动作名：两个端共用（日志按端存，前端按同一份文案翻译）。
const (
	OpAccountCreate     = "org.account.create"
	OpAccountUpdate     = "org.account.update"
	OpAccountStatus     = "org.account.status"
	OpAccountResetPwd   = "org.account.reset-password"
	OpAccountAssignRole = "org.account.assign-role"
	OpRoleCreate        = "org.role.create"
	OpRoleUpdate        = "org.role.update"
	OpRoleDelete        = "org.role.delete"
	OpRoleGrant         = "org.role.grant"
	OpSessionRevoke     = "org.session.revoke"
	OpIPDeny            = "org.ip-deny"
	OpIPUnblock         = "org.ip-unblock"
	OpIPAllow           = "org.ip-allow"
	OpProfileUpdate     = "org.profile.update"
	OpProfileAvatar     = "org.profile.avatar"
	OpProfileRevoke     = "org.profile.revoke-other-sessions"
	OpViewLoginLog      = "audit.view.org-loginlog"
	OpViewOplog         = "audit.view.org-oplog"
)

// Count 是概览上的一项计数：调用者有 Perm 时才算、才返回（Perm 是完整的权限码；空表示登录即可）。
type Count struct {
	Key  string
	Perm string
	Fn   func(ctx context.Context) (int64, error)
}

// Option 定制套件。
type Option func(*Kit)

// WithCount 给概览加一项计数（代理商端的"名下商户"）。
func WithCount(c Count) Option { return func(k *Kit) { k.counts = append(k.counts, c) } }

// Kit 是一个主体端的后台套件。用 New 创建。
type Kit struct {
	deps   *app.Deps
	kind   org.Kind
	code   string
	counts []Count
	// 一个主体算密码哈希（建子账号、重置员工密码）的限制，键是主体 ID（D-068 第 2 条）
	pwdBusy *ratelimit.KeyedGate
	pwdRate app.RateWindow
}

// 一个主体算密码哈希的上限（D-068 第 2 条）：同时最多 pwdOpsParallel 个在算——进程的密码计算闸门至少有 8 个位置，
// 一个主体占不满；每分钟最多 pwdOpsPerMinute 次，人工建账号、重置密码远到不了。
const (
	pwdOpsParallel  = 2
	pwdOpsPerMinute = 30
	pwdOpsMaxKeys   = 100_000
)

// hashPassword 为主体 orgID 算一次密码哈希：先过这个主体自己的上限，再进进程的密码计算闸门（auth.Service.HashPassword，
// 和登录核对密码共用，D-068）。哪一层满了都回 429，不排队。调用方先做完便宜的检查再调、在拿主体行锁之前调。
func (k *Kit) hashPassword(ctx context.Context, orgID uint64, plain string) (string, error) {
	key := strconv.FormatUint(orgID, 10)
	if !k.pwdBusy.TryEnter(key) {
		return "", httpx.ErrTooManyRequests
	}
	defer k.pwdBusy.Leave(key)
	ok, refund := k.pwdRate.Reserve(ctx, key)
	if !ok {
		return "", httpx.ErrTooManyRequests
	}
	hash, err := k.deps.Auth.HashPassword(k.code, plain)
	if errors.Is(err, httpx.ErrTooManyRequests) {
		refund() // 进程的闸门满了、什么都没算：不占这个主体的次数
	}
	return hash, err
}

// New 为一种主体创建后台套件。需要数据库（deps.Orgs、deps.IPACL）。
func New(deps *app.Deps, kind org.Kind, opts ...Option) (*Kit, error) {
	if deps == nil || deps.Orgs == nil || deps.IPACL == nil {
		return nil, errors.New("orgportal: 需要数据库（Deps.Orgs、Deps.IPACL 为空）")
	}
	if kind != org.Agent() && kind != org.Merchant() {
		return nil, errors.New("orgportal: 主体种类无效（只能用 org.Agent() 或 org.Merchant()）")
	}
	k := &Kit{
		deps: deps, kind: kind, code: kind.Portal(),
		pwdBusy: ratelimit.NewKeyedGate(pwdOpsParallel),
	}
	if deps.NewRateWindow != nil {
		k.pwdRate = deps.NewRateWindow("org-password:"+k.code, pwdOpsPerMinute, time.Minute, pwdOpsMaxKeys)
	} else {
		k.pwdRate = localRateWindow{ratelimit.New(pwdOpsPerMinute, time.Minute, pwdOpsMaxKeys, nil)}
	}
	for _, o := range opts {
		o(k)
	}
	return k, nil
}

// 单独装配套件的调用方可以不提供工厂，仍按本实例计数。
type localRateWindow struct{ window *ratelimit.Window }

func (w localRateWindow) Reserve(ctx context.Context, key string) (bool, func()) {
	ticket, ok := w.window.Reserve(ctx, key)
	if !ok {
		return false, nil
	}
	return true, ticket.Undo
}

// Code 返回端代号。
func (k *Kit) Code() string { return k.code }

// Perm 返回本端的完整权限码：Perm(PermAccountList) == "merchant:account:list"。
func (k *Kit) Perm(action string) string { return k.code + ":" + action }

// Portal 返回端的声明：主体端，用户来源是 core/org 的这种主体。
func (k *Kit) Portal() portal.Portal {
	return portal.Portal{Code: k.code, Users: k.deps.Orgs.Users(k.kind), Scoped: true}
}

// Perms 返回套件的权限码。名称和分组是不带端的翻译键（perm.org.*、org.*），两个端共用壳里的一份文案。
func (k *Kit) Perms() []rbac.Perm {
	p := func(action, group string, sensitive bool) rbac.Perm {
		// 看角色权限的接口由 PermRoleList 守着：有它的员工分配角色被拒时看得到细节（D-069）
		return rbac.Perm{Code: k.Perm(action), Name: "perm.org." + dotted(action), Portal: k.code, Group: group, Sensitive: sensitive, RoleView: action == PermRoleList}
	}
	return []rbac.Perm{
		p(PermDashboardView, "org.dashboard", false),
		p(PermAccountList, "org.account", false),
		p(PermAccountCreate, "org.account", true),
		p(PermAccountUpdate, "org.account", false),
		p(PermAccountStatus, "org.account", false),
		p(PermAccountAssignRole, "org.account", true),
		p(PermRoleList, "org.role", false),
		p(PermRoleCreate, "org.role", false),
		p(PermRoleUpdate, "org.role", false),
		p(PermRoleDelete, "org.role", false),
		p(PermRoleGrant, "org.role", true),
		p(PermSessionList, "org.session", false),
		p(PermSessionRevoke, "org.session", false),
		p(PermLoginLogList, "org.log", false),
		p(PermOplogList, "org.log", false),
	}
}

// dotted 把 account:list 写成 account.list（翻译键用点分）。
func dotted(action string) string {
	b := []byte(action)
	for i := range b {
		if b[i] == ':' {
			b[i] = '.'
		}
	}
	return string(b)
}

// Menus 返回数据中心、概览、账号与权限、日志和仅主账号可见的安全设置。
// 组件名 org/<页面>/index 由前端壳内置（D-067 第 8 条）。代理商端的"名下商户"排序 10，夹在概览和账号与权限之间。
func (k *Kit) Menus() []rbac.MenuNode {
	c := k.code
	return []rbac.MenuNode{
		{Portal: c, Name: "org-dashboard", Path: "/data-center", Component: "org/dashboard/index", TitleKey: "menu.org.dashboard", Icon: "DataAnalysis", Perm: k.Perm(PermDashboardView), Sort: 0, KeepAlive: true},
		{Portal: c, Name: "org-overview", Path: "/overview", Component: "org/overview/index", TitleKey: "menu.org.overview", Icon: "Odometer", Sort: 1},
		{Portal: c, Name: "org-access", Path: "/access", TitleKey: "menu.org.access", Icon: "UserFilled", Sort: 20},
		{Portal: c, Parent: "org-access", Name: "org-accounts", Path: "/access/accounts", Component: "org/accounts/index", TitleKey: "menu.org.accounts", Icon: "User", Perm: k.Perm(PermAccountList), Sort: 10, KeepAlive: true},
		{Portal: c, Parent: "org-access", Name: "org-roles", Path: "/access/roles", Component: "org/roles/index", TitleKey: "menu.org.roles", Icon: "Avatar", Perm: k.Perm(PermRoleList), Sort: 20, KeepAlive: true},
		{Portal: c, Parent: "org-access", Name: "org-sessions", Path: "/access/sessions", Component: "org/sessions/index", TitleKey: "menu.org.sessions", Icon: "Connection", Perm: k.Perm(PermSessionList), Sort: 30},
		{Portal: c, Name: "org-security", Path: "/security", TitleKey: "menu.org.security", Icon: "Lock", Sort: 40, SuperOnly: true},
		{Portal: c, Parent: "org-security", Name: "org-ip-allow", Path: "/security/ip-allow", Component: "org/ip-allow/index", TitleKey: "menu.org.ipAllow", Icon: "CircleCheck", Sort: 10, SuperOnly: true},
		{Portal: c, Parent: "org-security", Name: "org-ip-deny", Path: "/security/ip-deny", Component: "org/ip-deny/index", TitleKey: "menu.org.ipDeny", Icon: "CircleClose", Sort: 20, SuperOnly: true},
		{Portal: c, Name: "org-logs", Path: "/logs", TitleKey: "menu.org.logs", Icon: "Tickets", Sort: 30},
		{Portal: c, Parent: "org-logs", Name: "org-loginlog", Path: "/logs/login", Component: "org/loginlog/index", TitleKey: "menu.org.loginlog", Icon: "Finished", Perm: k.Perm(PermLoginLogList), Sort: 10},
		{Portal: c, Parent: "org-logs", Name: "org-oplog", Path: "/logs/operation", Component: "org/oplog/index", TitleKey: "menu.org.oplog", Icon: "Document", Perm: k.Perm(PermOplogList), Sort: 20},
	}
}

// Routes 注册套件的接口，都在 /api/<端>/v1/org/ 下。
func (k *Kit) Routes(r *app.Router) {
	// 整个分组按库核对（D-073）：平台程序停用主体、换主账号、吊销会话之后，这里的请求立即拒绝，不等状态缓存到期
	g := r.Portal(k.code).Group("/org").LiveAuth()
	h := &handlers{k: k}
	req := func(action string) rbac.Guard { return rbac.Require(k.Perm(action)) }

	g.GET("/dashboard", req(PermDashboardView), h.dashboard)
	g.GET("/ip-deny", rbac.RequireSuper(), h.listIPDeny)
	g.POST("/ip-deny", rbac.RequireSuper(), h.addIPDeny, oplog.Record(OpIPDeny))
	g.DELETE("/ip-deny/:id", rbac.RequireSuper(), h.removeIPDeny, oplog.Record(OpIPUnblock))
	g.GET("/overview", rbac.AuthOnly(), h.overview)
	// 主体的 IP 白名单（D-062 第 6 条）：只有主账号能看、能改
	g.GET("/ip-allow", rbac.RequireSuper(), h.getIPAllow)
	g.PUT("/ip-allow", rbac.RequireSuper(), h.setIPAllow, oplog.Record(OpIPAllow))

	g.GET("/accounts", req(PermAccountList), h.listAccounts)
	g.GET("/accounts/:id", req(PermAccountList), h.getAccount)
	g.POST("/accounts", req(PermAccountCreate), h.createAccount, oplog.Record(OpAccountCreate))
	g.PUT("/accounts/:id", req(PermAccountUpdate), h.updateAccount, oplog.Record(OpAccountUpdate))
	g.POST("/accounts/:id/status", req(PermAccountStatus), h.setAccountStatus, oplog.Record(OpAccountStatus))
	// 重置别人的密码等于能用对方的账号登录：只有主账号能做；主账号自己的密码主体内谁都重置不了（D-067 第 4 条）
	g.POST("/accounts/:id/reset-password", rbac.RequireSuper(), h.resetAccountPassword, oplog.Record(OpAccountResetPwd))
	g.PUT("/accounts/:id/roles", req(PermAccountAssignRole), h.assignRoles, oplog.Record(OpAccountAssignRole))

	g.GET("/roles", req(PermRoleList), h.listRoles)
	g.POST("/roles", req(PermRoleCreate), h.createRole, oplog.Record(OpRoleCreate))
	g.PUT("/roles/:id", req(PermRoleUpdate), h.updateRole, oplog.Record(OpRoleUpdate))
	g.DELETE("/roles/:id", req(PermRoleDelete), h.deleteRole, oplog.Record(OpRoleDelete))
	g.GET("/roles/:id/perms", req(PermRoleList), h.rolePerms)
	g.PUT("/roles/:id/perms", req(PermRoleGrant), h.grantRole, oplog.Record(OpRoleGrant))
	g.GET("/perms/tree", req(PermRoleList), h.permTree)

	g.GET("/sessions", req(PermSessionList), h.listSessions)
	g.POST("/sessions/:sid/revoke", req(PermSessionRevoke), h.revokeSession, oplog.Record(OpSessionRevoke))

	// 只看本主体的；每次查看记操作日志（D-032）
	g.GET("/login-logs", req(PermLoginLogList), h.listLoginLogs, oplog.Record(OpViewLoginLog))
	g.GET("/operation-logs", req(PermOplogList), h.listOperationLogs, oplog.Record(OpViewOplog))

	// 个人中心：只读写调用者本人
	g.GET("/profile", rbac.AuthOnly(), h.profile)
	g.PUT("/profile", rbac.AuthOnly(), h.updateProfile, oplog.Record(OpProfileUpdate))
	g.PUT("/profile/avatar", rbac.AuthOnly(), h.setAvatar, oplog.Record(OpProfileAvatar))
	g.POST("/profile/revoke-other-sessions", rbac.AuthOnly(), h.revokeOtherSessions, oplog.Record(OpProfileRevoke))
}

// handlers 只做参数绑定、调用 core/org 和 rbac、输出信封。
type handlers struct{ k *Kit }

// principal 返回当前身份和它的主体。主体端的身份一定带主体（D-061），没有就是装配错了：按 scope.ErrNoOrg 以 500 终止。
func principal(ctx context.Context) (auth.Principal, uint64, error) {
	p := auth.MustFromCtx(ctx)
	orgID, err := scope.MustOrg(ctx)
	return p, orgID, err
}

// withActor 拿主体行锁、按库重新认定操作人（含路由要求的权限码，D-048），再执行 fn。主体取锁内认定的身份。
func (h *handlers) withActor(c *gin.Context, fn func(ctx context.Context, actor auth.Principal, orgID uint64) error) error {
	ctx := c.Request.Context()
	if _, _, err := principal(ctx); err != nil {
		return err
	}
	return h.k.deps.RBAC.WithActor(ctx, auth.MustFromCtx(ctx), func(ctx context.Context, actor auth.Principal) error {
		if actor.OrgID == 0 {
			return scope.ErrNoOrg
		}
		return fn(ctx, actor, actor.OrgID)
	})
}

func pathID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return 0, false
	}
	return id, true
}

func queryStatus(c *gin.Context) *int {
	v, err := strconv.Atoi(c.Query("status"))
	if err != nil {
		return nil
	}
	return &v
}

// queryTime 解析 RFC 3339 时间参数；空串、格式错误、换算成 UTC 后年份超出 1–9999 的都当作没传（D-099）。
func queryTime(c *gin.Context, key string) time.Time {
	t, _ := httpx.ParseTime(c.Query(key))
	return t
}

func queryUserID(c *gin.Context) uint64 {
	v, _ := strconv.ParseUint(c.Query("userId"), 10, 64)
	return v
}

func queryBool(c *gin.Context, key string) *bool {
	switch c.Query(key) {
	case "1", "true":
		v := true
		return &v
	case "0", "false":
		v := false
		return &v
	}
	return nil
}
