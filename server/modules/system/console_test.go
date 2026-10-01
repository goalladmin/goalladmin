package system_test

// 规范 §13.2 第 42–43 条。控制台（D-030）：监控中心要单独的敏感权限，只能由超管授出；工作台要自己的权限码，只返回本人的数据。

import (
	"fmt"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// noMonitorCache 关掉监控缓存：同一个测试里先后两次读取要看到最新数据。缓存本身由 TestMonitor_CacheBoundsQueries 覆盖。
func noMonitorCache(cfg *conf.Config) {
	cfg.Monitor.ServerCache, cfg.Monitor.SecurityCache = 0, 0
}

func TestConsole_42_MonitorPermissionAndContent(t *testing.T) {
	f := newFixtureWith(t, noMonitorCache)
	admin, _ := f.admin("root")
	dash := f.createRole(admin, "dash", []string{system.PermDashboardView})
	_, dashUser := f.createUser(admin, "dash1", []uint64{dash})
	mon := f.createRole(admin, "mon", []string{system.PermMonitorView})
	_, monUser := f.createUser(admin, "mon1", []uint64{mon})
	f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "wrong-1"})

	// 只有数据中心权限的人看不到监控中心
	for _, path := range []string{"/system/monitor/security", "/system/monitor/server"} {
		require.Equal(t, 403, f.do(dashUser, "GET", path, nil).rec.Code, path)
		require.Equal(t, 401, f.do("", "GET", path, nil).rec.Code, path)
	}

	r := f.do(monUser, "GET", "/system/monitor/security", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	d := r.data()
	require.Len(t, d["hours"].([]any), 24)
	failed := d["failed"].([]any)
	require.EqualValues(t, 1, failed[23], "当前小时的一次失败")
	require.GreaterOrEqual(t, d["sessions"].(float64), float64(3))
	require.EqualValues(t, 0, d["lockedSessions"])
	require.Greater(t, d["operations"].(float64), float64(0))
	ips := d["failedIps"].([]any)
	require.Len(t, ips, 1)
	recent := d["recent"].([]any)
	require.NotEmpty(t, recent)
	first := recent[0].(map[string]any)
	require.NotContains(t, first, "userAgent", "精简视图不带 User-Agent")
	require.NotContains(t, first, "requestId")

	// 锁屏的会话计入
	require.Equal(t, 0, f.do(dashUser, "POST", "/auth/lock", nil).env.Code)
	d = f.do(monUser, "GET", "/system/monitor/security", nil).data()
	require.EqualValues(t, 1, d["lockedSessions"])

	r = f.do(monUser, "GET", "/system/monitor/server", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	s := r.data()
	rt := s["runtime"].(map[string]any)
	require.NotEmpty(t, rt["goVersion"])
	require.Positive(t, rt["goroutines"].(float64))
	require.Equal(t, true, s["enabled"])
	db := s["db"].(map[string]any)
	require.Equal(t, true, db["ok"])
	// 版本号只到大版本（D-031）：不带补丁号和发行版后缀
	require.Regexp(t, `^\d+\.\d+( MariaDB)?$`, db["version"])
	require.Regexp(t, `^go1\.\d+$`, rt["goVersion"])
	req := s["requests"].(map[string]any)
	minutes := req["minutes"].([]any)
	require.Len(t, minutes, 60)
	require.Positive(t, minutes[59].(map[string]any)["count"].(float64), "测试里的请求都记进了当前这一分钟")
	require.NotEmpty(t, req["routes"])

	// 监控权限是敏感权限：非超管即使自己有、也有授权权限，也不能授出
	mgr := f.createRole(admin, "mgr", []string{system.PermRoleGrant, system.PermRoleList, system.PermMonitorView})
	_, carol := f.createUser(admin, "carol", []uint64{mgr})
	r = f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", dash), gin.H{"codes": []string{system.PermMonitorView}})
	require.Equal(t, 403, r.rec.Code)
	require.Contains(t, r.rec.Body.String(), "sensitive")
}

func TestConsole_43_WorkspaceOnlyOwnData(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	none := f.createRole(admin, "none", nil)
	_, nobody := f.createUser(admin, "nobody", []uint64{none})
	require.Equal(t, 403, f.do(nobody, "GET", "/system/workspace", nil).rec.Code, "没有工作台权限")

	ws := f.createRole(admin, "ws", []string{system.PermWorkspaceView})
	_, alice := f.createUser(admin, "alice", []uint64{ws})
	f.do("", "POST", "/auth/login", gin.H{"username": "alice", "password": "wrong-1"})

	r := f.do(alice, "GET", "/system/workspace?tz=480", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	d := r.data()
	require.Equal(t, []any{"ws"}, d["roles"])
	sessions := d["sessions"].([]any)
	require.Len(t, sessions, 1)
	require.Equal(t, true, sessions[0].(map[string]any)["current"])
	require.NotContains(t, sessions[0].(map[string]any), "sid", "不返回会话 ID")
	// alice 只做过一次记日志的操作：首次登录后的改密
	require.EqualValues(t, 1, d["opsToday"])
	recent := d["recent"].([]any)
	require.Len(t, recent, 1)
	require.Equal(t, app.OpChangePassword, recent[0].(map[string]any)["action"])
	logins := d["logins"].([]any)
	require.Len(t, logins, 2, "一次失败、一次成功")
	require.Nil(t, d["lastLogin"], "只登录过这一次")

	// root 的工作台：自己的操作，看不到 alice 的登录记录
	r = f.do(admin, "GET", "/system/workspace?userId=999&username=alice", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	d = r.data()
	require.Greater(t, d["opsToday"].(float64), float64(0))
	require.NotEmpty(t, d["recent"])
	require.NotEmpty(t, d["topActions"])
	for _, x := range d["logins"].([]any) {
		require.Equal(t, true, x.(map[string]any)["success"], "root 自己的登录都是成功的，alice 那次失败不在里面")
	}

	// 再登录一次：新会话的"上次登录"是前一次成功登录，两个会话都列出、只有新的是当前
	alice2 := f.login("alice", "user-pass-456")
	d = f.do(alice2, "GET", "/system/workspace", nil).data()
	require.NotNil(t, d["lastLogin"])
	require.Equal(t, true, d["lastLogin"].(map[string]any)["success"])
	// 本次会话之后又有很多次登录（比"最近登录"列表还多，直接写日志表以免触发登录限流）：
	// 上次登录仍然找得到，且早于本次会话（D-043）
	var aliceID uint64
	require.NoError(t, f.gdb.Raw("SELECT id FROM ga_user WHERE username = 'alice'").Scan(&aliceID).Error)
	for i := 0; i < 10; i++ {
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_login_log (portal, username, user_id, session_id, success, created_at) VALUES ('platform', 'alice', ?, '', 1, DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? SECOND))", aliceID, i+1).Error)
	}
	d = f.do(alice2, "GET", "/system/workspace", nil).data()
	require.NotNil(t, d["lastLogin"], "上次登录不能因为后来的登录太多而丢失")
	lastAt, err := time.Parse(time.RFC3339Nano, d["lastLogin"].(map[string]any)["createdAt"].(string))
	require.NoError(t, err)
	sessAt, err := time.Parse(time.RFC3339Nano, sessionCreatedAt(d))
	require.NoError(t, err)
	require.True(t, lastAt.Before(sessAt), "上次登录必须早于本次会话：%s / %s", lastAt, sessAt)
	sessions = d["sessions"].([]any)
	require.Len(t, sessions, 2)
	current := 0
	for _, x := range sessions {
		if x.(map[string]any)["current"] == true {
			current++
		}
	}
	require.Equal(t, 1, current)

	// 会话多到本次会话不在工作台列出的那一页里（直接写会话表，比列表上限多），上次登录仍按本次登录的日志找得到（D-043）
	for i := 0; i < 25; i++ {
		require.NoError(t, f.gdb.Exec(`INSERT INTO ga_session (sid, portal, user_id, refresh_hash, rotated_at, expires_at, ip, user_agent, last_seen_at, created_at)
			VALUES (?, 'platform', ?, ?, UTC_TIMESTAMP(3), DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 DAY), '', '', DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MINUTE), DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MINUTE))`,
			fmt.Sprintf("%032d", i+1), aliceID, fmt.Sprintf("%064d", i+1), i+1, i+1).Error)
	}
	d = f.do(alice2, "GET", "/system/workspace", nil).data()
	require.Equal(t, "", sessionCreatedAt(d), "前提：本次会话已不在列出的那一页里")
	require.NotNil(t, d["lastLogin"], "上次登录不能依赖本次会话出现在会话列表里")
	lastAt, err = time.Parse(time.RFC3339Nano, d["lastLogin"].(map[string]any)["createdAt"].(string))
	require.NoError(t, err)
	require.True(t, lastAt.Before(sessAt), "上次登录仍然早于本次会话：%s / %s", lastAt, sessAt)

	require.Equal(t, 401, f.do("", "GET", "/system/workspace", nil).rec.Code)
}

// D-031：两类数据在进程内缓存，缓存期内不再查询——同时打开页面的人再多，负载也有上限。
func TestMonitor_CacheBoundsQueries(t *testing.T) {
	f := newFixture(t) // 默认配置：服务器状态 2 秒、安全统计 10 秒
	admin, _ := f.admin("root")
	mon := f.createRole(admin, "mon", []string{system.PermMonitorView})
	_, a := f.createUser(admin, "mon1", []uint64{mon})
	_, b := f.createUser(admin, "mon2", []uint64{mon})

	first := f.do(a, "GET", "/system/monitor/security", nil).data()
	require.EqualValues(t, 0, first["lockedSessions"])
	require.Equal(t, 0, f.do(b, "POST", "/auth/lock", nil).env.Code)
	// 缓存期内：另一个人来看，拿到的是同一份结果，锁屏数还没变
	require.Equal(t, first, f.do(admin, "GET", "/system/monitor/security", nil).data())

	s1 := f.do(a, "GET", "/system/monitor/server", nil).data()
	s2 := f.do(admin, "GET", "/system/monitor/server", nil).data()
	require.Equal(t, s1["now"], s2["now"], "2 秒内的第二次读取来自缓存")
}

// D-031：monitor.server 设为 false 时服务器状态整块关闭，只回 enabled=false；安全态势照常，权限照旧。
func TestMonitor_ServerSwitchOff(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) { cfg.Monitor.Server = false })
	admin, _ := f.admin("root")
	dash := f.createRole(admin, "dash", []string{system.PermDashboardView})
	_, dashUser := f.createUser(admin, "dash1", []uint64{dash})

	r := f.do(admin, "GET", "/system/monitor/server", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, map[string]any{"enabled": false}, r.data(), "除了 enabled 什么都不返回")
	require.Equal(t, 403, f.do(dashUser, "GET", "/system/monitor/server", nil).rec.Code)
	require.Equal(t, 401, f.do("", "GET", "/system/monitor/server", nil).rec.Code)

	r = f.do(admin, "GET", "/system/monitor/security", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Len(t, r.data()["hours"].([]any), 24)
}

// sessionCreatedAt 取工作台响应里当前会话的创建时间（RFC3339 字符串，可直接按字典序比较）。
func sessionCreatedAt(d map[string]any) string {
	for _, x := range d["sessions"].([]any) {
		m := x.(map[string]any)
		if m["current"] == true {
			return m["createdAt"].(string)
		}
	}
	return ""
}

// 工作台的"上次登录"：和本次登录同一毫秒的上一次登录也要找得到（时间只精确到毫秒，D-045）。
func TestConsole_43d_LastLoginInSameMillisecond(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	ws := f.createRole(admin, "ws", []string{system.PermWorkspaceView})
	aliceID, _ := f.createUser(admin, "alice", []uint64{ws})
	alice2 := f.login("alice", "user-pass-456")
	// 把 alice 所有成功登录的时间改成同一毫秒：ID 小的是上一次
	at := time.Now().UTC().Truncate(time.Millisecond)
	require.NoError(t, f.gdb.Exec("UPDATE ga_login_log SET created_at = ? WHERE user_id = ? AND success = 1", at, aliceID).Error)
	var ids []uint64
	require.NoError(t, f.gdb.Raw("SELECT id FROM ga_login_log WHERE user_id = ? AND success = 1 ORDER BY id", aliceID).Scan(&ids).Error)
	require.Len(t, ids, 2)

	r := f.do(alice2, "GET", "/system/workspace", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	last, ok := r.data()["lastLogin"].(map[string]any)
	require.True(t, ok, "同一毫秒的上一次登录不能漏掉: %s", r.rec.Body.String())
	require.Equal(t, true, last["success"])
}
