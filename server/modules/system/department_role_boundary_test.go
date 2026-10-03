package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/modules/system"
)

func TestDataScope_200_RoleGrantExistingDepartments(t *testing.T) {
	for _, tc := range []struct {
		name          string
		codes         []string
		before        string
		after         string
		disableHolder bool
		disableRole   bool
	}{
		{name: "add-permission", before: "dept"},
		{name: "widen-to-department", codes: []string{system.PermUserList}, before: "self", after: "dept"},
		{name: "widen-to-subtree", codes: []string{system.PermUserList}, before: "dept", after: "dept_tree"},
		{name: "disabled-holder", before: "dept", disableHolder: true},
		{name: "disabled-role", before: "dept", disableRole: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			f := w.f
			_, actor := w.manager("grant-manager", w.sales, "dept_tree", system.PermUserList, system.PermRoleGrant, system.PermRoleList)
			role := f.scopedRole(w.root, "existing-holders", tc.codes, tc.before)
			path := fmt.Sprintf("/system/roles/%d/perms", role)
			for _, id := range []uint64{w.alice, w.olga} {
				r := f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", id), gin.H{"roleIds": []uint64{role}})
				require.Equal(t, 0, r.env.Code, r.rec.Body.String())
			}
			if tc.disableHolder {
				require.Equal(t, 0, f.do(w.root, "POST", fmt.Sprintf("/system/users/%d/status", w.olga), gin.H{"status": 0}).env.Code)
			}
			if tc.disableRole {
				require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d", role), gin.H{"name": "existing-holders", "status": 0}).env.Code)
			}
			body := gin.H{"codes": []string{system.PermUserList}}
			if tc.after != "" {
				body["dataScopes"] = gin.H{system.DataUser: tc.after}
			}
			r := f.do(actor, "PUT", path, body)
			require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
			var count int64
			require.NoError(t, f.gdb.Table("ga_casbin_rule").Where("ptype = ? AND v0 = ? AND v1 = ?", "p", fmt.Sprintf("role:%d", role), "platform").Count(&count).Error)
			require.Equal(t, int64(len(tc.codes)), count, "拒绝后权限保持原状")
			var scope string
			require.NoError(t, f.gdb.Table("ga_role_data_scope").Where("role_id = ? AND resource = ?", role, system.DataUser).Pluck("scope", &scope).Error)
			require.Equal(t, tc.before, scope, "拒绝后范围保持原状")
			require.Equal(t, 0, f.do(w.root, "PUT", path, body).env.Code, "超管仍可管理跨部门角色")
		})
	}
}

func TestDataScope_200_RoleGrantPreservesUnchangedScopes(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, actor := w.manager("unchanged-manager", w.sales, "dept", system.PermUserList, system.PermRoleGrant, system.PermRoleList)
	outside := f.scopedRole(w.root, "outside-existing", []string{system.PermUserList}, "dept")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.olga), gin.H{"roleIds": []uint64{outside}}).env.Code)
	path := fmt.Sprintf("/system/roles/%d/perms", outside)
	r := f.do(actor, "PUT", path, gin.H{"codes": []string{system.PermUserList}, "dataScopes": gin.H{system.DataUser: "dept"}})
	require.Equal(t, 0, r.env.Code, "保留既有范围不被当作扩大: %s", r.rec.Body.String())
	r = f.do(actor, "PUT", path, gin.H{"codes": []string{system.PermUserList, system.PermRoleList}})
	require.Equal(t, 0, r.env.Code, "无关权限增加不改变既有部门覆盖: %s", r.rec.Body.String())
	inside := f.scopedRole(w.root, "inside-empty", nil, "dept")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{inside}}).env.Code)
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", inside), gin.H{"codes": []string{system.PermUserList}}).env.Code)
	unassigned := f.scopedRole(w.root, "unassigned", nil, "dept")
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", unassigned), gin.H{"codes": []string{system.PermUserList}}).env.Code)
	self := f.scopedRole(w.root, "self-any-department", nil, "self")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.olga), gin.H{"roleIds": []uint64{self}}).env.Code)
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", self), gin.H{"codes": []string{system.PermUserList}}).env.Code, "仅本人范围沿用既有语义")
}

func TestDataScope_200_RoleStatusExistingDepartments(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, actor := w.manager("status-manager", w.sales, "dept_tree", system.PermUserList, system.PermRoleUpdate, system.PermRoleList)
	outside := f.scopedRole(w.root, "status-outside", []string{system.PermUserList}, "dept_tree")
	for _, id := range []uint64{w.alice, w.olga} {
		require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", id), gin.H{"roleIds": []uint64{outside}}).env.Code)
	}
	path := fmt.Sprintf("/system/roles/%d", outside)
	set := func(tok string, status int) resp {
		return f.do(tok, "PUT", path, gin.H{"name": "status-outside", "status": status})
	}
	require.Equal(t, 403, set(actor, 0).rec.Code, "停用也需要原角色实际集合资格")
	require.Equal(t, 0, set(w.root, 0).env.Code)
	require.Equal(t, 0, f.do(w.root, "POST", fmt.Sprintf("/system/users/%d/status", w.olga), gin.H{"status": 0}).env.Code)
	r := set(actor, 1)
	require.Equal(t, 403, r.rec.Code, "停用的持有人仍需检查: %s", r.rec.Body.String())
	var status int
	require.NoError(t, f.gdb.Table("ga_role").Where("id = ?", outside).Pluck("status", &status).Error)
	require.Zero(t, status)
	require.Equal(t, 0, f.do(actor, "PUT", path, gin.H{"name": "metadata-only"}).env.Code, "省略状态只改元数据")
	require.Equal(t, 0, set(w.root, 1).env.Code)
	inside := f.scopedRole(w.root, "status-inside", []string{system.PermUserList}, "dept_tree")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.eve), gin.H{"roleIds": []uint64{inside}}).env.Code)
	for _, next := range []int{0, 1} {
		r = f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d", inside), gin.H{"name": "status-inside", "status": next})
		require.Equal(t, 0, r.env.Code, "本部门树内的正常启停: %s", r.rec.Body.String())
	}
}

