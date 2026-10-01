package rbac

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestDataScope_Order(t *testing.T) {
	all := AllScopes()
	for i := range all {
		require.True(t, all[i].Valid())
		for j := range all {
			require.Equal(t, i > j, all[i].Wider(all[j]), "%s vs %s", all[i], all[j])
		}
	}
	require.False(t, DataScope("everything").Valid())
	require.False(t, DataScope("").Valid())
}

func userResource() DataResource {
	return DataResource{Code: "system:user", Name: "data.system.user", Portal: "platform", Perms: []string{"system:user:list"}, Default: ScopeSelf}
}

func TestDataResource_Validate(t *testing.T) {
	require.NoError(t, userResource().Validate())
	bad := map[string]func(d *DataResource){
		"编码不合规范":   func(d *DataResource) { d.Code = "system" },
		"没有端":      func(d *DataResource) { d.Portal = "" },
		"没有权限码":    func(d *DataResource) { d.Perms = nil },
		"别的模块的权限码": func(d *DataResource) { d.Perms = []string{"order:item:list"} },
		"默认值不在可选里": func(d *DataResource) { d.Scopes = []DataScope{ScopeDept, ScopeAll} },
		"不认识的可选范围": func(d *DataResource) { d.Scopes = []DataScope{ScopeSelf, "everything"} },
		"不认识的默认值":  func(d *DataResource) { d.Default = "everything" },
		"权限码格式不对":  func(d *DataResource) { d.Perms = []string{"system:user"} },
	}
	for name, mutate := range bad {
		d := userResource()
		mutate(&d)
		require.Error(t, d.Validate(), name)
	}
	d := userResource()
	d.Scopes = []DataScope{ScopeSelf, ScopeDept}
	require.Equal(t, []DataScope{ScopeSelf, ScopeDept}, d.Choices())
	require.False(t, d.Allowed(ScopeAll))
	require.Equal(t, AllScopes(), userResource().Choices())
}

func TestRegistry_DataResources(t *testing.T) {
	newReg := func() *Registry {
		r := NewRegistry()
		require.NoError(t, r.AddPerms("system", []Perm{
			{Code: "system:user:list", Portal: "platform"}, {Code: "system:user:update", Portal: "platform"},
		}))
		require.NoError(t, r.AddPerms("order", []Perm{{Code: "order:item:list", Portal: "platform"}}))
		return r
	}

	r := newReg()
	require.NoError(t, r.AddDataResources("system", []DataResource{userResource()}))
	require.NoError(t, r.Finalize())
	require.Equal(t, "system:user", r.ResourceOf("platform", "system:user:list"))
	require.Empty(t, r.ResourceOf("platform", "system:user:update"))
	require.Len(t, r.DataResources("platform"), 1)
	_, ok := r.DataResource("merchant", "system:user")
	require.False(t, ok, "按端区分")

	// 模块不能借 system: 开头的资源去约束别人的权限码
	r = newReg()
	d := userResource()
	require.ErrorContains(t, r.AddDataResources("order", []DataResource{d}), "必须以 order: 开头")
	// 约束的权限码没有声明
	r = newReg()
	d = userResource()
	d.Perms = []string{"system:user:delete"}
	require.NoError(t, r.AddDataResources("system", []DataResource{d}))
	require.ErrorContains(t, r.Finalize(), "没有在端 platform 声明")
	// 一个权限码属于两个资源
	r = newReg()
	d2 := userResource()
	d2.Code = "system:account"
	require.NoError(t, r.AddDataResources("system", []DataResource{userResource(), d2}))
	require.ErrorContains(t, r.Finalize(), "同时属于")
	// 同一个资源登记两次
	r = newReg()
	require.ErrorContains(t, r.AddDataResources("system", []DataResource{userResource(), userResource()}), "已存在")
}

func TestDataFilter(t *testing.T) {
	all := DataFilter{Scope: ScopeAll, UserID: 7}
	require.True(t, all.Allows(0, 0))
	require.True(t, all.AllowsDept(0))

	self := DataFilter{Scope: ScopeSelf, UserID: 7}
	require.True(t, self.Allows(3, 7))
	require.False(t, self.Allows(3, 8))
	require.False(t, self.AllowsDept(3))

	dept := DataFilter{Scope: ScopeDeptTree, UserID: 7, DeptIDs: []uint64{3, 4}}
	require.True(t, dept.Allows(4, 9))
	require.True(t, dept.Allows(5, 7), "本人的数据总在范围内")
	require.False(t, dept.Allows(5, 9))
	require.False(t, dept.Allows(0, 9), "未分配部门的数据只有全部范围能看")
	require.True(t, dept.AllowsDept(3))
	require.False(t, dept.AllowsDept(0))

	gdb, err := gorm.Open(mysql.New(mysql.Config{DSN: "u:p@tcp(127.0.0.1:1)/x", SkipInitializeWithVersion: true}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	type row struct{ ID uint64 }
	sqlOf := func(f DataFilter, deptCol, ownerCol string) string {
		var out []row
		return f.Apply(gdb.Table("t"), deptCol, ownerCol).Find(&out).Statement.SQL.String()
	}
	require.NotContains(t, sqlOf(all, "dept_id", "id"), "WHERE")
	require.Contains(t, sqlOf(self, "dept_id", "created_by"), "WHERE created_by = ?")
	require.Contains(t, sqlOf(dept, "dept_id", "created_by"), "WHERE (dept_id IN (?,?) OR created_by = ?)")
	require.Contains(t, sqlOf(dept, "", "created_by"), "WHERE created_by = ?", "没有部门列时只剩本人")
	require.Panics(t, func() { sqlOf(self, "dept_id", "id; DROP TABLE x") })
	require.Panics(t, func() { sqlOf(dept, "dept_id OR 1=1", "id") })
}
