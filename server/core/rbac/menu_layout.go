package rbac

// 菜单管理（docs/decisions.md D-025）：结构归代码，后台只改显示和位置。
//
//   - 代码声明的菜单（路径、页面组件、权限码、所属端）后台不能改；能改的是显示名、图标、在侧边栏隐藏、上级和排序。
//   - 后台可以新建分组：纯目录，没有路径、页面和权限码，分组名由服务端生成（@g- 开头）。
//   - 可见性仍只由权限码决定：一个菜单要求的权限码 = 它自己的权限码 + 代码里所有祖先的权限码，与它现在挪到哪里无关；
//     带权限码的目录（及其代码子孙）只能装它在代码里本来的子孙，否则挪进去的菜单会被多要求一个权限码而消失。
//   - ga_menu_custom 只存"与代码不同的部分"；代码删掉的菜单，它的覆盖行自动失效。
//   - 所有写操作在一个事务里先读后写，并由进程内互斥串行化（v0.1 只支持单实例，规范 §6.3）。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 菜单节点的种类（管理界面用）。
const (
	MenuKindDir   = "dir"   // 代码声明的目录（没有页面组件）
	MenuKindPage  = "page"  // 代码声明的页面
	MenuKindGroup = "group" // 后台新建的分组
)

// 菜单管理的限制。
const (
	MaxMenuDepth     = 4       // 调整后菜单树的最大层数；代码自己声明得更深时以代码为准
	MaxMenuGroups    = 200     // 每个端最多的分组数
	MaxMenuTitleLen  = 32      // 显示名最多字符数（按 Unicode 字符计）
	MaxMenuSort      = 1000000 // 排序值上限
	MaxMenuLayoutLen = 2000    // 一次提交的节点数上限
	DefaultMenuLang  = "zh-CN"
	menuGroupPrefix  = "@g-"
	maxTitlesJSON    = 1024 // ga_menu_custom.titles 的列宽
	kindCode         = "code"
	kindGroup        = "group"
)

var (
	menuIconRe  = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,63}$`)
	menuLangRe  = regexp.MustCompile(`^[a-z]{2}-[A-Z]{2}$`)
	menuGroupRe = regexp.MustCompile(`^@g-[0-9a-f]{12}$`)
)

// ValidMenuIcon 报告图标名是否合规：图标组件名（大写字母开头的字母数字）。空表示不设图标。
func ValidMenuIcon(icon string) bool { return icon == "" || menuIconRe.MatchString(icon) }

// menuCustom 对应 ga_menu_custom。
type menuCustom struct {
	ID        uint64    `gorm:"column:id;primaryKey"`
	Portal    string    `gorm:"column:portal"`
	Name      string    `gorm:"column:name"`
	Kind      string    `gorm:"column:kind"`
	Titles    string    `gorm:"column:titles"`
	Icon      string    `gorm:"column:icon"`
	Hidden    bool      `gorm:"column:hidden"`
	Moved     bool      `gorm:"column:moved"`
	Parent    string    `gorm:"column:parent"`
	Sort      uint      `gorm:"column:sort"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	CreatedBy uint64    `gorm:"column:created_by"`
	UpdatedBy uint64    `gorm:"column:updated_by"`
}

// TableName 固定表名。
func (menuCustom) TableName() string { return "ga_menu_custom" }

// empty 报告代码菜单的覆盖行是否已与代码没有差异（可以删掉）。
func (r *menuCustom) empty() bool {
	return r.Kind == kindCode && r.Titles == "" && r.Icon == "" && !r.Hidden && !r.Moved
}

