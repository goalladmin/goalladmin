package main

// 平台的代理商管理、商户管理（modules/agent、modules/merchant，D-065）的反向测试，用平台程序真实注册的全部模块
// （modules()）跑完整的 HTTP 链路：规范 §13.2 第 138–141 条。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/agent"
	"github.com/goalladmin/goalladmin/server/modules/merchant"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

const platformBase = "/api/platform/v1"

type partnerFixture struct {
	t   *testing.T
	app *app.App
	gdb *gorm.DB
	ctx context.Context
}

func newPartnerFixture(t *testing.T) *partnerFixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	p.Login.IPRatePerMinute = 600 // 每条路由都要新建、登录一个人
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(modules()...) // 与平台程序注册的模块一致
	require.NoError(t, a.Setup())
	return &partnerFixture{t: t, app: a, gdb: gdb, ctx: a.Context(context.Background())}
}

type partnerResp struct {
	rec *httptest.ResponseRecorder
	env httpx.Envelope
}

func (r partnerResp) data() map[string]any {
	m, _ := r.env.Data.(map[string]any)
	return m
}

func (r partnerResp) list() []any {
	l, _ := r.data()["list"].([]any)
	return l
}

func (f *partnerFixture) do(tok, method, path string, body any) partnerResp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, platformBase+path, rd)
	req.RemoteAddr = "203.0.113.10:5000"
	req.Header.Set("X-GA-Client", "web")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return partnerResp{rec: rec, env: env}
}

func (f *partnerFixture) ok(tok, method, path string, body any) partnerResp {
	f.t.Helper()
	r := f.do(tok, method, path, body)
	require.Equal(f.t, 0, r.env.Code, "%s %s: %s", method, path, r.rec.Body.String())
	return r
}

func (f *partnerFixture) login(username, password string) string {
	f.t.Helper()
	r := f.ok("", "POST", "/auth/login", gin.H{"username": username, "password": password})
	tok, _ := r.data()["accessToken"].(string)
	require.NotEmpty(f.t, tok)
	return tok
}

// admin 用命令行的办法建一个超管并完成首次改密。
func (f *partnerFixture) admin(username string) string {
	f.t.Helper()
	pwd, err := system.CreateAdmin(f.ctx, f.app.Deps(), username)
	require.NoError(f.t, err)
	tok := f.login(username, pwd)
	f.ok(tok, "PUT", "/auth/password", gin.H{"oldPassword": pwd, "newPassword": "changed-pass-9"})
	return tok
}

func (f *partnerFixture) createRole(root, code string, perms []string) uint64 {
	f.t.Helper()
	r := f.ok(root, "POST", "/system/roles", gin.H{"code": code, "name": code})
	id := uint64(r.data()["id"].(float64))
	if len(perms) > 0 {
		f.ok(root, "PUT", fmt.Sprintf("/system/roles/%d/perms", id), gin.H{"codes": perms, "dataScopes": gin.H{system.DataUser: "all"}})
	}
	return id
}

