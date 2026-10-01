package system_test

// 规范 §13.2 第 12–17 条：授权的反向测试。用真实的 system 模块、真实 MySQL 跑完整 HTTP 链路。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

const base = "/api/platform/v1"

type fixture struct {
	t   *testing.T
	app *app.App
	gdb *gorm.DB
}

type resp struct {
	rec *httptest.ResponseRecorder
	env httpx.Envelope
}

func (r resp) data() map[string]any { m, _ := r.env.Data.(map[string]any); return m }

func newFixture(t *testing.T) *fixture { return newFixtureWith(t, nil) }

// newFixtureWith 允许测试在建应用前调整配置。
func newFixtureWith(t *testing.T, tweak func(cfg *conf.Config)) *fixture {
	t.Helper()
	return newFixtureOpts(t, tweak)
}

// newFixtureOpts 另外可以传 app 的选项。
func newFixtureOpts(t *testing.T, tweak func(cfg *conf.Config), opts ...app.Option) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.WithDB(context.Background(), gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	cfg.Portals[conf.DefaultPortalCode] = p
	if tweak != nil {
		tweak(cfg)
	}
	opts = append([]app.Option{app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithBcryptCost(4)}, opts...)
	a, err := app.New(cfg, opts...)
	require.NoError(t, err)
	a.Register(system.Module())
	require.NoError(t, a.Setup())
	return &fixture{t: t, app: a, gdb: gdb}
}

// try 与 do 相同，但不断言，可以在并发 goroutine 里调用；解析失败时 code 为 -1。
func (f *fixture) try(token, method, path string, body any) (status, code int) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, base+path, rd)
	req.RemoteAddr = "203.0.113.10:5000"
	req.Header.Set("X-GA-Client", "web") // 和浏览器里的前端一样（登录要求它，D-051）
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		return rec.Code, -1
	}
	return rec.Code, env.Code
}

func (f *fixture) do(token, method, path string, body any) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, base+path, rd)
	req.RemoteAddr = "203.0.113.10:5000"
	req.Header.Set("X-GA-Client", "web") // 和浏览器里的前端一样（登录要求它，D-051）
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return resp{rec: rec, env: env}
}

func (f *fixture) login(username, password string) string {
	f.t.Helper()
	r := f.do("", "POST", "/auth/login", gin.H{"username": username, "password": password})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	tok, _ := r.data()["accessToken"].(string)
	require.NotEmpty(f.t, tok)
	return tok
}

// admin 创建超管并完成首次改密，返回可用的访问令牌。
func (f *fixture) admin(username string) (token string, id uint64) {
	f.t.Helper()
	ctx := f.app.Context(context.Background())
	pwd, err := system.CreateAdmin(ctx, f.app.Deps(), username)
	require.NoError(f.t, err)
	tok := f.login(username, pwd)
	r := f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": pwd, "newPassword": "changed-pass-9"})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	me := f.do(tok, "GET", "/auth/me", nil)
	uid := uint64(me.data()["user"].(map[string]any)["id"].(float64))
	return tok, uid
}

// createUser 由超管创建普通用户（给定密码），并分配角色，返回用户 ID 和登录后的令牌。
func (f *fixture) createUser(admin, username string, roleIDs []uint64) (uint64, string) {
	f.t.Helper()
	r := f.do(admin, "POST", "/system/users", gin.H{"username": username, "password": "user-pass-123", "roleIds": roleIDs})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	uid := uint64(r.data()["user"].(map[string]any)["id"].(float64))
	tok := f.login(username, "user-pass-123")
	// 首次登录必须改密
	c := f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-123", "newPassword": "user-pass-456"})
	require.Equal(f.t, 0, c.env.Code, c.rec.Body.String())
	return uid, tok
}

