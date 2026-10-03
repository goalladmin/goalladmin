package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/modules/system"
	"github.com/stretchr/testify/require"
)

// 规范 §13.2 第 184 条（D-100）：非超管启停角色用同一条规则——只能启停自己分配得出去的角色；
// 重新启用账号时，账号身上停用中的角色也算。

func TestRole_184_DisableNeedsAssignableRole(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, ed := w.manager("editor", w.sales, "dept", append([]string{system.PermRoleUpdate}, userPerms...)...)
	wide := f.scopedRole(w.root, "wide-on", []string{system.PermUserList}, "all")
	sens := f.scopedRole(w.root, "sens-on", []string{system.PermUserCreate}, "self")
	ok := f.scopedRole(w.root, "ok-on", []string{system.PermUserList}, "dept")
	status := func(id uint64) int {
		var st int
		require.NoError(t, f.gdb.Raw("SELECT status FROM ga_role WHERE id = ?", id).Scan(&st).Error)
		return st
	}
	put := func(tok string, id uint64, name string, st int) resp {
		return f.do(tok, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": name, "status": st})
	}

	// 范围比自己宽的、含敏感权限码的：停用被拒，角色照旧启用
	r := put(ed, wide, "wide", 0)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.role.widerScope", fieldKey(r))
	require.Equal(t, 1, status(wide))
	r = put(ed, sens, "sens", 0)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.role.holdsSensitive", fieldKey(r))
	require.Equal(t, 1, status(sens))

	// 不动状态只改名字：不受这条限制
	require.Equal(t, 0, put(ed, wide, "wide-renamed", 1).env.Code)
	require.Equal(t, 1, status(wide))

	// 权限和范围都不超过自己的：可以停用，也可以再启用
	require.Equal(t, 0, put(ed, ok, "ok", 0).env.Code)
	require.Equal(t, 0, status(ok))
	require.Equal(t, 0, put(ed, ok, "ok", 1).env.Code)
	require.Equal(t, 1, status(ok))

	// 超管不受限
	require.Equal(t, 0, put(w.root, wide, "wide", 0).env.Code)
	require.Equal(t, 0, status(wide))
	require.Equal(t, 0, put(w.root, sens, "sens", 0).env.Code)
	require.Equal(t, 0, status(sens))
}

func TestRole_192_OmittedStatus(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	id := f.createRole(root, "preserve-status", nil)
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": "disabled", "status": 0}).env.Code)
	r := f.do(root, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": "renamed"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, float64(0), r.data()["status"])
	r = f.do(root, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": "enabled", "status": 1})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, float64(1), r.data()["status"])
}

func TestRole_184_ReenableUserCountsDisabledRoles(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	sensitive := f.createRole(root, "auditor", []string{system.PermMonitorView})
	plain := f.createRole(root, "viewer", []string{system.PermUserList})
	xavierID, _ := f.createUser(root, "xavier", []uint64{sensitive})
	yuriID, _ := f.createUser(root, "yuri", []uint64{plain})
	helenRole := f.scopedRole(root, "helen", []string{system.PermUserList, system.PermUserStatus}, "all")
	_, helen := f.createUser(root, "helen", []uint64{helenRole})
	setUser := func(tok string, id uint64, st int) resp {
		return f.do(tok, "POST", fmt.Sprintf("/system/users/%d/status", id), gin.H{"status": st})
	}
	setRole := func(id uint64, name string, st int) {
		r := f.do(root, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": name, "status": st})
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	}
	enabled := func(id uint64) bool {
		var st int
		require.NoError(t, f.gdb.Raw("SELECT status FROM ga_user WHERE id = ?", id).Scan(&st).Error)
		return st == 1
	}

	// 两个角色都停用着，两个账号也停用着
	setRole(sensitive, "auditor", 0)
	setRole(plain, "viewer", 0)
	require.Equal(t, 0, setUser(helen, xavierID, 0).env.Code)
	require.Equal(t, 0, setUser(helen, yuriID, 0).env.Code)

	// 账号身上有一个停用中的、自己分配不出去的角色：非超管不能把账号启用回来
	r := setUser(helen, xavierID, 1)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Contains(t, r.rec.Body.String(), "rbac.user.enableNotAssignable")
	require.NotContains(t, r.rec.Body.String(), system.PermMonitorView)
	require.False(t, enabled(xavierID))

	// 停用中的角色是自己分配得出去的：可以
	require.Equal(t, 0, setUser(helen, yuriID, 1).env.Code)
	require.True(t, enabled(yuriID))

	// 角色恢复之后那个账号仍然是停用的；超管可以启用
	setRole(sensitive, "auditor", 1)
	require.False(t, enabled(xavierID))
	require.Equal(t, 0, setUser(root, xavierID, 1).env.Code)
	require.True(t, enabled(xavierID))
}
