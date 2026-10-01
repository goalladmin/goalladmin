package system_test

// 按部门的数据权限（docs/decisions.md D-039，规范 §13.2 第 57–60 条）。
//
// 组织结构：总部 ─┬─ 销售部 ── 华东组
//                 └─ 运维部
// 人：mgr、alice 在销售部，eve 在华东组，olga 在运维部，nod 没有部门，root 是超管（也没有部门）。

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// 用户资源约束的全部权限码，外加查看角色（授权对话框要用）。
var userPerms = []string{
	system.PermUserList, system.PermUserUpdate, system.PermUserStatus, system.PermUserAssignRole,
	system.PermSessionList, system.PermSessionRevoke, system.PermRoleList,
}

type orgWorld struct {
	f                                 *fixture
	root                              string
	hq, sales, east, ops              uint64
	alice, eve, olga, nod             uint64
	aliceTok, eveTok, olgaTok, nodTok string
	ids                               map[uint64]string // 用户 ID → 登录名
}

// userIn 由超管建一个在 dept 部门的用户（dept 为 0 表示不分配），完成首次改密，返回 ID 和令牌。
func (f *fixture) userIn(admin, username string, dept uint64, roleIDs []uint64) (uint64, string) {
	f.t.Helper()
	r := f.do(admin, "POST", "/system/users", gin.H{"username": username, "password": "user-pass-123", "deptId": dept, "roleIds": roleIDs})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	uid := uint64(r.data()["user"].(map[string]any)["id"].(float64))
	tok := f.login(username, "user-pass-123")
	c := f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-123", "newPassword": "user-pass-456"})
	require.Equal(f.t, 0, c.env.Code, c.rec.Body.String())
	return uid, tok
}

// scopedRole 建一个角色，授给它 perms，用户资源的范围设为 scope（空表示不设，取默认值）。
func (f *fixture) scopedRole(admin, code string, perms []string, scope string) uint64 {
	f.t.Helper()
	r := f.do(admin, "POST", "/system/roles", gin.H{"code": code, "name": code})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	id := uint64(r.data()["id"].(float64))
	body := gin.H{"codes": perms}
	if scope != "" {
		body["dataScopes"] = gin.H{system.DataUser: scope}
	}
	g := f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", id), body)
	require.Equal(f.t, 0, g.env.Code, g.rec.Body.String())
	return id
}

func newOrgWorld(t *testing.T) *orgWorld {
	f := newFixture(t)
	root, rootID := f.admin("root")
	w := &orgWorld{f: f, root: root, ids: map[uint64]string{rootID: "root"}}
	w.hq = f.createDept(root, 0, "总部")
	w.sales = f.createDept(root, w.hq, "销售部")
	w.east = f.createDept(root, w.sales, "华东组")
	w.ops = f.createDept(root, w.hq, "运维部")
	w.alice, w.aliceTok = f.userIn(root, "alice", w.sales, nil)
	w.eve, w.eveTok = f.userIn(root, "eve", w.east, nil)
	w.olga, w.olgaTok = f.userIn(root, "olga", w.ops, nil)
	w.nod, w.nodTok = f.userIn(root, "nod", 0, nil)
	for id, name := range map[uint64]string{w.alice: "alice", w.eve: "eve", w.olga: "olga", w.nod: "nod"} {
		w.ids[id] = name
	}
	return w
}

// manager 建一个在 dept 部门、用户资源范围为 scope 的管理员。
func (w *orgWorld) manager(name string, dept uint64, scope string, perms ...string) (uint64, string) {
	if len(perms) == 0 {
		perms = userPerms
	}
	role := w.f.scopedRole(w.root, "r-"+name, perms, scope)
	id, tok := w.f.userIn(w.root, name, dept, []uint64{role})
	w.ids[id] = name
	return id, tok
}

func names(r resp, key string) []string {
	items := listItems(r)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it[key].(string))
	}
	sort.Strings(out)
	return out
}

func listItems(r resp) []map[string]any {
	var raw []any
	switch d := r.env.Data.(type) {
	case map[string]any:
		raw, _ = d["list"].([]any)
	case []any:
		raw = d
	}
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		out = append(out, x.(map[string]any))
	}
	return out
}

func (w *orgWorld) visible(tok string) []string {
	r := w.f.do(tok, "GET", "/system/users?pageSize=100", nil)
	require.Equal(w.f.t, 0, r.env.Code, r.rec.Body.String())
	return names(r, "username")
}

func (w *orgWorld) optionNames(tok string) []string {
	r := w.f.do(tok, "GET", "/system/options/users", nil)
	require.Equal(w.f.t, 0, r.env.Code, r.rec.Body.String())
	items := listItems(r)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, w.ids[uint64(it["id"].(float64))])
	}
	sort.Strings(out)
	return out
}