// createRole 由超管建角色并授权。这些测试写在按部门的数据权限（D-039）之前，前提是"有用户管理权限就能管所有人"，
// 所以这里把用户数据资源的范围明确设为全部；数据范围本身的测试在 datascope_test.go，自己设范围。
func (f *fixture) createRole(admin, code string, perms []string) uint64 {
	f.t.Helper()
	r := f.do(admin, "POST", "/system/roles", gin.H{"code": code, "name": code})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	id := uint64(r.data()["id"].(float64))
	if len(perms) > 0 {
		g := f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", id), gin.H{"codes": perms, "dataScopes": gin.H{system.DataUser: "all"}})
		require.Equal(f.t, 0, g.env.Code, g.rec.Body.String())
	}
	return id
}

func (f *fixture) superRoleID() uint64 {
	f.t.Helper()
	r, err := f.app.Deps().RBAC.RoleByCode(f.app.Context(context.Background()), "platform", rbac.SuperRoleCode)
	require.NoError(f.t, err)
	return r.ID
}

// ============ 12. 无权限码的用户访问 Require 路由 403 ============

func TestRBAC_12_NoPermissionIs403(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	ops := f.createRole(admin, "ops", nil)
	_, bob := f.createUser(admin, "bob", []uint64{ops})

	r := f.do(bob, "GET", "/system/users", nil)
	require.Equal(t, 403, r.rec.Code)
	require.Equal(t, httpx.CodeForbidden, r.env.Code)

	require.Equal(t, 200, f.do(bob, "GET", "/system/options/users", nil).rec.Code, "AuthOnly 路由登录即可")
	require.Equal(t, 200, f.do(admin, "GET", "/system/users", nil).rec.Code, "超管跳过判定")

	me := f.do(bob, "GET", "/auth/me", nil)
	require.Equal(t, []any{}, me.data()["perms"])
	require.Equal(t, []any{}, me.data()["menus"], "没有任何权限时目录也不可见")
}

// ============ 13. 修改角色权限后下一次请求立即生效 ============

func TestRBAC_13_RoleChangeTakesEffectImmediately(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	ops := f.createRole(admin, "ops", nil)
	_, bob := f.createUser(admin, "bob", []uint64{ops})
	require.Equal(t, 403, f.do(bob, "GET", "/system/users", nil).rec.Code)

	g := f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{system.PermUserList}})
	require.Equal(t, 0, g.env.Code, g.rec.Body.String())
	require.Equal(t, 200, f.do(bob, "GET", "/system/users", nil).rec.Code, "授权后不需要重新登录")

	me := f.do(bob, "GET", "/auth/me", nil)
	require.Equal(t, []any{system.PermUserList}, me.data()["perms"])
	menus := me.data()["menus"].([]any)
	require.Len(t, menus, 1)
	sys := menus[0].(map[string]any)
	require.Equal(t, "system", sys["name"])
	children := sys["children"].([]any)
	require.Len(t, children, 1, "只看到有权限的子菜单")
	require.Equal(t, "system-user", children[0].(map[string]any)["name"])

	g = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{}})
	require.Equal(t, 0, g.env.Code)
	require.Equal(t, 403, f.do(bob, "GET", "/system/users", nil).rec.Code, "收回后立即失效")

	// 停用角色同样立即生效
	g = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{system.PermUserList}})
	require.Equal(t, 0, g.env.Code)
	require.Equal(t, 200, f.do(bob, "GET", "/system/users", nil).rec.Code)
	u := f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d", ops), gin.H{"name": "ops", "status": 0})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
	require.Equal(t, 403, f.do(bob, "GET", "/system/users", nil).rec.Code)
}

// ============ 14. 非超管不能授出敏感权限或自己没有的权限 ============

