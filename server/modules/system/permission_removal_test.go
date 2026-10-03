package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/modules/system"
	"github.com/stretchr/testify/require"
)

func TestRoles_198_RemovalAndLatentScope(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, actor := w.manager("removal-manager", w.sales, "dept", system.PermUserList, system.PermUserAssignRole, system.PermRoleGrant, system.PermRoleList)
	wide := f.scopedRole(w.root, "protected-wide", []string{system.PermUserList}, "all")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{wide}}).env.Code)
	r := f.do(actor, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	var count int64
	require.NoError(t, f.gdb.Table("ga_user_role").Where("portal = ? AND user_id = ? AND role_id = ?", "platform", w.alice, wide).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{wide}}).env.Code, "仅保留原角色允许")
	r = f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", wide), gin.H{"codes": []string{}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.NoError(t, f.gdb.Table("ga_casbin_rule").Where("ptype = ? AND v0 = ? AND v1 = ? AND v2 = ?", "p", fmt.Sprintf("role:%d", wide), "platform", system.PermUserList).Count(&count).Error)
	require.Equal(t, int64(1), count, "权限移除失败不改变原授权")
	empty := f.createRole(w.root, "latent-empty", nil)
	r = f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", empty), gin.H{"codes": []string{}, "dataScopes": gin.H{system.DataUser: "all"}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", empty), gin.H{"codes": []string{}, "dataScopes": gin.H{system.DataUser: "self"}}).env.Code)
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{}}).env.Code)
}
