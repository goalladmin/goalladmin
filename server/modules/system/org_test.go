package system_test

// 部门与岗位（D-033，规范 §13.2 第 49–51 条）。

import (
	"fmt"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

func (f *fixture) createDept(admin string, parent uint64, name string) uint64 {
	f.t.Helper()
	r := f.do(admin, "POST", "/system/depts", gin.H{"parentId": parent, "name": name})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	return uint64(r.data()["id"].(float64))
}

func (f *fixture) createPost(admin, code string) uint64 {
	f.t.Helper()
	r := f.do(admin, "POST", "/system/posts", gin.H{"code": code, "name": "岗位 " + code})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	return uint64(r.data()["id"].(float64))
}

func fieldKey(r resp) string {
	fields, _ := r.data()["fields"].([]any)
	if len(fields) == 0 {
		return ""
	}
	k, _ := fields[0].(map[string]any)["key"].(string)
	return k
}

// 第 49 条：部门树的完整性——同级不重名、不成环、最多 10 层、上级必须存在；有下级或还有用户的部门不能删。
func TestOrg_49_DeptTreeRules(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	a := f.createDept(admin, 0, "总部")
	b := f.createDept(admin, a, "研发")
	c := f.createDept(admin, b, "后端组")

	// 同级重名被拒；不同上级下可以同名
	r := f.do(admin, "POST", "/system/depts", gin.H{"parentId": a, "name": "研发"})
	require.Equal(t, httpx.CodeConflict, r.env.Code)
	require.Equal(t, "system.dept.nameTaken", r.env.Key)
	f.createDept(admin, c, "研发")

	// 上级不存在；名字全是空白或带控制字符
	r = f.do(admin, "POST", "/system/depts", gin.H{"parentId": 999999, "name": "x"})
	require.Equal(t, "system.dept.parent", fieldKey(r))
	r = f.do(admin, "POST", "/system/depts", gin.H{"name": "   "})
	require.Equal(t, "system.org.nameLength", fieldKey(r))
	r = f.do(admin, "POST", "/system/depts", gin.H{"name": "a\u202eb"})
	require.Equal(t, "system.org.nameChars", fieldKey(r))

	// 不能挪到自己或自己的下级下面
	for _, parent := range []uint64{a, c} {
		r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", a), gin.H{"parentId": parent, "name": "总部"})
		require.Equal(t, "system.dept.cycle", fieldKey(r), "parent=%d", parent)
	}

	// 最多 10 层：从 c（第 3 层）往下建到第 10 层，第 11 层被拒
	last := c
	for i := 4; i <= 10; i++ {
		last = f.createDept(admin, last, fmt.Sprintf("第%d层", i))
	}
	r = f.do(admin, "POST", "/system/depts", gin.H{"parentId": last, "name": "第11层"})
	require.Equal(t, "system.dept.depth", fieldKey(r))
	// 挪子树也不能超过 10 层：新建一个两层的子树，挪到第 9 层下面就超了
	x := f.createDept(admin, 0, "外包")
	f.createDept(admin, x, "外包一组")
	var level9 uint64
	require.NoError(t, f.gdb.Raw("SELECT parent_id FROM ga_dept WHERE id = ?", last).Scan(&level9).Error)
	r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", x), gin.H{"parentId": level9, "name": "外包"})
	require.Equal(t, "system.dept.depth", fieldKey(r))

	// 负责人必须存在；列表带负责人名字和直属人数
	r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", b), gin.H{"parentId": a, "name": "研发", "leaderUserId": 999999})
	require.Equal(t, "system.dept.leader", fieldKey(r))
	r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", b), gin.H{"parentId": a, "name": "研发", "leaderUserId": adminID})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	// 新选的负责人必须是启用的用户；原来的负责人停用了，编辑部门别的字段时可以保留
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "gone", "password": "user-pass-123"})
	goneID := uint64(r.data()["user"].(map[string]any)["id"].(float64))
	r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", c), gin.H{"parentId": b, "name": "后端组", "leaderUserId": goneID})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 0, f.do(admin, "POST", fmt.Sprintf("/system/users/%d/status", goneID), gin.H{"status": 0}).env.Code)
	r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", c), gin.H{"parentId": b, "name": "后端组", "leaderUserId": goneID, "sort": 5})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", a), gin.H{"name": "总部", "leaderUserId": goneID})
	require.Equal(t, "system.dept.leader", fieldKey(r))

	// 删除：有下级不行；有用户不行；都没有了才行
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/depts/%d", b), nil)
	require.Equal(t, "system.dept.hasChildren", r.env.Key)
	lone := f.createDept(admin, 0, "临时")
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "tom", "password": "user-pass-123", "deptId": lone})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	tomID := uint64(r.data()["user"].(map[string]any)["id"].(float64))
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/depts/%d", lone), nil)
	require.Equal(t, "system.dept.hasUsers", r.env.Key)
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d", tomID), gin.H{"displayName": "tom", "deptId": 0})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/depts/%d", lone), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	r = f.do(admin, "GET", "/system/depts", nil)
	require.Equal(t, 0, r.env.Code)
	for _, v := range r.env.Data.([]any) {
		d := v.(map[string]any)
		if d["id"] == float64(b) {
			require.Equal(t, "root", d["leaderName"])
		}
	}
}