// sessionOwners 返回会话列表里出现的用户（登录名）。
func (w *orgWorld) sessionOwners(tok string) []string {
	r := w.f.do(tok, "GET", "/system/sessions?pageSize=100", nil)
	require.Equal(w.f.t, 0, r.env.Code, r.rec.Body.String())
	seen := map[string]bool{}
	for _, it := range listItems(r) {
		seen[it["username"].(string)] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (f *fixture) userDisplayName(id uint64) string {
	f.t.Helper()
	var name string
	require.NoError(f.t, f.gdb.Raw("SELECT display_name FROM ga_user WHERE id = ?", id).Scan(&name).Error)
	return name
}

// ============ 57. 四种范围下的读和写 ============

func TestDataScope_57_ReadAndWriteFollowScope(t *testing.T) {
	cases := []struct {
		name  string
		scope string
		dept  string // 管理员所在部门：sales、ops，空表示未分配
		see   []string
	}{
		{"m-self", "self", "sales", []string{"m-self"}},
		{"m-dept", "dept", "sales", []string{"alice", "m-dept"}},
		{"m-tree", "dept_tree", "sales", []string{"alice", "eve", "m-tree"}},
		{"m-nodept", "dept_tree", "", []string{"m-nodept"}}, // 没分配部门：部门类范围按仅本人算
		{"m-all", "all", "ops", nil},                        // nil 表示全部
	}
	for _, c := range cases {
		name := c.name
		t.Run(name, func(t *testing.T) {
			w := newOrgWorld(t) // 每种范围一个干净的组织，别的子测试建的人不会混进来
			f := w.f
			dept := map[string]uint64{"sales": w.sales, "ops": w.ops, "": 0}[c.dept]
			_, tok := w.manager(name, dept, c.scope)
			got := w.visible(tok)
			if c.see == nil {
				require.Contains(t, got, "root")
				require.Contains(t, got, "olga")
				require.Contains(t, got, "nod")
				require.Equal(t, got, w.optionNames(tok))
				return
			}
			require.Equal(t, c.see, got, "列表只有范围内的人")
			require.Equal(t, c.see, w.optionNames(tok), "下拉和列表一样")
			require.Equal(t, c.see, w.sessionOwners(tok), "会话列表只有范围内的人的会话")

			// 范围外的人：详情、修改、启停、分配角色、下线会话一律 404，数据没被改
			out := w.olga
			for _, tc := range []struct {
				method, path string
				body         any
			}{
				{"GET", fmt.Sprintf("/system/users/%d", out), nil},
				{"PUT", fmt.Sprintf("/system/users/%d", out), gin.H{"displayName": "pwned"}},
				{"POST", fmt.Sprintf("/system/users/%d/status", out), gin.H{"status": 0}},
				{"PUT", fmt.Sprintf("/system/users/%d/roles", out), gin.H{"roleIds": []uint64{}}},
				{"POST", fmt.Sprintf("/system/sessions/%s/revoke", f.sidOf(out)), nil},
				{"GET", fmt.Sprintf("/system/sessions?userId=%d", out), nil},
			} {
				r := f.do(tok, tc.method, tc.path, tc.body)
				if tc.method == "GET" && tc.body == nil && r.rec.Code == 200 {
					require.Empty(t, listItems(r), "%s %s", tc.method, tc.path)
					continue
				}
				require.Equal(t, 404, r.rec.Code, "%s %s: %s", tc.method, tc.path, r.rec.Body.String())
				require.Equal(t, httpx.CodeNotFound, r.env.Code)
			}
			require.Equal(t, "olga", f.userDisplayName(out))
			require.Len(t, f.sidOf(out), 32, "会话还在")
			st := f.do(w.root, "GET", fmt.Sprintf("/system/users/%d", out), nil)
			require.EqualValues(t, 1, st.data()["status"])

			// 范围内的人可以改
			if len(c.see) > 1 {
				in := w.alice
				r := f.do(tok, "PUT", fmt.Sprintf("/system/users/%d", in), gin.H{"displayName": "Alice " + name})
				require.Equal(t, 0, r.env.Code, r.rec.Body.String())
				require.Equal(t, 200, f.do(tok, "GET", fmt.Sprintf("/system/users/%d", in), nil).rec.Code)
			}
		})
	}

	w := newOrgWorld(t)
	f := w.f
	t.Run("超管永远是全部", func(t *testing.T) {
		got := w.visible(w.root)
		for _, n := range []string{"root", "alice", "eve", "olga", "nod"} {
			require.Contains(t, got, n)
		}
	})

	t.Run("新建角色的用户资源默认仅本人", func(t *testing.T) {
		role := f.scopedRole(w.root, "fresh", []string{system.PermUserList}, "")
		r := f.do(w.root, "GET", fmt.Sprintf("/system/roles/%d/data-scopes", role), nil)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		require.Equal(t, "self", r.data()[system.DataUser])
		_, tok := f.userIn(w.root, "freshman", w.sales, []uint64{role})
		require.Equal(t, []string{"freshman"}, w.visible(tok))
		// 超管角色的范围是全部，数据资源清单列出可选范围和默认值
		r = f.do(w.root, "GET", fmt.Sprintf("/system/roles/%d/data-scopes", f.superRoleID()), nil)
		require.Equal(t, "all", r.data()[system.DataUser])
		res := f.do(w.root, "GET", "/system/data-resources", nil)
		require.Equal(t, 0, res.env.Code)
		items := listItems(res)
		require.Len(t, items, 1)
		require.Equal(t, system.DataUser, items[0]["code"])
		require.Equal(t, "self", items[0]["default"])
		require.Equal(t, []any{"self", "dept", "dept_tree", "all"}, items[0]["scopes"])
	})

	t.Run("没有查看用户权限的人，下拉里只有自己", func(t *testing.T) {
		require.Equal(t, []string{"nod"}, w.optionNames(w.nodTok))
	})
}

// ============ 58. 按权限码合并，停用的角色不算 ============

func TestDataScope_58_MergedPerPermission(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	seeAll := f.scopedRole(w.root, "see-all", []string{system.PermUserList}, "all")
	editSelf := f.scopedRole(w.root, "edit-self", []string{system.PermUserUpdate}, "self")
	id, tok := f.userIn(w.root, "mixer", w.sales, []uint64{seeAll, editSelf})

	require.Contains(t, w.visible(tok), "olga", "查看是全部")
	// 两个角色都给了查看、范围不同：取宽的，和角色的先后无关
	listSelf := f.scopedRole(w.root, "list-self", []string{system.PermUserList}, "self")
	r0 := f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", id), gin.H{"roleIds": []uint64{seeAll, editSelf, listSelf}})
	require.Equal(t, 0, r0.env.Code, r0.rec.Body.String())
	require.Contains(t, w.visible(tok), "olga", "取最宽的")
	r := f.do(tok, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "x"})
	require.Equal(t, 404, r.rec.Code, "修改只能改自己：%s", r.rec.Body.String())
	r = f.do(tok, "PUT", fmt.Sprintf("/system/users/%d", id), gin.H{"displayName": "Mixer"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 另一个角色给了"修改 + 全部"但被停用：不算
	editAll := f.scopedRole(w.root, "edit-all", []string{system.PermUserUpdate}, "all")
	r = f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", id), gin.H{"roleIds": []uint64{seeAll, editSelf, editAll}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 0, f.do(tok, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "ok"}).env.Code, "启用时可以改别人")
	r = f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d", editAll), gin.H{"name": "edit-all", "status": 0})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 404, f.do(tok, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "no"}).rec.Code, "停用后不算")
}

// ============ 59. 授权时不能放宽到超过自己 ============

func TestDataScope_59_GrantNotWiderThanYourOwn(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	// carol：本部门范围，能授权（敏感，由超管授给她）、能分配角色
	_, carol := w.manager("carol", w.sales, "dept", append([]string{system.PermRoleGrant}, userPerms...)...)

	target := f.scopedRole(w.root, "target", []string{system.PermUserList}, "self")
	grant := func(role uint64, body gin.H) resp {
		return f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", role), body)
	}
	scopeOf := func(role uint64) string {
		r := f.do(w.root, "GET", fmt.Sprintf("/system/roles/%d/data-scopes", role), nil)
		return r.data()[system.DataUser].(string)
	}
	permsOf := func(role uint64) []any {
		r := f.do(w.root, "GET", fmt.Sprintf("/system/roles/%d/perms", role), nil)
		return r.env.Data.([]any)
	}

	// 放宽到比自己宽：拒绝
	r := grant(target, gin.H{"codes": []string{system.PermUserList}, "dataScopes": gin.H{system.DataUser: "all"}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.dataScope.wider", fieldKey(r))
	require.Equal(t, "self", scopeOf(target))
	// 和自己一样宽：可以；再收窄：可以
	r = grant(target, gin.H{"codes": []string{system.PermUserList}, "dataScopes": gin.H{system.DataUser: "dept"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "dept", scopeOf(target))
	r = grant(target, gin.H{"codes": []string{system.PermUserList}, "dataScopes": gin.H{system.DataUser: "self"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "self", scopeOf(target))

	// 超管先把一个空角色设成全部，carol 给它加自己有的权限码：拒绝（否则等于借角色放宽）
	wide := f.scopedRole(w.root, "wide", nil, "all")
	r = grant(wide, gin.H{"codes": []string{system.PermUserList}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.dataScope.wider", fieldKey(r))
	require.Empty(t, permsOf(wide))
	// 同一个请求里加权限码并收窄范围：按最终状态判断，可以；两者一起写进去
	r = grant(wide, gin.H{"codes": []string{system.PermUserList}, "dataScopes": gin.H{system.DataUser: "dept"}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, []any{system.PermUserList}, permsOf(wide))
	require.Equal(t, "dept", scopeOf(wide))
	// 一个请求里既加权限码又放宽：整体拒绝，权限码也没写进去
	r = grant(wide, gin.H{"codes": []string{system.PermUserList, system.PermUserUpdate}, "dataScopes": gin.H{system.DataUser: "all"}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, []any{system.PermUserList}, permsOf(wide))
	require.Equal(t, "dept", scopeOf(wide))

	// 分配角色：范围比自己宽的角色不能分配给别人
	allRole := f.scopedRole(w.root, "all-role", []string{system.PermUserList}, "all")
	r = f.do(carol, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{allRole}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "rbac.role.widerScope", fieldKey(r))
	deptRole := f.scopedRole(w.root, "dept-role", []string{system.PermUserList}, "dept")
	r = f.do(carol, "PUT", fmt.Sprintf("/system/users/%d/roles", w.alice), gin.H{"roleIds": []uint64{deptRole}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 不认识的资源、不允许的范围
	r = grant(target, gin.H{"codes": []string{}, "dataScopes": gin.H{"ghost:thing": "all"}})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	require.Equal(t, "rbac.dataScope.unknown", fieldKey(r))
	r = f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d/perms", target), gin.H{"codes": []string{}, "dataScopes": gin.H{system.DataUser: "everything"}})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	require.Equal(t, "rbac.dataScope.invalid", fieldKey(r))

	// 超管角色不存范围
	r = f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d/perms", f.superRoleID()), gin.H{"codes": []string{}, "dataScopes": gin.H{system.DataUser: "self"}})
	require.NotEqual(t, 0, r.env.Code)
}

// ============ 60. 部门的写入、改自己的部门、改部门上级、升级迁移 ============

func TestDataScope_60_DepartmentWrites(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	mgrID, mgr := w.manager("mgr", w.sales, "dept_tree", append([]string{system.PermUserCreate, system.PermDeptList, system.PermDeptUpdate}, userPerms...)...)

	create := func(name string, dept uint64) resp {
		return f.do(mgr, "POST", "/system/users", gin.H{"username": name, "password": "user-pass-123", "deptId": dept})
	}
	require.Equal(t, 0, create("new-east", w.east).env.Code, "新部门在范围内")
	for _, d := range []uint64{w.ops, w.hq, 0} {
		r := create(fmt.Sprintf("new-%d", d), d)
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "system.user.deptOutOfScope", fieldKey(r))
	}

	// 改部门：新部门也要在范围内；挪到"未分配"要全部范围
	move := func(id, dept uint64) resp {
		return f.do(mgr, "PUT", fmt.Sprintf("/system/users/%d", id), gin.H{"displayName": "x", "deptId": dept})
	}
	require.Equal(t, 0, move(w.alice, w.east).env.Code)
	for _, d := range []uint64{w.ops, 0} {
		r := move(w.alice, d)
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "system.user.deptOutOfScope", fieldKey(r))
	}
	// 不能改自己的部门（改了就能扩大自己的范围），改自己的资料可以
	r := move(mgrID, w.east)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "system.user.ownDept", fieldKey(r))
	require.Equal(t, 0, f.do(mgr, "PUT", fmt.Sprintf("/system/users/%d", mgrID), gin.H{"displayName": "Manager"}).env.Code)
	// 超管改谁的部门都可以
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "Alice", "deptId": 0}).env.Code)

	// 改部门上级：非超管要"查看用户"为全部范围；改名不受影响
	dept := func(tok string, id, parent uint64, name string) resp {
		return f.do(tok, "PUT", fmt.Sprintf("/system/depts/%d", id), gin.H{"parentId": parent, "name": name})
	}
	r = dept(mgr, w.ops, w.east, "运维部")
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "system.dept.moveNeedsAll", fieldKey(r))
	require.NotContains(t, w.visible(mgr), "olga", "没挪成，运维部的人还是看不到")
	require.Equal(t, 0, dept(mgr, w.ops, w.hq, "运维中心").env.Code, "改名可以")
	// 查看是全部、修改只是本部门及下级：也不能挪（挪过来就能改那些人了）
	lookAll := f.scopedRole(w.root, "look-all", []string{system.PermUserList, system.PermDeptUpdate}, "all")
	editTree := f.scopedRole(w.root, "edit-tree", []string{system.PermUserUpdate}, "dept_tree")
	_, half := f.userIn(w.root, "half", w.sales, []uint64{lookAll, editTree})
	r = dept(half, w.ops, w.east, "运维中心")
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "system.dept.moveNeedsAll", fieldKey(r))
	// 用户资源的每个权限码都是全部范围才可以
	_, boss := w.manager("boss", w.hq, "all", append([]string{system.PermUserCreate, system.PermDeptUpdate}, userPerms...)...)
	require.Equal(t, 0, dept(boss, w.ops, w.east, "运维中心").env.Code, "全部范围可以挪")
	require.Contains(t, w.visible(mgr), "olga", "挪到华东组下面之后，本部门及下级就包括运维")
}

// 升级迁移：已经存在的非超管角色在用户资源上写成全部，超管角色不写；可以重跑。
func TestDataScope_60b_UpgradeKeepsExistingRolesAtAll(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	old := f.scopedRole(root, "legacy", []string{system.PermUserList}, "")
	require.NoError(t, f.gdb.Exec("DELETE FROM ga_role_data_scope").Error)

	sql, err := fs.ReadFile(migrations.Core(), migrations.CoreDir+"/00011_role_data_scope.sql")
	require.NoError(t, err)
	ctx := db.WithDB(context.Background(), f.gdb)
	for i := 0; i < 2; i++ {
		for _, stmt := range db.SplitStatements(string(sql)) {
			require.NoError(t, db.From(ctx).Exec(stmt).Error)
		}
	}
	var rows []struct {
		RoleID uint64
		Scope  string
	}
	require.NoError(t, f.gdb.Raw("SELECT role_id, scope FROM ga_role_data_scope WHERE resource = ?", system.DataUser).Scan(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, old, rows[0].RoleID)
	require.Equal(t, "all", rows[0].Scope)
}

// D-039 的几处边界：重新启用角色、部门负责人、写操作的回显、建用户时顺手分配角色。
func TestDataScope_60c_SideDoors(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f

	t.Run("非超管重新启用角色要过分配时的检查", func(t *testing.T) {
		_, ed := w.manager("editor", w.sales, "dept", append([]string{system.PermRoleUpdate}, userPerms...)...)
		wide := f.scopedRole(w.root, "wide-off", []string{system.PermUserList}, "all")
		sens := f.scopedRole(w.root, "sens-off", []string{system.PermUserCreate}, "self")
		ok := f.scopedRole(w.root, "ok-off", []string{system.PermUserList}, "dept")
		for _, id := range []uint64{wide, sens, ok} {
			r := f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": "off", "status": 0})
			require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		}
		enable := func(id uint64) resp {
			return f.do(ed, "PUT", fmt.Sprintf("/system/roles/%d", id), gin.H{"name": "on", "status": 1})
		}
		r := enable(wide)
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "rbac.role.widerScope", fieldKey(r))
		r = enable(sens)
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "rbac.role.holdsSensitive", fieldKey(r))
		require.Equal(t, 0, enable(ok).env.Code, "权限和范围都不超过自己的可以")
		require.Equal(t, 0, f.do(ed, "PUT", fmt.Sprintf("/system/roles/%d", ok), gin.H{"name": "off", "status": 0}).env.Code, "停用总是可以")
	})

	t.Run("部门负责人只能选范围内的人", func(t *testing.T) {
		_, lead := w.manager("leadset", w.sales, "dept", system.PermUserList, system.PermDeptUpdate, system.PermDeptCreate)
		r := f.do(lead, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.olga})
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		require.Equal(t, "system.dept.leader", fieldKey(r), "范围外的人和不存在的人回同一个错误")
		r = f.do(lead, "POST", "/system/depts", gin.H{"parentId": w.sales, "name": "新组", "leaderUserId": w.olga})
		require.Equal(t, "system.dept.leader", fieldKey(r))
		r = f.do(lead, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.alice})
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	})

	t.Run("修改范围比查看范围宽时，写完不回显看不到的人", func(t *testing.T) {
		seeSelf := f.scopedRole(w.root, "see-self", []string{system.PermUserList}, "self")
		editAll := f.scopedRole(w.root, "edit-all2", []string{system.PermUserUpdate, system.PermUserAssignRole}, "all")
		_, tok := f.userIn(w.root, "blind", w.sales, []uint64{seeSelf, editAll})
		r := f.do(tok, "PUT", fmt.Sprintf("/system/users/%d", w.olga), gin.H{"displayName": "Olga"})
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		require.Nil(t, r.env.Data, "改成功了，但不回显查看范围外的资料")
		r = f.do(tok, "PUT", fmt.Sprintf("/system/users/%d/roles", w.olga), gin.H{"roleIds": []uint64{}})
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		require.Nil(t, r.env.Data)
	})

	t.Run("建用户时分配角色要有分配角色的权限", func(t *testing.T) {
		mini := f.scopedRole(w.root, "mini2", []string{system.PermUserList}, "dept")
		_, maker := w.manager("maker", w.sales, "dept", system.PermUserCreate, system.PermUserList)
		r := f.do(maker, "POST", "/system/users", gin.H{"username": "made1", "password": "user-pass-123", "deptId": w.sales, "roleIds": []uint64{mini}})
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "rbac.perm.notOwned", fieldKey(r))
		r = f.do(maker, "POST", "/system/users", gin.H{"username": "made2", "password": "user-pass-123", "deptId": w.sales})
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	})
}

// ============ 60d. 数据中心的活跃用户排名只列"查看用户"范围内的人 ============

// 排名带账号名：范围外的人不能从这里露出来。全部范围看整个端，部门范围只看范围内的人，
// 仅本人（包括只有数据中心权限、没有查看用户权限的）只看自己。
func TestDataScope_60d_DashboardTopUsersFollowScope(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	// 每个人都留一条操作日志，让他们都有资格进排名
	for id, name := range w.ids {
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_operation_log (portal, user_id, username, action, method, path, created_at) VALUES ('platform', ?, ?, 'x', 'POST', '/x', NOW(3))", id, name).Error)
	}
	top := func(tok string) []string {
		r := f.do(tok, "GET", "/system/dashboard?days=7&tz=480", nil)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		items := r.data()["topUsers"].([]any)
		out := make([]string, 0, len(items))
		for _, x := range items {
			out = append(out, x.(map[string]any)["username"].(string))
		}
		sort.Strings(out)
		return out
	}

	_, all := w.manager("d-all", w.sales, "all", system.PermDashboardView, system.PermUserList)
	_, tree := w.manager("d-tree", w.sales, "dept_tree", system.PermDashboardView, system.PermUserList)
	_, dept := w.manager("d-dept", w.sales, "dept", system.PermDashboardView, system.PermUserList)
	_, self := w.manager("d-self", w.sales, "self", system.PermDashboardView, system.PermUserList)
	_, only := w.manager("d-only", w.sales, "", system.PermDashboardView) // 没有查看用户的权限
	for _, name := range []string{"d-all", "d-tree", "d-dept", "d-self", "d-only"} {
		var id uint64
		require.NoError(t, f.gdb.Raw("SELECT id FROM ga_user WHERE username = ?", name).Scan(&id).Error)
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_operation_log (portal, user_id, username, action, method, path, created_at) VALUES ('platform', ?, ?, 'x', 'POST', '/x', NOW(3))", id, name).Error)
	}

	require.Subset(t, top(w.root), []string{"root", "alice", "eve", "olga", "nod"}, "超管看整个端")
	require.Subset(t, top(all), []string{"root", "alice", "eve", "olga", "nod"}, "全部范围看整个端")
	require.Equal(t, []string{"alice", "d-all", "d-dept", "d-self", "d-tree", "eve"}, sliceWithout(top(tree), "d-only"), "本部门及下级：销售部和华东组的人")
	require.Equal(t, []string{"alice", "d-all", "d-dept", "d-self", "d-tree"}, sliceWithout(top(dept), "d-only"), "本部门：只有销售部的人")
	require.Equal(t, []string{"d-self"}, top(self), "仅本人只看自己")
	require.Equal(t, []string{"d-only"}, top(only), "没有查看用户的权限就只看自己")
}

func sliceWithout(s []string, x string) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// 规范 §13.2 第 86 条：用户详情返回的就是检查过范围的那一行（D-051）。检查之后、拼资料之前有人把目标调出了范围
// 并改了资料：返回的只能是检查时那一版，不能是调走之后的新资料。
func TestDataScope_86_DetailReturnsCheckedRow(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, tok := w.manager("m-dept", w.sales, "dept")
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "alice", "email": "alice@sales.test"}).env.Code)

	// 详情接口第一次读到 alice 这一行之后，另一个连接把她调到运维部并换了邮箱
	armed := true
	name := "test:move-alice"
	require.NoError(t, f.gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !armed || tx.Statement.Table != "ga_user" || len(tx.Statement.Vars) == 0 || fmt.Sprint(tx.Statement.Vars[0]) != fmt.Sprint(w.alice) {
			return // 只挑按 alice 的 ID 读账号的那一次（认证中间件读的是 m-dept 自己）
		}
		armed = false
		other := f.gdb.Session(&gorm.Session{NewDB: true, Context: context.Background()})
		require.NoError(t, other.Exec("UPDATE ga_user SET dept_id = ?, email = ? WHERE id = ?", w.ops, "alice@ops.secret", w.alice).Error)
	}))
	r := f.do(tok, "GET", fmt.Sprintf("/system/users/%d", w.alice), nil)
	armed = false
	require.NoError(t, f.gdb.Callback().Query().Remove(name))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, float64(w.sales), r.data()["deptId"], "返回的是检查范围时那一版")
	require.Equal(t, "alice@sales.test", r.data()["email"], "调出范围之后的新资料不能带出来")

	// 调走之后再看：范围外，当作不存在
	require.Equal(t, httpx.CodeNotFound, f.do(tok, "GET", fmt.Sprintf("/system/users/%d", w.alice), nil).env.Code)
}