func TestRBAC_14_NonSuperGrantLimits(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	ops := f.createRole(admin, "ops", nil)
	mgr := f.createRole(admin, "mgr", []string{system.PermRoleGrant, system.PermRoleList, system.PermUserList, system.PermUserAssignRole})
	_, carol := f.createUser(admin, "carol", []uint64{mgr})
	bobID, _ := f.createUser(admin, "bob", []uint64{ops})

	// 敏感权限码
	r := f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{system.PermUserCreate}})
	require.Equal(t, 403, r.rec.Code)
	require.Contains(t, r.rec.Body.String(), "sensitive")
	// 自己没有的权限码
	r = f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{system.PermRoleDelete}})
	require.Equal(t, 403, r.rec.Code)
	require.Contains(t, r.rec.Body.String(), "do not have")
	// 未注册的权限码
	r = f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{"ghost:thing:do"}})
	require.Equal(t, 403, r.rec.Code)
	require.Contains(t, r.rec.Body.String(), "unknown")
	// 自己有的、非敏感的可以
	r = f.do(carol, "PUT", fmt.Sprintf("/system/roles/%d/perms", ops), gin.H{"codes": []string{system.PermUserList}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	// 什么都没变：ops 只拿到 user:list
	got := f.do(admin, "GET", fmt.Sprintf("/system/roles/%d/perms", ops), nil)
	require.Equal(t, []any{system.PermUserList}, got.env.Data)

	// 非超管不能把 super 角色分配给别人（即使有 assign-role 权限）
	r = f.do(carol, "PUT", fmt.Sprintf("/system/users/%d/roles", bobID), gin.H{"roleIds": []uint64{f.superRoleID()}})
	require.Equal(t, 403, r.rec.Code)
	require.Contains(t, r.rec.Body.String(), "super")
	// 超管可以
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", bobID), gin.H{"roleIds": []uint64{f.superRoleID()}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 超管角色本身不接受授权、不能停用、不能删除
	r = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", f.superRoleID()), gin.H{"codes": []string{system.PermUserList}})
	require.Equal(t, httpx.CodeLastSuper, r.env.Code)
	r = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d", f.superRoleID()), gin.H{"name": "x", "status": 0})
	require.Equal(t, httpx.CodeLastSuper, r.env.Code)
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/roles/%d", f.superRoleID()), nil)
	require.Equal(t, httpx.CodeLastSuper, r.env.Code)
}

// ============ 15. 不能停用或降级最后一个超管 ============

func TestRBAC_15_LastSuperIsProtected(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("admin")

	r := f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, httpx.CodeLastSuper, r.env.Code, "唯一超管不能去掉自己的超管角色")
	r = f.do(admin, "POST", fmt.Sprintf("/system/users/%d/status", adminID), gin.H{"status": 0})
	require.Equal(t, httpx.CodeValidation, r.env.Code, "不能停用自己的账号")

	// 第二个超管出现后，可以降级其中一个，但不能两个都降
	admin2, admin2ID := f.admin("admin2")
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", admin2ID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 403, f.do(admin2, "GET", "/system/users", nil).rec.Code, "被降级的账号立即失去超管能力")
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, httpx.CodeLastSuper, r.env.Code)

	// 恢复 admin2 为超管，再由 admin2 停用 admin：允许；反过来此时 admin2 是最后一个，不能停用
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", admin2ID), gin.H{"roleIds": []uint64{f.superRoleID()}})
	require.Equal(t, 0, r.env.Code)
	r = f.do(admin2, "POST", fmt.Sprintf("/system/users/%d/status", adminID), gin.H{"status": 0})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 401, f.do(admin, "GET", "/auth/me", nil).rec.Code, "被停用的账号会话立即失效")
	// 现在 admin2 是唯一启用的超管：再创建一个普通超管操作者验证保护
	admin3, _ := f.admin("admin3")
	r = f.do(admin3, "PUT", fmt.Sprintf("/system/users/%d/roles", admin2ID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, 0, r.env.Code, "有 admin3 在，admin2 可以降级")
	r = f.do(admin3, "POST", fmt.Sprintf("/system/users/%d/status", adminID), gin.H{"status": 1})
	require.Equal(t, 0, r.env.Code, "重新启用 admin")
}

// ============ 16. 每条端内路由都有守卫；Require 的权限码都已声明；Public 清单固定 ============

