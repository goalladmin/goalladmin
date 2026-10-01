package system_test

// 规范 §13.2 第 72 条（D-047、D-048）：后台的每一条写接口都在锁里重新认定操作人。
// 按路由表遍历：平台端所有需要权限码、超管或只需登录的写路由（认证接口除外）都必须在下面的表里（表和路由表双向核对，新加的写路由不补用例测试就挂）。
// 每条路由用一个新建的超管去调；请求通过认证、还没拿到锁时，另一条事务停用这个账号、吊销它的会话并提交。
// 请求拿到锁后必须回 401，业务表不能有任何变化。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// newRouteFixture 和 newMenuFixture 一样带演示菜单；每条路由都要新建、登录一个超管，把同 IP 的登录频率放宽到上限。
func newRouteFixture(t *testing.T) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	p.Login.IPRatePerMinute = 600
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithBcryptCost(4))
	require.NoError(t, err)
	a.Register(system.Module(), &menuModule{menus: demoMenus()})
	require.NoError(t, a.Setup())
	return &fixture{t: t, app: a, gdb: gdb}
}

// routeCase 给出一条写路由的一次合法调用：路径里的参数已经换成真实的 ID，请求体是一次会成功的写。
type routeCase struct {
	method, route string // route 是路由表里的写法（带 :id 这类占位）
	path          string
	body          any
	raw           []byte // 不为空时按原样发送，Content-Type 为 ct（上传头像）
	ct            string
}

func (f *fixture) send(tok string, c routeCase) resp {
	if c.raw != nil {
		return f.doRaw(tok, c.method, c.path, c.raw, c.ct)
	}
	return f.do(tok, c.method, c.path, c.body)
}

// routeTargets 是用例要用到的现成数据，由 root 在测试开始时建好。
type routeTargets struct {
	dave, dept, post, role, dict, item uint64
	daveSID, group                     string
}

func (f *fixture) routeTargets(root string) routeTargets {
	f.t.Helper()
	var t routeTargets
	t.dept = f.createDept(root, 0, "总部")
	t.post = f.createPost(root, "clerk")
	t.role = f.createRole(root, "plain", nil)
	t.dave, _ = f.createUser(root, "dave", nil)
	t.daveSID = f.mySID(root, t.dave)
	r := f.do(root, "POST", "/system/dicts", gin.H{"code": "bank", "name": "银行", "portal": "platform"})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	t.dict = uint64(r.data()["id"].(float64))
	r = f.do(root, "POST", fmt.Sprintf("/system/dicts/%d/items", t.dict), gin.H{"value": "icbc", "label": "工商银行"})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	t.item = uint64(r.data()["id"].(float64))
	r = f.do(root, "POST", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "常用"}})
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	t.group = r.data()["name"].(string)
	return t
}

func (f *fixture) routeCases(root string, t routeTargets) []routeCase {
	u, d, p, ro, di := fmt.Sprint(t.dave), fmt.Sprint(t.dept), fmt.Sprint(t.post), fmt.Sprint(t.role), fmt.Sprint(t.dict)
	it := fmt.Sprintf("/system/dicts/%d/items/%d", t.dict, t.item)
	layout := layoutOf(f.adminMenus(root), func(pos map[string]gin.H) { pos["demo-b"]["parent"] = "" })
	return []routeCase{
		{method: "POST", route: "/system/users", path: "/system/users", body: gin.H{"username": "erin", "password": "user-pass-123"}},
		{"PUT", "/system/users/:id", "/system/users/" + u, gin.H{"displayName": "改名", "deptId": t.dept}, nil, ""},
		{"POST", "/system/users/:id/status", "/system/users/" + u + "/status", gin.H{"status": 0}, nil, ""},
		{"POST", "/system/users/:id/reset-password", "/system/users/" + u + "/reset-password", nil, nil, ""},
		{"PUT", "/system/users/:id/roles", "/system/users/" + u + "/roles", gin.H{"roleIds": []uint64{t.role}}, nil, ""},
		{"DELETE", "/system/users/:id/avatar", "/system/users/" + u + "/avatar", nil, nil, ""},
		{"POST", "/system/depts", "/system/depts", gin.H{"parentId": 0, "name": "分部"}, nil, ""},
		{"PUT", "/system/depts/:id", "/system/depts/" + d, gin.H{"parentId": 0, "name": "改名"}, nil, ""},
		{"DELETE", "/system/depts/:id", "/system/depts/" + d, nil, nil, ""},
		{"POST", "/system/posts", "/system/posts", gin.H{"code": "boss", "name": "老板"}, nil, ""},
		{"PUT", "/system/posts/:id", "/system/posts/" + p, gin.H{"name": "改名"}, nil, ""},
		{"DELETE", "/system/posts/:id", "/system/posts/" + p, nil, nil, ""},
		{"POST", "/system/roles", "/system/roles", gin.H{"code": "fresh", "name": "fresh"}, nil, ""},
		{"PUT", "/system/roles/:id", "/system/roles/" + ro, gin.H{"name": "改名", "status": 1}, nil, ""},
		{"DELETE", "/system/roles/:id", "/system/roles/" + ro, nil, nil, ""},
		{"PUT", "/system/roles/:id/perms", "/system/roles/" + ro + "/perms", gin.H{"codes": []string{"demo:a:list"}}, nil, ""},
		{"POST", "/system/sessions/:sid/revoke", "/system/sessions/" + t.daveSID + "/revoke", nil, nil, ""},
		{"POST", "/system/dicts", "/system/dicts", gin.H{"code": "city", "name": "城市", "portal": "platform"}, nil, ""},
		{"PUT", "/system/dicts/:id", "/system/dicts/" + di, gin.H{"name": "改名", "portal": "platform"}, nil, ""},
		{"DELETE", "/system/dicts/:id", "/system/dicts/" + di, nil, nil, ""},
		{"POST", "/system/dicts/:id/items", "/system/dicts/" + di + "/items", gin.H{"value": "abc", "label": "农业银行"}, nil, ""},
		{"PUT", "/system/dicts/:id/items/:itemId", it, gin.H{"value": "icbc", "label": "改名"}, nil, ""},
		{"DELETE", "/system/dicts/:id/items/:itemId", it, nil, nil, ""},
		{"POST", "/system/dicts/:id/items/:itemId/reset", it + "/reset", nil, nil, ""},
		{"PUT", "/system/menu-layout", "/system/menu-layout", layout, nil, ""},
		{"PUT", "/system/menus/:name", "/system/menus/demo-a", gin.H{"titles": gin.H{"zh-CN": "改名"}}, nil, ""},
		{"POST", "/system/menus/:name/reset", "/system/menus/demo-a/reset", nil, nil, ""},
		{"POST", "/system/menu-groups", "/system/menu-groups", gin.H{"titles": gin.H{"zh-CN": "新分组"}}, nil, ""},
		{"DELETE", "/system/menu-groups/:name", "/system/menu-groups/" + t.group, nil, nil, ""},
		// 只需登录的本人写操作（D-048）
		{"PUT", "/system/profile", "/system/profile", gin.H{"displayName": "改名", "email": "x@example.com"}, nil, ""},
		{"POST", "/system/profile/revoke-other-sessions", "/system/profile/revoke-other-sessions", nil, nil, ""},
		{method: "POST", route: "/system/avatar", path: "/system/avatar", raw: testPNG(f.t, 8, 8), ct: "image/png"},
		{"PUT", "/system/avatar", "/system/avatar", gin.H{"preset": "ocean"}, nil, ""},
		{"DELETE", "/system/avatar", "/system/avatar", nil, nil, ""},
	}
}

