package rbac

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidPermCode(t *testing.T) {
	require.True(t, ValidPermCode("system:user:create"))
	require.True(t, ValidPermCode("system:role:reset-password"))
	require.False(t, ValidPermCode("system:user"))
	require.False(t, ValidPermCode("System:User:Create"))
	require.False(t, ValidPermCode("system:user:create:extra"))
	require.False(t, ValidPermCode("system:user:*"))
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.AddPerms("system", []Perm{
		{Code: "system:user:list", Name: "list", Portal: "platform", Group: "b"},
		{Code: "system:role:list", Name: "list", Portal: "platform", Group: "a"},
	}))
	require.ErrorContains(t, r.AddPerms("other", []Perm{{Code: "system:user:list", Name: "dup", Portal: "platform"}}), "已由")
	require.ErrorContains(t, r.AddPerms("other", []Perm{{Code: "bad", Portal: "platform"}}), "规范")
	require.ErrorContains(t, r.AddPerms("other", []Perm{{Code: "a:b:c"}}), "没有指定端")

	require.NoError(t, r.AddMenus("system", []MenuNode{
		{Portal: "platform", Name: "system", Sort: 2},
		{Portal: "platform", Name: "system-user", Parent: "system", Perm: "system:user:list", Sort: 1},
	}))
	require.ErrorContains(t, r.AddMenus("other", []MenuNode{{Portal: "platform", Name: "system"}}), "已存在")
	require.NoError(t, r.Finalize())

	require.True(t, r.Has("platform", "system:user:list"))
	require.False(t, r.Has("merchant", "system:user:list"))
	perms := r.Perms("platform")
	require.Equal(t, []string{"system:role:list", "system:user:list"}, []string{perms[0].Code, perms[1].Code}, "按 Group 排序")
	menus := r.Menus("platform")
	require.Equal(t, "system-user", menus[0].Name, "按 Sort 排序")

	bad := NewRegistry()
	require.NoError(t, bad.AddMenus("m", []MenuNode{{Portal: "platform", Name: "x", Perm: "no:such:perm"}}))
	require.ErrorContains(t, bad.Finalize(), "未声明的权限码")
}
