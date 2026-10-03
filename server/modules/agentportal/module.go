// Package agentportal 是代理商端自己的后台（D-067）：概览、名下商户（只读）、子账号、角色与权限、会话、登录日志、
// 操作日志、主体的 IP 白名单、个人中心。除了"名下商户"，都由内核的端后台套件（core/orgportal）提供。
//
// 代理商只读名下商户：只有 GET，按 org.ByChildOrgs 过滤（代理商取自会话里的身份），商户改了归属，原代理商下一次请求就看不到。
// 它只编进代理商程序，不编进平台程序和商户程序（scripts/depcheck.sh 规则 5）。
package agentportal

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/orgportal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// PermMerchantList 是查看名下商户的权限码。
const PermMerchantList = "agent:merchant:list"

type module struct {
	deps *app.Deps
	kit  *orgportal.Kit
}

// Module 返回代理商端模块。在代理商程序（server/cmd/agent）里显式注册。
func Module() app.Module { return &module{} }

func (m *module) Name() string { return "agentportal" }

// Init 注册代理商端：主体端，用户来源是 core/org 的代理商账号；概览加一项"名下商户"计数。
func (m *module) Init(deps *app.Deps) error {
	m.deps = deps
	kit, err := orgportal.New(deps, org.Agent(), orgportal.WithCount(orgportal.Count{
		Key: "merchants", Perm: PermMerchantList,
		Fn: func(ctx context.Context) (int64, error) {
			_, n, err := deps.Orgs.ChildMerchants(ctx, nil, httpx.PageQuery{Page: 1, PageSize: 1})
			return n, err
		},
	}))
	if err != nil {
		return err
	}
	m.kit = kit
	return deps.Portals.Register(kit.Portal())
}

func (m *module) Perms() []rbac.Perm {
	return append(m.kit.Perms(), rbac.Perm{
		Code: PermMerchantList, Name: "perm.agent.merchant.list", Portal: m.kit.Code(), Group: "agent.merchant",
	})
}

func (m *module) Menus() []rbac.MenuNode {
	return append(m.kit.Menus(), rbac.MenuNode{
		Portal: m.kit.Code(), Name: "agent-merchants", Path: "/merchants", Component: "merchants/index",
		TitleKey: "menu.agent.merchants", Icon: "Shop", Perm: PermMerchantList, Sort: 10, KeepAlive: true,
	})
}

// Routes 挂套件的接口，再加名下商户：只有 GET。
func (m *module) Routes(r *app.Router) {
	m.kit.Routes(r)
	g := r.Portal(m.kit.Code()).LiveAuth() // 和套件的后台一样按库核对（D-073）
	g.GET("/merchants", rbac.Require(PermMerchantList), m.listMerchants)
	g.GET("/merchants/:id", rbac.Require(PermMerchantList), m.getMerchant)
}

func (m *module) Start(ctx context.Context) error { return nil }
func (m *module) Stop(ctx context.Context) error  { return nil }

func (m *module) listMerchants(c *gin.Context) {
	q := httpx.BindPage(c)
	var status *int
	if s := c.Query("status"); s == "0" || s == "1" {
		v := int(s[0] - '0')
		status = &v
	}
	list, total, err := m.deps.Orgs.ChildMerchants(c.Request.Context(), status, q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, list, total, q.Page, q.PageSize)
}

func (m *module) getMerchant(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	v, err := m.deps.Orgs.ChildMerchant(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, v)
}
