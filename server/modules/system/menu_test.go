package system_test

// 规范 §13.2 第 36–39 条：菜单管理（D-025）的反向测试。结构归代码，后台只改显示和位置；
// 任何调整都不能改变"谁能看到什么、能打开哪个页面"。

import (
	"context"
	"fmt"
	"io"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// menuModule 声明一组演示菜单：
//
//	demo（目录）
//	├── demo-a（页面）
//	├── demo-b（页面）
//	│   └── demo-b-detail（挂在页面下的隐藏详情页）
//	├── demo-a-detail（隐藏详情页）
//	└── demo-sub（目录）
//	    └── demo-sub-page（页面）
//	audit（带权限码的目录）
//	└── audit-log（页面）
type menuModule struct{ menus []rbac.MenuNode }

func (m *menuModule) Name() string                { return "menudemo" }
func (m *menuModule) Init(*app.Deps) error        { return nil }
func (m *menuModule) Routes(*app.Router)          {}
func (m *menuModule) Start(context.Context) error { return nil }
func (m *menuModule) Stop(context.Context) error  { return nil }
func (m *menuModule) Menus() []rbac.MenuNode      { return m.menus }
func (m *menuModule) Perms() []rbac.Perm {
	return []rbac.Perm{
		{Code: "demo:a:list", Name: "a", Portal: "platform", Group: "demo"},
		{Code: "demo:b:list", Name: "b", Portal: "platform", Group: "demo"},
		{Code: "demo:audit:view", Name: "audit", Portal: "platform", Group: "demo"},
		{Code: "demo:audit:list", Name: "audit-log", Portal: "platform", Group: "demo"},
	}
}

func demoMenus() []rbac.MenuNode {
	return []rbac.MenuNode{
		{Portal: "platform", Name: "demo", Path: "/demo", TitleKey: "menu.demo", Icon: "Folder", Sort: 100},
		{Portal: "platform", Parent: "demo", Name: "demo-a", Path: "/demo/a", Component: "demo/a/index", TitleKey: "menu.demo.a", Icon: "Document", Perm: "demo:a:list", Sort: 10},
		{Portal: "platform", Parent: "demo", Name: "demo-b", Path: "/demo/b", Component: "demo/b/index", TitleKey: "menu.demo.b", Icon: "Document", Perm: "demo:b:list", Sort: 20},
		{Portal: "platform", Parent: "demo-b", Name: "demo-b-detail", Path: "/demo/b/:id", Component: "demo/b/detail", TitleKey: "menu.demo.b.detail", Perm: "demo:b:list", Sort: 10, Hidden: true},
		{Portal: "platform", Parent: "demo", Name: "demo-a-detail", Path: "/demo/a/:id", Component: "demo/a/detail", TitleKey: "menu.demo.a.detail", Perm: "demo:a:list", Sort: 30, Hidden: true},
		{Portal: "platform", Parent: "demo", Name: "demo-sub", Path: "/demo/sub", TitleKey: "menu.demo.sub", Sort: 40},
		{Portal: "platform", Parent: "demo-sub", Name: "demo-sub-page", Path: "/demo/sub/p", Component: "demo/sub/p", TitleKey: "menu.demo.sub.p", Perm: "demo:a:list", Sort: 10},
		{Portal: "platform", Name: "audit", Path: "/audit", TitleKey: "menu.audit", Perm: "demo:audit:view", Sort: 200},
		{Portal: "platform", Parent: "audit", Name: "audit-log", Path: "/audit/logs", Component: "audit/log", TitleKey: "menu.audit.log", Perm: "demo:audit:list", Sort: 10},
	}
}

func newMenuApp(t *testing.T, gdb *gorm.DB, menus []rbac.MenuNode) *app.App {
	t.Helper()
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithBcryptCost(4))
	require.NoError(t, err)
	a.Register(system.Module(), &menuModule{menus: menus})
	require.NoError(t, a.Setup())
	return a
}

func newMenuFixture(t *testing.T) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	return &fixture{t: t, app: newMenuApp(t, gdb, demoMenus()), gdb: gdb}
}