// onceAfterQuery 在第一条 SQL 以 prefix 开头的查询之后，调用一次 fn（fn 用另一个连接提交写入）。
// 用来在"算完范围、再查数据"之间插入并发的调部门。范围内的 ID 现在是子查询（D-055）：gorm 拼子查询时也会走一遍
// 查询回调（DryRun，不访问数据库），fn 就在拼子查询的时候提交——这时请求的快照已经建立，外层查询还没执行。
func (f *fixture) onceAfterQuery(name, prefix string, fn func(other *gorm.DB)) (disarm func()) {
	f.t.Helper()
	armed := true
	require.NoError(f.t, f.gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !armed || !strings.HasPrefix(tx.Statement.SQL.String(), prefix) {
			return
		}
		armed = false
		fn(f.gdb.Session(&gorm.Session{NewDB: true, Context: context.Background()}))
	}))
	return func() {
		armed = false
		require.NoError(f.t, f.gdb.Callback().Query().Remove(name))
	}
}

const idsInScopeSQL = "SELECT `id` FROM `ga_user`"

// 规范 §13.2 第 88 条：会话列表里"范围内有哪些人"和"这些人的会话"来自同一个快照（D-052）。
// 取完范围内的 ID 之后 alice 被调出范围并在别处登录：列表里不能出现她调走之后的新会话。
func TestDataScope_88a_SessionListOneSnapshot(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, tok := w.manager("m-dept", w.sales, "dept")
	sids := func(r resp) []string {
		out := []string{}
		for _, it := range listItems(r) {
			if it["userId"].(float64) == float64(w.alice) {
				out = append(out, it["sid"].(string))
			}
		}
		return out
	}
	before := sids(f.do(tok, "GET", "/system/sessions?pageSize=100", nil))
	require.NotEmpty(t, before, "alice 在范围内，能看到她的会话")

	var newSID string
	disarm := f.onceAfterQuery("test:move-alice-sessions", idsInScopeSQL, func(other *gorm.DB) {
		require.NoError(t, other.Exec("UPDATE ga_user SET dept_id = ? WHERE id = ?", w.ops, w.alice).Error)
		l := f.do("", "POST", "/auth/login", gin.H{"username": "alice", "password": "user-pass-456"})
		require.Equal(t, 0, l.env.Code, l.rec.Body.String())
		newSID = l.data()["sessionId"].(string)
	})
	r := f.do(tok, "GET", "/system/sessions?pageSize=100", nil)
	disarm()
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.NotEmpty(t, newSID)
	require.NotContains(t, sids(r), newSID, "调出范围之后的新会话不能列出来")
	require.ElementsMatch(t, before, sids(r), "列出来的是取范围时那一刻的会话")

	// 调走之后再看：她已经在范围外
	require.Empty(t, sids(f.do(tok, "GET", "/system/sessions?pageSize=100", nil)))
}