// 第 49 条：两个管理员同时把 A 挪到 B 下、把 B 挪到 A 下，只能成功一个，树里不会出现环。
func TestOrg_49b_ConcurrentMovesDoNotCycle(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	for round := range 5 {
		a := f.createDept(admin, 0, fmt.Sprintf("A%d", round))
		b := f.createDept(admin, 0, fmt.Sprintf("B%d", round))
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i, pair := range [][2]uint64{{a, b}, {b, a}} {
			wg.Add(1)
			go func(i int, id, parent uint64, name string) {
				defer wg.Done()
				_, codes[i] = f.try(admin, "PUT", fmt.Sprintf("/system/depts/%d", id), gin.H{"parentId": parent, "name": name})
			}(i, pair[0], pair[1], map[uint64]string{a: fmt.Sprintf("A%d", round), b: fmt.Sprintf("B%d", round)}[pair[0]])
		}
		wg.Wait()
		require.ElementsMatch(t, []int{0, httpx.CodeValidation}, codes, "round %d", round)
		var parents []uint64
		require.NoError(t, f.gdb.Raw("SELECT parent_id FROM ga_dept WHERE id IN ? ORDER BY id", []uint64{a, b}).Scan(&parents).Error)
		require.Contains(t, parents, uint64(0), "至少一个还在顶级")
	}
}

// 第 50 条：用户的部门、岗位——新选的必须存在且启用，原来就有的停用了也能保留；岗位最多 20 个、整体替换；
// 不传表示不改；还有人在用的岗位不能删；岗位编码合规、唯一、不可改；按部门筛选用户包括下级。
func TestOrg_50_UserAssignment(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	hq := f.createDept(admin, 0, "总部")
	rd := f.createDept(admin, hq, "研发")
	pm := f.createPost(admin, "pm")
	tl := f.createPost(admin, "tech-lead")

	// 岗位编码：格式、重复、不可改
	for _, code := range []string{"PM", "1abc", "a b", ""} {
		r := f.do(admin, "POST", "/system/posts", gin.H{"code": code, "name": "x"})
		require.Equal(t, "system.post.code", fieldKey(r), code)
	}
	r := f.do(admin, "POST", "/system/posts", gin.H{"code": "pm", "name": "x"})
	require.Equal(t, "system.post.codeTaken", r.env.Key)
	r = f.do(admin, "PUT", fmt.Sprintf("/system/posts/%d", pm), gin.H{"code": "hacked", "name": "产品经理"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "pm", r.data()["code"])

	// 建用户时一起分配
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "amy", "password": "user-pass-123", "deptId": rd, "postIds": []uint64{pm, tl, pm}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	u := r.data()["user"].(map[string]any)
	amy := uint64(u["id"].(float64))
	require.Equal(t, "研发", u["deptName"])
	require.Len(t, u["posts"], 2, "重复的岗位只算一次")

	// 停用部门和岗位：已经在里面的保留，改别的字段不受影响；新分配被拒
	f.do(admin, "PUT", fmt.Sprintf("/system/depts/%d", rd), gin.H{"parentId": hq, "name": "研发", "status": 0})
	f.do(admin, "PUT", fmt.Sprintf("/system/posts/%d", tl), gin.H{"name": "技术负责人", "status": 0})
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d", amy), gin.H{"displayName": "Amy", "deptId": rd, "postIds": []uint64{pm, tl}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "ben", "password": "user-pass-123", "deptId": rd})
	require.Equal(t, "system.user.deptDisabled", fieldKey(r))
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "ben", "password": "user-pass-123", "postIds": []uint64{tl}})
	require.Equal(t, "system.user.postDisabled", fieldKey(r))
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "ben", "password": "user-pass-123", "deptId": 999999})
	require.Equal(t, "system.user.dept", fieldKey(r))
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "ben", "password": "user-pass-123", "postIds": []uint64{999999}})
	require.Equal(t, "system.user.postMissing", fieldKey(r))
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user WHERE username = 'ben'").Scan(&n).Error)
	require.Zero(t, n, "分配失败时用户也不会建出来")

	// 最多 20 个岗位
	many := make([]uint64, 0, 21)
	for i := range 21 {
		many = append(many, f.createPost(admin, fmt.Sprintf("p%d", i)))
	}
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d", amy), gin.H{"displayName": "Amy", "postIds": many})
	require.Equal(t, "system.user.posts", fieldKey(r))

	// 不传 postIds 不改；传空数组清空
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d", amy), gin.H{"displayName": "Amy"})
	require.Len(t, r.data()["posts"], 2)
	require.EqualValues(t, rd, r.data()["deptId"])

	// 还有人在用的岗位不能删
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/posts/%d", pm), nil)
	require.Equal(t, "system.post.inUse", r.env.Key)
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d", amy), gin.H{"displayName": "Amy", "postIds": []uint64{}})
	require.Len(t, r.data()["posts"], 0)
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/posts/%d", pm), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 按部门筛选：默认包括下级部门
	r = f.do(admin, "GET", fmt.Sprintf("/system/users?deptId=%d", hq), nil)
	require.EqualValues(t, 1, r.data()["total"])
	r = f.do(admin, "GET", fmt.Sprintf("/system/users?deptId=%d&withChildren=0", hq), nil)
	require.EqualValues(t, 0, r.data()["total"])
}

