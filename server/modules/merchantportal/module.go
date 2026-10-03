// Package merchantportal 是商户端自己的后台（D-067）：概览、子账号、角色与权限、会话、登录日志、操作日志、
// 主体的 IP 白名单、个人中心。这些都由内核的端后台套件（core/orgportal）提供，这个模块只是注册商户端、把套件挂上。
// 商户端的业务功能（订单、结算……）以后作为别的模块加进商户程序（server/cmd/merchant）。
//
// 它只编进商户程序，不编进平台程序和代理商程序（scripts/depcheck.sh 规则 5）。
package merchantportal

import (
	"context"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/orgportal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

type module struct{ kit *orgportal.Kit }

// Module 返回商户端模块。在商户程序（server/cmd/merchant）里显式注册。
func Module() app.Module { return &module{} }

func (m *module) Name() string { return "merchantportal" }

// Init 注册商户端：主体端，用户来源是 core/org 的商户账号。
func (m *module) Init(deps *app.Deps) error {
	kit, err := orgportal.New(deps, org.Merchant())
	if err != nil {
		return err
	}
	m.kit = kit
	return deps.Portals.Register(kit.Portal())
}

func (m *module) Perms() []rbac.Perm     { return m.kit.Perms() }
func (m *module) Menus() []rbac.MenuNode { return m.kit.Menus() }
func (m *module) Routes(r *app.Router)   { m.kit.Routes(r) }

func (m *module) Start(ctx context.Context) error { return nil }
func (m *module) Stop(ctx context.Context) error  { return nil }
