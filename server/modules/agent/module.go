// Package agent 是平台端的"代理商管理"（D-065）：开代理商（同时建主账号）、改资料、启停、重置和更换主账号、
// 查看代理商的账号和会话、让会话下线、查看和清空代理商的 IP 白名单，以及代理商端的登录日志、操作日志。
// 商户挂到哪个代理商下面在"商户管理"里改（modules/merchant）；代理商停用不影响名下商户。
//
// 它只是内核 core/org 的接口层：表、加锁、会话吊销都在 core/org 里。代理商自己的后台（代理商端）是另一个程序，
// 它的模块在 server/modules/agentportal/，不编进平台程序（scripts/depcheck.sh 规则 5）。
package agent

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
	PermList   = "partner:agent:list"
	PermCreate = "partner:agent:create"
	PermUpdate = "partner:agent:update"
	PermStatus = "partner:agent:status" // 启停代理商、让代理商的会话下线
	PermOwner  = "partner:agent:owner"  // 重置主账号密码、更换主账号、清空代理商的 IP 白名单：等于能进这个代理商的后台，敏感
	PermLog    = "partner:agent:log"    // 代理商端的登录日志、操作日志
)

// 操作日志动作名。
const (
	OpCreate        = "agent.create"
	OpUpdate        = "agent.update"
	OpStatus        = "agent.status"
	OpResetOwner    = "agent.reset-owner-password"
	OpChangeOwner   = "agent.change-owner"
	OpRevokeSession = "agent.revoke-session"
	OpClearIPAllow  = "agent.ip-clear"
	OpClearIPDeny   = "agent.ip-deny-clear"
	OpViewLoginLog  = "audit.view.agent-loginlog"
	OpViewOplog     = "audit.view.agent-oplog"
)

type module struct{ h *handlers }

// Module 返回代理商管理模块。在平台程序的 main.go 里显式注册。
func Module() app.Module { return &module{} }

func (m *module) Name() string { return "agent" }

func (m *module) Init(deps *app.Deps) error {
	if deps.Orgs == nil || deps.IPACL == nil {
		return errors.New("agent: 需要数据库（Deps.Orgs、Deps.IPACL 为空）")
	}
	m.h = &handlers{deps: deps, kind: org.Agent()}
	return nil
}

func (m *module) Perms() []rbac.Perm {
	p := func(code, name string, sensitive bool) rbac.Perm {
		return rbac.Perm{Code: code, Name: name, Portal: portalCode, Group: "partner.agent", Sensitive: sensitive}
	}
	return []rbac.Perm{
		p(PermList, "perm.partner.agent.list", false),
		p(PermCreate, "perm.partner.agent.create", false),
		p(PermUpdate, "perm.partner.agent.update", false),
		p(PermStatus, "perm.partner.agent.status", false),
		p(PermOwner, "perm.partner.agent.owner", true),
		p(PermLog, "perm.partner.agent.log", false),
	}
}

func (m *module) Menus() []rbac.MenuNode {
	return []rbac.MenuNode{
		{Portal: portalCode, Name: "agent", Path: "/agent", TitleKey: "menu.agent", Icon: "Share", Sort: 100},
		{Portal: portalCode, Parent: "agent", Name: "agent-list", Path: "/agent/list", Component: "agent/list/index", TitleKey: "menu.agent.list", Icon: "List", Perm: PermList, Sort: 10, KeepAlive: true},
		{Portal: portalCode, Parent: "agent", Name: "agent-loginlog", Path: "/agent/login-logs", Component: "agent/loginlog/index", TitleKey: "menu.agent.loginlog", Icon: "Finished", Perm: PermLog, Sort: 30},
		{Portal: portalCode, Parent: "agent", Name: "agent-oplog", Path: "/agent/operation-logs", Component: "agent/oplog/index", TitleKey: "menu.agent.oplog", Icon: "Document", Perm: PermLog, Sort: 40},
	}
}

// Routes 注册代理商管理接口。前缀 /api/platform/v1/agent。写接口都在 WithActor 里（D-047）。
func (m *module) Routes(r *app.Router) {
	g := r.Portal(portalCode).Group("/agent")
	h := m.h

	g.GET("/agents", rbac.Require(PermList), h.list)
	g.GET("/agents/:id", rbac.Require(PermList), h.get)
	g.POST("/agents", rbac.Require(PermCreate), h.create, oplog.Record(OpCreate))
	g.PUT("/agents/:id", rbac.Require(PermUpdate), h.update, oplog.Record(OpUpdate))
	g.POST("/agents/:id/status", rbac.Require(PermStatus), h.setStatus, oplog.Record(OpStatus))
	g.POST("/agents/:id/reset-owner-password", rbac.Require(PermOwner), h.resetOwnerPassword, oplog.Record(OpResetOwner))
	g.PUT("/agents/:id/owner", rbac.Require(PermOwner), h.changeOwner, oplog.Record(OpChangeOwner))
	g.GET("/agents/:id/accounts", rbac.Require(PermList), h.accounts)
	g.GET("/agents/:id/sessions", rbac.Require(PermList), h.sessions)
	g.POST("/agents/:id/sessions/:sid/revoke", rbac.Require(PermStatus), h.revokeSession, oplog.Record(OpRevokeSession))
	g.GET("/agents/:id/ip-allow", rbac.Require(PermList), h.ipAllow)
	g.DELETE("/agents/:id/ip-allow", rbac.Require(PermOwner), h.clearIPAllow, oplog.Record(OpClearIPAllow))
	// 代理商自己设的 IP 黑名单：平台能看、能整体清空（D-102）
	g.GET("/agents/:id/ip-deny", rbac.Require(PermList), h.ipDeny)
	g.DELETE("/agents/:id/ip-deny", rbac.Require(PermOwner), h.clearIPDeny, oplog.Record(OpClearIPDeny))

	// 代理商端的日志：只读；查看本身也记操作日志（D-032）
	g.GET("/login-logs", rbac.Require(PermLog), h.loginLogs, oplog.Record(OpViewLoginLog))
	g.GET("/operation-logs", rbac.Require(PermLog), h.operationLogs, oplog.Record(OpViewOplog))
}

func (m *module) Start(context.Context) error { return nil }
func (m *module) Stop(context.Context) error  { return nil }