// 第 51 条：权限——部门、岗位接口各要各的权限码；用户表单用的下拉要 system:user:list；增删改记操作日志；都不是敏感权限。
func TestOrg_51_Perms(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	f.createDept(admin, 0, "总部")
	viewerRole := f.createRole(admin, "user-viewer", []string{system.PermUserList})
	_, viewer := f.createUser(admin, "viewer", []uint64{viewerRole})
	otherRole := f.createRole(admin, "log-viewer", []string{system.PermOplogList})
	_, other := f.createUser(admin, "other", []uint64{otherRole})

	require.Equal(t, 0, f.do(viewer, "GET", "/system/options/depts", nil).env.Code)
	require.Equal(t, 0, f.do(viewer, "GET", "/system/options/posts", nil).env.Code)
	require.Equal(t, 403, f.do(other, "GET", "/system/options/depts", nil).rec.Code)
	require.Equal(t, 403, f.do(other, "GET", "/system/options/posts", nil).rec.Code)
	for _, rt := range [][2]string{{"GET", "/system/depts"}, {"POST", "/system/depts"}, {"GET", "/system/posts"}, {"POST", "/system/posts"}} {
		require.Equal(t, 403, f.do(viewer, rt[0], rt[1], gin.H{"name": "x", "code": "x"}).rec.Code, rt)
	}
	for _, code := range []string{system.PermDeptList, system.PermDeptCreate, system.PermDeptUpdate, system.PermDeptDelete,
		system.PermPostList, system.PermPostCreate, system.PermPostUpdate, system.PermPostDelete} {
		p, ok := f.app.Deps().Perms.Perm("platform", code)
		require.True(t, ok, code)
		require.False(t, p.Sensitive, code)
	}
	require.Len(t, f.oplogs(system.OpDeptCreate), 1)
}

// 第 49 条：编辑部门里的用户和删除这个部门同时发生：不会死锁或出 500，结果要么删掉（用户已离开）、要么因为还有人而拒绝。
func TestOrg_49c_EditUserWhileDeletingDept(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	for round := range 8 {
		d := f.createDept(admin, 0, fmt.Sprintf("D%d", round))
		r := f.do(admin, "POST", "/system/users", gin.H{"username": fmt.Sprintf("member%d", round), "password": "user-pass-123", "deptId": d})
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		uid := uint64(r.data()["user"].(map[string]any)["id"].(float64))
		var wg sync.WaitGroup
		var editStatus, delStatus, delCode int
		wg.Add(2)
		go func() {
			defer wg.Done()
			editStatus, _ = f.try(admin, "PUT", fmt.Sprintf("/system/users/%d", uid), gin.H{"displayName": "x", "deptId": d, "postIds": []uint64{}})
		}()
		go func() {
			defer wg.Done()
			delStatus, delCode = f.try(admin, "DELETE", fmt.Sprintf("/system/depts/%d", d), nil)
		}()
		wg.Wait()
		require.Less(t, editStatus, 500, "round %d", round)
		require.Less(t, delStatus, 500, "round %d", round)
		require.Equal(t, httpx.CodeConflict, delCode, "用户一直在这个部门里，删除必须被拒绝")
	}
}
