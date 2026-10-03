package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/modules/system"
)

// 规范 §13.2 第 152 条（D-069）：分配角色被拒时，角色里是哪个权限码挡住了只告诉有"查看角色"权限的人；
// 只有"分配角色"、没有"查看角色"的人得到笼统的拒绝（rbac.role.notAssignable 和角色 ID），响应里没有权限码。
// 两种人都分配不出去，库里不变。
func TestRBAC_152_AssignRejectionDetail(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	sens := f.createRole(admin, "sens", []string{system.PermRoleGrant}) // 授权是敏感权限，不受数据范围约束
	base := []string{system.PermUserList, system.PermUserAssignRole}
	_, blind := f.createUser(admin, "blind", []uint64{f.createRole(admin, "blind", base)})
	_, viewer := f.createUser(admin, "viewer", []uint64{f.createRole(admin, "viewer", append([]string{system.PermRoleList}, base...))})
	target, _ := f.createUser(admin, "target", nil)
	path := fmt.Sprintf("/system/users/%d/roles", target)
	rolesOf := func() []any {
		r := f.do(admin, "GET", fmt.Sprintf("/system/users/%d", target), nil)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		roles, _ := r.data()["roles"].([]any)
		return roles
	}

	r := f.do(blind, "PUT", path, gin.H{"roleIds": []uint64{sens}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.role.notAssignable", fieldKey(r))
	body := r.rec.Body.String()
	require.NotContains(t, body, system.PermRoleGrant, "没有查看角色权限：响应里不该有角色里的权限码")
	require.NotContains(t, body, `"perm"`)
	require.Contains(t, body, fmt.Sprintf(`"id":%d`, sens), "角色 ID 是他自己传的，可以回")
	require.Empty(t, rolesOf())

	r = f.do(viewer, "PUT", path, gin.H{"roleIds": []uint64{sens}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.role.holdsSensitive", fieldKey(r))
	require.Contains(t, r.rec.Body.String(), system.PermRoleGrant, "有查看角色权限：和以前一样带细节")
	require.Empty(t, rolesOf())
}