// adminMenus 读管理界面的扁平列表，返回 名字 → 节点。
func (f *fixture) adminMenus(token string) map[string]map[string]any {
	f.t.Helper()
	r := f.do(token, "GET", "/system/menus", nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	out := map[string]map[string]any{}
	for _, v := range r.env.Data.([]any) {
		m := v.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

// layoutOf 把管理列表转成保存位置用的请求体；mutate 可以改其中的项。
func layoutOf(nodes map[string]map[string]any, mutate func(pos map[string]gin.H)) gin.H {
	pos := map[string]gin.H{}
	for name, n := range nodes {
		pos[name] = gin.H{"name": name, "parent": n["parent"], "sort": n["sort"]}
	}
	if mutate != nil {
		mutate(pos)
	}
	items := make([]gin.H, 0, len(pos))
	for _, p := range pos {
		items = append(items, p)
	}
	return gin.H{"items": items}
}

// meTree 取 /auth/me 的菜单树，展平成 名字 → (节点, 上级名)。
type meNode struct {
	node   map[string]any
	parent string
}

func (f *fixture) meMenus(token string) map[string]meNode {
	f.t.Helper()
	r := f.do(token, "GET", "/auth/me", nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	out := map[string]meNode{}
	var walk func(list []any, parent string)
	walk = func(list []any, parent string) {
		for _, v := range list {
			m := v.(map[string]any)
			out[m["name"].(string)] = meNode{node: m, parent: parent}
			if c, ok := m["children"].([]any); ok {
				walk(c, m["name"].(string))
			}
		}
	}
	menus, _ := r.data()["menus"].([]any)
	walk(menus, "")
	return out
}

func (f *fixture) countCustom() int64 {
	f.t.Helper()
	var n int64
	require.NoError(f.t, f.gdb.Raw("SELECT COUNT(*) FROM ga_menu_custom").Scan(&n).Error)
	return n
}

// manyTitles 造 8 种语言、每种 32 个 "<"：字符数合法，但 JSON 转义后超过 titles 列宽。
func manyTitles() gin.H {
	out := gin.H{}
	for _, l := range []string{"aa-AA", "bb-BB", "cc-CC", "dd-DD", "ee-EE", "ff-FF", "gg-GG", "hh-HH"} {
		v := ""
		for i := 0; i < 32; i++ {
			v += "<"
		}
		out[l] = v
	}
	return out
}

// ============ 36. 菜单管理只能改显示：路径、组件、权限码、端都改不了 ============

func TestMenu_36_OnlyDisplayCanChange(t *testing.T) {
	f := newMenuFixture(t)
	admin, _ := f.admin("root")

	// 夹带不允许改的字段：整个请求被拒绝（3002），不是悄悄忽略
	for _, field := range []string{"path", "component", "perm", "portal", "titleKey", "parent", "keepAlive"} {
		r := f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"titles": gin.H{"zh-CN": "改名"}, field: "/evil"})
		require.Equal(t, httpx.CodeBadRequest, r.env.Code, "夹带 %s: %s", field, r.rec.Body.String())
	}
	require.Zero(t, f.countCustom(), "被拒绝的请求不能留下任何调整")

	// 不合法的值
	bad := []gin.H{
		{"icon": "javascript:alert(1)"},
		{"icon": "../../x"},
		{"icon": "lower"},
		{"titles": gin.H{"zh-CN": "一二三四五六七八九十一二三四五六七八九十一二三四五六七八九十一二三"}},
		{"titles": gin.H{"zh-CN": "换\n行"}},
		{"titles": gin.H{"<script>": "x"}},
		{"titles": gin.H{"zh-CN": "用户\u202e理管"}}, // 从右到左覆盖
		{"titles": gin.H{"zh-CN": "用\u200b户"}},   // 零宽空格
		{"titles": manyTitles()},                 // 转义后超过列宽：必须是 3001 而不是 500
	}
	for _, body := range bad {
		r := f.do(admin, "PUT", "/system/menus/demo-a", body)
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%v: %s", body, r.rec.Body.String())
	}
	require.Zero(t, f.countCustom())

	// 代码里隐藏的菜单不能取消隐藏
	r := f.do(admin, "PUT", "/system/menus/demo-a-detail", gin.H{"hidden": false})
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, r.rec.Body.String())
	// 不存在的菜单
	r = f.do(admin, "PUT", "/system/menus/ghost", gin.H{"hidden": true})
	require.Equal(t, httpx.CodeNotFound, r.env.Code)

	// 合法的修改：显示名、图标、隐藏生效；路径、组件、权限码不变
	r = f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"titles": gin.H{"zh-CN": " 订单 ", "en-US": "Orders"}, "icon": "Tickets", "hidden": false})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "PUT", "/system/menus/demo-b", gin.H{"hidden": true})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me := f.meMenus(admin)
	a := me["demo-a"].node
	require.Equal(t, map[string]any{"zh-CN": "订单", "en-US": "Orders"}, a["titles"])
	require.Equal(t, "Tickets", a["icon"])
	require.Equal(t, "/demo/a", a["path"])
	require.Equal(t, "demo/a/index", a["component"])
	require.Equal(t, "menu.demo.a", a["titleKey"])
	require.Equal(t, true, me["demo-b"].node["hidden"])
	require.Equal(t, "/demo/b", me["demo-b"].node["path"], "隐藏只影响侧边栏，路由仍在")

	// 管理列表给出代码原值和"已调整"标记
	nodes := f.adminMenus(admin)
	require.Equal(t, true, nodes["demo-a"]["customized"])
	require.Equal(t, "Document", nodes["demo-a"]["default"].(map[string]any)["icon"])
	require.Equal(t, "demo:a:list", nodes["demo-a"]["perm"])
	require.Equal(t, "page", nodes["demo-a"]["kind"])
	require.Equal(t, "dir", nodes["demo"]["kind"])
	require.Equal(t, true, nodes["demo-a-detail"]["codeHidden"])

	// 恢复默认：调整行删掉，回到代码的样子
	r = f.do(admin, "POST", "/system/menus/demo-a/reset", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(admin)
	require.Nil(t, me["demo-a"].node["titles"])
	require.Equal(t, "Document", me["demo-a"].node["icon"])
	// 把图标改回代码原值、取消隐藏：与代码不再有差异的行被删掉
	r = f.do(admin, "PUT", "/system/menus/demo-b", gin.H{"icon": "Document", "hidden": false})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Zero(t, f.countCustom())
}

