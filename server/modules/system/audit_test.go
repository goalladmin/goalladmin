package system_test

// D-032 运维中心与安全审计的测试。

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// sidOf 返回某用户当前唯一有效会话的 sid。
func (f *fixture) sidOf(userID uint64) string {
	f.t.Helper()
	var sids []string
	require.NoError(f.t, f.app.Deps().DB.Raw("SELECT sid FROM ga_session WHERE user_id = ? AND revoked_at IS NULL", userID).Scan(&sids).Error)
	require.Len(f.t, sids, 1)
	return sids[0]
}

// 登录日志和操作日志都记会话 ID，按会话、按 IP 能查出同一条线索（D-032 第 6 条）。
func TestAudit_LogsCarrySessionID(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	sid := f.sidOf(adminID)
	require.Len(t, sid, 32)

	// 失败的登录没有会话
	fail := f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "wrong"})
	require.NotEqual(t, 0, fail.env.Code)

	r := f.do(admin, "GET", "/system/login-logs?username=root", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 2, r.data()["total"])
	for _, v := range r.data()["list"].([]any) {
		row := v.(map[string]any)
		if row["success"] == true {
			require.Equal(t, sid, row["sessionId"])
		} else {
			require.Equal(t, "", row["sessionId"])
		}
	}
	r = f.do(admin, "GET", "/system/login-logs?sessionId="+sid, nil)
	require.EqualValues(t, 1, r.data()["total"])

	// 操作日志：首次改密那一条带着这个会话
	rows, _, err := oplog.List(f.app.Context(context.Background()), oplog.Filter{Action: app.OpChangePassword}, 1, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, sid, rows[0].SessionID)

	r = f.do(admin, "GET", "/system/operation-logs?sessionId="+sid, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 3, r.data()["total"], "首次改密 + 前面两次查看登录日志（查看留痕）")
	r = f.do(admin, "GET", "/system/operation-logs?sessionId=00000000000000000000000000000000", nil)
	require.EqualValues(t, 0, r.data()["total"])
	r = f.do(admin, "GET", "/system/operation-logs?ip=203.0.113.10", nil)
	require.EqualValues(t, 5, r.data()["total"], "再加上前面两次查看操作日志")
	r = f.do(admin, "GET", "/system/operation-logs?ip=198.51.100.1", nil)
	require.EqualValues(t, 0, r.data()["total"])
}

// 操作日志、登录日志挂在运维中心下（D-032 第 1 条）。
func TestAudit_OpsMenu(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	menus := f.meMenus(admin)
	require.Contains(t, menus, "ops")
	require.Equal(t, "ops", menus["system-oplog"].parent)
	require.Equal(t, "ops", menus["system-loginlog"].parent)
	require.Equal(t, "/ops/operation-logs", menus["system-oplog"].node["path"])
}

// flushAudit 把审计记录里攒着的次数写进库（关停时框架也会这么做）。
func (f *fixture) flushAudit() {
	f.t.Helper()
	require.NoError(f.t, f.app.Stop(f.app.Context(context.Background())))
}

func findRow(t *testing.T, list []any, key, want string) map[string]any {
	t.Helper()
	for _, v := range list {
		row := v.(map[string]any)
		if row[key] == want {
			return row
		}
	}
	t.Fatalf("没有 %s=%s 的行", key, want)
	return nil
}