// 规范 §13.2 第 88 条：数据中心的活跃用户排名同样在一个快照里读（D-052）：调出范围之后的操作不算进来。
func TestDataScope_88b_TopUsersOneSnapshot(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, tok := w.manager("d-dept", w.sales, "dept", system.PermDashboardView, system.PermUserList)
	logOp := func(g *gorm.DB) {
		require.NoError(t, g.Exec("INSERT INTO ga_operation_log (portal, user_id, username, action, method, path, created_at) VALUES ('platform', ?, 'alice', 'x', 'POST', '/x', NOW(3))", w.alice).Error)
	}
	logOp(f.gdb)
	aliceCount := func(r resp) float64 {
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		for _, x := range r.data()["topUsers"].([]any) {
			if m := x.(map[string]any); m["userId"].(float64) == float64(w.alice) {
				return m["count"].(float64)
			}
		}
		return 0
	}
	base := aliceCount(f.do(tok, "GET", "/system/dashboard?days=7&tz=480", nil))
	require.Positive(t, base, "alice 在范围内，排名里有她")
	disarm := f.onceAfterQuery("test:move-alice-top", idsInScopeSQL, func(other *gorm.DB) {
		require.NoError(t, other.Exec("UPDATE ga_user SET dept_id = ? WHERE id = ?", w.ops, w.alice).Error)
		logOp(other)
		logOp(other)
	})
	r := f.do(tok, "GET", "/system/dashboard?days=7&tz=480", nil)
	disarm()
	require.Equal(t, base, aliceCount(r), "只算取范围那一刻的操作，调走之后的两次不算")
	require.Equal(t, float64(0), aliceCount(f.do(tok, "GET", "/system/dashboard?days=7&tz=480", nil)), "调走之后不在排名里")
}