// ============ 37. 位置调整在服务端整体校验，任何一项不合格都不写 ============

func TestMenu_37_LayoutIsValidatedAsAWhole(t *testing.T) {
	f := newMenuFixture(t)
	admin, _ := f.admin("root")

	// 建两个分组：g1 在顶级，g2 在 g1 里
	r := f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "常用"}, "icon": "Star", "sort": 5})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	g1 := r.data()["name"].(string)
	require.Regexp(t, `^@g-[0-9a-f]{12}$`, g1)
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "二级"}, "parent": g1})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	g2 := r.data()["name"].(string)

	// 分组的入参同样严格：不能指定名字、路径、组件、权限码
	for _, field := range []string{"name", "path", "component", "perm"} {
		r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "x"}, field: "x"})
		require.Equal(t, httpx.CodeBadRequest, r.env.Code, "夹带 %s: %s", field, r.rec.Body.String())
	}
	// 分组必须有中文名；上级不能是页面
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"en-US": "x"}})
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "x"}, "parent": "demo-a"})
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	// 分组不能建在带权限码的目录里
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "x"}, "parent": "audit"})
	require.Equal(t, httpx.CodeValidation, r.env.Code)

	before := f.adminMenus(admin)
	customBefore := f.countCustom()
	reject := func(label string, wantCode int, why string, mutate func(pos map[string]gin.H)) {
		t.Helper()
		r := f.do(admin, "PUT", "/system/menu-layout", layoutOf(before, mutate))
		require.Equal(t, wantCode, r.env.Code, "%s: %s", label, r.rec.Body.String())
		require.Contains(t, r.rec.Body.String(), why, "%s：被拒绝的原因不对", label)
		require.Equal(t, before, f.adminMenus(admin), "%s: 被拒绝后菜单不能有任何变化", label)
		require.Equal(t, customBefore, f.countCustom())
	}
	reject("成环", httpx.CodeValidation, "rbac.menu.cycle", func(p map[string]gin.H) { p[g1]["parent"] = g2 })
	reject("挂到自己下面", httpx.CodeValidation, "rbac.menu.badParent", func(p map[string]gin.H) { p["demo"]["parent"] = "demo" })
	reject("上级是页面", httpx.CodeValidation, "rbac.menu.badParent", func(p map[string]gin.H) { p["demo-b"]["parent"] = "demo-a" })
	reject("上级不存在", httpx.CodeValidation, "rbac.menu.badParent", func(p map[string]gin.H) { p["demo-b"]["parent"] = "nowhere" })
	reject("挪进带权限码的目录", httpx.CodeValidation, "rbac.menu.badParent", func(p map[string]gin.H) { p["demo-a"]["parent"] = "audit" })
	reject("分组挪进带权限码的目录", httpx.CodeValidation, "rbac.menu.badParent", func(p map[string]gin.H) { p[g2]["parent"] = "audit" })
	reject("排序越界", httpx.CodeValidation, "rbac.menu.sortRange", func(p map[string]gin.H) { p["demo-b"]["sort"] = -1 })
	reject("少了节点", httpx.CodeConflict, "rbac.menu.changed", func(p map[string]gin.H) { delete(p, "demo-b") })
	reject("多了节点", httpx.CodeConflict, "rbac.menu.changed", func(p map[string]gin.H) { p["ghost"] = gin.H{"name": "ghost", "parent": "", "sort": 1} })
	reject("超过 4 层", httpx.CodeValidation, `"key":"rbac.menu.maxDepth","params":{"limit":4}`, func(p map[string]gin.H) {
		// g1 → g2 → demo → demo-sub → demo-sub-page 是 5 层
		p["demo"]["parent"] = g2
	})
	reject("夹带字段", httpx.CodeBadRequest, "", func(p map[string]gin.H) { p["demo-a"]["path"] = "/evil" })
	reject("重复节点", httpx.CodeValidation, "rbac.menu.duplicate", func(p map[string]gin.H) {
		p["dup"] = gin.H{"name": "demo-a", "parent": "", "sort": 1}
		delete(p, "demo-b")
	})

	// 合法的调整：demo-a 挪进 g2，demo 目录挪进 g1，demo-b 挪到顶级
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(before, func(p map[string]gin.H) {
		p["demo-a"]["parent"], p["demo-a"]["sort"] = g2, 10
		p["demo"]["parent"] = g1
		p["demo-b"]["parent"], p["demo-b"]["sort"] = "", 1
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me := f.meMenus(admin)
	require.Equal(t, "", me["demo-b"].parent)
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) {
		p["demo-b"]["parent"], p["demo-b"]["sort"] = "demo", 20 // 放回代码位置
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(admin)
	require.Equal(t, "demo", me["demo-b"].parent)
	require.Equal(t, g2, me["demo-a"].parent)
	require.Equal(t, g1, me["demo"].parent)
	require.Equal(t, g1, me[g2].parent)
	require.Equal(t, "/demo/a", me["demo-a"].node["path"], "挪位置不改路由路径")
	require.Equal(t, map[string]any{"zh-CN": "常用"}, me[g1].node["titles"])
	require.Equal(t, "", me[g1].node["path"])

	// 分组没有默认值可恢复
	r = f.do(admin, "POST", "/system/menus/"+g1+"/reset", nil)
	require.Equal(t, httpx.CodeValidation, r.env.Code)

	// 删除 g1：里面的代码目录回到代码位置（顶级），里面的分组 g2 挪到 g1 的上级（顶级），demo-a 仍在 g2 里
	r = f.do(admin, "DELETE", "/system/menu-groups/"+g1, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(admin)
	require.NotContains(t, me, g1)
	require.Equal(t, "", me["demo"].parent)
	require.Equal(t, "", me[g2].parent)
	require.Equal(t, g2, me["demo-a"].parent)
	// 删除 g2：demo-a 回到代码里的上级 demo
	r = f.do(admin, "DELETE", "/system/menu-groups/"+g2, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(admin)
	require.Equal(t, "demo", me["demo-a"].parent)
	require.Zero(t, f.countCustom(), "全部回到代码位置后不留调整行")

	// 代码菜单不能当分组删
	r = f.do(admin, "DELETE", "/system/menu-groups/demo", nil)
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code)

	// 恢复默认、删除分组也做成环校验：demo 挪进分组 h，h 挂在 demo-sub 下，demo-sub 挪到顶级；
	// 这时把 demo-sub 恢复到代码位置（demo 下面）会成环 demo-sub → demo → h → demo-sub，必须拒绝且什么都不变
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "h"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	h := r.data()["name"].(string)
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) {
		p["demo-sub"]["parent"] = ""
		p[h]["parent"] = "demo-sub"
		p["demo"]["parent"] = h
		p["demo-b"]["parent"] = "" // 否则 demo-b 下的详情页到了第 5 层
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	snapshot := f.adminMenus(admin)
	r = f.do(admin, "POST", "/system/menus/demo-sub/reset", nil)
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	require.Contains(t, r.rec.Body.String(), "rbac.menu.cycle")
	require.Equal(t, snapshot, f.adminMenus(admin))
}

// ============ 38. 任何调整都不改变可见性 ============

func TestMenu_38_AdjustmentsNeverChangeVisibility(t *testing.T) {
	f := newMenuFixture(t)
	admin, _ := f.admin("root")
	onlyA := f.createRole(admin, "only-a", []string{"demo:a:list", "demo:audit:list"})
	_, alice := f.createUser(admin, "alice", []uint64{onlyA})
	none := f.createRole(admin, "none", nil)
	_, bob := f.createUser(admin, "bob", []uint64{none})

	// alice 在代码声明下能看到的页面（她有 audit-log 自己的权限码，但没有 audit 目录的，所以看不到它）
	pages := func(tok string) []string {
		var out []string
		for name, n := range f.meMenus(tok) {
			if c, _ := n.node["component"].(string); c != "" {
				out = append(out, name)
			}
		}
		sort.Strings(out)
		return out
	}
	alicePages, bobPages := pages(alice), pages(bob)
	require.Equal(t, []string{"demo-a", "demo-a-detail", "demo-sub-page"}, alicePages)
	require.Empty(t, bobPages)

	// 新建分组，把 demo-b（alice 没权限）放进去，并把它改名、换图标；再建一个空分组
	r := f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "财务"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	fin := r.data()["name"].(string)
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "空"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	empty := r.data()["name"].(string)
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) {
		p["demo-b"]["parent"] = fin
		// 把"系统管理"目录也挪进分组：alice 和 bob 都没有系统管理的权限
		p["system"]["parent"] = fin
		// 把 audit-log 挪出带权限码的 audit 目录：它仍然要求 audit 目录的权限码，alice 照样看不到
		p["audit-log"]["parent"] = ""
		// demo-sub-page 挪进分组、demo-a 挪到顶级
		p["demo-sub-page"]["parent"] = fin
		p["demo-a"]["parent"] = ""
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "PUT", "/system/menus/demo-b", gin.H{"titles": gin.H{"zh-CN": "人人可见？"}, "icon": "Star"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 调整前后，每个人能看到的页面集合完全相同
	require.Equal(t, alicePages, pages(alice))
	require.Equal(t, bobPages, pages(bob))
	me := f.meMenus(alice)
	require.NotContains(t, me, "audit-log", "挪出带权限码的目录不会让它多给人看到")
	require.Equal(t, fin, me["demo-sub-page"].parent, "财务分组里有 alice 能看的页面，分组对她可见")
	require.NotContains(t, me, "demo-b")
	require.NotContains(t, me, "system")
	require.NotContains(t, me, empty)
	// bob 什么权限都没有：没有任何菜单（分组没有权限码，但不会因此对所有人可见）
	require.Empty(t, f.meMenus(bob))
	// 给 alice 加上 audit 目录的权限码后，挪到顶级的 audit-log 就出现了——判定只看代码里的权限链
	r = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", onlyA), gin.H{"codes": []string{"demo:a:list", "demo:audit:list", "demo:audit:view"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(alice)
	require.Contains(t, me, "audit-log")
	require.Equal(t, "", me["audit-log"].parent)
	// 超管看得到财务分组（里面有东西），看不到空分组
	me = f.meMenus(admin)
	require.Equal(t, fin, me["demo-b"].parent)
	require.Equal(t, fin, me["system"].parent)
	require.Equal(t, "demo-b", me["demo-b-detail"].parent, "挂在页面下的代码详情页照常在，保存位置不受它影响")
	require.NotContains(t, me, empty)

	// 隐藏 demo-a 不影响 alice 打开它：路由仍在树里（hidden=true），权限判定不变
	r = f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"hidden": true})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(alice)
	require.Equal(t, true, me["demo-a"].node["hidden"])
	// 接口权限不受菜单影响：alice 仍然不能调系统管理的接口
	r = f.do(alice, "GET", "/system/users", nil)
	require.Equal(t, 403, r.rec.Code)
}

// ============ 39. 权限：改菜单是敏感权限；只能管本端；只读权限不能改 ============

func TestMenu_39_PermissionsAndPortalScope(t *testing.T) {
	f := newMenuFixture(t)
	admin, adminID := f.admin("root")

	// system:menu:update 是敏感权限：有授权权限的非超管也授不出去
	mgr := f.createRole(admin, "mgr", []string{system.PermRoleGrant, system.PermRoleList, system.PermMenuList})
	_, carol := f.createUser(admin, "carol", []uint64{mgr})
	ops := f.createRole(admin, "ops", nil)
	r := f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{system.PermMenuUpdate}})
	require.Equal(t, 403, r.rec.Code)
	require.Contains(t, r.rec.Body.String(), "sensitive")

	// 只有 menu:list 的人能看不能改
	r = f.do(carol, "GET", "/system/menus", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	for _, c := range []struct{ method, path string }{
		{"PUT", "/system/menus/demo-a"},
		{"POST", "/system/menus/demo-a/reset"},
		{"PUT", "/system/menu-layout"},
		{"POST", "/system/menu-groups"},
		{"DELETE", "/system/menu-groups/@g-000000000000"},
	} {
		r = f.do(carol, c.method, c.path, gin.H{})
		require.Equal(t, 403, r.rec.Code, "%s %s", c.method, c.path)
	}

	// 别的端的分组：本端的超管看不到、改不了、删不了，也不能挂到它下面
	ctx := f.app.Context(context.Background())
	now := time.Now().UTC()
	require.NoError(t, db.From(ctx).Exec(
		"INSERT INTO ga_menu_custom (portal, name, kind, titles, moved, created_at, updated_at) VALUES ('other', '@g-aaaaaaaaaaaa', 'group', '{\"zh-CN\":\"别的端\"}', 1, ?, ?)", now, now).Error)
	require.NotContains(t, f.adminMenus(admin), "@g-aaaaaaaaaaaa")
	r = f.do(admin, "PUT", "/system/menus/@g-aaaaaaaaaaaa", gin.H{"titles": gin.H{"zh-CN": "劫持"}})
	require.Equal(t, httpx.CodeNotFound, r.env.Code)
	r = f.do(admin, "DELETE", "/system/menu-groups/@g-aaaaaaaaaaaa", nil)
	require.Equal(t, httpx.CodeNotFound, r.env.Code)
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "x"}, "parent": "@g-aaaaaaaaaaaa"})
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) { p["demo-a"]["parent"] = "@g-aaaaaaaaaaaa" }))
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	var titles string
	require.NoError(t, f.gdb.Raw("SELECT titles FROM ga_menu_custom WHERE portal = 'other'").Scan(&titles).Error)
	require.Equal(t, `{"zh-CN":"别的端"}`, titles)

	// 服务层：端取自操作者身份，模块代码没有办法以别的端的名义操作
	svc := f.app.Deps().RBAC
	other := auth.Principal{Portal: "other", UserID: adminID, Super: true}
	nodes, err := svc.MenuAdmin(ctx, other)
	require.NoError(t, err)
	require.Len(t, nodes, 1, "别的端只有它自己的分组，看不到 platform 的菜单")

	// 改动都记了操作日志（被拒绝的尝试也记，带错误码）
	r = f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"titles": gin.H{"zh-CN": "改"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	var ok, failed int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE action = ? AND code = 0", system.OpMenuUpdate).Scan(&ok).Error)
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE action = ? AND code = ?", system.OpMenuUpdate, httpx.CodeNotFound).Scan(&failed).Error)
	require.EqualValues(t, 1, ok)
	require.EqualValues(t, 1, failed, "改别的端分组的尝试也留了记录")
}

