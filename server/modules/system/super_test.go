package system_test

// 超管账号保护（D-035）：重置密码只归超管、超管的密码只能在命令行重置、非超管不能对超管账号做任何写操作。

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// mySID 用令牌查某个用户当前的会话 ID（令牌要有看会话的权限，超管即可）。
func (f *fixture) mySID(token string, userID uint64) string {
	f.t.Helper()
	r := f.do(token, "GET", fmt.Sprintf("/system/sessions?userId=%d", userID), nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	list := r.data()["list"].([]any)
	require.NotEmpty(f.t, list)
	return list[0].(map[string]any)["sid"].(string)
}

// opsUser 建一个有全部用户和会话管理权限（但不是超管）的账号。
func (f *fixture) opsUser(admin string) (uint64, string) {
	f.t.Helper()
	role := f.createRole(admin, "ops", []string{
		system.PermUserList, system.PermUserUpdate, system.PermUserStatus, system.PermUserAssignRole,
		system.PermSessionList, system.PermSessionRevoke, system.PermRoleList, system.PermRoleUpdate,
	})
	return f.createUser(admin, "ops", []uint64{role})
}

func TestSuper_ResetPasswordIsSuperOnly(t *testing.T) {
	f := newFixture(t)
	root, rootID := f.admin("root")
	_, second := f.createUser(root, "second", []uint64{f.superRoleID()})
	daveID, dave := f.createUser(root, "dave", nil)
	_, ops := f.opsUser(root)

	// 权限码已经不存在：不能授给任何角色，路由守卫是"只有超管"
	require.False(t, f.app.Deps().Perms.Has("platform", "system:user:reset-password"))
	role := f.createRole(root, "helpdesk", nil)
	g := f.do(root, "PUT", fmt.Sprintf("/system/roles/%d/perms", role), gin.H{"codes": []string{"system:user:reset-password"}})
	require.NotEqual(t, 0, g.env.Code, "未注册的权限码不能授予: %s", g.rec.Body.String())
	found := false
	for _, rt := range f.app.Routes() {
		if rt.Path == base+"/system/users/:id/reset-password" {
			found = true
			require.Equal(t, rbac.GuardSuper, rt.Guard)
		}
	}
	require.True(t, found)
	var superRoutes []string
	for _, rt := range f.app.Routes() {
		if rt.Guard == rbac.GuardSuper {
			superRoutes = append(superRoutes, rt.Method+" "+rt.Path)
		}
	}
	require.Equal(t, []string{"POST " + base + "/system/users/:id/reset-password"}, superRoutes, "超管专用路由只有这一条，新加的要在这里登记")

	// 非超管：即使有全部用户管理权限也不能重置任何人的密码
	r := f.do(ops, "POST", fmt.Sprintf("/system/users/%d/reset-password", daveID), nil)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.NotContains(t, r.rec.Body.String(), "initialPassword")
	require.Equal(t, 0, f.do(dave, "GET", "/auth/me", nil).env.Code, "被拒的重置不影响对方")

	// 超管重置普通用户：成功，对方旧会话失效
	r = f.do(root, "POST", fmt.Sprintf("/system/users/%d/reset-password", daveID), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.GreaterOrEqual(t, len(r.data()["initialPassword"].(string)), 20)
	require.Equal(t, 401, f.do(dave, "GET", "/auth/me", nil).rec.Code)

	// 超管的密码谁都不能在网页上重置：别的超管不行，自己也不行
	secondID := uint64(f.do(second, "GET", "/auth/me", nil).data()["user"].(map[string]any)["id"].(float64))
	for _, tc := range []struct {
		name  string
		token string
		id    uint64
	}{{"超管重置另一个超管", root, secondID}, {"超管重置自己", root, rootID}, {"另一个超管重置 root", second, rootID}} {
		r := f.do(tc.token, "POST", fmt.Sprintf("/system/users/%d/reset-password", tc.id), nil)
		require.Equal(t, 403, r.rec.Code, "%s: %s", tc.name, r.rec.Body.String())
		require.Equal(t, "system.user.resetSuper", fieldKey(r), tc.name)
		require.NotContains(t, r.rec.Body.String(), "initialPassword", tc.name)
	}
	require.Equal(t, 0, f.do(second, "GET", "/auth/me", nil).env.Code, "被拒的重置不吊销会话")
	f.login("root", "changed-pass-9")

	// 超管被降级后，下一次请求就不能再重置
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/users/%d/roles", secondID), gin.H{"roleIds": []uint64{}}).env.Code)
	r = f.do(second, "POST", fmt.Sprintf("/system/users/%d/reset-password", daveID), nil)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())

	// 非超管被拒记为安全事件，说明是 super
	f.flushAudit()
	ctx := f.app.Context(context.Background())
	events, _, err := f.app.Deps().Audit.ListSecurityEvents(ctx, audit.SecurityFilter{Portal: "platform", Kind: "forbidden", Username: "ops"}, 1, 20)
	require.NoError(t, err)
	superDenied := false
	for _, e := range events {
		if e.Detail == rbac.GuardSuperDetail {
			superDenied = true
		}
	}
	require.True(t, superDenied, "非超管调用超管专用接口要记安全事件")
}

func TestSuper_NonSuperCannotWriteSuperAccounts(t *testing.T) {
	f := newFixture(t)
	root, rootID := f.admin("root")
	secondID, second := f.createUser(root, "second", []uint64{f.superRoleID()})
	daveID, dave := f.createUser(root, "dave", nil)
	_, ops := f.opsUser(root)
	rootSID := f.mySID(root, rootID)

	protected := func(name string, r resp, key string) {
		t.Helper()
		require.Equal(t, 403, r.rec.Code, "%s: %s", name, r.rec.Body.String())
		require.Equal(t, httpx.CodeForbidden, r.env.Code, name)
		require.Equal(t, key, fieldKey(r), name)
	}
	// 改资料（包括部门、岗位）
	protected("改超管资料", f.do(ops, "PUT", fmt.Sprintf("/system/users/%d", rootID), gin.H{"displayName": "pwned", "email": "x@evil.test"}), "system.user.superProtected")
	u := f.do(root, "GET", fmt.Sprintf("/system/users/%d", rootID), nil)
	require.NotEqual(t, "pwned", u.data()["displayName"], "被拒的修改没有写进去")
	// 停用：还有另一个超管，所以不是"最后一个超管"那条规则在起作用
	protected("停用超管", f.do(ops, "POST", fmt.Sprintf("/system/users/%d/status", secondID), gin.H{"status": 0}), "system.user.superProtected")
	require.Equal(t, 0, f.do(second, "GET", "/auth/me", nil).env.Code, "超管仍然可用")
	// 改部门、岗位同样不行
	deptID := f.createDept(root, 0, "总部")
	postID := f.createPost(root, "ceo")
	protected("给超管调部门", f.do(ops, "PUT", fmt.Sprintf("/system/users/%d", rootID), gin.H{"displayName": "root", "deptId": deptID, "postIds": []uint64{postID}}), "system.user.superProtected")
	u = f.do(root, "GET", fmt.Sprintf("/system/users/%d", rootID), nil)
	require.EqualValues(t, 0, u.data()["deptId"], "部门没有被改")
	require.Empty(t, u.data()["posts"], "岗位没有被改")
	// 分配角色（D-020 原有规则）：没有写进去，root 仍是超管
	r := f.do(ops, "PUT", fmt.Sprintf("/system/users/%d/roles", rootID), gin.H{"roleIds": []uint64{}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	holds, err := f.app.Deps().RBAC.HoldsSuperRole(f.app.Context(context.Background()), "platform", rootID)
	require.NoError(t, err)
	require.True(t, holds)
	// 内置超管角色：非超管连名字也不能改
	superRole := f.superRoleID()
	protected("改内置超管角色", f.do(ops, "PUT", fmt.Sprintf("/system/roles/%d", superRole), gin.H{"name": "Guest", "status": 1}), "rbac.role.superOnlyEdit")
	sr, err := f.app.Deps().RBAC.RoleByCode(f.app.Context(context.Background()), "platform", rbac.SuperRoleCode)
	require.NoError(t, err)
	require.NotEqual(t, "Guest", sr.Name)
	// 让超管的会话下线
	protected("踢超管下线", f.do(ops, "POST", "/system/sessions/"+rootSID+"/revoke", nil), "system.session.superProtected")
	require.Equal(t, 0, f.do(root, "GET", "/auth/me", nil).env.Code, "超管会话仍然有效")
	// 会话列表标出超管的会话
	s := f.do(ops, "GET", fmt.Sprintf("/system/sessions?userId=%d", rootID), nil)
	require.Equal(t, true, s.data()["list"].([]any)[0].(map[string]any)["superAccount"])
	s = f.do(ops, "GET", fmt.Sprintf("/system/sessions?userId=%d", daveID), nil)
	require.Equal(t, false, s.data()["list"].([]any)[0].(map[string]any)["superAccount"])

	// 停用了的超管也受保护：非超管不能把他重新启用
	require.Equal(t, 0, f.do(root, "POST", fmt.Sprintf("/system/users/%d/status", secondID), gin.H{"status": 0}).env.Code)
	protected("启用已停用的超管", f.do(ops, "POST", fmt.Sprintf("/system/users/%d/status", secondID), gin.H{"status": 1}), "system.user.superProtected")
	bad := f.do("", "POST", "/auth/login", gin.H{"username": "second", "password": "user-pass-456"})
	require.Equal(t, httpx.CodeLoginFailed, bad.env.Code, "被拒的启用没有写进去，账号仍是停用")

	// 对普通用户照常可以：保护只针对超管账号
	require.Equal(t, 0, f.do(ops, "PUT", fmt.Sprintf("/system/users/%d", daveID), gin.H{"displayName": "Dave"}).env.Code)
	daveSID := f.mySID(root, daveID)
	require.Equal(t, 0, f.do(ops, "POST", "/system/sessions/"+daveSID+"/revoke", nil).env.Code)
	require.Equal(t, 401, f.do(dave, "GET", "/auth/me", nil).rec.Code)
	require.Equal(t, 0, f.do(ops, "POST", fmt.Sprintf("/system/users/%d/status", daveID), gin.H{"status": 0}).env.Code)

	// 超管之间照常可以互相管理
	require.Equal(t, 0, f.do(root, "POST", fmt.Sprintf("/system/users/%d/status", secondID), gin.H{"status": 1}).env.Code)
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/users/%d", secondID), gin.H{"displayName": "Second"}).env.Code)
	second = f.login("second", "user-pass-456")
	require.Equal(t, 0, f.do(second, "PUT", fmt.Sprintf("/system/users/%d", rootID), gin.H{"displayName": "Root"}).env.Code)
	// 被降级（换成一个有编辑用户权限的普通角色）之后，下一次请求就失去超管的豁免
	editor := f.createRole(root, "editor", []string{system.PermUserList, system.PermUserUpdate})
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/users/%d/roles", secondID), gin.H{"roleIds": []uint64{editor}}).env.Code)
	protected("降级后改超管资料", f.do(second, "PUT", fmt.Sprintf("/system/users/%d", rootID), gin.H{"displayName": "x"}), "system.user.superProtected")

	// 业务层的拒绝同样记安全事件（越权被拒，D-032）
	f.flushAudit()
	events, _, err := f.app.Deps().Audit.ListSecurityEvents(f.app.Context(context.Background()), audit.SecurityFilter{Portal: "platform", Kind: "forbidden", Username: "ops"}, 1, 50)
	require.NoError(t, err)
	paths := map[string]bool{}
	for _, e := range events {
		paths[e.Method+" "+e.Path] = true
	}
	// 安全事件里记的是路由模板
	for _, p := range []string{"PUT /system/users/:id", "POST /system/users/:id/status", "PUT /system/users/:id/roles", "PUT /system/roles/:id", "POST /system/sessions/:sid/revoke"} {
		m, rel, _ := strings.Cut(p, " ")
		require.True(t, paths[m+" "+base+rel], "非超管对超管账号的写操作被拒要记安全事件：%s（%v）", p, paths)
	}

	// 不存在的会话：404，不泄露任何信息
	require.Equal(t, 404, f.do(ops, "POST", "/system/sessions/0123456789abcdef0123456789abcdef/revoke", nil).rec.Code)
}

func TestSuper_ResetPasswordByCLI(t *testing.T) {
	f := newFixture(t)
	root, rootID := f.admin("root")
	daveID, _ := f.createUser(root, "dave", nil)
	require.Equal(t, 0, f.do(root, "POST", fmt.Sprintf("/system/users/%d/status", daveID), gin.H{"status": 0}).env.Code)
	ctx := f.app.Context(context.Background())

	// 和真实的命令行一样，用另一个应用实例（另一套认证器和缓存）连同一个库
	cli, err := app.New(f.app.Deps().Conf, app.WithDB(f.gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	cli.Register(system.Module())
	require.NoError(t, cli.Setup())
	cliCtx := cli.Context(context.Background())

	// 超管的密码只能这样重置：新密码只给一次，旧会话全部吊销，登录后必须改密
	res, err := system.ResetPasswordByCLI(cliCtx, cli.Deps(), "  ROOT ", "ops@bastion")
	require.NoError(t, err)
	require.NoError(t, res.AuditErr)
	require.True(t, res.Super)
	require.True(t, res.Enabled)
	require.GreaterOrEqual(t, len(res.Password), 20)
	var active int64
	require.NoError(t, f.gdb.Table("ga_session").Where("user_id = ? AND revoked_at IS NULL", rootID).Count(&active).Error)
	require.Zero(t, active, "库里的会话全部吊销（运行中的服务状态缓存最迟 15 秒后看到）")
	bad := f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": "changed-pass-9"})
	require.Equal(t, httpx.CodeLoginFailed, bad.env.Code, "旧密码不能再用")
	login := f.do("", "POST", "/auth/login", gin.H{"username": "root", "password": res.Password})
	require.Equal(t, 0, login.env.Code, login.rec.Body.String())
	require.Equal(t, true, login.data()["mustChangePwd"], "登录后必须先改密")

	// 任何账号都能用命令行重置；停用的账号重置后仍是停用，结果里说明
	res, err = system.ResetPasswordByCLI(cliCtx, cli.Deps(), "dave", "")
	require.NoError(t, err)
	require.False(t, res.Super)
	require.False(t, res.Enabled)
	bad = f.do("", "POST", "/auth/login", gin.H{"username": "dave", "password": res.Password})
	require.Equal(t, httpx.CodeLoginFailed, bad.env.Code, "停用的账号拿到新密码也登不上")

	// 不存在的账号
	_, err = system.ResetPasswordByCLI(cliCtx, cli.Deps(), "nobody", "")
	require.ErrorContains(t, err, "不存在")

	// 记一条 cli 安全事件（命令行进程退出时写完）
	require.NoError(t, cli.Stop(cliCtx))
	events, _, err := f.app.Deps().Audit.ListSecurityEvents(ctx, audit.SecurityFilter{Portal: "platform", Kind: system.KindCLI}, 1, 20)
	require.NoError(t, err)
	got := false
	for _, e := range events {
		if e.Detail == "admin reset-password (ops@bastion)" && e.Username == "root" {
			got = true
		}
	}
	require.True(t, got)
}

// 权限码删掉以后（D-035）：库里残留的授权不再回给前端，授权对话框整体提交不会被拒；升级迁移把它删掉。
func TestSuper_RemovedPermCodeDoesNotBreakGrants(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	role := f.createRole(root, "helpdesk", []string{system.PermUserList})
	stale := func() int64 {
		var n int64
		require.NoError(t, f.gdb.Table("ga_casbin_rule").Where("ptype = 'p' AND v1 = 'platform' AND v2 = 'system:user:reset-password'").Count(&n).Error)
		return n
	}
	// 模拟升级前授过这个权限码
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_casbin_rule (ptype, v0, v1, v2) VALUES ('p', ?, 'platform', 'system:user:reset-password')", fmt.Sprintf("role:%d", role)).Error)
	require.NoError(t, f.app.Deps().RBAC.Reload())
	require.EqualValues(t, 1, stale())

	// 读出来的授权里没有它；原样提交回去照常成功，保存一次就清掉了
	r := f.do(root, "GET", fmt.Sprintf("/system/roles/%d/perms", role), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	raw := r.env.Data.([]any)
	codes := make([]string, 0, len(raw))
	for _, c := range raw {
		codes = append(codes, c.(string))
	}
	require.Equal(t, []string{system.PermUserList}, codes)
	g := f.do(root, "PUT", fmt.Sprintf("/system/roles/%d/perms", role), gin.H{"codes": codes})
	require.Equal(t, 0, g.env.Code, g.rec.Body.String())
	require.EqualValues(t, 0, stale())

	// 升级迁移直接删掉残留（可以重跑）
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_casbin_rule (ptype, v0, v1, v2) VALUES ('p', ?, 'platform', 'system:user:reset-password')", fmt.Sprintf("role:%d", role)).Error)
	sql, err := fs.ReadFile(migrations.Core(), migrations.CoreDir+"/00009_drop_reset_password_perm.sql")
	require.NoError(t, err)
	for range 2 {
		require.NoError(t, f.gdb.Exec(string(sql)).Error)
	}
	require.EqualValues(t, 0, stale())
}