// 规范 §13.2 第 94 条：用户详情的数据范围、操作人所在的部门、被读的用户在同一个数据库视图里（D-054）。
// 旧状态：m 在销售部、仅本人；新状态：m 调到运维部、本部门。销售部的 alice 在两个状态下都看不到。
// 读 alice 之后有人成套改成新状态：不能拼成"新的本部门范围 + 旧的销售部"而把 alice 放出来。
func TestDataScope_94b_ScopeAndActorDeptSameView(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	role := f.scopedRole(w.root, "r-m", userPerms, "self")
	_, tok := f.userIn(w.root, "mover", w.sales, []uint64{role})
	var mID uint64
	require.NoError(t, f.gdb.Raw("SELECT id FROM ga_user WHERE username = 'mover'").Scan(&mID).Error)
	require.Equal(t, httpx.CodeNotFound, f.do(tok, "GET", fmt.Sprintf("/system/users/%d", w.alice), nil).env.Code, "旧状态下看不到 alice")

	armed := true
	name := "test:move-m"
	require.NoError(t, f.gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !armed || tx.Statement.Table != "ga_user" || len(tx.Statement.Vars) == 0 || fmt.Sprint(tx.Statement.Vars[0]) != fmt.Sprint(w.alice) {
			return
		}
		armed = false
		other := f.gdb.Session(&gorm.Session{NewDB: true, Context: context.Background()})
		require.NoError(t, other.Exec("UPDATE ga_user SET dept_id = ? WHERE id = ?", w.ops, mID).Error)
		g := f.do(w.root, "PUT", fmt.Sprintf("/system/roles/%d/perms", role), gin.H{"codes": userPerms, "dataScopes": gin.H{system.DataUser: "dept"}})
		require.Equal(t, 0, g.env.Code, g.rec.Body.String())
	}))
	r := f.do(tok, "GET", fmt.Sprintf("/system/users/%d", w.alice), nil)
	armed = false
	require.NoError(t, f.gdb.Callback().Query().Remove(name))
	require.False(t, armed)
	require.Equal(t, httpx.CodeNotFound, r.env.Code, "拼出来的范围不能把 alice 放出来：%s", r.rec.Body.String())

	// 新状态下：m 在运维部、本部门，看得到运维部的 olga，看不到 alice
	require.Equal(t, 0, f.do(tok, "GET", fmt.Sprintf("/system/users/%d", w.olga), nil).env.Code)
	require.Equal(t, httpx.CodeNotFound, f.do(tok, "GET", fmt.Sprintf("/system/users/%d", w.alice), nil).env.Code)
}