// ============ 代码变了之后：调整行里过时的部分被忽略，菜单照常可用 ============

func TestMenu_StaleCustomizationsAfterCodeChange(t *testing.T) {
	f := newMenuFixture(t)
	admin, _ := f.admin("root")
	r := f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) {
		p["demo-b"]["parent"] = "system" // demo-b 挪到"系统管理"目录下
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "g"}, "parent": "demo"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	g := r.data()["name"].(string)
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) {
		p["demo-a"]["parent"] = g
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 新版本代码删掉了 demo 目录（demo-a、demo-b 改成顶级），并删掉了 demo-a-detail
	menus := []rbac.MenuNode{
		{Portal: "platform", Name: "demo-a", Path: "/demo/a", Component: "demo/a/index", TitleKey: "menu.demo.a", Perm: "demo:a:list", Sort: 10},
		{Portal: "platform", Name: "demo-b", Path: "/demo/b", Component: "demo/b/index", TitleKey: "menu.demo.b", Perm: "demo:b:list", Sort: 20},
	}
	f.app = newMenuApp(t, f.gdb, menus)
	me := f.meMenus(admin)
	require.Equal(t, "system", me["demo-b"].parent, "仍然有效的调整保留")
	require.Equal(t, g, me["demo-a"].parent, "分组的上级 demo 没了，分组回到顶级，里面的菜单还在")
	require.Equal(t, "", me[g].parent)
	require.NotContains(t, me, "demo")

	// 代码变了之后照常能保存位置、删除分组
	r = f.do(admin, "PUT", "/system/menu-layout", layoutOf(f.adminMenus(admin), func(p map[string]gin.H) {
		p["demo-b"]["parent"] = g
	}))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "DELETE", "/system/menu-groups/"+g, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	me = f.meMenus(admin)
	require.Equal(t, "", me["demo-a"].parent)
	require.Equal(t, "", me["demo-b"].parent, "分组删掉后 demo-b 回到新代码里的位置（顶级）")
}