func (f *partnerFixture) createUser(root, username string, roleIDs []uint64) (uint64, string) {
	f.t.Helper()
	r := f.ok(root, "POST", "/system/users", gin.H{"username": username, "password": "user-pass-123", "roleIds": roleIDs})
	id := uint64(r.data()["user"].(map[string]any)["id"].(float64))
	tok := f.login(username, "user-pass-123")
	f.ok(tok, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-123", "newPassword": "user-pass-456"})
	return id, tok
}

func (f *partnerFixture) superRoleID() uint64 {
	f.t.Helper()
	r, err := f.app.Deps().RBAC.RoleByCode(f.ctx, "platform", rbac.SuperRoleCode)
	require.NoError(f.t, err)
	return r.ID
}

type orgRef struct {
	id       uint64
	code     string
	ownerID  uint64
	password string
}

// newOrg 通过接口开一个代理商（kind = "agent"）或商户（"merchant"）。
func (f *partnerFixture) newOrg(tok, kind, name string, extra gin.H) orgRef {
	f.t.Helper()
	body := gin.H{"name": name, "ownerUsername": "admin"}
	for k, v := range extra {
		body[k] = v
	}
	r := f.ok(tok, "POST", "/"+kind+"/"+kind+"s", body)
	o := r.data()[kind].(map[string]any)
	return orgRef{
		id: uint64(o["id"].(float64)), code: o["code"].(string),
		ownerID: uint64(r.data()["ownerId"].(float64)), password: r.data()["initialPassword"].(string),
	}
}

// addAccount 直接往主体的账号表插一个员工（平台程序里没有主体端的接口，D-067 的子账号接口在主体端的程序里）。
func (f *partnerFixture) addAccount(kind string, orgID uint64, username string) uint64 {
	f.t.Helper()
	table := "ga_" + kind + "_user"
	require.NoError(f.t, f.gdb.Exec("INSERT INTO "+table+" (org_id, username, password_hash, status, created_at, updated_at) VALUES (?, ?, 'x', 1, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", orgID, username).Error)
	var id uint64
	require.NoError(f.t, f.gdb.Raw("SELECT id FROM "+table+" WHERE org_id = ? AND username = ?", orgID, username).Scan(&id).Error)
	return id
}

// addSession 直接往会话表插一个有效会话（主体端的程序不在这个进程里）。
func (f *partnerFixture) addSession(portal string, orgID, userID uint64) string {
	f.t.Helper()
	var b [16]byte
	_, _ = rand.Read(b[:])
	sid := hex.EncodeToString(b[:])
	require.NoError(f.t, f.gdb.Exec("INSERT INTO ga_session (sid, portal, org_id, user_id, refresh_hash, rotated_at, expires_at, last_seen_at, created_at) VALUES (?, ?, ?, ?, REPEAT('0', 64), UTC_TIMESTAMP(3), UTC_TIMESTAMP(3) + INTERVAL 1 DAY, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))",
		sid, portal, orgID, userID).Error)
	return sid
}

func (f *partnerFixture) sessionRevoked(sid string) string {
	f.t.Helper()
	var reason *string
	require.NoError(f.t, f.gdb.Raw("SELECT IF(revoked_at IS NULL, NULL, revoke_reason) FROM ga_session WHERE sid = ?", sid).Scan(&reason).Error)
	if reason == nil {
		return ""
	}
	return *reason
}

// 138. 敏感权限、只读权限、初始密码只出现一次。
func TestPartner_138_SensitivePermsAndOneTimePassword(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")

	// 主账号相关和改归属是敏感权限：有授权权限的非超管也授不出去；别的照常能授
	all := []string{
		agent.PermList, agent.PermCreate, agent.PermUpdate, agent.PermStatus, agent.PermOwner, agent.PermLog,
		merchant.PermList, merchant.PermCreate, merchant.PermUpdate, merchant.PermStatus, merchant.PermOwner, merchant.PermTransfer, merchant.PermLog,
	}
	granter := f.createRole(root, "granter", append([]string{system.PermRoleList, system.PermRoleGrant}, all...))
	_, g := f.createUser(root, "granter", []uint64{granter})
	target := f.createRole(root, "target", nil)
	for _, perm := range []string{agent.PermOwner, merchant.PermOwner, merchant.PermTransfer} {
		r := f.do(g, "PUT", fmt.Sprintf("/system/roles/%d/perms", target), gin.H{"codes": []string{perm}})
		require.NotEqual(t, 0, r.env.Code, "%s 是敏感权限，非超管授不出去", perm)
	}
	for _, perm := range []string{agent.PermList, agent.PermCreate, agent.PermStatus, agent.PermLog, merchant.PermList, merchant.PermCreate, merchant.PermUpdate, merchant.PermStatus, merchant.PermLog} {
		f.ok(g, "PUT", fmt.Sprintf("/system/roles/%d/perms", target), gin.H{"codes": []string{perm}})
	}

	ag := f.newOrg(root, "agent", "华东代理", nil)
	m := f.newOrg(root, "merchant", "商户甲", gin.H{"agentId": ag.id})
	require.Len(t, m.password, 20)
	other := f.addAccount("merchant", m.id, "staff")

	// 只有查看权限的人：读得到，什么都改不了
	viewer := f.createRole(root, "viewer", []string{agent.PermList, merchant.PermList, agent.PermLog, merchant.PermLog})
	_, v := f.createUser(root, "viewer", []uint64{viewer})
	f.ok(v, "GET", "/merchant/merchants", nil)
	f.ok(v, "GET", fmt.Sprintf("/merchant/merchants/%d/accounts", m.id), nil)
	f.ok(v, "GET", "/agent/agents", nil)
	mp, ap := fmt.Sprintf("/merchant/merchants/%d", m.id), fmt.Sprintf("/agent/agents/%d", ag.id)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/merchant/merchants", gin.H{"name": "x", "ownerUsername": "admin"}},
		{"PUT", mp, gin.H{"name": "x"}},
		{"POST", mp + "/status", gin.H{"status": 0}},
		{"POST", mp + "/reset-owner-password", nil},
		{"PUT", mp + "/owner", gin.H{"userId": other}},
		{"PUT", mp + "/agent", gin.H{"agentId": 0}},
		{"DELETE", mp + "/ip-allow", nil},
		{"DELETE", mp + "/ip-deny", nil},
		{"POST", mp + "/sessions/" + strings.Repeat("a", 32) + "/revoke", nil},
		{"POST", "/agent/agents", gin.H{"name": "x", "ownerUsername": "admin"}},
		{"PUT", ap, gin.H{"name": "x"}},
		{"POST", ap + "/status", gin.H{"status": 0}},
		{"POST", ap + "/reset-owner-password", nil},
		{"PUT", ap + "/owner", gin.H{"userId": 1}},
		{"DELETE", ap + "/ip-allow", nil},
		{"DELETE", ap + "/ip-deny", nil},
		{"POST", ap + "/sessions/" + strings.Repeat("a", 32) + "/revoke", nil},
	} {
		r := f.do(v, c.method, c.path, c.body)
		require.Equal(t, 403, r.rec.Code, "%s %s: %s", c.method, c.path, r.rec.Body.String())
	}
	// 没有日志权限看不到日志；有列表权限不等于能看日志
	listOnly := f.createRole(root, "list-only", []string{merchant.PermList})
	_, lo := f.createUser(root, "listonly", []uint64{listOnly})
	require.Equal(t, 403, f.do(lo, "GET", "/merchant/login-logs", nil).rec.Code)
	require.Equal(t, 403, f.do(lo, "GET", "/agent/agents", nil).rec.Code, "商户的权限不管代理商")

	// 初始密码只出现在开户和重置的那一次响应里：详情、列表、账号列表、操作日志里都没有
	reset := f.ok(root, "POST", mp+"/reset-owner-password", nil).data()["initialPassword"].(string)
	require.Len(t, reset, 20)
	for _, path := range []string{mp, "/merchant/merchants", mp + "/accounts", "/system/operation-logs?pageSize=200", "/merchant/operation-logs?pageSize=200"} {
		body := f.ok(root, "GET", path, nil).rec.Body.String()
		require.NotContains(t, body, m.password, path)
		require.NotContains(t, body, reset, path)
	}
	var bodies []string
	require.NoError(t, f.gdb.Raw("SELECT CONCAT(IFNULL(body, ''), query, error) FROM ga_operation_log").Scan(&bodies).Error)
	require.NotEmpty(t, bodies)
	for _, b := range bodies {
		require.NotContains(t, b, m.password)
		require.NotContains(t, b, reset)
	}
	// 开户、重置都记了操作日志
	var actions []string
	require.NoError(t, f.gdb.Raw("SELECT action FROM ga_operation_log WHERE portal = 'platform' AND action LIKE 'merchant.%' ORDER BY id").Scan(&actions).Error)
	require.Equal(t, []string{"merchant.create", "merchant.reset-owner-password"}, actions)
}

