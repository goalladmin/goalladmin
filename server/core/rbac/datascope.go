package rbac

// 按部门的数据权限（规范 §7.2，docs/decisions.md D-039）。
//
// 权限码管"能不能做这件事"，数据范围管"能对哪些数据做"：范围只收窄权限码，永远不放宽。
// 模块在代码里声明数据资源（DataResource），角色在每个资源上选一个范围，存 ga_role_data_scope；
// 判断某个权限码的范围时，只看拥有这个权限码的启用角色，取最宽的；超管永远是全部。

import (
	"fmt"
	"regexp"
	"slices"

	"gorm.io/gorm"
)

// DataScope 是数据范围。四种范围是一条链：每种都包含本人的数据，宽的包含窄的。
type DataScope string

// 数据范围，从窄到宽。
const (
	ScopeSelf     DataScope = "self"      // 仅本人
	ScopeDept     DataScope = "dept"      // 本部门（加本人）
	ScopeDeptTree DataScope = "dept_tree" // 本部门及下级（加本人）
	ScopeAll      DataScope = "all"       // 全部
)

// AllScopes 是全部范围，从窄到宽。
func AllScopes() []DataScope { return []DataScope{ScopeSelf, ScopeDept, ScopeDeptTree, ScopeAll} }

// rank 返回范围的宽度：越宽越大；不认识的范围是 -1。
func (s DataScope) rank() int {
	switch s {
	case ScopeSelf:
		return 0
	case ScopeDept:
		return 1
	case ScopeDeptTree:
		return 2
	case ScopeAll:
		return 3
	}
	return -1
}

// Valid 报告是不是认识的范围。
func (s DataScope) Valid() bool { return s.rank() >= 0 }

// Wider 报告 s 是否比 o 宽。
func (s DataScope) Wider(o DataScope) bool { return s.rank() > o.rank() }

// DataResource 是模块声明的一个数据资源（D-039）。
type DataResource struct {
	Code    string      // <模块>:<资源>，全小写，段内可用连字符；必须以声明它的模块名开头
	Name    string      // 授权界面上显示的名字（i18n 键 data.<模块>.<资源>，或直接文本）
	Portal  string      // 属于哪个端
	Perms   []string    // 受这个资源约束的权限码：只能是同一个端、同一个模块（权限码第一段与资源第一段相同）的；一个权限码最多属于一个资源
	Default DataScope   // 角色没设置时的范围；安全的一侧是窄
	Scopes  []DataScope // 可选的范围；空表示四种都可以。必须包含 Default
}

var dataResourceRe = regexp.MustCompile(`^[a-z][a-z0-9-]*:[a-z][a-z0-9-]*$`)

// Allowed 报告这个资源是否允许选范围 s。
func (d DataResource) Allowed(s DataScope) bool {
	if !s.Valid() {
		return false
	}
	return len(d.Scopes) == 0 || slices.Contains(d.Scopes, s)
}

// Choices 返回可选的范围，从窄到宽。
func (d DataResource) Choices() []DataScope {
	out := make([]DataScope, 0, 4)
	for _, s := range AllScopes() {
		if d.Allowed(s) {
			out = append(out, s)
		}
	}
	return out
}

// Validate 检查声明的合法性，模块注册时调用。
func (d DataResource) Validate() error {
	if !dataResourceRe.MatchString(d.Code) {
		return fmt.Errorf("rbac: 数据资源 %q 不符合 <模块>:<资源> 规范", d.Code)
	}
	if d.Portal == "" {
		return fmt.Errorf("rbac: 数据资源 %q 没有指定端", d.Code)
	}
	if len(d.Perms) == 0 {
		return fmt.Errorf("rbac: 数据资源 %q 没有列出受约束的权限码", d.Code)
	}
	for _, s := range d.Scopes {
		if !s.Valid() {
			return fmt.Errorf("rbac: 数据资源 %q 的可选范围 %q 不认识", d.Code, s)
		}
	}
	if !d.Allowed(d.Default) {
		return fmt.Errorf("rbac: 数据资源 %q 的默认范围 %q 不在可选范围里", d.Code, d.Default)
	}
	mod := modulePrefix(d.Code)
	for _, p := range d.Perms {
		if !ValidPermCode(p) {
			return fmt.Errorf("rbac: 数据资源 %q 的权限码 %q 不合规范", d.Code, p)
		}
		if modulePrefix(p) != mod {
			return fmt.Errorf("rbac: 数据资源 %q 只能约束本模块（%s:）的权限码，%q 不是", d.Code, mod, p)
		}
	}
	return nil
}

func modulePrefix(code string) string {
	for i := 0; i < len(code); i++ {
		if code[i] == ':' {
			return code[:i]
		}
	}
	return code
}

// DataFilter 是某个人在某个资源、某个权限码上的数据范围，已经解析成具体的部门（D-039）。
//
// 数据的"部门"和"本人"由调用方给出列名：用户表是 dept_id 和 id，业务表通常是 dept_id 和 created_by。
type DataFilter struct {
	Scope   DataScope // 生效的范围；没分配部门时部门类范围已降为 self
	UserID  uint64    // 操作人
	DeptIDs []uint64  // 部门类范围覆盖的部门；仅本人和全部时为空
}

// All 报告是否不受限。
func (f DataFilter) All() bool { return f.Scope == ScopeAll }

// Allows 判断一条数据是否在范围内：deptID 是数据所属部门，ownerID 是数据所属的人。
func (f DataFilter) Allows(deptID, ownerID uint64) bool {
	if f.All() {
		return true
	}
	if ownerID != 0 && ownerID == f.UserID {
		return true
	}
	return deptID != 0 && slices.Contains(f.DeptIDs, deptID)
}

// AllowsDept 判断一个部门是否在范围内（新建数据、改数据所属部门时用）：0（未分配）只有全部范围才算。
func (f DataFilter) AllowsDept(deptID uint64) bool {
	if f.All() {
		return true
	}
	return deptID != 0 && slices.Contains(f.DeptIDs, deptID)
}

var columnRe = regexp.MustCompile(`^[a-z_][a-z0-9_]*(\.[a-z_][a-z0-9_]*)?$`)

// Apply 把范围加到查询上。deptCol 是数据所属部门的列，ownerCol 是数据所属的人的列；列名是代码里的常量，
// 不合规范时直接 panic（它们不该来自请求）。deptCol 可以为空：资源没有部门列时，部门类范围只剩本人。
func (f DataFilter) Apply(tx *gorm.DB, deptCol, ownerCol string) *gorm.DB {
	if f.All() {
		return tx
	}
	if !columnRe.MatchString(ownerCol) || (deptCol != "" && !columnRe.MatchString(deptCol)) {
		panic(fmt.Sprintf("rbac: DataFilter.Apply 的列名不合规范：%q、%q", deptCol, ownerCol))
	}
	if deptCol == "" || len(f.DeptIDs) == 0 {
		return tx.Where(ownerCol+" = ?", f.UserID)
	}
	return tx.Where("("+deptCol+" IN ? OR "+ownerCol+" = ?)", f.DeptIDs, f.UserID)
}