// 排序值可以直接填：只改同级顺序，上级不变；越界拒绝；改回代码里的值时不留调整行；分组也能改。
func TestMenu_SortCanBeSetDirectly(t *testing.T) {
	f := newMenuFixture(t)
	admin, _ := f.admin("root")

	order := func() []string {
		var names []string
		for _, n := range f.meMenus(admin) {
			if n.parent == "demo" {
				names = append(names, n.node["name"].(string))
			}
		}
		sort.Slice(names, func(i, j int) bool {
			me := f.meMenus(admin)
			si, sj := me[names[i]].node["sort"].(float64), me[names[j]].node["sort"].(float64)
			if si != sj {
				return si < sj
			}
			return names[i] < names[j]
		})
		return names
	}
	require.Equal(t, []string{"demo-a", "demo-b", "demo-a-detail", "demo-sub"}, order())

	// 越界、负数：拒绝且不留调整
	for _, v := range []int{-1, rbac.MaxMenuSort + 1} {
		r := f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"sort": v})
		require.Equal(t, httpx.CodeValidation, r.env.Code, "sort=%d: %s", v, r.rec.Body.String())
	}
	require.Zero(t, f.countCustom())

	// demo-a 排到最后；上级仍是 demo
	r := f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"sort": 50})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, []string{"demo-b", "demo-a-detail", "demo-sub", "demo-a"}, order())
	nodes := f.adminMenus(admin)
	require.Equal(t, "demo", nodes["demo-a"]["parent"])
	require.EqualValues(t, 50, nodes["demo-a"]["sort"])
	require.Equal(t, true, nodes["demo-a"]["customized"])

	// 不带 sort 的修改不动排序
	r = f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"titles": gin.H{"zh-CN": "甲"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 50, f.adminMenus(admin)["demo-a"]["sort"])

	// 改回代码里的排序和显示：调整行删除
	r = f.do(admin, "PUT", "/system/menus/demo-a", gin.H{"sort": 10})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Zero(t, f.countCustom())
	require.Equal(t, []string{"demo-a", "demo-b", "demo-a-detail", "demo-sub"}, order())

	// 分组的排序
	r = f.do(admin, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "分组"}, "parent": "demo", "sort": 5})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	g := r.data()["name"].(string)
	r = f.do(admin, "PUT", "/system/menus/"+g, gin.H{"titles": gin.H{"zh-CN": "分组"}, "sort": 25})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	nodes = f.adminMenus(admin)
	require.EqualValues(t, 25, nodes[g]["sort"])
	require.Equal(t, "demo", nodes[g]["parent"])
}