func TestDataScope_200_EnableAccountDepartment(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	wide := f.scopedRole(w.root, "account-status-all", []string{system.PermUserStatus}, "all")
	narrow := f.scopedRole(w.root, "account-list-tree", []string{system.PermUserList}, "dept_tree")
	_, actor := f.userIn(w.root, "account-enable-manager", w.sales, []uint64{wide, narrow})
	role := f.scopedRole(w.root, "account-relative", []string{system.PermUserList}, "dept")
	for _, id := range []uint64{w.olga, w.alice} {
		require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", id), gin.H{"roleIds": []uint64{role}}).env.Code)
		require.Equal(t, 0, f.do(w.root, "POST", fmt.Sprintf("/system/users/%d/status", id), gin.H{"status": 0}).env.Code)
	}
	path := fmt.Sprintf("/system/users/%d/status", w.olga)
	r := f.do(actor, "POST", path, gin.H{"status": 1})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Contains(t, r.rec.Body.String(), "rbac.user.enableNotAssignable", "失败不泄露具体角色内容")
	var status int
	require.NoError(t, f.gdb.Table("ga_user").Where("id = ?", w.olga).Pluck("status", &status).Error)
	require.Zero(t, status)
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d", role), gin.H{"name": "account-relative", "status": 0}).env.Code)
	require.Equal(t, 403, f.do(actor, "POST", path, gin.H{"status": 1}).rec.Code, "停用角色仍检查实际范围")
	require.Equal(t, 0, f.do(actor, "POST", fmt.Sprintf("/system/users/%d/status", w.alice), gin.H{"status": 1}).env.Code, "同集合账号可以启用")
	require.Equal(t, 0, f.do(w.root, "POST", path, gin.H{"status": 1}).env.Code)
}

func TestDataScope_200_RemovalActualDepartment(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	wide := f.scopedRole(w.root, "remove-assign-all", []string{system.PermUserAssignRole, system.PermRoleGrant, system.PermRoleList}, "all")
	narrow := f.scopedRole(w.root, "remove-list-dept", []string{system.PermUserList}, "dept")
	_, actor := f.userIn(w.root, "relative-removal-manager", w.sales, []uint64{wide, narrow})
	role := f.scopedRole(w.root, "removal-relative", []string{system.PermUserList}, "dept")
	for _, id := range []uint64{w.olga, w.alice} {
		require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", id), gin.H{"roleIds": []uint64{role}}).env.Code)
	}
	path := fmt.Sprintf("/system/roles/%d/perms", role)
	for _, body := range []gin.H{
		{"codes": []string{}},
		{"codes": []string{system.PermUserList}, "dataScopes": gin.H{system.DataUser: "self"}},
	} {
		r := f.do(actor, "PUT", path, body)
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	}
	var count int64
	require.NoError(t, f.gdb.Table("ga_casbin_rule").Where("v0 = ? AND v1 = ? AND v2 = ?", fmt.Sprintf("role:%d", role), "platform", system.PermUserList).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var scope string
	require.NoError(t, f.gdb.Table("ga_role_data_scope").Where("role_id = ? AND resource = ?", role, system.DataUser).Pluck("scope", &scope).Error)
	require.Equal(t, "dept", scope)
	out := fmt.Sprintf("/system/users/%d/roles", w.olga)
	require.Equal(t, 403, f.do(actor, "PUT", out, gin.H{"roleIds": []uint64{}}).rec.Code)
	require.Equal(t, 0, f.do(actor, "PUT", out, gin.H{"roleIds": []uint64{role}}).env.Code, "保持既有分配不被当作撤权")
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{}}).env.Code, "只移除当前目标的覆盖，其他持有人不改变")
	require.NoError(t, f.gdb.Table("ga_user_role").Where("portal = ? AND user_id = ? AND role_id = ?", "platform", w.olga, role).Count(&count).Error)
	require.Equal(t, int64(1), count)
	inside := f.scopedRole(w.root, "inside-removal", []string{system.PermUserList}, "dept")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{inside}}).env.Code)
	require.Equal(t, 0, f.do(actor, "PUT", fmt.Sprintf("/system/roles/%d/perms", inside), gin.H{"codes": []string{}}).env.Code, "同集合角色仍可移除权限")
}