func TestRBAC_16_RouteTableIsGuarded(t *testing.T) {
	f := newFixture(t)
	routes := f.app.Routes()
	require.NotEmpty(t, routes)
	var public []string
	for _, r := range routes {
		require.NotEmpty(t, r.Guard, "%s %s 没有守卫", r.Method, r.Path)
		switch r.Guard {
		case rbac.GuardRequire:
			require.True(t, f.app.Deps().Perms.Has(r.Portal, r.Perm), "%s %s 需要的权限码 %q 未声明", r.Method, r.Path, r.Perm)
		case rbac.GuardPublic:
			public = append(public, r.Method+" "+r.Path)
		}
	}
	require.ElementsMatch(t, []string{
		"GET " + base + "/auth/captcha", "POST " + base + "/auth/login", "POST " + base + "/auth/refresh",
	}, public, "Public 路由只能是这三条（docs/api.md）")

	// 未登录访问任何非 Public 路由都是 401
	for _, r := range routes {
		if r.Guard == rbac.GuardPublic {
			continue
		}
		req := httptest.NewRequest(r.Method, r.Path, nil)
		rec := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(rec, req)
		require.Equal(t, 401, rec.Code, "%s %s", r.Method, r.Path)
	}
}

// ============ 17. 策略表里未注册的权限码不产生任何效果；prune 清理 ============