// 139. 主体端的日志：平台按端看到这个端的全部，按编号只看到这个主体的；看不到别的端的。查看本身记操作日志。
func TestPartner_139_LogsByPortalAndOrg(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	a1, a2 := f.newOrg(root, "agent", "代理一", nil), f.newOrg(root, "agent", "代理二", nil)
	m1 := f.newOrg(root, "merchant", "商户一", nil)
	require.Equal(t, a1.id, m1.id, "前提：代理商和商户的 ID 撞在一起")

	login := "INSERT INTO ga_login_log (portal, org_id, org_code, username, user_id, success, created_at) VALUES (?, ?, ?, ?, ?, 1, UTC_TIMESTAMP(3))"
	op := "INSERT INTO ga_operation_log (portal, org_id, user_id, username, action, method, path, created_at) VALUES (?, ?, ?, ?, 'role.create', 'POST', '/x', UTC_TIMESTAMP(3))"
	for _, row := range []struct {
		portal string
		org    uint64
		code   string
		user   string
	}{
		{"agent", a1.id, a1.code, "a1-admin"}, {"agent", a1.id, a1.code, "a1-staff"}, {"agent", a2.id, a2.code, "a2-admin"},
		{"agent", 0, "A00000000", "nobody"}, // 编号不存在的失败登录
		{"merchant", m1.id, m1.code, "m1-admin"},
	} {
		require.NoError(t, f.gdb.Exec(login, row.portal, row.org, row.code, row.user, 1).Error)
		if row.org != 0 {
			require.NoError(t, f.gdb.Exec(op, row.portal, row.org, 1, row.user).Error)
		}
	}

	users := func(r partnerResp) []string {
		out := make([]string, 0, len(r.list()))
		for _, it := range r.list() {
			out = append(out, it.(map[string]any)["username"].(string))
		}
		sort.Strings(out)
		return out
	}
	r := f.ok(root, "GET", "/agent/login-logs", nil)
	require.Equal(t, []string{"a1-admin", "a1-staff", "a2-admin", "nobody"}, users(r), "代理商端的全部，没有商户端、平台端的")
	r = f.ok(root, "GET", "/agent/login-logs?orgCode="+strings.ToLower(a1.code), nil)
	require.Equal(t, []string{"a1-admin", "a1-staff"}, users(r))
	require.Equal(t, "代理一", r.list()[0].(map[string]any)["orgName"])
	require.Empty(t, f.ok(root, "GET", "/agent/login-logs?orgCode=A99999999", nil).list(), "编号不存在时结果为空，不是全部")
	require.Empty(t, f.ok(root, "GET", "/agent/login-logs?orgCode="+m1.code, nil).list(), "商户的编号在代理商端不存在")
	require.Equal(t, []string{"m1-admin"}, users(f.ok(root, "GET", "/merchant/login-logs", nil)))

	r = f.ok(root, "GET", "/agent/operation-logs", nil)
	require.Equal(t, []string{"a1-admin", "a1-staff", "a2-admin"}, users(r))
	r = f.ok(root, "GET", "/agent/operation-logs?orgCode="+a2.code, nil)
	require.Equal(t, []string{"a2-admin"}, users(r))
	require.Equal(t, a2.code, r.list()[0].(map[string]any)["orgCode"])
	require.Equal(t, "代理二", r.list()[0].(map[string]any)["orgName"])
	r = f.ok(root, "GET", "/merchant/operation-logs", nil)
	require.Equal(t, []string{"m1-admin"}, users(r), "商户端的操作日志：没有平台端、代理商端的")

	// 查看留痕
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE portal = 'platform' AND action = ?", agent.OpViewLoginLog).Scan(&n).Error)
	require.EqualValues(t, 4, n)
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE portal = 'platform' AND action = ?", merchant.OpViewOplog).Scan(&n).Error)
	require.EqualValues(t, 1, n)
}