// 规范 §13.2 第 97 条：部门列表的负责人按调用者"查看用户"的范围遮蔽（D-055）。部门结构和人数照常给；
// 范围外的负责人只标记"有负责人"，不给 ID 和名字。改部门时不给负责人就保持原来的（调用者可能看不到他是谁）。
func TestDataScope_97_DeptLeaderMaskedByUserScope(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	// 销售部的负责人是运维部的 olga
	r := f.do(w.root, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.olga})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	sales := func(tok string) map[string]any {
		r := f.do(tok, "GET", "/system/depts", nil)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		for _, x := range r.env.Data.([]any) {
			if m := x.(map[string]any); m["id"].(float64) == float64(w.sales) {
				return m
			}
		}
		t.Fatal("没有销售部")
		return nil
	}
	root := sales(w.root)
	require.Equal(t, float64(w.olga), root["leaderUserId"])
	require.Equal(t, false, root["leaderHidden"])
	require.NotEmpty(t, root["leaderName"])

	// 本部门范围的管理员（在销售部）：olga 在运维部，看不到是谁
	_, mgr := w.manager("m-dept", w.sales, "dept", append([]string{system.PermDeptList, system.PermDeptUpdate}, userPerms...)...)
	m := sales(mgr)
	require.Equal(t, float64(0), m["leaderUserId"])
	require.Equal(t, "", m["leaderName"])
	require.Equal(t, true, m["leaderHidden"])
	require.Equal(t, sales(w.root)["userCount"], m["userCount"], "人数照常给")

	// 只有部门列表权限、没有查看用户的权限：负责人一律看不到（仅本人）
	_, only := w.manager("d-only", w.sales, "", system.PermDeptList)
	require.Equal(t, true, sales(only)["leaderHidden"])

	// 管理员改部门、不给负责人：保持 olga，回显照样遮蔽
	u := f.do(mgr, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售一部"})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
	require.Equal(t, true, u.data()["leaderHidden"])
	require.Equal(t, float64(0), u.data()["leaderUserId"])
	require.Equal(t, float64(w.olga), sales(w.root)["leaderUserId"], "没给负责人：保持原来的")

	// 明确给 0：清空
	u = f.do(mgr, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售一部", "leaderUserId": 0})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
	require.Equal(t, float64(0), sales(w.root)["leaderUserId"])
	require.Equal(t, false, sales(w.root)["leaderHidden"])
}

// 规范 §13.2 第 99 条：会话列表、活跃用户排名按范围过滤时，范围内的用户 ID 作为子查询交给数据库，不先把全部 ID 取到内存里
// （D-055）：范围很大（本部门及下级）时，中间列表会很长。
func TestDataScope_99_ScopeFilterStaysInDatabase(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, tok := w.manager("d-tree", w.hq, "dept_tree", append([]string{system.PermDashboardView}, userPerms...)...)
	var materialized int
	name := "test:count-id-lists"
	require.NoError(t, f.gdb.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if !tx.DryRun && strings.HasPrefix(tx.Statement.SQL.String(), idsInScopeSQL) {
			materialized++
		}
	}))
	defer func() { require.NoError(t, f.gdb.Callback().Query().Remove(name)) }()
	r := f.do(tok, "GET", "/system/sessions?pageSize=100", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.NotEmpty(t, listItems(r), "范围内有人在线")
	d := f.do(tok, "GET", "/system/dashboard?days=7&tz=480", nil)
	require.Equal(t, 0, d.env.Code, d.rec.Body.String())
	require.Zero(t, materialized, "范围内的 ID 不能单独查出来再拼进 IN 列表")
}