// 错误日志（D-032 第 3 条）：5xx 和 panic 按指纹合并成一行、累加次数；错误文本去掉具体值和凭据；
// 4xx 不记；列表不带调用栈，调用栈要单独的敏感权限；响应里不泄露任何细节。
func TestAudit_ErrorLog(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	pr := f.app.Router().Portal("platform")
	pr.GET("/test/boom", rbac.AuthOnly(), func(c *gin.Context) {
		s := []int{}
		_ = s[len(c.Query("x"))+3] // 运行时 panic：下标越界
	})
	n := 0
	pr.GET("/test/down", rbac.AuthOnly(), func(c *gin.Context) {
		n++
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(fmt.Errorf("dial tcp ga:s3cret@tcp(10.0.0.%d:3306): refused for 'alice'", n)))
	})
	pr.GET("/test/bad", rbac.AuthOnly(), func(c *gin.Context) { httpx.Fail(c, httpx.ErrNotFound) })

	for range 3 {
		r := f.do(admin, "GET", "/test/boom", nil)
		require.Equal(t, 500, r.rec.Code)
		require.Equal(t, httpx.CodeInternal, r.env.Code)
		require.NotContains(t, r.rec.Body.String(), "out of range")
		require.NotContains(t, r.rec.Body.String(), "audit_test.go")
	}
	for range 4 {
		r := f.do(admin, "GET", "/test/down", nil)
		require.Equal(t, 503, r.rec.Code)
		require.NotContains(t, r.rec.Body.String(), "s3cret")
	}
	for range 2 {
		require.Equal(t, 404, f.do(admin, "GET", "/test/bad", nil).rec.Code)
	}
	f.flushAudit()

	r := f.do(admin, "GET", "/system/error-logs", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 2, r.data()["total"], "4xx 不记，同一类错误只占一行")
	list := r.data()["list"].([]any)
	boom := findRow(t, list, "route", base+"/test/boom")
	require.Equal(t, "panic", boom["kind"])
	require.EqualValues(t, 3, boom["count"])
	require.Equal(t, "root", boom["lastUsername"])
	require.NotEmpty(t, boom["lastRequestId"])
	require.Len(t, boom["lastSessionId"], 32)
	require.Contains(t, boom["message"], "index out of range")
	require.NotContains(t, boom, "stack", "列表不带调用栈")

	down := findRow(t, list, "route", base+"/test/down")
	require.Equal(t, "error", down["kind"])
	require.EqualValues(t, 4, down["count"], "只有 IP 不同的同一类错误合并成一行")
	require.EqualValues(t, httpx.CodeUnavailable, down["code"])
	msg := down["message"].(string)
	require.NotContains(t, msg, "s3cret")
	require.NotContains(t, msg, "alice")
	require.Contains(t, msg, "refused for ?")

	// 过滤
	r = f.do(admin, "GET", "/system/error-logs?kind=panic", nil)
	require.EqualValues(t, 1, r.data()["total"])
	r = f.do(admin, "GET", "/system/error-logs?route="+base+"/test/d", nil)
	require.EqualValues(t, 1, r.data()["total"])

	// 详情带调用栈：只有文件名和行号的最后三级，没有构建机目录
	id := uint64(boom["id"].(float64))
	r = f.do(admin, "GET", fmt.Sprintf("/system/error-logs/%d", id), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	stack := r.data()["stack"].(string)
	require.Contains(t, stack, "system/audit_test.go:")
	require.NotContains(t, stack, "/home/")
	require.False(t, strings.HasPrefix(stack, "goroutine "), "去掉了第一行的协程编号")
	r = f.do(admin, "GET", "/system/error-logs/999999", nil)
	require.Equal(t, 404, r.rec.Code)

	// 权限：只有列表权限的看不到调用栈；没有权限的列表也看不到
	roleID := f.createRole(admin, "errviewer", []string{system.PermErrorLogList})
	_, viewer := f.createUser(admin, "errviewer", []uint64{roleID})
	require.Equal(t, 0, f.do(viewer, "GET", "/system/error-logs", nil).env.Code)
	require.Equal(t, 403, f.do(viewer, "GET", fmt.Sprintf("/system/error-logs/%d", id), nil).rec.Code)
	roleID2 := f.createRole(admin, "nobody", []string{system.PermOplogList})
	_, nobody := f.createUser(admin, "nobody", []uint64{roleID2})
	require.Equal(t, 403, f.do(nobody, "GET", "/system/error-logs", nil).rec.Code)

	// 两个权限都是敏感权限：只有超管能授出
	for _, code := range []string{system.PermErrorLogList, system.PermErrorLogDetail} {
		p, ok := f.app.Deps().Perms.Perm("platform", code)
		require.True(t, ok, code)
		require.True(t, p.Sensitive, code)
	}
}