// 141. 两种主体、不同主体互不相干：代理商的接口碰不到商户，会话、账号、IP 白名单只限 URL 里的这个主体。
func TestPartner_141_KindsAndOrgsIsolated(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	a1 := f.newOrg(root, "agent", "代理一", nil)
	m1, m2 := f.newOrg(root, "merchant", "商户一", nil), f.newOrg(root, "merchant", "商户二", nil)
	require.Equal(t, a1.id, m1.id)

	// 代理商只有 1 号，商户有 1、2 号：代理商的接口按代理商表查
	require.Equal(t, 404, f.do(root, "GET", fmt.Sprintf("/agent/agents/%d", m2.id), nil).rec.Code)
	require.Equal(t, 404, f.do(root, "POST", fmt.Sprintf("/agent/agents/%d/status", m2.id), gin.H{"status": 0}).rec.Code)
	require.Equal(t, 404, f.do(root, "POST", fmt.Sprintf("/agent/agents/%d/reset-owner-password", m2.id), nil).rec.Code)

	// 会话：别的主体的、另一个端同 ID 主体的，都按不存在处理
	s1, s2, sa := f.addSession("merchant", m1.id, m1.ownerID), f.addSession("merchant", m2.id, m2.ownerID), f.addSession("agent", a1.id, a1.ownerID)
	sess := f.ok(root, "GET", fmt.Sprintf("/merchant/merchants/%d/sessions", m1.id), nil).list()
	require.Len(t, sess, 1)
	require.Equal(t, s1, sess[0].(map[string]any)["sid"])
	require.Equal(t, 404, f.do(root, "POST", fmt.Sprintf("/merchant/merchants/%d/sessions/%s/revoke", m1.id, s2), nil).rec.Code)
	require.Equal(t, 404, f.do(root, "POST", fmt.Sprintf("/merchant/merchants/%d/sessions/%s/revoke", m1.id, sa), nil).rec.Code)
	require.Empty(t, f.sessionRevoked(s2))
	require.Empty(t, f.sessionRevoked(sa))
	f.ok(root, "POST", fmt.Sprintf("/merchant/merchants/%d/sessions/%s/revoke", m1.id, s1), nil)
	require.Equal(t, "admin", f.sessionRevoked(s1))

	// 停用商户一：只吊销商户一的会话
	s1b := f.addSession("merchant", m1.id, m1.ownerID)
	f.ok(root, "POST", fmt.Sprintf("/merchant/merchants/%d/status", m1.id), gin.H{"status": 0})
	require.Equal(t, "disabled", f.sessionRevoked(s1b))
	require.Empty(t, f.sessionRevoked(s2))
	require.Empty(t, f.sessionRevoked(sa))
	f.ok(root, "POST", fmt.Sprintf("/merchant/merchants/%d/status", m1.id), gin.H{"status": 1})

	// 更换主账号：别的商户的账号不行
	r := f.do(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/owner", m1.id), gin.H{"userId": m2.ownerID})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	staff := f.addAccount("merchant", m1.id, "staff")
	f.ok(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/owner", m1.id), gin.H{"userId": staff})
	got := f.ok(root, "GET", fmt.Sprintf("/merchant/merchants/%d", m1.id), nil).data()
	require.Equal(t, "staff", got["ownerUsername"])

	// IP 白名单：清空只动这一个主体的
	ipa := f.app.Deps().IPACL
	set := func(tg ipacl.Target) {
		_, err := ipa.SetAllow(f.ctx, tg, []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 1, "")
		require.NoError(t, err)
	}
	set(ipacl.OrgTarget("merchant", m1.id))
	set(ipacl.OrgTarget("merchant", m2.id))
	set(ipacl.OrgTarget("agent", a1.id))
	items := f.ok(root, "GET", fmt.Sprintf("/merchant/merchants/%d/ip-allow", m1.id), nil).data()["items"].([]any)
	require.Len(t, items, 1)
	r = f.ok(root, "DELETE", fmt.Sprintf("/merchant/merchants/%d/ip-allow", m1.id), nil)
	require.EqualValues(t, 1, r.data()["removed"])
	for tg, want := range map[ipacl.Target]int{ipacl.OrgTarget("merchant", m1.id): 0, ipacl.OrgTarget("merchant", m2.id): 1, ipacl.OrgTarget("agent", a1.id): 1} {
		rules, err := ipa.ListAllow(f.ctx, tg)
		require.NoError(t, err)
		require.Len(t, rules, want, "%+v", tg)
	}
	require.Equal(t, 404, f.do(root, "GET", "/merchant/merchants/999/ip-allow", nil).rec.Code)
	require.Equal(t, 404, f.do(root, "DELETE", "/merchant/merchants/999/ip-allow", nil).rec.Code)

	// 改归属、按代理商筛选、选代理商
	f.ok(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/agent", m2.id), gin.H{"agentId": a1.id})
	l := f.ok(root, "GET", fmt.Sprintf("/merchant/merchants?agentId=%d", a1.id), nil).list()
	require.Len(t, l, 1)
	require.Equal(t, a1.code, l[0].(map[string]any)["agentCode"])
	require.Equal(t, "代理一", l[0].(map[string]any)["agentName"])
	require.Len(t, f.ok(root, "GET", "/merchant/merchants?agentId=0", nil).list(), 1, "直属平台的")
	r = f.do(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/agent", m2.id), gin.H{"agentId": 999})
	require.Equal(t, httpx.CodeValidation, r.env.Code)
	require.Equal(t, httpx.CodeValidation, f.do(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/agent", m2.id), gin.H{}).env.Code, "agentId 必填")
	opts := f.ok(root, "GET", "/merchant/options/agents?keyword="+a1.code, nil).env.Data.([]any)
	require.Len(t, opts, 1)
}

// partnerTables 是两个模块的写操作会碰的表（日志表不算：被拒绝的请求照样会记日志）。
var partnerTables = []string{"ga_agent", "ga_merchant", "ga_agent_user", "ga_merchant_user", "ga_ip_rule"}

func (f *partnerFixture) snapshot() map[string]string {
	f.t.Helper()
	out := map[string]string{}
	for _, tb := range partnerTables {
		var rows []map[string]any
		require.NoError(f.t, f.gdb.Table(tb).Order("id").Find(&rows).Error, tb)
		b, err := json.Marshal(rows)
		require.NoError(f.t, err)
		out[tb] = string(b)
	}
	// 主体端的会话（平台端的会话测试自己会吊销）
	var rows []map[string]any
	require.NoError(f.t, f.gdb.Table("ga_session").Where("portal <> 'platform'").Order("id").Find(&rows).Error)
	b, err := json.Marshal(rows)
	require.NoError(f.t, err)
	out["ga_session"] = string(b)
	return out
}

// holdSuperLock 在另一个事务里占住平台的超管锁，执行 during，等 release 再提交。
func (f *partnerFixture) holdSuperLock(during func(tx *gorm.DB), release <-chan struct{}) <-chan error {
	done := make(chan error, 1)
	locked := make(chan struct{})
	go func() {
		done <- f.gdb.Transaction(func(tx *gorm.DB) error {
			var rows []map[string]any
			if err := tx.Raw("SELECT id FROM ga_role WHERE portal = 'platform' AND is_super = 1 FOR UPDATE").Scan(&rows).Error; err != nil {
				close(locked)
				return err
			}
			during(tx)
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	return done
}

// 140（规范 §13.2 第 72 条的一部分）：两个模块的每一条写接口都在锁里重新认定操作人。按路由表遍历，表和路由表双向核对；
// 请求通过认证、还没拿到锁时操作人被停用、会话被吊销，拿到锁后必须回 401，业务表不变。
func TestPartner_140_EveryWriteRouteRechecksActor(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	ag := f.newOrg(root, "agent", "代理", nil)
	m := f.newOrg(root, "merchant", "商户", nil)
	agStaff, mStaff := f.addAccount("agent", ag.id, "staff"), f.addAccount("merchant", m.id, "staff")
	// 员工的会话：重置主账号密码不会顺带把它吊销
	agSID, mSID := f.addSession("agent", ag.id, agStaff), f.addSession("merchant", m.id, mStaff)
	_, err := f.app.Deps().IPACL.SetAllow(f.ctx, ipacl.OrgTarget("agent", ag.id), []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 1, "")
	require.NoError(t, err)
	_, err = f.app.Deps().IPACL.SetAllow(f.ctx, ipacl.OrgTarget("merchant", m.id), []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 1, "")
	require.NoError(t, err)
	// 主体自己设的黑名单：平台清空它是一次真的写（D-102）
	_, err = f.app.Deps().IPACL.AddOrgDeny(f.ctx, ipacl.OrgTarget("agent", ag.id), ipacl.DenyInput{CIDR: "203.0.113.7"}, 1, "")
	require.NoError(t, err)
	_, err = f.app.Deps().IPACL.AddOrgDeny(f.ctx, ipacl.OrgTarget("merchant", m.id), ipacl.DenyInput{CIDR: "203.0.113.7"}, 1, "")
	require.NoError(t, err)

	a, mm := fmt.Sprintf("/agent/agents/%d", ag.id), fmt.Sprintf("/merchant/merchants/%d", m.id)
	type routeCase struct {
		method, route, path string
		body                any
	}
	// 顺序按对照组（最后由没被停用的超管依次真的执行一遍）排：停用放在最后，下线会话放在停用之前，商户改归属时代理商还启用着
	cases := []routeCase{
		{"POST", "/agent/agents", "/agent/agents", gin.H{"name": "新代理", "ownerUsername": "boss"}},
		{"PUT", "/agent/agents/:id", a, gin.H{"name": "改名"}},
		{"POST", "/agent/agents/:id/reset-owner-password", a + "/reset-owner-password", nil},
		{"PUT", "/agent/agents/:id/owner", a + "/owner", gin.H{"userId": agStaff}},
		{"POST", "/agent/agents/:id/sessions/:sid/revoke", a + "/sessions/" + agSID + "/revoke", nil},
		{"DELETE", "/agent/agents/:id/ip-allow", a + "/ip-allow", nil},
		{"DELETE", "/agent/agents/:id/ip-deny", a + "/ip-deny", nil},
		{"POST", "/merchant/merchants", "/merchant/merchants", gin.H{"name": "新商户", "ownerUsername": "admin", "agentId": ag.id}},
		{"PUT", "/merchant/merchants/:id", mm, gin.H{"name": "改名"}},
		{"POST", "/merchant/merchants/:id/reset-owner-password", mm + "/reset-owner-password", nil},
		{"PUT", "/merchant/merchants/:id/owner", mm + "/owner", gin.H{"userId": mStaff}},
		{"PUT", "/merchant/merchants/:id/agent", mm + "/agent", gin.H{"agentId": ag.id}},
		{"POST", "/merchant/merchants/:id/sessions/:sid/revoke", mm + "/sessions/" + mSID + "/revoke", nil},
		{"DELETE", "/merchant/merchants/:id/ip-allow", mm + "/ip-allow", nil},
		{"DELETE", "/merchant/merchants/:id/ip-deny", mm + "/ip-deny", nil},
		{"POST", "/agent/agents/:id/status", a + "/status", gin.H{"status": 0}},
		{"POST", "/merchant/merchants/:id/status", mm + "/status", gin.H{"status": 0}},
	}

	want := map[string]bool{}
	for _, r := range f.app.Routes() {
		if r.Portal != "platform" || r.Method == "GET" {
			continue
		}
		if !strings.HasPrefix(r.Path, platformBase+"/agent/") && !strings.HasPrefix(r.Path, platformBase+"/merchant/") {
			continue
		}
		want[r.Method+" "+r.Path] = true
	}
	have := map[string]bool{}
	for _, c := range cases {
		key := c.method + " " + platformBase + c.route
		require.False(t, have[key], "重复的用例：%s", key)
		have[key] = true
	}
	require.Equal(t, want, have, "用例表和两个模块的写路由不一致")

	sup := f.superRoleID()
	for i, c := range cases {
		actorID, tok := f.createUser(root, fmt.Sprintf("actor%02d", i), []uint64{sup})
		before := f.snapshot()
		release := make(chan struct{})
		var r partnerResp
		var wg sync.WaitGroup
		held := f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET status = 0 WHERE id = ?", actorID).Error)
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'disabled' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", actorID).Error)
		}, release)
		wg.Add(1)
		go func() {
			defer wg.Done()
			r = f.do(tok, c.method, c.path, c.body)
		}()
		time.Sleep(300 * time.Millisecond) // 让请求通过认证、走到锁前面
		close(release)
		require.NoError(t, <-held)
		wg.Wait()
		require.Equal(t, 401, r.rec.Code, "%s %s: %s", c.method, c.route, r.rec.Body.String())
		after := f.snapshot()
		for tb := range before {
			require.Equal(t, before[tb], after[tb], "%s %s 改动了 %s", c.method, c.route, tb)
		}
	}

	// 对照：同样的请求由没被停用的超管发出，确实会改动（用例本身是有效的写）
	for _, c := range cases {
		before := f.snapshot()
		r := f.do(root, c.method, c.path, c.body)
		require.Equal(t, 0, r.env.Code, "%s %s: %s", c.method, c.route, r.rec.Body.String())
		require.NotEqual(t, before, f.snapshot(), "%s %s 没有改动任何东西", c.method, c.route)
	}
}

// 规范 §13.2 第 150 条（D-068）：平台开主体、重置主账号密码要算初始密码的哈希，和登录核对密码占同一个并发上限。
// 便宜的检查先做——字段不合法、主体不存在的请求不去占位置；位置满了回 429、什么都不写；让出之后照常。
func TestPartner_150_ChecksBeforeHashing(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	// 换一个主体服务，算哈希的函数由测试控制（处理函数每次都从 Deps 取）
	var admitted atomic.Int64
	var full atomic.Bool
	deps := f.app.Deps()
	deps.Orgs = org.New(org.Options{Hash: func(plain string) (string, error) {
		if full.Load() {
			return "", httpx.ErrTooManyRequests
		}
		admitted.Add(1)
		return deps.Auth.HashPassword(conf.DefaultPortalCode, plain)
	}})

	for _, kind := range []string{"agent", "merchant"} {
		base := "/" + kind + "/" + kind + "s"
		before, n := f.snapshot(), admitted.Load()
		for name, c := range map[string]struct {
			path   string
			body   any
			status int
			code   int
		}{
			"主账号登录名不合法": {base, gin.H{"name": "店", "ownerUsername": "1bad"}, 200, httpx.CodeValidation},
			"名称为空":      {base, gin.H{"name": " ", "ownerUsername": "admin"}, 200, httpx.CodeValidation},
			"重置不存在的主体":  {base + "/987654/reset-owner-password", nil, 404, httpx.CodeNotFound},
		} {
			r := f.do(root, "POST", c.path, c.body)
			require.Equal(t, c.status, r.rec.Code, "%s %s: %s", kind, name, r.rec.Body.String())
			require.Equal(t, c.code, r.env.Code, "%s %s", kind, name)
			require.Equal(t, n, admitted.Load(), "%s %s：不该去算哈希", kind, name)
		}

		full.Store(true)
		r := f.do(root, "POST", base, gin.H{"name": "店", "ownerUsername": "admin"})
		require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
		require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
		require.Equal(t, before, f.snapshot(), "位置满了什么都不写")
		full.Store(false)

		o := f.newOrg(root, kind, "店", nil)
		require.Equal(t, n+1, admitted.Load())
		before = f.snapshot()
		full.Store(true)
		r = f.do(root, "POST", fmt.Sprintf("%s/%d/reset-owner-password", base, o.id), nil)
		require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
		require.Equal(t, before, f.snapshot(), "位置满了密码不换")
		full.Store(false)
		r = f.ok(root, "POST", fmt.Sprintf("%s/%d/reset-owner-password", base, o.id), nil)
		require.NotEmpty(t, r.data()["initialPassword"])
		require.Equal(t, n+2, admitted.Load())
		require.NotEqual(t, before, f.snapshot())
	}
}

// 181（D-097）、183（D-099）在平台端代理商管理、商户管理上的接线：让主体会话下线的接口只认规范写法的会话号；
// 主体日志查询的时间参数超出范围不出 500。
func TestPartner_181_183_SessionIDAndTimeFilters(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	for _, kind := range []string{"agent", "merchant"} {
		o := f.newOrg(root, kind, "Shop "+kind, nil)
		uid := f.addAccount(kind, o.id, "clerk")
		var sid string
		for i := 0; i < 20 && strings.ToUpper(sid) == sid; i++ {
			sid = f.addSession(kind, o.id, uid)
		}
		require.NotEqual(t, sid, strings.ToUpper(sid))
		base := fmt.Sprintf("/%s/%ss/%d/sessions/", kind, kind, o.id)
		require.Equal(t, 404, f.do(root, "POST", base+strings.ToUpper(sid)+"/revoke", nil).rec.Code, kind)
		require.Empty(t, f.sessionRevoked(sid), "%s：大写写法不能让会话下线", kind)
		f.ok(root, "POST", base+sid+"/revoke", nil)
		require.NotEmpty(t, f.sessionRevoked(sid), kind)

		early := url.QueryEscape("0000-01-01T00:00:00+23:59")
		late := url.QueryEscape("9999-12-31T23:59:59-23:59")
		for _, path := range []string{"/" + kind + "/login-logs", "/" + kind + "/operation-logs"} {
			r := f.do(root, "GET", path+"?from="+early+"&to="+late, nil)
			require.Equal(t, 200, r.rec.Code, "%s: %s", path, r.rec.Body.String())
			require.Equal(t, 0, r.env.Code, "%s: %s", path, r.rec.Body.String())
		}
	}
}
