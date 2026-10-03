// Package merchant 是平台端的"商户管理"（D-065）：开商户（同时建主账号）、改资料、启停、重置和更换主账号、
// 改归属代理商、查看商户的账号和会话、让会话下线、查看和清空商户的 IP 白名单，以及商户端的登录日志、操作日志。
//
// 它只是内核 core/org 的接口层：表、加锁、会话吊销都在 core/org 里。商户自己的后台（商户端）是另一个程序，
// 它的模块在 server/modules/merchantportal/，不编进平台程序（scripts/depcheck.sh 规则 5）。
package merchant

import (
	"context"
	"errors"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// portalCode 是本模块的接口所在的端：平台端。
const portalCode = conf.DefaultPortalCode

// 权限码（规范 §9.5，D-066）。敏感的只有超管能授予（规范 §6.5）。
const (
	PermList     = "partner:merchant:list"
	PermCreate   = "partner:merchant:create"
	PermUpdate   = "partner:merchant:update"
	PermStatus   = "partner:merchant:status"   // 启停商户、让商户的会话下线
	PermOwner    = "partner:merchant:owner"    // 重置主账号密码、更换主账号、清空商户的 IP 白名单：等于能进这个商户的后台，敏感
	PermTransfer = "partner:merchant:transfer" // 改归属代理商：代理商能看名下商户的数据，敏感
	PermLog      = "partner:merchant:log"      // 商户端的登录日志、操作日志
)

// 操作日志动作名。
const (
	OpCreate        = "merchant.create"
	OpUpdate        = "merchant.update"
	OpStatus        = "merchant.status"
	OpResetOwner    = "merchant.reset-owner-password"
	OpChangeOwner   = "merchant.change-owner"
	OpTransfer      = "merchant.transfer"
	OpRevokeSession = "merchant.revoke-session"
	OpClearIPAllow  = "merchant.ip-clear"
	OpClearIPDeny   = "merchant.ip-deny-clear"
	OpViewLoginLog  = "audit.view.merchant-loginlog"
	OpViewOplog     = "audit.view.merchant-oplog"
)

type module struct{ h *handlers }

// Module 返回商户管理模块。在平台程序的 main.go 里显式注册。
func Module() app.Module { return &module{} }

func (m *module) Name() string { return "merchant" }

func (m *module) Init(deps *app.Deps) error {
	if deps.Orgs == nil || deps.IPACL == nil {
		return errors.New("merchant: 需要数据库（Deps.Orgs、Deps.IPACL 为空）")
	}
	m.h = &handlers{deps: deps, kind: org.Merchant()}
	return nil
}

func (m *module) Perms() []rbac.Perm {
	p := func(code, name string, sensitive bool) rbac.Perm {
		return rbac.Perm{Code: code, Name: name, Portal: portalCode, Group: "partner.merchant", Sensitive: sensitive}
	}
	return []rbac.Perm{
		p(PermList, "perm.partner.merchant.list", false),
		p(PermCreate, "perm.partner.merchant.create", false),
		p(PermUpdate, "perm.partner.merchant.update", false),
		p(PermStatus, "perm.partner.merchant.status", false),
		p(PermOwner, "perm.partner.merchant.owner", true),
		p(PermTransfer, "perm.partner.merchant.transfer", true),
		p(PermLog, "perm.partner.merchant.log", false),
	}
}

func (m *module) Menus() []rbac.MenuNode {
	return []rbac.MenuNode{
		{Portal: portalCode, Name: "merchant", Path: "/merchant", TitleKey: "menu.merchant", Icon: "Shop", Sort: 110},
		{Portal: portalCode, Parent: "merchant", Name: "merchant-list", Path: "/merchant/list", Component: "merchant/list/index", TitleKey: "menu.merchant.list", Icon: "List", Perm: PermList, Sort: 10, KeepAlive: true},
		{Portal: portalCode, Parent: "merchant", Name: "merchant-loginlog", Path: "/merchant/login-logs", Component: "merchant/loginlog/index", TitleKey: "menu.merchant.loginlog", Icon: "Finished", Perm: PermLog, Sort: 30},
		{Portal: portalCode, Parent: "merchant", Name: "merchant-oplog", Path: "/merchant/operation-logs", Component: "merchant/oplog/index", TitleKey: "menu.merchant.oplog", Icon: "Document", Perm: PermLog, Sort: 40},
	}
}

// Routes 注册商户管理接口。前缀 /api/platform/v1/merchant。写接口都在 WithActor 里（D-047）。
func (m *module) Routes(r *app.Router) {
	g := r.Portal(portalCode).Group("/merchant")
	h := m.h

	g.GET("/merchants", rbac.Require(PermList), h.list)
	g.GET("/merchants/:id", rbac.Require(PermList), h.get)
	g.POST("/merchants", rbac.Require(PermCreate), h.create, oplog.Record(OpCreate))
	g.PUT("/merchants/:id", rbac.Require(PermUpdate), h.update, oplog.Record(OpUpdate))
	g.POST("/merchants/:id/status", rbac.Require(PermStatus), h.setStatus, oplog.Record(OpStatus))
	g.POST("/merchants/:id/reset-owner-password", rbac.Require(PermOwner), h.resetOwnerPassword, oplog.Record(OpResetOwner))
	g.PUT("/merchants/:id/owner", rbac.Require(PermOwner), h.changeOwner, oplog.Record(OpChangeOwner))
	g.PUT("/merchants/:id/agent", rbac.Require(PermTransfer), h.transfer, oplog.Record(OpTransfer))
	g.GET("/merchants/:id/accounts", rbac.Require(PermList), h.accounts)
	g.GET("/merchants/:id/sessions", rbac.Require(PermList), h.sessions)
	g.POST("/merchants/:id/sessions/:sid/revoke", rbac.Require(PermStatus), h.revokeSession, oplog.Record(OpRevokeSession))
	g.GET("/merchants/:id/ip-allow", rbac.Require(PermList), h.ipAllow)
	g.DELETE("/merchants/:id/ip-allow", rbac.Require(PermOwner), h.clearIPAllow, oplog.Record(OpClearIPAllow))
	// 商户自己设的 IP 黑名单：平台能看、能整体清空（D-102）
	g.GET("/merchants/:id/ip-deny", rbac.Require(PermList), h.ipDeny)
	g.DELETE("/merchants/:id/ip-deny", rbac.Require(PermOwner), h.clearIPDeny, oplog.Record(OpClearIPDeny))
	// 新建、改归属时选代理商；列表按代理商筛选
	g.GET("/options/agents", rbac.Require(PermList), h.agentOptions)

	// 商户端的日志：只读；查看本身也记操作日志（D-032）
	g.GET("/login-logs", rbac.Require(PermLog), h.loginLogs, oplog.Record(OpViewLoginLog))
	g.GET("/operation-logs", rbac.Require(PermLog), h.operationLogs, oplog.Record(OpViewOplog))
}

func (m *module) Start(context.Context) error { return nil }
func (m *module) Stop(context.Context) error  { return nil }