func TestRBAC_17_UnregisteredPolicyHasNoEffect(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	ops := f.createRole(admin, "ops", []string{system.PermUserList})
	bobID, bob := f.createUser(admin, "bob", []uint64{ops})

	// 直接往策略表插一条未注册的权限码，并让服务重载
	gdb := f.app.Deps().DB
	require.NoError(t, gdb.Exec("INSERT INTO ga_casbin_rule (ptype, v0, v1, v2) VALUES ('p', ?, 'platform', 'ghost:thing:do')", fmt.Sprintf("role:%d", ops)).Error)
	require.NoError(t, f.app.Deps().RBAC.Reload())

	ctx := f.app.Context(context.Background())
	ok, err := f.app.Deps().RBAC.Allowed(ctx, "platform", bobID, "ghost:thing:do")
	require.NoError(t, err)
	require.False(t, ok, "未注册的权限码判定为 false")
	me := f.do(bob, "GET", "/auth/me", nil)
	require.Equal(t, []any{system.PermUserList}, me.data()["perms"], "未注册的码不出现在 perms 里")

	n, err := f.app.Deps().RBAC.Prune(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	var left int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_casbin_rule WHERE v2 = 'ghost:thing:do'").Scan(&left).Error)
	require.EqualValues(t, 0, left)
	require.Equal(t, 200, f.do(bob, "GET", "/system/users", nil).rec.Code, "已注册的授权不受影响")
}

// ============ 附：用户管理流程与会话管理 ============

func TestSystem_UserLifecycle(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")

	// 创建（系统生成密码）
	r := f.do(admin, "POST", "/system/users", gin.H{"username": "dave", "displayName": "Dave", "email": "dave@example.com"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	initial, _ := r.data()["initialPassword"].(string)
	require.Len(t, initial, 20)
	daveID := uint64(r.data()["user"].(map[string]any)["id"].(float64))
	require.NotContains(t, r.rec.Body.String(), "password_hash")
	require.NotContains(t, r.rec.Body.String(), "passwordHash")

	// 重名
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "dave"})
	require.Equal(t, httpx.CodeConflict, r.env.Code)
	// 非法用户名
	r = f.do(admin, "POST", "/system/users", gin.H{"username": "Bad Name"})
	require.Equal(t, httpx.CodeValidation, r.env.Code)

	// 列表与搜索
	l := f.do(admin, "GET", "/system/users?keyword=dav&pageSize=10", nil)
	require.Equal(t, 0, l.env.Code)
	require.EqualValues(t, 1, l.data()["total"])

	// 更新资料
	u := f.do(admin, "PUT", fmt.Sprintf("/system/users/%d", daveID), gin.H{"displayName": "David", "phone": "123"})
	require.Equal(t, 0, u.env.Code, u.rec.Body.String())
	require.Equal(t, "David", u.data()["displayName"])

	// 登录 → 必须改密 → 重置密码后旧会话失效
	dave := f.login("dave", initial)
	require.Equal(t, 403, f.do(dave, "GET", "/system/options/users", nil).rec.Code)
	rp := f.do(admin, "POST", fmt.Sprintf("/system/users/%d/reset-password", daveID), nil)
	require.Equal(t, 0, rp.env.Code, rp.rec.Body.String())
	newPwd, _ := rp.data()["initialPassword"].(string)
	require.Len(t, newPwd, 20)
	require.Equal(t, 401, f.do(dave, "GET", "/auth/me", nil).rec.Code, "重置密码后旧会话失效")
	dave = f.login("dave", newPwd)

	// 会话列表与吊销
	s := f.do(admin, "GET", fmt.Sprintf("/system/sessions?userId=%d", daveID), nil)
	require.Equal(t, 0, s.env.Code, s.rec.Body.String())
	require.EqualValues(t, 1, s.data()["total"])
	sid := s.data()["list"].([]any)[0].(map[string]any)["sid"].(string)
	require.Equal(t, "dave", s.data()["list"].([]any)[0].(map[string]any)["username"])
	rv := f.do(admin, "POST", "/system/sessions/"+sid+"/revoke", nil)
	require.Equal(t, 0, rv.env.Code)
	require.Equal(t, 401, f.do(dave, "GET", "/auth/me", nil).rec.Code)

	// 停用 → 不能登录；启用 → 恢复
	require.Equal(t, 0, f.do(admin, "POST", fmt.Sprintf("/system/users/%d/status", daveID), gin.H{"status": 0}).env.Code)
	bad := f.do("", "POST", "/auth/login", gin.H{"username": "dave", "password": newPwd})
	require.Equal(t, httpx.CodeLoginFailed, bad.env.Code)
	require.Equal(t, 0, f.do(admin, "POST", fmt.Sprintf("/system/users/%d/status", daveID), gin.H{"status": 1}).env.Code)
	f.login("dave", newPwd)

	// 角色删除保护
	ops := f.createRole(admin, "ops", nil)
	require.Equal(t, 0, f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", daveID), gin.H{"roleIds": []uint64{ops}}).env.Code)
	require.Equal(t, httpx.CodeConflict, f.do(admin, "DELETE", fmt.Sprintf("/system/roles/%d", ops), nil).env.Code, "被引用的角色不能删")
	require.Equal(t, 0, f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", daveID), gin.H{"roleIds": []uint64{}}).env.Code)
	require.Equal(t, 0, f.do(admin, "DELETE", fmt.Sprintf("/system/roles/%d", ops), nil).env.Code)
	require.Equal(t, 404, f.do(admin, "GET", fmt.Sprintf("/system/roles/%d/perms", ops), nil).rec.Code)

	// 权限树
	tree := f.do(admin, "GET", "/system/perms/tree", nil)
	require.Equal(t, 0, tree.env.Code)
	require.Contains(t, tree.rec.Body.String(), `"sensitive":true`)
}

// ============ 14b. 非超管分配角色：不能借角色拿到自己没有的权限 ============

func TestRBAC_14b_AssignRolesBoundedByActorPerms(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	// ops 只有"查看用户"和"分配角色"（后者是敏感权限，由超管授予）
	assigner := f.createRole(admin, "assigner", []string{system.PermUserList, system.PermUserAssignRole})
	opsID, ops := f.createUser(admin, "ops", []uint64{assigner})
	// 三个候选角色：含敏感权限的、含 ops 没有的普通权限的、权限是 ops 子集的
	danger := f.createRole(admin, "danger", []string{system.PermUserCreate})
	viewer := f.createRole(admin, "viewer", []string{system.PermRoleList})
	mini := f.createRole(admin, "mini", []string{system.PermUserList})
	targetID, _ := f.createUser(admin, "target", nil)

	for _, tc := range []struct {
		name  string
		roles []uint64
		want  int
	}{
		{"给自己加含敏感权限的角色", []uint64{assigner, danger}, 403},
		{"给自己加含自己没有的权限的角色", []uint64{assigner, viewer}, 403},
		{"给别人加含敏感权限的角色", []uint64{danger}, 403},
		{"给自己加权限子集的角色", []uint64{assigner, mini}, 200},
		{"给别人加权限子集的角色", []uint64{mini}, 200},
	} {
		uid := opsID
		if strings.Contains(tc.name, "别人") {
			uid = targetID
		}
		r := f.do(ops, "PUT", fmt.Sprintf("/system/users/%d/roles", uid), gin.H{"roleIds": tc.roles})
		require.Equal(t, tc.want, r.rec.Code, "%s: %s", tc.name, r.rec.Body.String())
	}
	// 非超管不能动超管账号的角色（哪怕是清空）
	secondID, _ := f.createUser(admin, "second", []uint64{f.superRoleID()})
	r := f.do(ops, "PUT", fmt.Sprintf("/system/users/%d/roles", secondID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	// 超管不受限制
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", targetID), gin.H{"roleIds": []uint64{danger, viewer}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
}

// ============ 15b. 停用的超管不算数：唯一启用的超管不能卸掉自己的超管角色 ============

func TestRBAC_15b_DisabledSuperDoesNotCount(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	superID := f.superRoleID()
	secondID, _ := f.createUser(admin, "second", []uint64{superID})

	// 两个启用的超管：root 卸掉自己的超管角色是允许的（先验证规则本身），再装回去
	r := f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	second := f.login("second", "user-pass-456")
	r = f.do(second, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{superID}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	// 停用 second 之后，root 就是唯一可用的超管，卸角色和停用自己都必须被拒
	r = f.do(admin, "POST", fmt.Sprintf("/system/users/%d/status", secondID), gin.H{"status": 0})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, httpx.CodeLastSuper, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "POST", fmt.Sprintf("/system/users/%d/status", adminID), gin.H{"status": 0})
	require.NotEqual(t, 0, r.env.Code, "不能停用最后一个可用超管")
}

// ============ 18. 角色只能在自己的端里被查看和改动 ============

func TestRBAC_18_RoleOpsAreScopedToPortal(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	ctx := f.app.Context(context.Background())

	// 直接造一个属于别的端的角色，并给它一条授权记录；platform 端的超管对它做任何操作都应视为"不存在"。
	// 内核的角色表不对外暴露 gorm 模型（§16.2），这里直接写 SQL。
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_role (portal, code, name, status, created_at, updated_at) VALUES ('other', 'other-admin', 'other-admin', 1, NOW(3), NOW(3))").Error)
	var otherID uint64
	require.NoError(t, f.gdb.Raw("SELECT id FROM ga_role WHERE portal = 'other' AND code = 'other-admin'").Scan(&otherID).Error)
	require.NotZero(t, otherID)
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_casbin_rule (ptype, v0, v1, v2) VALUES ('p', ?, 'other', 'other:thing:read')", fmt.Sprintf("role:%d", otherID)).Error)

	r := f.do(admin, "GET", fmt.Sprintf("/system/roles/%d/perms", otherID), nil)
	require.Equal(t, httpx.CodeNotFound, r.env.Code, "看别的端的角色授权: %s", r.rec.Body.String())
	r = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d", otherID), gin.H{"name": "hijacked", "status": 0})
	require.Equal(t, httpx.CodeNotFound, r.env.Code, "改别的端的角色: %s", r.rec.Body.String())
	r = f.do(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", otherID), gin.H{"codes": []string{}})
	require.Equal(t, httpx.CodeNotFound, r.env.Code, "改别的端的角色授权: %s", r.rec.Body.String())
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/roles/%d", otherID), nil)
	require.Equal(t, httpx.CodeNotFound, r.env.Code, "删别的端的角色: %s", r.rec.Body.String())
	r = f.do(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{f.superRoleID(), otherID}})
	require.Equal(t, httpx.CodeValidation, r.env.Code, "把别的端的角色分配给本端用户: %s", r.rec.Body.String())

	// 别的端的角色和它的授权原封不动
	var got struct {
		Name   string
		Status int
	}
	require.NoError(t, f.gdb.Raw("SELECT name, status FROM ga_role WHERE id = ?", otherID).Scan(&got).Error)
	require.Equal(t, "other-admin", got.Name)
	require.Equal(t, 1, got.Status)
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_casbin_rule WHERE v0 = ? AND v1 = 'other'", fmt.Sprintf("role:%d", otherID)).Scan(&n).Error)
	require.EqualValues(t, 1, n)

	// 服务层的最后一道防线：身份来自 platform 端的操作者，不能以别的端的名义操作角色，哪怕模块代码把端传错
	svc := f.app.Deps().RBAC
	actor := auth.Principal{Portal: "platform", UserID: adminID, Super: true}
	_, err := svc.CreateRole(ctx, actor, "other", rbac.RoleInput{Code: "sneaky", Name: "sneaky", Status: 1})
	require.ErrorIs(t, err, httpx.ErrForbidden)
	require.ErrorIs(t, svc.GrantRolePerms(ctx, actor, "other", otherID, []string{"other:thing:read"}), httpx.ErrForbidden)
	require.ErrorIs(t, svc.DeleteRole(ctx, actor, "other", otherID), httpx.ErrForbidden)
	require.ErrorIs(t, svc.AssignUserRoles(ctx, actor, "other", adminID, []uint64{otherID}), httpx.ErrForbidden)
}

// ============ 15c. "最后一个可用超管"的判断在并发下也成立 ============

func TestRBAC_15c_LastSuperCheckIsRaceFree(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("root")
	superID := f.superRoleID()
	secondID, _ := f.createUser(admin, "second", []uint64{superID})
	second := f.login("second", "user-pass-456")
	ctx := f.app.Context(context.Background())

	enabledSupers := func() int {
		n := 0
		for _, id := range []uint64{adminID, secondID} {
			var u system.User
			require.NoError(t, db.From(ctx).First(&u, id).Error)
			roles, err := f.app.Deps().RBAC.UserRoles(ctx, "platform", id)
			require.NoError(t, err)
			if u.Status == system.StatusEnabled && len(roles) == 1 && roles[0].IsSuper {
				n++
			}
		}
		return n
	}
	reset := func() {
		// 两个都恢复成启用的超管（直接写库，绕开被测逻辑）
		require.NoError(t, db.From(ctx).Model(&system.User{}).Where("id IN ?", []uint64{adminID, secondID}).Update("status", system.StatusEnabled).Error)
		for _, id := range []uint64{adminID, secondID} {
			require.NoError(t, f.app.Deps().RBAC.AssignUserRoles(ctx, auth.Principal{Portal: "platform", Username: "test", Super: true}, "platform", id, []uint64{superID}))
			f.app.Deps().Auth.ForgetAccount("platform", id)
		}
		require.Equal(t, 2, enabledSupers())
	}

	// 互相停用、互相降级各跑若干轮：每一轮都只能有一个成功，另一个必须得到"最后一个超管"的拒绝
	for round := 0; round < 6; round++ {
		reset()
		var codes [2]int
		var wg sync.WaitGroup
		wg.Add(2)
		if round%2 == 0 {
			go func() {
				defer wg.Done()
				_, codes[0] = f.try(admin, "POST", fmt.Sprintf("/system/users/%d/status", secondID), gin.H{"status": 0})
			}()
			go func() {
				defer wg.Done()
				_, codes[1] = f.try(second, "POST", fmt.Sprintf("/system/users/%d/status", adminID), gin.H{"status": 0})
			}()
		} else {
			go func() {
				defer wg.Done()
				_, codes[0] = f.try(admin, "PUT", fmt.Sprintf("/system/users/%d/roles", secondID), gin.H{"roleIds": []uint64{}})
			}()
			go func() {
				defer wg.Done()
				_, codes[1] = f.try(second, "PUT", fmt.Sprintf("/system/users/%d/roles", adminID), gin.H{"roleIds": []uint64{}})
			}()
		}
		wg.Wait()
		require.Equal(t, 1, enabledSupers(), "第 %d 轮后必须还剩一个可用超管，结果码 %v", round, codes)
		// 一个成功、一个被拒。被拒的那个：它的请求在锁外排队时对方已经提交，锁内重新认定身份（D-045）——
		// 它已经不是可用的超管了：账号已被停用的整个请求作废（1004，D-046）；只是被降级的按普通账号处理，
		// 目标不在它的范围里（404）或者目标是超管账号（2001）。以前它还被当成超管，看到的是"最后一个超管"（4002）
		require.Contains(t, codes[:], 0, "第 %d 轮：必须有一个成功，结果码 %v", round, codes)
		require.ElementsMatch(t, []int{0, codes[0] + codes[1]}, codes[:], "第 %d 轮：只能有一个成功，结果码 %v", round, codes)
		require.Contains(t, []int{httpx.CodeLastSuper, httpx.CodeNotFound, httpx.CodeForbidden, httpx.CodeTokenInvalid}, codes[0]+codes[1], "第 %d 轮：被拒的原因，结果码 %v", round, codes)
		// 被停用/降级的那个令牌之后一定失效，重新登录一次给下一轮用
		f.app.Deps().Auth.ForgetAccount("platform", adminID)
		f.app.Deps().Auth.ForgetAccount("platform", secondID)
		reset()
		admin = f.login("root", "changed-pass-9")
		second = f.login("second", "user-pass-456")
	}
}

// ============ 密码重置与会话吊销是同一个事务 ============

func TestSystem_PasswordResetIsAtomicWithSessionRevoke(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("root")
	uid, tok := f.createUser(admin, "carol", nil)
	require.Equal(t, 0, f.do(tok, "GET", "/auth/me", nil).env.Code)

	// 故障注入：让会话表的任何 UPDATE 失败（吊销会话就是 UPDATE ga_session）
	var fail atomic.Bool
	require.NoError(t, f.gdb.Callback().Update().Before("gorm:update").Register("test:fail-session-update", func(tx *gorm.DB) {
		if fail.Load() && tx.Statement.Model != nil && reflect.TypeOf(tx.Statement.Model).String() == "*session.Session" {
			_ = tx.AddError(errors.New("injected: session update failed"))
		}
	}))
	fail.Store(true)
	r := f.do(admin, "POST", fmt.Sprintf("/system/users/%d/reset-password", uid), nil)
	require.NotEqual(t, 0, r.env.Code, "吊销失败时接口必须报错: %s", r.rec.Body.String())
	require.Nil(t, r.env.Data, "报错时不能把已生成的新密码返回出去")
	fail.Store(false)

	// 密码没有被换掉，会话也还在：整个操作回滚了
	require.Equal(t, 0, f.do(tok, "GET", "/auth/me", nil).env.Code, "旧会话仍然有效")
	f.login("carol", "user-pass-456")

	// 故障解除后正常重置：旧密码和旧会话都失效
	r = f.do(admin, "POST", fmt.Sprintf("/system/users/%d/reset-password", uid), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.NotEmpty(t, r.data()["initialPassword"])
	require.Equal(t, 401, f.do(tok, "GET", "/auth/me", nil).rec.Code)
	bad := f.do("", "POST", "/auth/login", gin.H{"username": "carol", "password": "user-pass-456"})
	require.Equal(t, httpx.CodeLoginFailed, bad.env.Code)
}

// ============ admin create 与创建用户接口用同一条登录名规则 ============

func TestSystem_CreateAdminNormalizesAndValidatesUsername(t *testing.T) {
	f := newFixture(t)
	ctx := f.app.Context(context.Background())
	for _, bad := range []string{"Root User", "ro", "1root", "root@x", strings.Repeat("a", 65)} {
		_, err := system.CreateAdmin(ctx, f.app.Deps(), bad)
		require.Error(t, err, bad)
	}
	pwd, err := system.CreateAdmin(ctx, f.app.Deps(), " ROOT2 ")
	require.NoError(t, err)
	tok := f.login("root2", pwd)
	require.NotEmpty(t, tok)
	_, err = system.CreateAdmin(ctx, f.app.Deps(), "root2")
	require.Error(t, err, "同名（归一化后）不能再建")
}