// snapshot 把业务表逐行读出来（日志类的表不算：被拒绝的请求照样会记日志）。操作人自己的账号行只比资料和头像
// （状态由测试自己改），会话行不比（测试会把它们全部吊销）。
func (f *fixture) snapshot(actorID uint64) map[string]string {
	f.t.Helper()
	skip := map[string]bool{"ga_login_log": true, "ga_operation_log": true, "ga_error_log": true, "ga_security_event": true}
	var tables []string
	require.NoError(f.t, f.gdb.Raw("SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name LIKE 'ga\\_%'").Scan(&tables).Error)
	out := map[string]string{}
	for _, tb := range tables {
		if skip[tb] {
			continue
		}
		q := f.gdb.Table(tb)
		switch tb {
		case "ga_user":
			q = q.Where("id <> ?", actorID)
		case "ga_session":
			q = q.Where("user_id <> ?", actorID)
		}
		var rows []map[string]any
		require.NoError(f.t, q.Order("1").Find(&rows).Error, tb)
		b, err := json.Marshal(rows)
		require.NoError(f.t, err)
		out[tb] = string(b)
	}
	var own []map[string]any
	require.NoError(f.t, f.gdb.Raw("SELECT display_name, email, phone, bio, avatar FROM ga_user WHERE id = ?", actorID).Scan(&own).Error)
	b, err := json.Marshal(own)
	require.NoError(f.t, err)
	out["ga_user#self"] = string(b)
	return out
}

// 81. 操作人在途中被停用、会话被吊销：每一条后台写接口都回 401，什么也不写。
func TestStaleActor_72_EveryWriteRouteRechecksActor(t *testing.T) {
	f := newRouteFixture(t)
	root, _ := f.admin("root")
	targets := f.routeTargets(root)
	cases := f.routeCases(root, targets)

	// 表和路由表双向核对
	want := map[string]bool{}
	for _, r := range f.app.Routes() {
		// 需要权限码、超管的写路由，和只需登录的写路由（认证接口本身除外：登录、刷新、退出、改密、锁屏各有自己的规则）
		if r.Portal != "platform" || r.Method == "GET" || strings.HasPrefix(r.Path, base+"/auth/") ||
			(r.Guard != rbac.GuardRequire && r.Guard != rbac.GuardSuper && r.Guard != rbac.GuardAuthOnly) {
			continue
		}
		want[r.Method+" "+r.Path] = true
	}
	have := map[string]bool{}
	for _, c := range cases {
		key := c.method + " " + base + c.route
		require.False(t, have[key], "重复的用例：%s", key)
		have[key] = true
	}
	var missing, extra []string
	for k := range want {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	for k := range have {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	require.Empty(t, missing, "这些写路由没有用例")
	require.Empty(t, extra, "这些用例对应的路由不存在")

	sup := f.superRoleID()
	for i, c := range cases {
		name := fmt.Sprintf("actor%02d", i)
		actorID, tok := f.createUser(root, name, []uint64{sup})
		before := f.snapshot(actorID)
		disable := func(release <-chan struct{}) <-chan error {
			// 和停用账号一样：拿超管锁、改状态、吊销全部会话，在一个事务里
			return f.holdSuperLock(func(tx *gorm.DB) {
				require.NoError(t, tx.Exec("UPDATE ga_user SET status = 0 WHERE id = ?", actorID).Error)
				require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'disabled' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", actorID).Error)
			}, release)
		}
		r := f.inFlight(disable, func() resp { return f.send(tok, c) })
		require.Equal(t, 401, r.rec.Code, "%s %s: %s", c.method, c.route, r.rec.Body.String())
		after := f.snapshot(actorID)
		for tb := range before {
			require.Equal(t, before[tb], after[tb], "%s %s 改动了 %s", c.method, c.route, tb)
		}
	}
}