// 规范 §13.2 第 101 条：超管在途中被降级后改部门（D-056）。写入照常（剩下的角色有编辑部门的权限），
// 回显的负责人按降级后的"查看用户"范围遮蔽，不能沿用请求开始时的超管身份把范围外的负责人带出来。
func TestDataScope_101_DeptEchoUsesActorAfterRecheck(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	// 销售部的负责人是运维部的 olga
	r := f.do(w.root, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.olga})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	olgaName := r.data()["leaderName"].(string)
	require.NotEmpty(t, olgaName)

	// sam 是超管，另有一个本部门范围的角色（能编辑部门、能查看本部门的人）
	role := f.scopedRole(w.root, "r-sam", append([]string{system.PermDeptList, system.PermDeptUpdate}, userPerms...), "dept")
	_, sam := f.userIn(w.root, "sam", w.sales, []uint64{f.superRoleID(), role})
	_, _, revoke := f.revokeOf("sam")

	u := f.inFlight(revoke, func() resp {
		return f.do(sam, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售一部"})
	})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
	require.Equal(t, true, u.data()["leaderHidden"])
	require.Equal(t, float64(0), u.data()["leaderUserId"])
	require.NotContains(t, u.rec.Body.String(), olgaName, "降级后看不到 olga，回显不能带她的名字")
	require.False(t, f.holdsSuper(f.userID("sam")))

	// 明确给原负责人：按锁内认定的身份（已降级）检查，olga 在范围外，和给别的范围外的人一样被拒（D-058），什么都不写
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d/roles", f.userID("sam")),
		gin.H{"roleIds": []uint64{f.superRoleID(), role}}).env.Code)
	u = f.inFlight(revoke, func() resp {
		return f.do(sam, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售二部", "leaderUserId": w.olga})
	})
	require.Equal(t, httpx.CodeValidation, u.env.Code, u.rec.Body.String())
	require.Contains(t, u.rec.Body.String(), "system.dept.leader")
	require.NotContains(t, u.rec.Body.String(), olgaName)
	// 负责人没变
	r = f.do(w.root, "GET", "/system/depts", nil)
	require.Contains(t, r.rec.Body.String(), olgaName)
}

