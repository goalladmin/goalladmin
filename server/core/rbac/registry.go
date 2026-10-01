package rbac

import (
	"fmt"
	"sort"
	"sync"
)

// Registry 是进程内的权限码与菜单注册表。app 在启动时收集各模块的声明填进来；之后只读。
type Registry struct {
	mu    sync.RWMutex
	perms map[string]Perm         // key: portal + "/" + code
	menus map[string]MenuNode     // key: portal + "/" + name
	byMod map[string][]string     // 模块名 → 权限码 key 列表，便于排查
	data  map[string]DataResource // key: portal + "/" + 资源编码（D-039）
	perm2 map[string]string       // key: portal + "/" + 权限码 → 资源编码；Finalize 时建立
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{perms: map[string]Perm{}, menus: map[string]MenuNode{}, byMod: map[string][]string{},
		data: map[string]DataResource{}, perm2: map[string]string{}}
}

func key(portal, code string) string { return portal + "/" + code }

// AddPerms 登记一个模块声明的权限码；重复登记同一端同一码是错误。
func (r *Registry) AddPerms(module string, perms []Perm) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range perms {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("模块 %s: %w", module, err)
		}
		k := key(p.Portal, p.Code)
		if prev, dup := r.perms[k]; dup {
			return fmt.Errorf("模块 %s: 权限码 %s 在端 %s 已由 %q 声明", module, p.Code, p.Portal, prev.Name)
		}
		r.perms[k] = p
		r.byMod[module] = append(r.byMod[module], k)
	}
	return nil
}

// AddMenus 登记一个模块声明的菜单节点；同端同名是错误。父节点可以由别的模块声明，在 Finalize 时校验。
func (r *Registry) AddMenus(module string, menus []MenuNode) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range menus {
		if err := m.Validate(); err != nil {
			return fmt.Errorf("模块 %s: %w", module, err)
		}
		k := key(m.Portal, m.Name)
		if _, dup := r.menus[k]; dup {
			return fmt.Errorf("模块 %s: 菜单 %s 在端 %s 已存在", module, m.Name, m.Portal)
		}
		r.menus[k] = m
	}
	return nil
}

// AddDataResources 登记一个模块声明的数据资源（D-039）；同端同编码是错误。资源的第一段必须是模块名：
// 模块不能借一个叫 system:xxx 的资源去约束或放宽别人的权限码。
func (r *Registry) AddDataResources(module string, res []DataResource) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range res {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("模块 %s: %w", module, err)
		}
		if modulePrefix(d.Code) != module {
			return fmt.Errorf("模块 %s: 数据资源 %s 必须以 %s: 开头", module, d.Code, module)
		}
		k := key(d.Portal, d.Code)
		if _, dup := r.data[k]; dup {
			return fmt.Errorf("模块 %s: 数据资源 %s 在端 %s 已存在", module, d.Code, d.Portal)
		}
		d.Perms = append([]string(nil), d.Perms...)
		d.Scopes = append([]DataScope(nil), d.Scopes...)
		r.data[k] = d
	}
	return nil
}

// Finalize 在所有模块登记完之后做交叉校验：菜单的父节点存在、菜单引用的权限码已声明；
// 数据资源约束的权限码已在同一端声明，且一个权限码最多属于一个资源。
func (r *Registry) Finalize() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perm2 = map[string]string{}
	for _, d := range r.data {
		for _, p := range d.Perms {
			pk := key(d.Portal, p)
			if _, ok := r.perms[pk]; !ok {
				return fmt.Errorf("rbac: 数据资源 %s 约束的权限码 %q 没有在端 %s 声明", d.Code, p, d.Portal)
			}
			if prev, dup := r.perm2[pk]; dup && prev != d.Code {
				return fmt.Errorf("rbac: 权限码 %s 同时属于数据资源 %s 和 %s", p, prev, d.Code)
			}
			r.perm2[pk] = d.Code
		}
	}
	for k, m := range r.menus {
		if m.Parent != "" {
			if _, ok := r.menus[key(m.Portal, m.Parent)]; !ok {
				return fmt.Errorf("rbac: 菜单 %s 的父节点 %q 不存在", k, m.Parent)
			}
		}
		if m.Perm != "" {
			if _, ok := r.perms[key(m.Portal, m.Perm)]; !ok {
				return fmt.Errorf("rbac: 菜单 %s 引用了未声明的权限码 %q", k, m.Perm)
			}
		}
	}
	return nil
}

// Has 报告某端是否声明了该权限码。
func (r *Registry) Has(portal, code string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.perms[key(portal, code)]
	return ok
}

// Perm 返回某端的权限码声明。
func (r *Registry) Perm(portal, code string) (Perm, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.perms[key(portal, code)]
	return p, ok
}

// Perms 返回某端的全部权限码，按 Group、Code 排序。
func (r *Registry) Perms(portal string) []Perm {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Perm
	for _, p := range r.perms {
		if p.Portal == portal {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Code < out[j].Code
	})
	return out
}

// Menus 返回某端的全部菜单节点，按 Sort、Name 排序（树形组装由调用方完成）。
func (r *Registry) Menus(portal string) []MenuNode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []MenuNode
	for _, m := range r.menus {
		if m.Portal == portal {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// DataResource 返回某端的数据资源声明。
func (r *Registry) DataResource(portal, code string) (DataResource, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.data[key(portal, code)]
	return d, ok
}

// DataResources 返回某端的全部数据资源，按编码排序。
func (r *Registry) DataResources(portal string) []DataResource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []DataResource
	for _, d := range r.data {
		if d.Portal == portal {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// ResourceOf 返回约束某个权限码的数据资源编码；不受任何资源约束时返回空。
func (r *Registry) ResourceOf(portal, perm string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.perm2[key(portal, perm)]
}