// layoutNode 是生效后的一个节点：代码声明叠加后台调整，或后台分组。
type layoutNode struct {
	MenuNode
	Titles map[string]string
	Group  bool
	// codeDir 表示代码里声明为目录（没有页面组件且代码里有子节点）：子节点全被挪走后不再显示。
	codeDir bool
	// custom 是对应的后台调整行，没有时为 nil。
	custom *menuCustom
	// def 是代码声明的原值（分组为零值）。
	def MenuNode
	// need 是看到这个节点必须具备的全部权限码：自己的加上代码里所有祖先的。分组为空。
	// 它只由代码决定，挪位置不改变它，所以"谁能看到这个菜单"不受后台调整影响。
	need []string
}

// needs 报告 a 要求的权限码是否都在 b 要求的权限码里（a ⊆ b）。
func needs(a, b []string) bool {
	for _, x := range a {
		found := false
		for _, y := range b {
			if x == y {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// accepts 报告 p 能不能做 c 的上级：c 在代码里的上级总是可以；否则 p 必须是目录或分组，
// 且 p 要求的权限码 c 都要求（这样挪进去不会让 c 多要求一个权限码而对某些人消失）。
func accepts(p, c *layoutNode) bool {
	if p == c {
		return false
	}
	if !c.Group && c.def.Parent == p.Name {
		return true
	}
	return p.container() && needs(p.need, c.need)
}

// container 报告节点能不能装子节点：分组，或没有页面组件的代码菜单。
func (n *layoutNode) container() bool { return n.Group || n.Component == "" }

// ---- 读取 ----

func (s *Service) loadMenuCustom(ctx context.Context, portal string, lock bool) ([]menuCustom, error) {
	var rows []menuCustom
	q := db.From(ctx).Where("portal = ?", portal).Order("id ASC")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return rows, q.Find(&rows).Error
}

// menuCustomCached 读某端的后台调整（带进程内缓存，写入提交后清掉）。
// 读库前记下缓存的失效代数，读完用 SetIfGen 回填：期间有写入提交并清过缓存就不回填，免得把旧数据填回去。
// 判断和写入是缓存内部的一步，不是"查代数再 Set"两步——两步之间写入方仍可能清缓存（D-043）。
func (s *Service) menuCustomCached(ctx context.Context, portal string) ([]menuCustom, error) {
	if v, ok := s.menuCache.Get(portal); ok {
		return v, nil
	}
	gen := s.menuCache.Gen()
	rows, err := s.loadMenuCustom(ctx, portal, false)
	if err != nil {
		return nil, err
	}
	s.hook("menu.read")
	if !db.InTx(ctx) { // 事务快照可能早于取代数的那一刻，不能拿它回填
		s.menuCache.SetIfGen(portal, rows, gen)
	}
	return rows, nil
}

// menuChanged 在写入提交后调用：清缓存（Delete 会让缓存的失效代数加一，正在读库的读方不会回填旧数据）。
func (s *Service) menuChanged(portal string) {
	s.menuCache.Delete(portal)
}

// parseTitles 解析存储的显示名；坏数据当作没有。
func parseTitles(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

// resolveLayout 把代码声明和后台调整合成生效的节点列表（按 Sort、Name 排序）。
// 调整里引用了不存在的上级、上级不能装子节点、或形成环的，回到代码位置（分组回到顶级），不报错：
// 这种情况只会因为代码删掉或改动了菜单而出现，菜单不能因此整体不可用。
func resolveLayout(code []MenuNode, rows []menuCustom) []*layoutNode {
	codeChildren := map[string]bool{}
	for _, n := range code {
		if n.Parent != "" {
			codeChildren[n.Parent] = true
		}
	}
	byRow := map[string]*menuCustom{}
	for i := range rows {
		byRow[rows[i].Name] = &rows[i]
	}
	nodes := make([]*layoutNode, 0, len(code)+len(rows))
	byName := map[string]*layoutNode{}
	for _, c := range code {
		n := &layoutNode{MenuNode: c, def: c, codeDir: c.Component == "" && codeChildren[c.Name]}
		if r, ok := byRow[c.Name]; ok && r.Kind == kindCode {
			n.custom = r
			n.Titles = parseTitles(r.Titles)
			if r.Icon != "" {
				n.Icon = r.Icon
			}
			n.Hidden = c.Hidden || r.Hidden
			if r.Moved {
				n.Parent, n.Sort = r.Parent, int(r.Sort)
			}
		}
		nodes = append(nodes, n)
		byName[n.Name] = n
	}
	codeByName := make(map[string]MenuNode, len(code))
	for _, c := range code {
		codeByName[c.Name] = c
	}
	for _, n := range nodes {
		seen := map[string]bool{}
		for c, ok := codeByName[n.Name], true; ok && !seen[c.Name]; c, ok = codeByName[c.Parent] {
			seen[c.Name] = true
			if c.Perm != "" {
				n.need = append(n.need, c.Perm)
			}
		}
	}
	for i := range rows {
		r := &rows[i]
		if r.Kind != kindGroup || !menuGroupRe.MatchString(r.Name) {
			continue
		}
		n := &layoutNode{
			MenuNode: MenuNode{Portal: r.Portal, Name: r.Name, Parent: r.Parent, Icon: r.Icon, Hidden: r.Hidden, Sort: int(r.Sort)},
			Titles:   parseTitles(r.Titles), Group: true, custom: r,
		}
		nodes = append(nodes, n)
		byName[n.Name] = n
	}

	revert := func(n *layoutNode) {
		if n.Group {
			n.Parent = ""
		} else {
			n.Parent, n.Sort = n.def.Parent, n.def.Sort
		}
	}
	for _, n := range nodes {
		if n.Parent == "" {
			continue
		}
		if p, ok := byName[n.Parent]; !ok || !accepts(p, n) {
			revert(n)
		}
	}
	// 环：从每个节点向上走，走回自己或超过节点总数就说明在环上，回到代码位置。最多两轮（代码位置本身无环时一轮即可）。
	for round := 0; round < 2; round++ {
		for _, n := range nodes {
			cyclic := false
			for cur, steps := n, 0; cur != nil && cur.Parent != ""; steps++ {
				cur = byName[cur.Parent]
				if cur == n || steps > len(nodes) {
					cyclic = true
					break
				}
			}
			if cyclic {
				revert(n)
			}
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Sort != nodes[j].Sort {
			return nodes[i].Sort < nodes[j].Sort
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes
}

// layout 返回某端生效的菜单节点。
func (s *Service) layout(ctx context.Context, portal string) ([]*layoutNode, error) {
	rows, err := s.menuCustomCached(ctx, portal)
	if err != nil {
		return nil, err
	}
	return resolveLayout(s.reg.Menus(portal), rows), nil
}

// buildTree 把生效节点组装成树并按可见性裁剪（规范 §6.6）：
// 每个节点先看 need（自己和代码祖先的权限码）是否都具备；页面满足就可见，不看子节点；
// 目录和分组还要有至少一个可见的子节点；代码目录的子节点全被挪走、或分组是空的，不显示。
func buildTree(nodes []*layoutNode, visible func(perm string) bool) []*MenuTree {
	children := map[string][]*layoutNode{}
	var roots []*layoutNode
	for _, n := range nodes {
		if n.Parent == "" {
			roots = append(roots, n)
		} else {
			children[n.Parent] = append(children[n.Parent], n)
		}
	}
	var build func(n *layoutNode) *MenuTree
	build = func(n *layoutNode) *MenuTree {
		t := &MenuTree{
			Name: n.Name, Path: n.Path, Component: n.Component, TitleKey: n.TitleKey, Titles: n.Titles,
			Icon: n.Icon, KeepAlive: n.KeepAlive, Hidden: n.Hidden, Sort: n.Sort,
		}
		for _, c := range children[n.Name] {
			if ct := build(c); ct != nil {
				t.Children = append(t.Children, ct)
			}
		}
		for _, perm := range n.need {
			if !visible(perm) {
				return nil
			}
		}
		isDir := len(children[n.Name]) > 0
		switch {
		case n.Component != "":
			// 页面：权限满足即可见（代码里挂在它下面的详情页不影响它）
		case isDir && len(t.Children) == 0:
			return nil
		case !isDir && (n.Group || n.codeDir):
			return nil
		}
		sort.SliceStable(t.Children, func(i, j int) bool { return t.Children[i].Sort < t.Children[j].Sort })
		return t
	}
	out := make([]*MenuTree, 0, len(roots))
	for _, r := range roots {
		if t := build(r); t != nil {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sort < out[j].Sort })
	return out
}

// ---- 管理界面 ----

// MenuDefault 是代码菜单在代码里声明的原值，供管理界面对比和"恢复默认"。
type MenuDefault struct {
	Parent string `json:"parent"`
	Sort   int    `json:"sort"`
	Icon   string `json:"icon"`
}

// MenuAdminNode 是管理界面上的一个菜单节点（扁平列表，前端按 parent 组装成树）。
type MenuAdminNode struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Parent     string            `json:"parent"`
	Sort       int               `json:"sort"`
	Path       string            `json:"path"`
	Component  string            `json:"component"`
	Perm       string            `json:"perm"`
	TitleKey   string            `json:"titleKey"`
	Titles     map[string]string `json:"titles"`
	Icon       string            `json:"icon"`
	Hidden     bool              `json:"hidden"`
	CodeHidden bool              `json:"codeHidden"`
	Customized bool              `json:"customized"`
	// Need 是看到它要具备的全部权限码（自己的加代码祖先的），前端拖动时据此提示哪里放不进去。
	Need    []string     `json:"need"`
	Default *MenuDefault `json:"default,omitempty"`
}

// MenuAdmin 列出操作者所在端的全部菜单节点（不按权限裁剪），供菜单管理页使用。
func (s *Service) MenuAdmin(ctx context.Context, actor auth.Principal) ([]MenuAdminNode, error) {
	nodes, err := s.layout(ctx, actor.Portal)
	if err != nil {
		return nil, err
	}
	out := make([]MenuAdminNode, 0, len(nodes))
	for _, n := range nodes {
		v := MenuAdminNode{
			Name: n.Name, Parent: n.Parent, Sort: n.Sort, Path: n.Path, Component: n.Component, Perm: n.Perm,
			TitleKey: n.TitleKey, Titles: n.Titles, Icon: n.Icon, Hidden: n.Hidden,
		}
		if v.Titles == nil {
			v.Titles = map[string]string{}
		}
		v.Need = append([]string{}, n.need...)
		switch {
		case n.Group:
			v.Kind = MenuKindGroup
		case n.Component == "":
			v.Kind = MenuKindDir
		default:
			v.Kind = MenuKindPage
		}
		if !n.Group {
			v.CodeHidden = n.def.Hidden
			v.Customized = n.custom != nil
			v.Default = &MenuDefault{Parent: n.def.Parent, Sort: n.def.Sort, Icon: n.def.Icon}
		}
		out = append(out, v)
	}
	return out, nil
}

// MenuDisplayInput 是修改一个菜单显示方式的入参。
type MenuDisplayInput struct {
	Titles map[string]string // 按语言的显示名；代码菜单留空的语言沿用翻译键，分组必须有 zh-CN
	Icon   string            // 图标名；代码菜单留空沿用代码
	Hidden bool              // 在侧边栏隐藏（代码里已隐藏的菜单不能取消隐藏）
	Sort   *int              // 同级排序值（0–MaxMenuSort，越小越靠前）；nil 表示不改。上级不变
}

// MenuGroupInput 是新建分组的入参。
type MenuGroupInput struct {
	Titles map[string]string
	Icon   string
	Hidden bool
	Parent string // 上级目录或分组；空表示顶级
	Sort   int
}

// MenuPosition 是拖动后一个节点的位置。
type MenuPosition struct {
	Name   string
	Parent string
	Sort   int
}

// fieldErr 返回带翻译键的参数错误（D-026）：key 以 rbac.menu. 开头，msg 是英文兜底说明。
func fieldErr(field, key, msg string, params ...any) error {
	return httpx.ErrValidation.WithFields(httpx.NewField(field, "rbac.menu."+key, msg, params...))
}

// normalizeTitles 校验并规整显示名：语言代码形如 zh-CN；去掉首尾空白，空的语言丢掉；
// 每个最多 MaxMenuTitleLen 个字符，不能含控制字符。返回存储用的 JSON（没有任何语言时为空串）。
func normalizeTitles(in map[string]string) (map[string]string, string, error) {
	if len(in) > 8 {
		return nil, "", fieldErr("titles", "tooManyLangs", "too many languages")
	}
	out := map[string]string{}
	for lang, v := range in {
		if !menuLangRe.MatchString(lang) {
			return nil, "", fieldErr("titles", "badLang", fmt.Sprintf("invalid language code %q, expected a form like en-US", lang), "lang", lang)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if utf8.RuneCountInString(v) > MaxMenuTitleLen {
			return nil, "", fieldErr("titles", "titleMax", fmt.Sprintf("a title can be at most %d characters", MaxMenuTitleLen), "max", MaxMenuTitleLen)
		}
		for _, r := range v {
			// 控制字符和格式字符（从右到左覆盖、零宽字符等）都不允许：后者能做出看起来一样、实际不同的名字
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return nil, "", fieldErr("titles", "titleChars", "a title cannot contain control or invisible formatting characters")
			}
		}
		out[lang] = v
	}
	if len(out) == 0 {
		return out, "", nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	if len(b) > maxTitlesJSON {
		return nil, "", fieldErr("titles", "titlesTooLong", "titles are too long")
	}
	return out, string(b), nil
}

func validSort(v int) bool { return v >= 0 && v <= MaxMenuSort }

// menuTx 在事务里、持有互斥时执行菜单写操作：读出（并锁住）该端的调整行，算出生效节点，交给 fn；
// 提交后清掉该端的缓存。操作人在超管锁里重新认定（D-047），加锁顺序固定为"菜单互斥 → 超管锁"。
func (s *Service) menuTx(ctx context.Context, actor auth.Principal, fn func(ctx context.Context, nodes []*layoutNode, byName map[string]*layoutNode) error) error {
	portal := actor.Portal
	s.menuMu.Lock()
	defer s.menuMu.Unlock()
	return s.WithActor(ctx, actor, func(ctx context.Context, _ auth.Principal) error {
		rows, err := s.loadMenuCustom(ctx, portal, true)
		if err != nil {
			return err
		}
		nodes := resolveLayout(s.reg.Menus(portal), rows)
		byName := make(map[string]*layoutNode, len(nodes))
		for _, n := range nodes {
			byName[n.Name] = n
		}
		if err := fn(ctx, nodes, byName); err != nil {
			return err
		}
		db.AfterCommit(ctx, func() { s.menuChanged(portal) })
		return nil
	})
}

// saveCustom 写回一行调整：代码菜单的行与代码不再有差异时删除，否则插入或更新。
func (s *Service) saveCustom(ctx context.Context, actor auth.Principal, r *menuCustom) error {
	now := s.now().UTC()
	if r.empty() {
		if r.ID == 0 {
			return nil
		}
		return db.From(ctx).Where("id = ?", r.ID).Delete(&menuCustom{}).Error
	}
	r.UpdatedAt, r.UpdatedBy = now, actor.UserID
	if r.ID == 0 {
		r.CreatedAt, r.CreatedBy = now, actor.UserID
		return db.From(ctx).Create(r).Error
	}
	return db.From(ctx).Model(&menuCustom{}).Where("id = ?", r.ID).Updates(map[string]any{
		"titles": r.Titles, "icon": r.Icon, "hidden": r.Hidden, "moved": r.Moved, "parent": r.Parent, "sort": r.Sort,
		"updated_at": r.UpdatedAt, "updated_by": r.UpdatedBy,
	}).Error
}

// customFor 返回节点已有的调整行，没有时新建一行（未写库）。
func customFor(portal string, n *layoutNode) *menuCustom {
	if n.custom != nil {
		c := *n.custom
		return &c
	}
	return &menuCustom{Portal: portal, Name: n.Name, Kind: kindCode}
}

// UpdateMenu 修改一个菜单（代码菜单或分组）的显示方式。
func (s *Service) UpdateMenu(ctx context.Context, actor auth.Principal, name string, in MenuDisplayInput) error {
	titles, titlesJSON, err := normalizeTitles(in.Titles)
	if err != nil {
		return err
	}
	if !ValidMenuIcon(in.Icon) {
		return fieldErr("icon", "badIcon", "invalid icon name")
	}
	if in.Sort != nil && !validSort(*in.Sort) {
		return fieldErr("sort", "sortRange", "sort value out of range", "max", MaxMenuSort)
	}
	portal := actor.Portal
	return s.menuTx(ctx, actor, func(ctx context.Context, _ []*layoutNode, byName map[string]*layoutNode) error {
		n, ok := byName[name]
		if !ok {
			return httpx.ErrNotFound
		}
		r := customFor(portal, n)
		if n.Group {
			if titles[DefaultMenuLang] == "" {
				return fieldErr("titles", "groupNeedsDefault", "a group needs a title in the default language", "lang", DefaultMenuLang)
			}
			r.Titles, r.Icon, r.Hidden = titlesJSON, in.Icon, in.Hidden
			if in.Sort != nil {
				r.Sort = uint(*in.Sort)
			}
			return s.saveCustom(ctx, actor, r)
		}
		if n.def.Hidden && !in.Hidden {
			return httpx.ErrDeclaredInCode.WithFields(httpx.NewField("hidden", "rbac.menu.codeHidden", "a menu hidden in code cannot be shown"))
		}
		icon := in.Icon
		if icon == n.def.Icon {
			icon = ""
		}
		r.Titles, r.Icon = titlesJSON, icon
		r.Hidden = in.Hidden && !n.def.Hidden
		// 改排序只动同级顺序，上级保持当前生效的上级；回到代码里的位置时清掉"已移动"
		if in.Sort != nil && *in.Sort != n.Sort {
			if n.Parent == n.def.Parent && *in.Sort == n.def.Sort {
				r.Moved, r.Parent, r.Sort = false, "", 0
			} else {
				r.Moved, r.Parent, r.Sort = true, n.Parent, uint(*in.Sort)
			}
		}
		return s.saveCustom(ctx, actor, r)
	})
}

// checkPositions 校验一组完整的位置（节点名 → 上级名）：上级可以接受（accepts）、不成环、层数不超过 limit。
// 所有改变位置的写操作（保存拖动结果、恢复默认、删除分组、新建分组）都先算出结果再用它校验，不合格就整体拒绝。
func checkPositions(byName map[string]*layoutNode, parentOf map[string]string, limit int) error {
	for name, p := range parentOf {
		if p == "" {
			continue
		}
		pn, ok := byName[p]
		if !ok || !accepts(pn, byName[name]) {
			return fieldErr("items", "badParent", fmt.Sprintf("%q cannot be placed under %q: the parent must be a folder or group of this portal, and a folder with a permission code only holds its own children from code", name, p), "name", name, "parent", p)
		}
	}
	for name := range parentOf {
		depth, seen := 1, map[string]bool{name: true}
		for p := parentOf[name]; p != ""; p = parentOf[p] {
			if seen[p] {
				return fieldErr("items", "cycle", fmt.Sprintf("the parents of %q form a cycle", name), "name", name)
			}
			seen[p] = true
			depth++
		}
		if depth > limit {
			return fieldErr("items", "maxDepth", fmt.Sprintf("menus can be at most %d levels deep", limit), "limit", limit)
		}
	}
	return nil
}

// currentPositions 返回当前生效的位置（节点名 → 上级名）。
func currentPositions(nodes []*layoutNode) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		out[n.Name] = n.Parent
	}
	return out
}

// ResetMenu 把一个代码菜单恢复成代码声明的样子（显示名、图标、隐藏、位置）。
func (s *Service) ResetMenu(ctx context.Context, actor auth.Principal, name string) error {
	portal := actor.Portal
	limit := s.maxDepth(portal)
	return s.menuTx(ctx, actor, func(ctx context.Context, nodes []*layoutNode, byName map[string]*layoutNode) error {
		n, ok := byName[name]
		if !ok {
			return httpx.ErrNotFound
		}
		if n.Group {
			return fieldErr("name", "groupNoDefault", "a group has no default to restore")
		}
		if n.custom == nil {
			return nil
		}
		pos := currentPositions(nodes)
		pos[name] = n.def.Parent
		if err := checkPositions(byName, pos, limit); err != nil {
			return err
		}
		return db.From(ctx).Where("id = ?", n.custom.ID).Delete(&menuCustom{}).Error
	})
}

// maxDepth 是调整后允许的最大层数：MaxMenuDepth，代码自己声明得更深时以代码为准。
func (s *Service) maxDepth(portal string) int {
	code := s.reg.Menus(portal)
	parent := make(map[string]string, len(code))
	for _, n := range code {
		parent[n.Name] = n.Parent
	}
	d := MaxMenuDepth
	for _, n := range code {
		depth := 1
		for p := parent[n.Name]; p != "" && depth <= len(code); p = parent[p] {
			depth++
		}
		if depth > d {
			d = depth
		}
	}
	return d
}

func newGroupName() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return menuGroupPrefix + hex.EncodeToString(b), nil
}

// CreateMenuGroup 新建一个分组，返回服务端生成的分组名。
func (s *Service) CreateMenuGroup(ctx context.Context, actor auth.Principal, in MenuGroupInput) (string, error) {
	titles, titlesJSON, err := normalizeTitles(in.Titles)
	if err != nil {
		return "", err
	}
	if titles[DefaultMenuLang] == "" {
		return "", fieldErr("titles", "groupNeedsDefault", "a group needs a title in the default language", "lang", DefaultMenuLang)
	}
	if !ValidMenuIcon(in.Icon) {
		return "", fieldErr("icon", "badIcon", "invalid icon name")
	}
	if !validSort(in.Sort) {
		return "", fieldErr("sort", "sortRange", "sort value out of range", "max", MaxMenuSort)
	}
	name, err := newGroupName()
	if err != nil {
		return "", err
	}
	portal := actor.Portal
	limit := s.maxDepth(portal)
	err = s.menuTx(ctx, actor, func(ctx context.Context, nodes []*layoutNode, byName map[string]*layoutNode) error {
		groups := 0
		for _, n := range nodes {
			if n.Group {
				groups++
			}
		}
		if groups >= MaxMenuGroups {
			return fieldErr("name", "maxGroups", fmt.Sprintf("at most %d groups per portal", MaxMenuGroups), "max", MaxMenuGroups)
		}
		g := &layoutNode{MenuNode: MenuNode{Portal: portal, Name: name}, Group: true}
		byName[name] = g
		pos := currentPositions(nodes)
		pos[name] = in.Parent
		if err := checkPositions(byName, pos, limit); err != nil {
			return err
		}
		r := &menuCustom{
			Portal: portal, Name: name, Kind: kindGroup, Titles: titlesJSON, Icon: in.Icon, Hidden: in.Hidden,
			Moved: true, Parent: in.Parent, Sort: uint(in.Sort),
		}
		return s.saveCustom(ctx, actor, r)
	})
	if err != nil {
		return "", err
	}
	return name, nil
}

// DeleteMenuGroup 删除一个分组：里面的代码菜单回到代码声明的位置，里面的分组挪到被删分组的上级。
func (s *Service) DeleteMenuGroup(ctx context.Context, actor auth.Principal, name string) error {
	portal := actor.Portal
	limit := s.maxDepth(portal)
	return s.menuTx(ctx, actor, func(ctx context.Context, nodes []*layoutNode, byName map[string]*layoutNode) error {
		g, ok := byName[name]
		if !ok {
			return httpx.ErrNotFound
		}
		if !g.Group {
			return httpx.ErrDeclaredInCode
		}
		pos := currentPositions(nodes)
		delete(pos, name)
		var moved []*layoutNode
		for _, n := range nodes {
			if n.Parent != name {
				continue
			}
			if n.Group {
				pos[n.Name] = g.Parent
			} else {
				pos[n.Name] = n.def.Parent
			}
			moved = append(moved, n)
		}
		if err := checkPositions(byName, pos, limit); err != nil {
			return err
		}
		for _, n := range moved {
			r := customFor(portal, n)
			if n.Group {
				r.Parent = g.Parent
			} else {
				r.Moved, r.Parent, r.Sort = false, "", 0
			}
			if err := s.saveCustom(ctx, actor, r); err != nil {
				return err
			}
		}
		return db.From(ctx).Where("id = ?", g.custom.ID).Delete(&menuCustom{}).Error
	})
}

// ErrMenuChanged 表示提交的位置与当前的菜单集合不一致（代码刚增删了菜单，或别人刚建删了分组）。
var ErrMenuChanged = httpx.NewKey(httpx.CodeConflict, "rbac.menu.changed", "the menus have changed; refresh and try again")

// SaveMenuLayout 保存拖动后的位置：必须提交该端全部节点，整体校验通过才写入，否则一项都不写。
func (s *Service) SaveMenuLayout(ctx context.Context, actor auth.Principal, items []MenuPosition) error {
	if len(items) > MaxMenuLayoutLen {
		return fieldErr("items", "tooManyItems", "too many items")
	}
	portal := actor.Portal
	limit := s.maxDepth(portal)
	return s.menuTx(ctx, actor, func(ctx context.Context, nodes []*layoutNode, byName map[string]*layoutNode) error {
		if len(items) != len(nodes) {
			return ErrMenuChanged
		}
		want := make(map[string]MenuPosition, len(items))
		for _, it := range items {
			if _, dup := want[it.Name]; dup {
				return fieldErr("items", "duplicate", fmt.Sprintf("%q appears more than once", it.Name), "name", it.Name)
			}
			if _, ok := byName[it.Name]; !ok {
				return ErrMenuChanged
			}
			if !validSort(it.Sort) {
				return fieldErr("items", "sortRange", fmt.Sprintf("sort value of %q out of range", it.Name), "name", it.Name, "max", MaxMenuSort)
			}
			want[it.Name] = it
		}
		pos := make(map[string]string, len(items))
		for _, it := range items {
			pos[it.Name] = it.Parent
		}
		if err := checkPositions(byName, pos, limit); err != nil {
			return err
		}
		for _, n := range nodes {
			it := want[n.Name]
			if n.Parent == it.Parent && n.Sort == it.Sort {
				continue
			}
			r := customFor(portal, n)
			if !n.Group && it.Parent == n.def.Parent && it.Sort == n.def.Sort {
				r.Moved, r.Parent, r.Sort = false, "", 0
			} else {
				r.Moved, r.Parent, r.Sort = true, it.Parent, uint(it.Sort)
			}
			if err := s.saveCustom(ctx, actor, r); err != nil {
				return err
			}
		}
		return nil
	})
}