// 菜单调整（D-036）：数据中心、工作台在根目录，安全监控在权限管理下；菜单名和路由不变。
func TestMenu_D037Layout(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	me := f.meMenus(root)
	require.NotContains(t, me, "dashboard", "控制台目录去掉了")
	// D-041：安全监控回到根目录、紧跟数据中心；会话管理挪到运维中心。菜单名和路径都不变
	for name, want := range map[string]struct{ parent, path string }{
		"dashboard-data":      {"", "/dashboard/data-center"},
		"dashboard-monitor":   {"", "/dashboard/monitor"},
		"dashboard-workspace": {"", "/dashboard/workspace"},
		"system-session":      {"ops", "/system/sessions"},
	} {
		require.Contains(t, me, name)
		require.Equal(t, want.parent, me[name].parent, name)
		require.Equal(t, want.path, me[name].node["path"], name)
	}
	// 根目录的顺序：数据中心、安全监控、工作台
	sortOf := func(n string) float64 { return me[n].node["sort"].(float64) }
	require.Less(t, sortOf("dashboard-data"), sortOf("dashboard-monitor"))
	require.Less(t, sortOf("dashboard-monitor"), sortOf("dashboard-workspace"))
	// D-042：代码里声明的排序值。根目录：数据中心 10、安全监控 20、工作台 30、运维中心 500、权限管理 600、系统设置 800；
	// 运维中心下：操作日志 10、登录日志 20、错误日志 30、安全事件 50、调查时间线 60、会话管理 70
	for name, want := range map[string]float64{
		"dashboard-data": 10, "dashboard-monitor": 20, "dashboard-workspace": 30,
		"ops": 500, "system": 600, "settings": 800,
		"system-oplog": 10, "system-loginlog": 20, "ops-errorlog": 30,
		"ops-security": 50, "ops-timeline": 60, "system-session": 70,
	} {
		require.Contains(t, me, name)
		require.Equal(t, want, sortOf(name), name)
	}
	for _, name := range []string{"system-oplog", "system-loginlog", "ops-errorlog", "ops-security", "ops-timeline", "system-session"} {
		require.Equal(t, "ops", me[name].parent, name)
	}
}