// 95. 错误日志按端隔离（D-050）：本端的管理员只看本端和不属于任何端的错误，别的端的列表里没有、详情当作不存在；
// 没登录的请求按路由模板归到端。
func TestAudit_82_ErrorLogIsolatedByPortal(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	f.app.Router().Portal("platform").GET("/test/pubboom", rbac.Public(), func(c *gin.Context) {
		httpx.Fail(c, httpx.ErrUnavailable.WithCause(fmt.Errorf("db down")))
	})
	require.Equal(t, 503, f.do("", "GET", "/test/pubboom", nil).rec.Code)
	f.flushAudit()
	now := time.Now().UTC()
	for _, row := range []struct{ fp, portal, route string }{
		{strings.Repeat("a", 32), "other", "/api/other/v1/boom"},
		{strings.Repeat("b", 32), "", ""},
	} {
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_error_log (fingerprint, kind, portal, route, message, count, first_at, last_at, last_username) VALUES (?, 'error', ?, ?, 'x', 1, ?, ?, 'someone')",
			row.fp, row.portal, row.route, now, now).Error)
	}
	var otherID uint64
	require.NoError(t, f.gdb.Raw("SELECT id FROM ga_error_log WHERE portal = 'other'").Scan(&otherID).Error)

	r := f.do(admin, "GET", "/system/error-logs", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	list := r.data()["list"].([]any)
	pub := findRow(t, list, "route", base+"/test/pubboom")
	require.Equal(t, "platform", pub["portal"], "没登录的请求按路由归到端")
	findRow(t, list, "fingerprint", strings.Repeat("b", 32))
	for _, v := range list {
		require.NotEqual(t, "other", v.(map[string]any)["portal"], "别的端的错误不在列表里")
	}
	require.EqualValues(t, 2, r.data()["total"])
	require.Equal(t, 404, f.do(admin, "GET", fmt.Sprintf("/system/error-logs/%d", otherID), nil).rec.Code)
}