// 规范 §13.2 第 106 条（D-058）：改部门时明确给出原负责人，也要他在调用者"查看用户"的范围内；看不到他的人给他的 ID，
// 和给不存在的、范围外的 ID 回同一个错误，逐个试 ID 认不出被遮蔽的负责人。保持原负责人靠不给 leaderUserId。
func TestDataScope_106_HiddenLeaderCannotBeProbed(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	r := f.do(w.root, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.olga})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	_, mgr := w.manager("m-probe", w.sales, "dept", append([]string{system.PermDeptList, system.PermDeptUpdate}, userPerms...)...)

	answer := func(leader uint64) string {
		u := f.do(mgr, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": leader})
		key := ""
		if strings.Contains(u.rec.Body.String(), "system.dept.leader") {
			key = "system.dept.leader"
		}
		return fmt.Sprintf("%d %d %s", u.rec.Code, u.env.Code, key)
	}
	hidden, outside, missing := answer(w.olga), answer(w.nod), answer(999999)
	require.Equal(t, fmt.Sprintf("200 %d system.dept.leader", httpx.CodeValidation), hidden)
	require.Equal(t, outside, hidden, "原负责人和别的范围外的人回同样的结果")
	require.Equal(t, missing, hidden, "原负责人和不存在的 ID 回同样的结果")

	// 不给 leaderUserId：保持原负责人
	u := f.do(mgr, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售一部"})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
	root := f.do(w.root, "GET", "/system/depts", nil)
	require.Contains(t, root.rec.Body.String(), fmt.Sprintf(`"leaderUserId":%d`, w.olga))

	// 范围内的原负责人停用了，明确给出照样可以保留
	require.Equal(t, 0, f.do(w.root, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.alice}).env.Code)
	require.Equal(t, 0, f.do(w.root, "POST", fmt.Sprintf("/system/users/%d/status", w.alice), gin.H{"status": 0}).env.Code)
	u = f.do(mgr, "PUT", fmt.Sprintf("/system/depts/%d", w.sales), gin.H{"parentId": w.hq, "name": "销售部", "leaderUserId": w.alice})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
}

// 规范 §13.2 第 108 条（D-058）：重新启用停用的账号等于把它的角色的权限交回去。非超管只能启用"每个启用的角色都是
// 自己能分配的"账号：角色含敏感权限码、含自己没有的权限码或范围比自己宽的，只有超管能启用。停用不受这条限制。
func TestDataScope_108_ReenableNeedsAssignableRoles(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	sensitive := f.createRole(root, "auditor", []string{system.PermMonitorView})
	plain := f.createRole(root, "viewer", []string{system.PermUserList})
	xavierID, _ := f.createUser(root, "xavier", []uint64{sensitive})
	yuriID, _ := f.createUser(root, "yuri", []uint64{plain})
	helenRole := f.scopedRole(root, "helen", []string{system.PermUserList, system.PermUserStatus}, "all")
	_, helen := f.createUser(root, "helen", []uint64{helenRole})
	status := func(tok string, id uint64, st int) resp {
		return f.do(tok, "POST", fmt.Sprintf("/system/users/%d/status", id), gin.H{"status": st})
	}
	enabled := func(id uint64) bool {
		var st int
		require.NoError(t, f.gdb.Raw("SELECT status FROM ga_user WHERE id = ?", id).Scan(&st).Error)
		return st == 1
	}

	// 停用：照常可以
	require.Equal(t, 0, status(helen, xavierID, 0).env.Code)
	require.Equal(t, 0, status(helen, yuriID, 0).env.Code)

	// 含敏感权限码的角色：非超管不能重新启用
	r := status(helen, xavierID, 1)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	// 只说"有你不能分配的角色"，不说是哪个角色、哪个权限码（D-059）
	require.Contains(t, r.rec.Body.String(), "rbac.user.enableNotAssignable")
	require.NotContains(t, r.rec.Body.String(), system.PermMonitorView)
	require.NotContains(t, r.rec.Body.String(), fmt.Sprintf(`"id":%d`, sensitive))
	require.False(t, enabled(xavierID))

	// 含自己也有的权限码（范围不比自己宽）：可以
	require.Equal(t, 0, status(helen, yuriID, 1).env.Code)
	require.True(t, enabled(yuriID))

	// 超管可以
	require.Equal(t, 0, status(root, xavierID, 1).env.Code)
	require.True(t, enabled(xavierID))
	// 已经是启用的账号再"启用"一次不做检查（不改变任何人的权限）
	require.Equal(t, 0, status(helen, xavierID, 1).env.Code)
}
