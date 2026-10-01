package system_test

// 首页概览（D-027）：只用真实数据，按权限访问，日期按浏览器时区连续补齐。

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/modules/system"
)

func TestDashboard_RealDataAndPermission(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	// 活跃用户排名只列"查看用户"范围内的人（D-039、第 69d 条）：这里给全部范围，看整个端
	role := f.scopedRole(admin, "viewer", []string{system.PermDashboardView, system.PermUserList}, "all")
	_, viewer := f.createUser(admin, "viewer1", []uint64{role})
	none := f.createRole(admin, "none", nil)
	_, nobody := f.createUser(admin, "nobody1", []uint64{none})
	// 两次登录失败
	f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "wrong-1"})
	f.do("", "POST", "/auth/login", gin.H{"username": "ghost", "password": "wrong-2"})

	r := f.do(viewer, "GET", "/system/dashboard?days=7&tz=480", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	d := r.data()
	days := d["days"].([]any)
	require.Len(t, days, 7)
	today := time.Now().UTC().Add(8 * time.Hour).Format("2006-01-02")
	require.Equal(t, today, days[6], "最后一天是浏览器时区的今天")

	users := d["users"].(map[string]any)
	require.EqualValues(t, 3, users["total"])
	require.EqualValues(t, 3, users["enabled"])
	require.EqualValues(t, 3, users["new"])
	require.GreaterOrEqual(t, d["sessions"].(float64), float64(3))

	logins := d["logins"].(map[string]any)
	success, failed := logins["success"].([]any), logins["failed"].([]any)
	require.Len(t, success, 7)
	require.GreaterOrEqual(t, success[6].(float64), float64(3), "root、viewer1、nobody1 今天都登录过")
	require.EqualValues(t, 2, failed[6])
	for i := range 6 {
		require.EqualValues(t, 0, success[i], "之前的日子补零")
	}
	reasons := map[string]float64{}
	for _, x := range d["reasons"].([]any) {
		m := x.(map[string]any)
		reasons[m["reason"].(string)] = m["count"].(float64)
	}
	require.EqualValues(t, 2, reasons["bad_password"]+reasons["no_account"])

	ops := d["operations"].([]any)
	require.Greater(t, ops[6].(float64), float64(0))
	actions := map[string]bool{}
	for _, x := range d["topActions"].([]any) {
		actions[x.(map[string]any)["action"].(string)] = true
	}
	require.True(t, actions[system.OpUserCreate], "建用户记了操作日志")

	// D-030 新增：每天新用户、活跃用户排名、按钟点的活跃分布
	newUsers := d["newUsers"].([]any)
	require.Len(t, newUsers, 7)
	require.EqualValues(t, 3, newUsers[6])
	topUsers := d["topUsers"].([]any)
	require.NotEmpty(t, topUsers)
	require.Equal(t, "root", topUsers[0].(map[string]any)["username"], "建角色、建用户的是 root")
	hours := d["hours"].(map[string]any)
	hl, ho := hours["logins"].([]any), hours["operations"].([]any)
	require.Len(t, hl, 24)
	require.Len(t, ho, 24)
	var sumLogins, sumOps float64
	for i := range 24 {
		sumLogins += hl[i].(float64)
		sumOps += ho[i].(float64)
	}
	require.Equal(t, success[6].(float64), sumLogins, "钟点分布的总数等于期间的成功登录数")
	require.Equal(t, ops[6].(float64), sumOps)

	// 没有权限：403
	r = f.do(nobody, "GET", "/system/dashboard", nil)
	require.Equal(t, 403, r.rec.Code)

	// 参数越界夹到边界，非法值用默认
	r = f.do(viewer, "GET", "/system/dashboard?days=100000&tz=99999", nil)
	require.Equal(t, 0, r.env.Code)
	require.Len(t, r.data()["days"].([]any), 90)
	r = f.do(viewer, "GET", "/system/dashboard?days=abc", nil)
	require.Len(t, r.data()["days"].([]any), 30)
}