// 越权被拒写进安全事件（规范 §13.2 第 46 条）：守卫拒绝的带权限码；业务层拒绝的（2001）同样记；
// 刷接口时合并成一行；登录限流、命令行建管理员也记；查看安全事件要敏感权限 system:secevent:list。
func TestAudit_46_ForbiddenAndOtherEvents(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	roleID := f.createRole(admin, "assigner", []string{system.PermUserList, system.PermUserAssignRole})
	bobID, bob := f.createUser(admin, "bob", []uint64{roleID})
	bobSID := f.sidOf(bobID)

	// 守卫拒绝：bob 没有角色列表权限，连刷 50 次
	for range 50 {
		require.Equal(t, 403, f.do(bob, "GET", "/system/roles", nil).rec.Code)
	}
	// 业务层拒绝：非超管不能分配超管角色
	r := f.do(bob, "PUT", fmt.Sprintf("/system/users/%d/roles", bobID), gin.H{"roleIds": []uint64{f.superRoleID()}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeForbidden, r.env.Code)
	// 登录限流：同一 IP 一分钟 20 次
	for range 21 {
		f.do("", "POST", "/auth/login", gin.H{"username": "mallory", "password": "x"})
	}
	f.flushAudit()

	r = f.do(admin, "GET", "/system/security-events?kind=forbidden", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 2, r.data()["total"])
	list := r.data()["list"].([]any)
	guard := findRow(t, list, "detail", system.PermRoleList)
	require.EqualValues(t, 50, guard["count"], "同一来源同一分钟只占一行")
	require.EqualValues(t, bobID, guard["userId"])
	require.Equal(t, "bob", guard["username"])
	require.Equal(t, bobSID, guard["sessionId"])
	require.Equal(t, "GET", guard["method"])
	require.Equal(t, base+"/system/roles", guard["path"])
	require.EqualValues(t, 2, guard["level"])
	svc := findRow(t, list, "path", base+"/system/users/:id/roles")
	require.Equal(t, "", svc["detail"])
	require.EqualValues(t, 1, svc["count"])

	r = f.do(admin, "GET", "/system/security-events?kind=login_rate_limited", nil)
	require.EqualValues(t, 1, r.data()["total"])
	row := r.data()["list"].([]any)[0].(map[string]any)
	require.Equal(t, "mallory", row["username"])
	require.Equal(t, "203.0.113.10", row["ip"])

	// 命令行建管理员
	r = f.do(admin, "GET", "/system/security-events?kind=cli", nil)
	require.EqualValues(t, 1, r.data()["total"])
	row = r.data()["list"].([]any)[0].(map[string]any)
	require.Equal(t, "root", row["username"])
	require.Equal(t, "admin create", row["detail"])

	// 过滤：会话、IP、用户、级别
	r = f.do(admin, "GET", "/system/security-events?sessionId="+bobSID, nil)
	require.EqualValues(t, 2, r.data()["total"])
	r = f.do(admin, "GET", fmt.Sprintf("/system/security-events?userId=%d", bobID), nil)
	require.EqualValues(t, 2, r.data()["total"])
	r = f.do(admin, "GET", "/system/security-events?ip=198.51.100.9", nil)
	require.EqualValues(t, 0, r.data()["total"])
	r = f.do(admin, "GET", "/system/security-events?level=3", nil)
	require.EqualValues(t, 0, r.data()["total"])

	// 权限：bob 看不到；它是敏感权限
	require.Equal(t, 403, f.do(bob, "GET", "/system/security-events", nil).rec.Code)
	p, ok := f.app.Deps().Perms.Perm("platform", system.PermSecEventList)
	require.True(t, ok)
	require.True(t, p.Sensitive)
}

// 调查时间线（规范 §13.2 第 47 条）：按会话、用户、IP 把登录、操作、安全事件串成一条线；
// 恰好给一个查询对象；能往前翻页；要敏感权限 system:audit:timeline。
func TestAudit_47_Timeline(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	roleID := f.createRole(admin, "viewer", []string{system.PermUserList})
	bobID, bob := f.createUser(admin, "bob", []uint64{roleID}) // 登录 + 首次改密
	sid := f.sidOf(bobID)
	require.Equal(t, 403, f.do(bob, "GET", "/system/roles", nil).rec.Code) // 越权
	f.do("", "POST", "/auth/login", gin.H{"username": "bob", "password": "wrong"})
	f.flushAudit()

	at := func(it any) time.Time {
		v, err := time.Parse(time.RFC3339Nano, it.(map[string]any)["at"].(string))
		require.NoError(t, err)
		return v
	}
	types := func(items []any) map[string]int {
		out := map[string]int{}
		for i, v := range items {
			out[v.(map[string]any)["type"].(string)]++
			if i > 0 {
				require.False(t, at(v).After(at(items[i-1])), "按时间倒序")
			}
		}
		return out
	}

	// 按会话：这个会话的登录、改密、越权
	r := f.do(admin, "GET", "/system/audit/timeline?sessionId="+sid, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	items := r.data()["items"].([]any)
	require.Equal(t, map[string]int{"login": 1, "operation": 1, "security": 1}, types(items))
	require.Equal(t, false, r.data()["more"])
	for _, v := range items {
		it := v.(map[string]any)
		require.Equal(t, sid, it["sessionId"])
		switch it["type"] {
		case "login":
			require.Equal(t, true, it["success"])
		case "operation":
			require.Equal(t, "auth.password", it["action"])
			require.NotContains(t, it, "body", "时间线不带请求体")
		case "security":
			require.Equal(t, "forbidden", it["kind"])
			require.Equal(t, system.PermRoleList, it["detail"])
		}
	}

	// 按用户：另外多一次失败的登录（没有会话）
	r = f.do(admin, "GET", fmt.Sprintf("/system/audit/timeline?userId=%d", bobID), nil)
	require.Equal(t, map[string]int{"login": 2, "operation": 1, "security": 1}, types(r.data()["items"].([]any)))

	// 按 IP：root 和 bob 的都在
	r = f.do(admin, "GET", "/system/audit/timeline?ip=203.0.113.10", nil)
	all := types(r.data()["items"].([]any))
	require.GreaterOrEqual(t, all["login"], 3)
	require.GreaterOrEqual(t, all["operation"], 4)

	// 翻页：一页 2 条翻到底，和一次取全部的结果一模一样（同一毫秒的记录不漏不重）
	r = f.do(admin, "GET", "/system/audit/timeline?ip=203.0.113.10&limit=200", nil)
	want := r.data()["items"].([]any)
	var got []any
	next := ""
	for range 100 {
		r = f.do(admin, "GET", "/system/audit/timeline?ip=203.0.113.10&limit=2&cursor="+url.QueryEscape(next), nil)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		got = append(got, r.data()["items"].([]any)...)
		if r.data()["more"] != true {
			break
		}
		next = r.data()["next"].(string)
	}
	// 翻页请求本身也记操作日志（查看留痕），比较时去掉查看时间线的记录
	key := func(v any) string { m := v.(map[string]any); return fmt.Sprint(m["type"], m["id"]) }
	strip := func(list []any) []string {
		var out []string
		for _, v := range list {
			if v.(map[string]any)["action"] != system.OpViewTimeline {
				out = append(out, key(v))
			}
		}
		return out
	}
	require.Equal(t, strip(want), strip(got))
	r = f.do(admin, "GET", "/system/audit/timeline?ip=203.0.113.10&cursor=garbage", nil)
	require.Equal(t, httpx.CodeValidation, r.env.Code)

	// 查询对象必须恰好一个
	for _, qs := range []string{"", "?ip=1.2.3.4&sessionId=" + sid} {
		r = f.do(admin, "GET", "/system/audit/timeline"+qs, nil)
		require.Equal(t, httpx.CodeValidation, r.env.Code, qs)
	}

	// 权限
	require.Equal(t, 403, f.do(bob, "GET", "/system/audit/timeline?sessionId="+sid, nil).rec.Code)
	p, ok := f.app.Deps().Perms.Perm("platform", system.PermAuditTimeline)
	require.True(t, ok)
	require.True(t, p.Sensitive)
}

// 查看审计数据本身留痕（规范 §13.2 第 48 条）：六个查看接口各记一条操作日志，带查询条件；
// 没有权限被拒的查看不算查看，记在安全事件里。
func TestAudit_48_ViewsAreRecorded(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	sid := f.sidOf(adminID)
	paths := map[string]string{
		system.OpViewOplog:          "/system/operation-logs?username=bob",
		system.OpViewLoginLog:       "/system/login-logs?ip=198.51.100.7",
		system.OpViewErrorLog:       "/system/error-logs?kind=panic",
		system.OpViewSecEvent:       "/system/security-events?level=3",
		system.OpViewTimeline:       "/system/audit/timeline?sessionId=" + sid,
		system.OpViewErrorLogDetail: "/system/error-logs/123",
	}
	for _, p := range paths {
		f.do(admin, "GET", p, nil)
	}
	for action, p := range paths {
		rows := f.oplogs(action)
		require.Len(t, rows, 1, action)
		e := rows[0]
		require.Equal(t, adminID, e.UserID)
		require.Equal(t, sid, e.SessionID)
		require.Equal(t, "GET", e.Method)
		if i := strings.Index(p, "?"); i > 0 {
			require.Equal(t, p[i+1:], e.Query, action)
		}
	}
	// 详情不存在也记（查看的尝试本身就是线索），结果是 404
	require.Equal(t, 404, f.oplogs(system.OpViewErrorLogDetail)[0].HTTPStatus)

	// 所有日志资源只有读取路由：不能改、不能删（D-032 第 2 条）
	n := 0
	for _, rt := range f.app.Routes() {
		if strings.Contains(rt.Path, "-logs") || strings.Contains(rt.Path, "/security-events") || strings.Contains(rt.Path, "/audit/") {
			require.Equal(t, "GET", rt.Method, rt.Path)
			n++
		}
	}
	require.Equal(t, 6, n)

	// 没有权限的查看：不是操作日志，是安全事件
	roleID := f.createRole(admin, "none", []string{system.PermUserList})
	_, bob := f.createUser(admin, "bob", []uint64{roleID})
	require.Equal(t, 403, f.do(bob, "GET", "/system/audit/timeline?ip=1.2.3.4", nil).rec.Code)
	require.Len(t, f.oplogs(system.OpViewTimeline), 1)
	f.flushAudit()
	r := f.do(admin, "GET", "/system/security-events?username=bob", nil)
	require.EqualValues(t, 1, r.data()["total"])
	require.Equal(t, system.PermAuditTimeline, r.data()["list"].([]any)[0].(map[string]any)["detail"])
}

// 同一毫秒里的多条记录翻页时不漏不重（规范 §13.2 第 47 条）。
func TestAudit_47b_TimelineCursorSameMillisecond(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	at := time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC)
	for i := range 3 {
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_login_log (portal, username, user_id, success, reason, ip, created_at) VALUES ('platform', ?, 0, 0, 'no_account', '192.0.2.9', ?)", fmt.Sprintf("u%d", i), at).Error)
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_operation_log (portal, action, method, path, ip, created_at) VALUES ('platform', 'x.y', 'GET', '/p', '192.0.2.9', ?)", at).Error)
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_security_event (dedup_key, window_start, portal, kind, ip, count, first_at, last_at) VALUES (?, ?, 'platform', 'forbidden', '192.0.2.9', 1, ?, ?)", fmt.Sprintf("%032d", i), at, at, at).Error)
	}
	seen := map[string]bool{}
	next := ""
	for range 20 {
		r := f.do(admin, "GET", "/system/audit/timeline?ip=192.0.2.9&limit=2&cursor="+url.QueryEscape(next), nil)
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		for _, v := range r.data()["items"].([]any) {
			m := v.(map[string]any)
			k := fmt.Sprint(m["type"], m["id"])
			require.False(t, seen[k], "重复：%s", k)
			seen[k] = true
		}
		if r.data()["more"] != true {
			break
		}
		next = r.data()["next"].(string)
	}
	require.Len(t, seen, 9)
}

// 登录类事件不按账号拆行：换着账号刷也只占一行（D-032）；不属于任何端的事件也看得到。
func TestAudit_46b_LoginFloodAndGlobalEvents(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	for i := range 40 {
		f.do("", "POST", "/auth/login", gin.H{"username": fmt.Sprintf("m%d", i), "password": "x"})
	}
	require.NoError(t, f.app.Deps().Audit.RecordSecurity(f.app.Context(context.Background()), audit.NewSecurityEvent{Kind: system.KindCLI, Detail: "rbac prune: 3"}))
	f.flushAudit()
	r := f.do(admin, "GET", "/system/security-events?kind=login_rate_limited", nil)
	require.EqualValues(t, 1, r.data()["total"])
	// 同一 IP 一分钟 20 次；fixture 里 root 已经从这个 IP 登录过一次，所以 40 次里有 21 次被限流
	require.EqualValues(t, 21, r.data()["list"].([]any)[0].(map[string]any)["count"])
	r = f.do(admin, "GET", "/system/security-events?kind=cli", nil)
	require.EqualValues(t, 2, r.data()["total"], "建管理员 + 不属于任何端的清理策略")
}
