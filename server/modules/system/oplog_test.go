package system_test

// 规范 §13.2 第 19 条：操作日志里的密码、密钥字段已脱敏。顺带覆盖操作日志、登录日志查询接口。

import (
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

func (f *fixture) oplogs(action string) []oplog.Entry {
	f.t.Helper()
	rows, _, err := oplog.List(f.app.Context(context.Background()), oplog.Filter{Action: action}, 1, 50)
	require.NoError(f.t, err)
	return rows
}

func TestOplog_19_SecretsAreMasked(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")

	// 创建用户：请求体里的密码、查询串里的 token 都不能进日志
	req := httptest.NewRequest("POST", base+"/system/users?token=leak-token&note=ok", strings.NewReader(`{"username":"bob","password":"Very-Secret-1","displayName":"Bob"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+admin)
	req.Header.Set("User-Agent", "oplog-test")
	req.RemoteAddr = "203.0.113.10:5000"
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	// 响应里的 initialPassword 不能进日志（不记响应体）
	require.Contains(t, rec.Body.String(), `"user"`)

	rows := f.oplogs(system.OpUserCreate)
	require.Len(t, rows, 1)
	e := rows[0]
	require.Equal(t, "platform", e.Portal)
	require.Equal(t, adminID, e.UserID)
	require.Equal(t, "root", e.Username)
	require.Equal(t, "POST", e.Method)
	require.Equal(t, base+"/system/users", e.Path)
	require.Equal(t, 200, e.HTTPStatus)
	require.Equal(t, 0, e.Code)
	require.Equal(t, "", e.Error)
	require.Equal(t, "203.0.113.10", e.IP)
	require.Equal(t, "oplog-test", e.UserAgent)
	require.NotEmpty(t, e.RequestID)
	require.Contains(t, e.Body, `"username":"bob"`)
	require.Contains(t, e.Body, `"password":"***"`)
	require.NotContains(t, e.Body, "Very-Secret-1")
	require.Equal(t, "note=ok&token=***", e.Query)
	require.NotContains(t, e.Query, "leak-token")

	// 修改密码：/auth/password 由框架挂日志，新旧密码都脱敏
	r := f.do(admin, "PUT", "/auth/password", gin.H{"oldPassword": "changed-pass-9", "newPassword": "changed-pass-10"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	rows = f.oplogs(app.OpChangePassword)
	require.Len(t, rows, 2, "fixture 的首次改密 + 这次") // admin() 里改过一次
	for _, e := range rows {
		require.Contains(t, e.Body, `"oldPassword":"***"`)
		require.Contains(t, e.Body, `"newPassword":"***"`)
		require.NotContains(t, e.Body, "changed-pass")
	}

	// 失败的请求也记，带错误码和文案
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "bob", "password": "Very-Secret-2"})
	require.NotEqual(t, 0, r.env.Code)
	rows = f.oplogs(system.OpUserCreate)
	require.Len(t, rows, 2)
	failed := rows[0] // 时间倒序
	require.Equal(t, r.env.Code, failed.Code)
	require.Equal(t, r.env.Msg, failed.Error)
	require.NotContains(t, failed.Body, "Very-Secret-2")

	// 不挂 Record 的路由不记：查询用户列表
	f.do(admin, "GET", "/system/users", nil)
	all, total, err := oplog.List(f.app.Context(context.Background()), oplog.Filter{Portal: "platform"}, 1, 100)
	require.NoError(t, err)
	require.EqualValues(t, 4, total)
	for _, e := range all {
		require.NotEqual(t, "GET", e.Method)
	}
}

// 规范 §13.2 第 18 条的补充：挂了操作日志的路由，没带 Content-Length 的超限请求体同样被 413 拒绝，
// 且日志里不会存下超限的内容。
func TestOplog_18_ChunkedOversizedBodyIs413(t *testing.T) {
	f := newFixtureWith(t, func(cfg *conf.Config) { cfg.Server.MaxBodyBytes = 300 })
	admin, _ := f.admin("root")

	big := `{"username":"bob","password":"Very-Secret-1","remark":"` + strings.Repeat("x", 400) + `"}`
	req := httptest.NewRequest("POST", base+"/system/users", io.NopCloser(strings.NewReader(big))) // 非 *strings.Reader：不设 Content-Length
	req.ContentLength = -1
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+admin)
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, 413, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"code":4013`)

	rows := f.oplogs(system.OpUserCreate)
	require.Len(t, rows, 1)
	require.Equal(t, 413, rows[0].HTTPStatus)
	require.Equal(t, 4013, rows[0].Code)
	require.NotContains(t, rows[0].Body, "Very-Secret-1")
	require.NotContains(t, rows[0].Body, "xxxx")
	require.Less(t, len(rows[0].Body), 64)
}

func TestOplog_ListEndpointsAndPerms(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	roleID := f.createRole(admin, "viewer", []string{system.PermOplogList})
	_, viewer := f.createUser(admin, "viewer", []uint64{roleID})

	// 操作日志：按动作过滤、按用户过滤、只看失败
	r := f.do(admin, "GET", "/system/operation-logs?action="+system.OpUserCreate, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 1, r.data()["total"])
	r = f.do(admin, "GET", fmt.Sprintf("/system/operation-logs?userId=%d", adminID), nil)
	require.EqualValues(t, 5, r.data()["total"]) // 改密 + 建角色 + 授权 + 建用户 + 上一次查看操作日志（D-032 查看留痕）
	r = f.do(admin, "GET", "/system/operation-logs?failed=1", nil)
	require.EqualValues(t, 0, r.data()["total"])
	r = f.do(admin, "GET", "/system/operation-logs?from=2000-01-01T00:00:00Z&to=2001-01-01T00:00:00Z", nil)
	require.EqualValues(t, 0, r.data()["total"])

	// viewer 有 oplog 权限、没有 loginlog 权限
	r = f.do(viewer, "GET", "/system/operation-logs", nil)
	require.Equal(t, 0, r.env.Code)
	r = f.do(viewer, "GET", "/system/login-logs", nil)
	require.Equal(t, 403, r.rec.Code)

	// 登录日志：成功和失败各有记录
	fail := f.do("", "POST", "/auth/login", gin.H{"username": "viewer", "password": "wrong"})
	require.NotEqual(t, 0, fail.env.Code)
	r = f.do(admin, "GET", "/system/login-logs?username=view", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 2, r.data()["total"])
	r = f.do(admin, "GET", "/system/login-logs?username=viewer&success=0", nil)
	require.EqualValues(t, 1, r.data()["total"])
	list := r.data()["list"].([]any)
	row := list[0].(map[string]any)
	require.Equal(t, false, row["success"])
	require.Equal(t, "203.0.113.10", row["ip"])
	require.NotEmpty(t, row["reason"])
	r = f.do(admin, "GET", "/system/login-logs?success=1&ip=203.0.113.10", nil)
	require.EqualValues(t, 2, r.data()["total"]) // root、viewer 各登录一次

	// 日志只读：没有更新和删除路由
	for _, rt := range f.app.Routes() {
		if strings.Contains(rt.Path, "-logs") {
			require.Equal(t, "GET", rt.Method, rt.Path)
		}
	}
}

// 179（D-095）：JSON 接口只收声明成 JSON 的请求体。类型不对的请求不执行，操作日志里也不出现请求体里的密码。
func TestOplog_179_NonJSONContentTypeIsRejectedAndNotLogged(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	send := func(method, path, contentType, body string) (*httptest.ResponseRecorder, string) {
		req := httptest.NewRequest(method, base+path, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Authorization", "Bearer "+admin)
		req.RemoteAddr = "203.0.113.10:5000"
		rec := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(rec, req)
		return rec, rec.Body.String()
	}
	types := []string{"application/x-www-form-urlencoded", "text/plain", "", "multipart/form-data; boundary=x"}

	// 改密：不执行（旧密码仍然有效），日志里没有新旧密码
	for _, ct := range types {
		rec, out := send("PUT", "/auth/password", ct, `{"oldPassword":"changed-pass-9","newPassword":"Leaked-New-Pass-1"}`)
		require.Equal(t, 400, rec.Code, "%q: %s", ct, out)
		require.Contains(t, out, `"code":3002`, ct)
	}
	rows := f.oplogs(app.OpChangePassword)
	require.Len(t, rows, 1+len(types), "fixture 的首次改密 + 每个被拒绝的请求各一条")
	for _, e := range rows {
		require.NotContains(t, e.Body, "changed-pass-9")
		require.NotContains(t, e.Body, "Leaked-New-Pass-1")
		require.NotContains(t, e.Body, "changed-pass", "URL 编码之后的也不能有")
	}
	f.login("root", "changed-pass-9")

	// 建用户：不执行，日志里没有密码
	for _, ct := range types {
		rec, out := send("POST", "/system/users", ct, `{"username":"mallory","password":"Leaked-User-Pass-1"}`)
		require.Equal(t, 400, rec.Code, "%q: %s", ct, out)
		require.Contains(t, out, `"code":3002`, ct)
	}
	var n int64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT COUNT(*) FROM ga_user WHERE username = 'mallory'").Scan(&n).Error)
	require.Zero(t, n)
	rows = f.oplogs(system.OpUserCreate)
	require.Len(t, rows, len(types))
	for _, e := range rows {
		require.Equal(t, 400, e.HTTPStatus)
		require.Equal(t, 3002, e.Code)
		require.NotContains(t, e.Body, "Leaked-User-Pass-1")
		require.NotContains(t, e.Body, "Leaked")
	}

	// 授权这类写接口同样不执行：类型不对就查不到"做了什么"的情形不存在
	roleID := f.createRole(admin, "auditor", nil)
	rec, out := send("PUT", fmt.Sprintf("/system/roles/%d/perms", roleID), "text/plain", `{"codes":["system:user:list"]}`)
	require.Equal(t, 400, rec.Code, out)
	r := f.do(admin, "GET", fmt.Sprintf("/system/roles/%d/perms", roleID), nil)
	require.Equal(t, 0, r.env.Code)
	require.Empty(t, r.env.Data, "类型不对的授权没有生效")

	// 声明成 JSON 的照常执行
	rec, out = send("POST", "/system/users", "application/json; charset=utf-8", `{"username":"carol","password":"Fine-Pass-123"}`)
	require.Equal(t, 200, rec.Code, out)
	require.Contains(t, out, `"code":0`)
}
