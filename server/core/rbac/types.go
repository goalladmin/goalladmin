// Package rbac 是授权：权限码注册表、菜单声明、路由守卫。
//
// 本文件只定义模块声明用的数据类型；守卫（Public / AuthOnly / Require）和 Casbin 封装在 service.go、guard.go。
package rbac

import (
	"fmt"
	"regexp"
)

// Perm 是模块声明的一个权限码（规范 §6.1）。
type Perm struct {
	Code      string // 三段：<模块>:<资源>:<动作>，全小写，段内可用连字符
	Name      string // 授权界面上显示的名字（i18n 键或直接文本）
	Portal    string // 属于哪个端
	Group     string // 授权界面上的分组
	Sensitive bool   // 敏感权限：只有超管能授予（规范 §6.5）
}

// MenuNode 是模块声明的一个菜单节点（规范 §6.6）。
type MenuNode struct {
	Portal    string // 挂到哪个端
	Parent    string // 父节点 Name；空表示顶级
	Name      string // 端内唯一；小写字母开头，只含小写字母、数字和连字符（后台分组名以 @ 开头，不会与之重名，D-025）
	Path      string // 前端路由路径
	Component string // 相对 apps/<端>/views/ 的组件路径；目录节点留空
	TitleKey  string // i18n 键
	Icon      string
	Perm      string // 可见所需的权限码；空表示登录即可见
	Sort      int    // 越小越靠前
	KeepAlive bool
	Hidden    bool // 不在侧边栏显示但仍注册路由（如详情页）
}

var permCodeRe = regexp.MustCompile(`^[a-z][a-z0-9-]*:[a-z][a-z0-9-]*:[a-z][a-z0-9-]*$`)

var menuNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// ValidMenuName 报告代码声明的菜单名是否合规。
func ValidMenuName(name string) bool { return menuNameRe.MatchString(name) }

// ValidPermCode 报告权限码是否符合三段规范。
func ValidPermCode(code string) bool { return permCodeRe.MatchString(code) }

// Validate 检查声明的合法性，模块注册时调用。
func (p Perm) Validate() error {
	if !ValidPermCode(p.Code) {
		return fmt.Errorf("rbac: 权限码 %q 不符合 <模块>:<资源>:<动作> 规范", p.Code)
	}
	if p.Portal == "" {
		return fmt.Errorf("rbac: 权限码 %q 没有指定端", p.Code)
	}
	return nil
}

// Validate 检查菜单节点的合法性。
func (m MenuNode) Validate() error {
	if m.Name == "" || m.Portal == "" {
		return fmt.Errorf("rbac: 菜单节点缺少 Name 或 Portal: %+v", m)
	}
	if !ValidMenuName(m.Name) {
		return fmt.Errorf("rbac: 菜单名 %q 不合规范：小写字母开头，只含小写字母、数字和连字符，最长 64 位", m.Name)
	}
	if m.Parent != "" && !ValidMenuName(m.Parent) {
		return fmt.Errorf("rbac: 菜单 %q 的父节点名 %q 不合规范", m.Name, m.Parent)
	}
	if m.Perm != "" && !ValidPermCode(m.Perm) {
		return fmt.Errorf("rbac: 菜单 %q 的权限码 %q 不合规范", m.Name, m.Perm)
	}
	return nil
}
