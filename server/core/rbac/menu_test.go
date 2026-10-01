package rbac

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildMenuTree(t *testing.T) {
	nodes := []MenuNode{
		{Portal: "p", Name: "dash", Path: "/dash", Sort: 1},
		{Portal: "p", Name: "sys", Path: "/sys", Sort: 900},
		{Portal: "p", Parent: "sys", Name: "sys-user", Path: "/sys/users", Perm: "s:u:l", Sort: 10},
		{Portal: "p", Parent: "sys", Name: "sys-role", Path: "/sys/roles", Perm: "s:r:l", Sort: 20},
		{Portal: "p", Name: "audit", Path: "/audit", Perm: "a:l:v", Sort: 950},
		{Portal: "p", Parent: "audit", Name: "audit-log", Path: "/audit/logs", Perm: "a:l:l", Sort: 1},
	}
	names := func(ts []*MenuTree) []string {
		out := make([]string, 0, len(ts))
		for _, t := range ts {
			out = append(out, t.Name)
		}
		return out
	}

	// 只有 s:r:l：仪表盘（无 Perm）可见，sys 目录因子节点可见而可见，只含 role；audit 目录自身有 Perm 且没有 → 不可见
	has := map[string]bool{"s:r:l": true, "a:l:l": true}
	tree := BuildMenuTree(nodes, func(p string) bool { return p == "" || has[p] })
	require.Equal(t, []string{"dash", "sys"}, names(tree))
	require.Equal(t, []string{"sys-role"}, names(tree[1].Children))

	// 什么权限都没有：只剩仪表盘
	tree = BuildMenuTree(nodes, func(p string) bool { return p == "" })
	require.Equal(t, []string{"dash"}, names(tree))

	// 全部可见（超管）：顺序按 Sort
	tree = BuildMenuTree(nodes, func(string) bool { return true })
	require.Equal(t, []string{"dash", "sys", "audit"}, names(tree))
	require.Equal(t, []string{"sys-user", "sys-role"}, names(tree[1].Children))
	require.Equal(t, []string{"audit-log"}, names(tree[2].Children))
}

// 库里的调整数据坏了（上级是页面、上级不存在、互相挂成环、分组名不合法）时，菜单回到代码位置照常可用，不报错也不死循环。
func TestResolveLayout_BadStoredDataFallsBackToCode(t *testing.T) {
	code := []MenuNode{
		{Portal: "p", Name: "a", Path: "/a", Sort: 1},
		{Portal: "p", Parent: "a", Name: "a-page", Path: "/a/p", Component: "a/p", Sort: 1},
		{Portal: "p", Name: "b", Path: "/b", Sort: 2},
		{Portal: "p", Parent: "b", Name: "b-page", Path: "/b/p", Component: "b/p", Sort: 1},
	}
	rows := []menuCustom{
		{Portal: "p", Name: "a", Kind: kindCode, Moved: true, Parent: "b"},                 // a → b
		{Portal: "p", Name: "b", Kind: kindCode, Moved: true, Parent: "a"},                 // b → a：成环
		{Portal: "p", Name: "b-page", Kind: kindCode, Moved: true, Parent: "a-page"},       // 上级是页面
		{Portal: "p", Name: "@g-000000000001", Kind: kindGroup, Moved: true, Parent: "zz"}, // 上级不存在
		{Portal: "p", Name: "evil", Kind: kindGroup, Moved: true, Titles: `{"zh-CN":"x"}`}, // 不是服务端生成的分组名
		{Portal: "p", Name: "a-page", Kind: kindCode, Titles: `not json`, Icon: "Star"},    // 显示名坏了
	}
	nodes := resolveLayout(code, rows)
	parent := map[string]string{}
	for _, n := range nodes {
		parent[n.Name] = n.Parent
	}
	require.NotContains(t, parent, "evil")
	require.Equal(t, "", parent["@g-000000000001"])
	require.Equal(t, "b", parent["b-page"])
	// 环上的节点至少有一个回到了代码位置，最终无环
	for name := range parent {
		seen := map[string]bool{}
		for p := name; p != ""; p = parent[p] {
			require.False(t, seen[p], "从 %s 出发有环", name)
			seen[p] = true
		}
	}
	tree := buildTree(nodes, func(string) bool { return true })
	names := map[string]bool{}
	var walk func(ts []*MenuTree)
	walk = func(ts []*MenuTree) {
		for _, x := range ts {
			require.False(t, names[x.Name], "节点 %s 出现了两次", x.Name)
			names[x.Name] = true
			walk(x.Children)
		}
	}
	walk(tree)
	require.True(t, names["a-page"] && names["b-page"], "两个页面都还在树里：%v", names)
	for _, n := range nodes {
		if n.Name == "a-page" {
			require.Nil(t, n.Titles)
			require.Equal(t, "Star", n.Icon)
		}
	}
}

