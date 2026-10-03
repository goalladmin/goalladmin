package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/modules/system"
)

func TestDataScope_191_DepartmentBoundary(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	wide := f.scopedRole(w.root, "edit-all", []string{system.PermUserUpdate, system.PermUserCreate, system.PermUserAssignRole}, "all")
	narrow := f.scopedRole(w.root, "see-dept", []string{system.PermUserList, system.PermUserStatus, system.PermSessionRevoke}, "dept")
	_, actor := f.userIn(w.root, "boundary-manager", w.sales, []uint64{wide, narrow})
	r := f.do(actor, "PUT", fmt.Sprintf("/system/users/%d", w.olga), gin.H{"displayName": "unchanged", "deptId": w.sales})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	var dept uint64
	require.NoError(t, f.gdb.Raw("SELECT dept_id FROM ga_user WHERE id = ?", w.olga).Scan(&dept).Error)
	require.Equal(t, w.ops, dept)
	require.Equal(t, 404, f.do(actor, "GET", fmt.Sprintf("/system/users/%d", w.olga), nil).rec.Code)

	viewer := f.scopedRole(w.root, "relative-viewer", []string{system.PermUserList}, "dept")
	makeUser := func(name string, dept uint64) resp {
		return f.do(actor, "POST", "/system/users", gin.H{"username": name, "password": "user-pass-123", "deptId": dept, "roleIds": []uint64{viewer}})
	}
	r = makeUser("outside-viewer", w.ops)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	var count int64
	require.NoError(t, f.gdb.Table("ga_user").Where("username = ?", "outside-viewer").Count(&count).Error)
	require.Zero(t, count, "失败创建整个回滚")
	require.Equal(t, 0, makeUser("inside-viewer", w.sales).env.Code)
	r = f.do(actor, "PUT", fmt.Sprintf("/system/users/%d/roles", w.olga), gin.H{"roleIds": []uint64{viewer}})
	require.Equal(t, 403, r.rec.Code, "相对部门角色的等级相同，但实际部门不同: %s", r.rec.Body.String())
	require.NoError(t, f.gdb.Table("ga_user_role").Where("portal = ? AND user_id = ?", "platform", w.olga).Count(&count).Error)
	require.Zero(t, count, "拒绝分配不留下成员关系")

	_, treeActor := w.manager("tree-manager", w.sales, "dept_tree", append([]string{system.PermUserCreate}, userPerms...)...)
	r = f.do(treeActor, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "Alice", "deptId": w.east})
	require.Equal(t, 0, r.env.Code, "同范围调整保留: %s", r.rec.Body.String())
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d", w.olga), gin.H{"displayName": "Olga", "deptId": w.sales}).env.Code)
}