// 分组和子节点全被挪走的代码目录不显示；页面即使没有子节点也照常按权限显示。
func TestBuildTree_EmptyContainersAreHidden(t *testing.T) {
	code := []MenuNode{
		{Portal: "p", Name: "dir", Path: "/dir", Sort: 1},
		{Portal: "p", Parent: "dir", Name: "page", Path: "/dir/p", Component: "d/p", Perm: "x:y:z", Sort: 1},
	}
	rows := []menuCustom{
		{Portal: "p", Name: "page", Kind: kindCode, Moved: true, Parent: ""},
		{Portal: "p", Name: "@g-000000000002", Kind: kindGroup, Moved: true, Titles: `{"zh-CN":"空"}`},
	}
	tree := buildTree(resolveLayout(code, rows), func(string) bool { return true })
	require.Len(t, tree, 1)
	require.Equal(t, "page", tree[0].Name)
	require.Empty(t, buildTree(resolveLayout(code, rows), func(p string) bool { return p == "" }))
}

// 看到一个菜单要的权限码 = 自己的 + 代码祖先的，与现在挪到哪里无关；库里把页面挪进带权限码的目录的数据会被退回。
func TestResolveLayout_VisibilityFollowsCodeAncestors(t *testing.T) {
	code := []MenuNode{
		{Portal: "p", Name: "audit", Path: "/audit", Perm: "a:d:v", Sort: 1},
		{Portal: "p", Parent: "audit", Name: "audit-log", Path: "/audit/log", Component: "a/l", Perm: "a:l:l", Sort: 1},
		{Portal: "p", Name: "open", Path: "/open", Component: "o", Perm: "o:p:l", Sort: 2},
	}
	rows := []menuCustom{
		{Portal: "p", Name: "audit-log", Kind: kindCode, Moved: true, Parent: ""},             // 挪出带权限码的目录
		{Portal: "p", Name: "open", Kind: kindCode, Moved: true, Parent: "audit"},             // 挪进带权限码的目录：不接受，退回
		{Portal: "p", Name: "@g-000000000003", Kind: kindGroup, Moved: true, Parent: "audit"}, // 分组也不能进
	}
	nodes := resolveLayout(code, rows)
	by := map[string]*layoutNode{}
	for _, n := range nodes {
		by[n.Name] = n
	}
	require.Equal(t, "", by["audit-log"].Parent)
	require.Equal(t, []string{"a:l:l", "a:d:v"}, by["audit-log"].need)
	require.Equal(t, "", by["open"].Parent)
	require.Equal(t, "", by["@g-000000000003"].Parent)

	has := func(perms ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range perms {
				if x == p {
					return true
				}
			}
			return p == ""
		}
	}
	top := func(ts []*MenuTree) []string {
		out := make([]string, 0, len(ts))
		for _, x := range ts {
			out = append(out, x.Name)
		}
		return out
	}
	// 只有 audit-log 自己的权限码：挪到顶级后仍然看不到
	require.Equal(t, []string{"open"}, top(buildTree(nodes, has("a:l:l", "o:p:l"))))
	// 两个都有才看得到
	require.ElementsMatch(t, []string{"audit-log", "open"}, top(buildTree(nodes, has("a:l:l", "a:d:v", "o:p:l"))))
}
