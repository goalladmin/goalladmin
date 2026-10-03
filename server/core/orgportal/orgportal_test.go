package orgportal_test

// 主体端自己的后台（端后台套件，D-067、D-068）的反向测试：规范 §13.2 第 143–148、151 条。代理商、商户两种主体各跑一遍，
// 每个测试都是一个只注册了这一个主体端的程序（和 cmd/agent、cmd/merchant 一样），连真实 MySQL、走完整的 HTTP 链路。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/orgportal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// newPass 是首次登录改密之后的密码。
const (
	newPass   = "changed-pass-9"
	staffPass = "staff-pass-123"
	clientIP  = "203.0.113.10"
)

// kitModule 是只挂套件的模块：和 modules/merchantportal 一样（代理商端的"名下商户"在 modules/agentportal 里测）。
type kitModule struct {
	kind org.Kind
	kit  *orgportal.Kit
}

func (m *kitModule) Name() string { return "kit" + m.kind.Portal() }
func (m *kitModule) Init(deps *app.Deps) error {
	kit, err := orgportal.New(deps, m.kind)
	if err != nil {
		return err
	}
	m.kit = kit
	return deps.Portals.Register(kit.Portal())
}
func (m *kitModule) Perms() []rbac.Perm              { return m.kit.Perms() }
func (m *kitModule) Menus() []rbac.MenuNode          { return m.kit.Menus() }
func (m *kitModule) Routes(r *app.Router)            { m.kit.Routes(r) }
func (m *kitModule) Start(ctx context.Context) error { return nil }
func (m *kitModule) Stop(ctx context.Context) error  { return nil }

type fixture struct {
	t    *testing.T
	kind org.Kind
	app  *app.App
	gdb  *gorm.DB
	ctx  context.Context
	base string
	user string // 账号表
}

// forKinds 对代理商、商户各跑一遍。
func forKinds(t *testing.T, fn func(t *testing.T, f *fixture)) {
	for _, kind := range []org.Kind{org.Agent(), org.Merchant()} {
		t.Run(kind.Portal(), func(t *testing.T) { fn(t, newFixture(t, kind)) })
	}
}

func newFixture(t *testing.T, kind org.Kind) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Portals = map[string]conf.Portal{kind.Portal(): {
		AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: "org-portal-test-secret-0123456789abcdef0123",
		Login: conf.PortalLogin{IPRatePerMinute: 600, AccountRatePerMinute: 120}, // 每个用例都要登录好几次
	}}
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1), app.WithoutMigrations())
	require.NoError(t, err)
	a.Register(&kitModule{kind: kind})
	require.NoError(t, a.Setup())
	return &fixture{t: t, kind: kind, app: a, gdb: gdb, ctx: a.Context(context.Background()), base: app.PortalPrefix(kind.Portal()), user: "ga_" + kind.Portal() + "_user"}
}

func (f *fixture) perm(action string) string { return f.kind.Portal() + ":" + action }

type resp struct {
	rec *httptest.ResponseRecorder
	env httpx.Envelope
}

func (r resp) data() map[string]any {
	m, _ := r.env.Data.(map[string]any)
	return m
}

func (r resp) list() []map[string]any {
	l, _ := r.data()["list"].([]any)
	out := make([]map[string]any, 0, len(l))
	for _, v := range l {
		out = append(out, v.(map[string]any))
	}
	return out
}

// key 返回错误的翻译键（字段错误取第一个字段的键）。
func (r resp) key() string {
	var raw struct {
		Key  string `json:"key"`
		Data struct {
			Fields []struct {
				Key string `json:"key"`
			} `json:"fields"`
		} `json:"data"`
	}
	_ = json.Unmarshal(r.rec.Body.Bytes(), &raw)
	if len(raw.Data.Fields) > 0 {
		return raw.Data.Fields[0].Key
	}
	return raw.Key
}

// raw 发一个请求：path 相对端的前缀，可以带查询串；contentType 为空时不设。
func (f *fixture) raw(tok, method, path, contentType string, body []byte, ip string) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, f.base+path, rd)
	req.RemoteAddr = ip + ":5000"
	req.Header.Set("X-GA-Client", "web")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return resp{rec: rec, env: env}
}

func (f *fixture) do(tok, method, path string, body any) resp {
	f.t.Helper()
	if body == nil {
		return f.raw(tok, method, path, "", nil, clientIP)
	}
	b, err := json.Marshal(body)
	require.NoError(f.t, err)
	return f.raw(tok, method, path, "application/json", b, clientIP)
}

func (f *fixture) ok(tok, method, path string, body any) resp {
	f.t.Helper()
	r := f.do(tok, method, path, body)
	require.Equal(f.t, 0, r.env.Code, "%s %s: %s", method, path, r.rec.Body.String())
	return r
}

type orgRef struct {
	id       uint64
	code     string
	ownerID  uint64
	password string
}

// newOrg 像平台那样开一个主体（主账号 admin，随机初始密码）。
func (f *fixture) newOrg(name string) orgRef {
	f.t.Helper()
	orgs := f.app.Deps().Orgs
	pwd, err := orgs.NewInitialPassword()
	require.NoError(f.t, err)
	c, err := orgs.Create(f.ctx, f.kind, org.CreateInput{Name: name, OwnerUsername: "admin", Remark: "平台的备注"}, pwd, 1)
	require.NoError(f.t, err)
	return orgRef{id: c.Org.ID, code: c.Org.Code, ownerID: c.OwnerID, password: c.Password}
}

func (f *fixture) login(code, username, password string) string {
	f.t.Helper()
	r := f.ok("", "POST", "/auth/login", gin.H{"org": code, "username": username, "password": password})
	tok, _ := r.data()["accessToken"].(string)
	require.NotEmpty(f.t, tok)
	return tok
}

// owner 用初始密码登录主账号并完成首次改密。
func (f *fixture) owner(o orgRef) string {
	f.t.Helper()
	tok := f.login(o.code, "admin", o.password)
	f.ok(tok, "PUT", "/auth/password", gin.H{"oldPassword": o.password, "newPassword": newPass})
	return tok
}

// role 建一个角色并授权（perms 是完整的权限码）。
func (f *fixture) role(tok, code string, perms ...string) uint64 {
	f.t.Helper()
	r := f.ok(tok, "POST", "/org/roles", gin.H{"code": code, "name": code})
	id := uint64(r.data()["id"].(float64))
	if len(perms) > 0 {
		f.ok(tok, "PUT", fmt.Sprintf("/org/roles/%d/perms", id), gin.H{"codes": perms})
	}
	return id
}

// staff 由 tok（主账号）建一个员工、登录、完成首次改密。
func (f *fixture) staff(tok string, o orgRef, username string, roleIDs ...uint64) (uint64, string) {
	f.t.Helper()
	if roleIDs == nil {
		roleIDs = []uint64{}
	}
	r := f.ok(tok, "POST", "/org/accounts", gin.H{"username": username, "password": staffPass, "roleIds": roleIDs})
	id := uint64(r.data()["account"].(map[string]any)["id"].(float64))
	st := f.login(o.code, username, staffPass)
	f.ok(st, "PUT", "/auth/password", gin.H{"oldPassword": staffPass, "newPassword": newPass})
	return id, st
}

// activeSID 返回账号的一个有效会话。
func (f *fixture) activeSID(userID uint64) string {
	f.t.Helper()
	var sid string
	require.NoError(f.t, f.gdb.Raw("SELECT sid FROM ga_session WHERE portal = ? AND user_id = ? AND revoked_at IS NULL ORDER BY id LIMIT 1", f.kind.Portal(), userID).Scan(&sid).Error)
	require.NotEmpty(f.t, sid)
	return sid
}

// allPerms 是套件的全部权限码（完整写法）。
func (f *fixture) allPerms() []string {
	actions := []string{
		orgportal.PermAccountList, orgportal.PermAccountCreate, orgportal.PermAccountUpdate, orgportal.PermAccountStatus, orgportal.PermAccountAssignRole,
		orgportal.PermRoleList, orgportal.PermRoleCreate, orgportal.PermRoleUpdate, orgportal.PermRoleDelete, orgportal.PermRoleGrant,
		orgportal.PermSessionList, orgportal.PermSessionRevoke, orgportal.PermLoginLogList, orgportal.PermOplogList,
	}
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, f.perm(a))
	}
	return out
}

// snapshot 是授权、账号、会话、IP 名单几张表的内容。actor 非 0 时，这个账号的状态列和它的会话的吊销列不算：
// "途中被停用"的用例里它们本来就要被另一个事务改掉。
func (f *fixture) snapshot(actor uint64) map[string]string {
	f.t.Helper()
	out := map[string]string{}
	dump := func(name string, q *gorm.DB, fix func(row map[string]any)) {
		var rows []map[string]any
		require.NoError(f.t, q.Find(&rows).Error, name)
		for _, r := range rows {
			if fix != nil {
				fix(r)
			}
		}
		b, err := json.Marshal(rows)
		require.NoError(f.t, err)
		out[name] = string(b)
	}
	isActor := func(v any) bool { id, ok := v.(uint64); return ok && actor != 0 && id == actor }
	dump(f.user, f.gdb.Table(f.user).Order("id"), func(r map[string]any) {
		if isActor(r["id"]) {
			delete(r, "status")
		}
	})
	dump("ga_session", f.gdb.Table("ga_session").Where("portal = ?", f.kind.Portal()).Order("id"), func(r map[string]any) {
		delete(r, "last_seen_at") // 请求本身会刷新它，不算"写"
		if isActor(r["user_id"]) {
			delete(r, "revoked_at")
			delete(r, "revoke_reason")
		}
	})
	dump("ga_role", f.gdb.Table("ga_role").Order("id"), nil)
	dump("ga_user_role", f.gdb.Table("ga_user_role").Order("user_id, role_id"), nil)
	dump("ga_casbin_rule", f.gdb.Table("ga_casbin_rule").Order("id"), nil)
	dump("ga_ip_rule", f.gdb.Table("ga_ip_rule").Order("id"), nil)
	dump("ga_"+f.kind.Portal(), f.gdb.Table("ga_"+f.kind.Portal()).Order("id"), nil)
	return out
}

// 143. 主账号保护：员工有全部账号、会话的权限也改不了主账号的资料、状态、角色、会话、密码；主体内没有人能重置主账号的密码，
// 主账号也停不了自己；重置别人的密码只有主账号能做。
func TestOrgPortal_143_OwnerProtected(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o := f.newOrg("甲")
		own := f.owner(o)
		mgrRole := f.role(own, "manager", f.allPerms()...)
		mgrID, mgr := f.staff(own, o, "manager", mgrRole)
		otherID, otherTok := f.staff(own, o, "other")
		ownerSID := f.activeSID(o.ownerID)

		before := f.snapshot(0)
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"PUT", fmt.Sprintf("/org/accounts/%d", o.ownerID), gin.H{"displayName": "改掉主账号"}},
			{"POST", fmt.Sprintf("/org/accounts/%d/status", o.ownerID), gin.H{"status": 0}},
			{"PUT", fmt.Sprintf("/org/accounts/%d/roles", o.ownerID), gin.H{"roleIds": []uint64{mgrRole}}},
			{"POST", "/org/sessions/" + ownerSID + "/revoke", nil},
			{"POST", fmt.Sprintf("/org/accounts/%d/reset-password", o.ownerID), nil},
			{"POST", fmt.Sprintf("/org/accounts/%d/reset-password", otherID), nil}, // 员工的也不行：只有主账号能重置
			{"PUT", "/org/ip-allow", gin.H{"items": []gin.H{{"cidr": clientIP}}}},
		} {
			r := f.do(mgr, c.method, c.path, c.body)
			require.Equal(t, 403, r.rec.Code, "%s %s: %s", c.method, c.path, r.rec.Body.String())
		}
		require.Equal(t, before, f.snapshot(0), "员工对主账号的写操作不能留下任何改动")

		// 对照：员工对员工照常
		f.ok(mgr, "PUT", fmt.Sprintf("/org/accounts/%d", otherID), gin.H{"displayName": "改个名"})
		f.ok(mgr, "POST", fmt.Sprintf("/org/accounts/%d/status", otherID), gin.H{"status": 0})
		// 停用同一个事务里吊销它的全部会话
		var live int64
		require.NoError(t, f.gdb.Table("ga_session").Where("portal = ? AND user_id = ? AND revoked_at IS NULL", f.kind.Portal(), otherID).Count(&live).Error)
		require.Zero(t, live)
		require.Equal(t, 401, f.do(otherTok, "GET", "/org/overview", nil).rec.Code)
		f.ok(mgr, "POST", fmt.Sprintf("/org/accounts/%d/status", otherID), gin.H{"status": 1})
		// 重新启用等于把账号的角色交回去（D-058）：账号有员工自己分配不了的角色（含敏感权限码）时，员工启用不了，主账号可以
		owners := f.role(own, "owners-only", f.perm(orgportal.PermRoleGrant))
		f.ok(own, "PUT", fmt.Sprintf("/org/accounts/%d/roles", otherID), gin.H{"roleIds": []uint64{owners}})
		f.ok(mgr, "POST", fmt.Sprintf("/org/accounts/%d/status", otherID), gin.H{"status": 0})
		r0 := f.do(mgr, "POST", fmt.Sprintf("/org/accounts/%d/status", otherID), gin.H{"status": 1})
		require.Equal(t, 403, r0.rec.Code, r0.rec.Body.String())
		require.Equal(t, "rbac.user.enableNotAssignable", r0.key())
		f.ok(own, "POST", fmt.Sprintf("/org/accounts/%d/status", otherID), gin.H{"status": 1})

		// 主账号自己的密码主体内谁都重置不了；主账号也停不了自己
		r := f.do(own, "POST", fmt.Sprintf("/org/accounts/%d/reset-password", o.ownerID), nil)
		require.Equal(t, 403, r.rec.Code)
		require.Equal(t, "org.account.resetOwner", r.key())
		r = f.do(own, "POST", fmt.Sprintf("/org/accounts/%d/status", o.ownerID), gin.H{"status": 0})
		require.Equal(t, httpx.CodeValidation, r.env.Code)
		require.Equal(t, "org.account.disableSelf", r.key())
		var status int
		require.NoError(t, f.gdb.Table(f.user).Select("status").Where("id = ?", o.ownerID).Scan(&status).Error)
		require.Equal(t, 1, status)

		// 主账号重置员工的密码：新密码只在响应里出现一次、下次登录必须改，员工的会话全被吊销
		r = f.ok(own, "POST", fmt.Sprintf("/org/accounts/%d/reset-password", mgrID), nil)
		pwd, _ := r.data()["initialPassword"].(string)
		require.NotEmpty(t, pwd)
		require.Equal(t, 401, f.do(mgr, "GET", "/org/overview", nil).rec.Code)
		tok := f.login(o.code, "manager", pwd)
		require.Equal(t, httpx.CodePwdChangeRequired, f.do(tok, "GET", "/org/overview", nil).env.Code)
		require.NotContains(t, f.ok(own, "GET", fmt.Sprintf("/org/accounts/%d", mgrID), nil).rec.Body.String(), pwd)
	})
}

// 144. 主体内授权：非主账号授不出自己没有的权限码，敏感权限码（建账号、分配角色、授权）只有主账号能授，自己有也不行；
// 建账号时顺手分配角色要有分配权限；分配的角色不能带自己没有的权限码；别的主体的角色分配不了。
func TestOrgPortal_144_GrantWithinOrg(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o := f.newOrg("甲")
		own := f.owner(o)
		grantRole := f.role(own, "granter", f.perm(orgportal.PermRoleList), f.perm(orgportal.PermRoleGrant), f.perm(orgportal.PermLoginLogList))
		_, g := f.staff(own, o, "granter", grantRole)
		target := f.role(own, "target")
		perms := func() []any {
			return f.ok(own, "GET", fmt.Sprintf("/org/roles/%d/perms", target), nil).env.Data.([]any)
		}

		f.ok(g, "PUT", fmt.Sprintf("/org/roles/%d/perms", target), gin.H{"codes": []string{f.perm(orgportal.PermLoginLogList)}})
		r := f.do(g, "PUT", fmt.Sprintf("/org/roles/%d/perms", target), gin.H{"codes": []string{f.perm(orgportal.PermSessionList)}})
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "rbac.perm.notOwned", r.key())
		r = f.do(g, "PUT", fmt.Sprintf("/org/roles/%d/perms", target), gin.H{"codes": []string{f.perm(orgportal.PermRoleGrant)}})
		require.Equal(t, 403, r.rec.Code, "授权是敏感权限码：员工自己有也授不出去")
		require.Equal(t, "rbac.perm.sensitive", r.key())
		require.Equal(t, []any{f.perm(orgportal.PermLoginLogList)}, perms())
		// 主账号能授敏感权限码
		f.ok(own, "PUT", fmt.Sprintf("/org/roles/%d/perms", target), gin.H{"codes": []string{f.perm(orgportal.PermAccountCreate), f.perm(orgportal.PermRoleGrant)}})
		require.Len(t, perms(), 2)

		// 建账号时带角色：没有分配权限 403，账号也没建（角色里的权限码建的人都有，挡住它的只能是"分配角色"这个权限码）
		creatorRole := f.role(own, "creator", f.perm(orgportal.PermAccountList), f.perm(orgportal.PermAccountCreate))
		_, cr := f.staff(own, o, "creator", creatorRole)
		mini := f.role(own, "mini", f.perm(orgportal.PermAccountList))
		r = f.do(cr, "POST", "/org/accounts", gin.H{"username": "withrole", "password": staffPass, "roleIds": []uint64{mini}})
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		var n int64
		require.NoError(t, f.gdb.Table(f.user).Where("org_id = ? AND username = 'withrole'", o.id).Count(&n).Error)
		require.Zero(t, n)
		require.Equal(t, "rbac.perm.notOwned", r.key())
		r = f.ok(cr, "POST", "/org/accounts", gin.H{"username": "plain"})
		plainID := uint64(r.data()["account"].(map[string]any)["id"].(float64))
		require.NotEmpty(t, r.data()["initialPassword"], "没给密码时生成一个，只在这次响应里")

		// 分配角色：能分配的只有权限码都在自己手里、又不含敏感权限码的角色
		low := f.role(own, "low", f.perm(orgportal.PermLoginLogList))
		asRole := f.role(own, "assigner", f.perm(orgportal.PermAccountList), f.perm(orgportal.PermAccountAssignRole), f.perm(orgportal.PermLoginLogList))
		_, as := f.staff(own, o, "assigner", asRole)
		f.ok(as, "PUT", fmt.Sprintf("/org/accounts/%d/roles", plainID), gin.H{"roleIds": []uint64{low}})
		r = f.do(as, "PUT", fmt.Sprintf("/org/accounts/%d/roles", plainID), gin.H{"roleIds": []uint64{target}})
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		// 给自己分配带敏感权限码的角色也不行
		asID := uint64(0)
		for _, row := range f.ok(own, "GET", "/org/accounts?keyword=assigner", nil).list() {
			asID = uint64(row["id"].(float64))
		}
		r = f.do(as, "PUT", fmt.Sprintf("/org/accounts/%d/roles", asID), gin.H{"roleIds": []uint64{asRole, target}})
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		// 这个员工没有"查看角色"的权限：只知道不能分配，不知道是角色里的哪个权限码挡住的（D-069，第 152 条）
		require.Equal(t, "rbac.role.notAssignable", r.key())
		require.NotContains(t, r.rec.Body.String(), f.kind.Portal()+":", "响应里不该有角色里的权限码")
		// 多一个"查看角色"的员工：照样分配不出去，但看得到原因
		viewRole := f.role(own, "assigner-view", f.perm(orgportal.PermAccountList), f.perm(orgportal.PermAccountAssignRole), f.perm(orgportal.PermLoginLogList), f.perm(orgportal.PermRoleList))
		_, av := f.staff(own, o, "assignerview", viewRole)
		r = f.do(av, "PUT", fmt.Sprintf("/org/accounts/%d/roles", plainID), gin.H{"roleIds": []uint64{target}})
		require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
		require.Equal(t, "rbac.role.holdsSensitive", r.key())
		require.Contains(t, r.rec.Body.String(), f.kind.Portal()+":")
		roles := f.ok(own, "GET", fmt.Sprintf("/org/accounts/%d", plainID), nil).data()["roles"].([]any)
		require.Len(t, roles, 1)
		require.Equal(t, float64(low), roles[0].(map[string]any)["id"])

		// 别的主体的角色：按不存在处理，什么都不写
		o2 := f.newOrg("乙")
		own2 := f.owner(o2)
		other := f.role(own2, "theirs", f.perm(orgportal.PermLoginLogList))
		r = f.do(own, "PUT", fmt.Sprintf("/org/accounts/%d/roles", plainID), gin.H{"roleIds": []uint64{other}})
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		require.Equal(t, "rbac.role.notFound", r.key())
		r = f.do(own, "POST", "/org/accounts", gin.H{"username": "cross", "roleIds": []uint64{other}})
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		require.NoError(t, f.gdb.Table(f.user).Where("org_id = ? AND username = 'cross'", o.id).Count(&n).Error)
		require.Zero(t, n, "分配失败时账号也不留下")
	})
}

// 145. 跨主体：主体 A 的主账号对主体 B 的账号、角色、会话逐一做详情、修改、删除，一律 404、库里不变；列表、日志只有本主体的。
func TestOrgPortal_145_CrossOrg(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		a, b := f.newOrg("A"), f.newOrg("B")
		ownA, ownB := f.owner(a), f.owner(b)
		bRole := f.role(ownB, "b-role", f.perm(orgportal.PermLoginLogList))
		bStaff, _ := f.staff(ownB, b, "bstaff", bRole)
		bSID := f.activeSID(bStaff)
		aStaff, _ := f.staff(ownA, a, "astaff")

		before := f.snapshot(0)
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", fmt.Sprintf("/org/accounts/%d", bStaff), nil},
			{"GET", fmt.Sprintf("/org/accounts/%d", b.ownerID), nil},
			{"PUT", fmt.Sprintf("/org/accounts/%d", bStaff), gin.H{"displayName": "x"}},
			{"POST", fmt.Sprintf("/org/accounts/%d/status", bStaff), gin.H{"status": 0}},
			{"POST", fmt.Sprintf("/org/accounts/%d/reset-password", bStaff), nil},
			{"PUT", fmt.Sprintf("/org/accounts/%d/roles", bStaff), gin.H{"roleIds": []uint64{}}},
			{"PUT", fmt.Sprintf("/org/roles/%d", bRole), gin.H{"code": "b-role", "name": "x"}},
			{"DELETE", fmt.Sprintf("/org/roles/%d", bRole), nil},
			{"GET", fmt.Sprintf("/org/roles/%d/perms", bRole), nil},
			{"PUT", fmt.Sprintf("/org/roles/%d/perms", bRole), gin.H{"codes": []string{}}},
			{"POST", "/org/sessions/" + bSID + "/revoke", nil},
		} {
			r := f.do(ownA, c.method, c.path, c.body)
			require.Equal(t, 404, r.rec.Code, "%s %s: %s", c.method, c.path, r.rec.Body.String())
		}
		require.Equal(t, before, f.snapshot(0))

		// 列表只有本主体的
		for _, row := range f.ok(ownA, "GET", "/org/accounts?pageSize=100", nil).list() {
			require.Equal(t, float64(a.id), row["orgId"])
		}
		require.Len(t, f.ok(ownA, "GET", "/org/accounts?pageSize=100", nil).list(), 2)
		roles := f.ok(ownA, "GET", "/org/roles", nil).env.Data.([]any)
		require.Empty(t, roles, "A 没建过角色")
		for _, row := range f.ok(ownA, "GET", "/org/sessions?pageSize=100", nil).list() {
			require.Contains(t, []float64{float64(a.ownerID), float64(aStaff)}, row["userId"])
		}
		// 日志只有本主体的（B 那边登录过、建过角色）
		logins := f.ok(ownA, "GET", "/org/login-logs?pageSize=100", nil).list()
		require.NotEmpty(t, logins)
		for _, row := range logins {
			require.Equal(t, float64(a.id), row["orgId"])
		}
		ops := f.ok(ownA, "GET", "/org/operation-logs?pageSize=100", nil).list()
		require.NotEmpty(t, ops)
		for _, row := range ops {
			require.Equal(t, float64(a.id), row["orgId"])
		}
		// 看日志本身记一条操作日志（D-032）
		var n int64
		require.NoError(t, f.gdb.Table("ga_operation_log").Where("portal = ? AND org_id = ? AND action = ?", f.kind.Portal(), a.id, orgportal.OpViewOplog).Count(&n).Error)
		require.EqualValues(t, 1, n)
	})
}

// env 是"途中被停用"一个用例的主体：主账号（操作人）、一个员工和它的会话、一个角色、主账号的第二个会话。
type env struct {
	o        orgRef
	own      string
	staffID  uint64
	staffSID string
	roleID   uint64
}

func (f *fixture) newEnv(name string) env {
	f.t.Helper()
	o := f.newOrg(name)
	own := f.owner(o)
	f.login(o.code, "admin", newPass) // 第二个会话：下线其他设备有东西可下
	roleID := f.role(own, "r1", f.perm(orgportal.PermLoginLogList))
	staffID, _ := f.staff(own, o, "staff")
	return env{o: o, own: own, staffID: staffID, staffSID: f.activeSID(staffID), roleID: roleID}
}

// holdOrg 在另一个事务里锁住主体行、做 change 里的改动，等 release 再提交：请求在这期间通过认证、停在锁前面，
// 拿到锁时看到的是 change 提交之后的状态。
func (f *fixture) holdOrg(o orgRef, release <-chan struct{}, change func(tx *gorm.DB) error) <-chan error {
	done := make(chan error, 1)
	locked := make(chan struct{})
	go func() {
		done <- f.gdb.Transaction(func(tx *gorm.DB) error {
			defer close(locked)
			var id uint64
			if err := tx.Raw("SELECT id FROM ga_"+f.kind.Portal()+" WHERE id = ? FOR UPDATE", o.id).Scan(&id).Error; err != nil {
				return err
			}
			if err := change(tx); err != nil {
				return err
			}
			locked <- struct{}{}
			<-release
			return nil
		})
	}()
	<-locked
	return done
}

// 途中的四种改动：停用账号并吊销它的全部会话；只停用（会话没吊销）；只吊销会话（账号仍启用）；
// 只收回账号的全部角色（账号、会话都还有效）。
func (f *fixture) disableAndRevoke(userID uint64) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		if err := f.disableOnly(userID)(tx); err != nil {
			return err
		}
		return f.revokeOnly(userID)(tx)
	}
}

func (f *fixture) disableOnly(userID uint64) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Exec("UPDATE "+f.user+" SET status = 0 WHERE id = ?", userID).Error
	}
}

func (f *fixture) revokeOnly(userID uint64) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'disabled' WHERE portal = ? AND user_id = ? AND revoked_at IS NULL", f.kind.Portal(), userID).Error
	}
}

func (f *fixture) dropRoles(userID uint64) func(tx *gorm.DB) error {
	return func(tx *gorm.DB) error {
		return tx.Exec("DELETE FROM ga_user_role WHERE portal = ? AND user_id = ?", f.kind.Portal(), userID).Error
	}
}

// midFlight 发出请求，让它通过认证、停在主体行锁前面时提交 change，返回响应。
func (f *fixture) midFlight(o orgRef, change func(tx *gorm.DB) error, tok, method, path string, body any) resp {
	f.t.Helper()
	release := make(chan struct{})
	held := f.holdOrg(o, release, change)
	got := make(chan resp, 1)
	go func() { got <- f.do(tok, method, path, body) }()
	time.Sleep(300 * time.Millisecond) // 让请求通过认证、走到锁前面
	close(release)
	require.NoError(f.t, <-held)
	select {
	case r := <-got:
		return r
	case <-time.After(30 * time.Second):
		f.t.Fatalf("%s %s：放开锁之后请求没有返回", method, path)
		return resp{}
	}
}

// 146. 每一条写接口都在锁里重新认定操作人（规范 §13.2 第 72、140 条的同一做法）：按路由表遍历、双向核对；
// 请求通过认证、还没拿到主体行锁（个人中心：账号行锁、会话行锁）时操作人被停用、会话被吊销，拿到锁后必须回 401，什么都不写。
// 几种改动分开各走一遍，认定不能只靠其中一样：停用并吊销；只停用（会话没吊销）；只吊销会话（账号仍启用）；
// 只收回员工的角色（账号、会话都还有效，需要权限码的路由回 403）。对照：同样的请求由正常的主账号、角色还在的员工发出确实会写。
func TestOrgPortal_146_EveryWriteRouteRechecksActor(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		type routeCase struct {
			method, route string
			build         func(e env) (string, any)
		}
		cases := []routeCase{
			{"POST", "/org/accounts", func(e env) (string, any) {
				return "/org/accounts", gin.H{"username": "newbie", "password": staffPass}
			}},
			{"PUT", "/org/accounts/:id", func(e env) (string, any) {
				return fmt.Sprintf("/org/accounts/%d", e.staffID), gin.H{"displayName": "改名"}
			}},
			{"POST", "/org/accounts/:id/status", func(e env) (string, any) {
				return fmt.Sprintf("/org/accounts/%d/status", e.staffID), gin.H{"status": 0}
			}},
			{"POST", "/org/accounts/:id/reset-password", func(e env) (string, any) {
				return fmt.Sprintf("/org/accounts/%d/reset-password", e.staffID), nil
			}},
			{"PUT", "/org/accounts/:id/roles", func(e env) (string, any) {
				return fmt.Sprintf("/org/accounts/%d/roles", e.staffID), gin.H{"roleIds": []uint64{e.roleID}}
			}},
			{"POST", "/org/roles", func(e env) (string, any) { return "/org/roles", gin.H{"code": "r2", "name": "新角色"} }},
			{"PUT", "/org/roles/:id", func(e env) (string, any) {
				return fmt.Sprintf("/org/roles/%d", e.roleID), gin.H{"code": "r1", "name": "改名"}
			}},
			{"DELETE", "/org/roles/:id", func(e env) (string, any) { return fmt.Sprintf("/org/roles/%d", e.roleID), nil }},
			{"PUT", "/org/roles/:id/perms", func(e env) (string, any) {
				return fmt.Sprintf("/org/roles/%d/perms", e.roleID), gin.H{"codes": []string{f.perm(orgportal.PermOplogList)}}
			}},
			{"POST", "/org/sessions/:sid/revoke", func(e env) (string, any) { return "/org/sessions/" + e.staffSID + "/revoke", nil }},
			{"PUT", "/org/ip-allow", func(e env) (string, any) {
				return "/org/ip-allow", gin.H{"items": []gin.H{{"cidr": clientIP}}}
			}},
			{"PUT", "/org/profile", func(e env) (string, any) { return "/org/profile", gin.H{"displayName": "我自己"} }},
			{"PUT", "/org/profile/avatar", func(e env) (string, any) { return "/org/profile/avatar", gin.H{"preset": "ocean"} }},
			{"POST", "/org/profile/revoke-other-sessions", func(e env) (string, any) { return "/org/profile/revoke-other-sessions", nil }},
			{"POST", "/org/ip-deny", func(e env) (string, any) { return "/org/ip-deny", gin.H{"cidr": "198.51.100.9"} }},
			{"DELETE", "/org/ip-deny/:id", func(e env) (string, any) {
				r := f.ok(e.own, "POST", "/org/ip-deny", gin.H{"cidr": "198.51.100.9"})
				return fmt.Sprintf("/org/ip-deny/%.0f", r.data()["id"]), nil
			}},
		}
		want := map[string]bool{}
		for _, r := range f.app.Routes() {
			if r.Portal == f.kind.Portal() && r.Method != "GET" && strings.HasPrefix(r.Path, f.base+"/org/") {
				want[r.Method+" "+r.Path] = true
			}
		}
		have := map[string]bool{}
		for _, c := range cases {
			key := c.method + " " + f.base + c.route
			require.False(t, have[key], "重复的用例：%s", key)
			have[key] = true
		}
		require.Equal(t, want, have, "用例表和套件的写路由不一致")

		// 停用并吊销；只停用（会话没吊销）；只吊销会话（账号仍启用）：都是 401、什么都不写
		for _, mode := range []struct {
			name   string
			change func(userID uint64) func(tx *gorm.DB) error
		}{{"停用并下线", f.disableAndRevoke}, {"只停用", f.disableOnly}, {"只下线", f.revokeOnly}} {
			for i, c := range cases {
				e := f.newEnv(fmt.Sprintf("%s%02d", mode.name, i))
				path, body := c.build(e)
				before := f.snapshot(e.o.ownerID)
				r := f.midFlight(e.o, mode.change(e.o.ownerID), e.own, c.method, path, body)
				require.Equal(t, 401, r.rec.Code, "%s：%s %s: %s", mode.name, c.method, c.route, r.rec.Body.String())
				after := f.snapshot(e.o.ownerID)
				for tb := range before {
					require.Equal(t, before[tb], after[tb], "%s：%s %s 改动了 %s", mode.name, c.method, c.route, tb)
				}
			}
		}

		// 只收回员工的角色：账号、会话都还有效，需要权限码的路由按锁内的授权回 403、什么都不写；
		// 对照：角色还在的同一个员工发同样的请求确实会写。只有主账号能做的接口、个人中心不看权限码，不在这里
		guarded := 0
		for i, c := range cases {
			if c.route == "/org/accounts/:id/reset-password" || c.route == "/org/ip-allow" || strings.HasPrefix(c.route, "/org/ip-deny") || strings.HasPrefix(c.route, "/org/profile") {
				continue
			}
			guarded++
			for _, revoked := range []bool{true, false} {
				e := f.newEnv(fmt.Sprintf("收权%02d%t", i, revoked))
				mgrID, mgr := f.staff(e.own, e.o, "mgr", f.role(e.own, "mgr", f.allPerms()...))
				path, body := c.build(e)
				before := f.snapshot(0)
				if !revoked {
					f.ok(mgr, c.method, path, body)
					require.NotEqual(t, before, f.snapshot(0), "%s %s 没有改动任何东西", c.method, c.route)
					continue
				}
				r := f.midFlight(e.o, f.dropRoles(mgrID), mgr, c.method, path, body)
				require.Equal(t, 403, r.rec.Code, "%s %s: %s", c.method, c.route, r.rec.Body.String())
				after := f.snapshot(0)
				delete(before, "ga_user_role") // 收回角色本身
				delete(after, "ga_user_role")
				require.Equal(t, before, after, "%s %s", c.method, c.route)
			}
		}
		require.Equal(t, 9, guarded, "需要权限码的写路由数变了：核对上面的排除条件")

		// 只有主账号能做的接口：途中被平台换下的主账号，拿到锁后按新状态 403，什么都不写
		for i, c := range []routeCase{cases[3], cases[10], cases[14], cases[15]} {
			e := f.newEnv(fmt.Sprintf("换人%02d", i))
			path, body := c.build(e)
			before := f.snapshot(0)
			release := make(chan struct{})
			held := make(chan error, 1)
			locked := make(chan struct{})
			go func() {
				held <- f.gdb.Transaction(func(tx *gorm.DB) error {
					err := tx.Exec("UPDATE ga_"+f.kind.Portal()+" SET owner_user_id = ? WHERE id = ?", e.staffID, e.o.id).Error
					close(locked)
					<-release
					return err
				})
			}()
			<-locked
			got := make(chan resp, 1)
			go func() { got <- f.do(e.own, c.method, path, body) }()
			time.Sleep(300 * time.Millisecond)
			close(release)
			require.NoError(t, <-held)
			r := <-got
			require.Equal(t, 403, r.rec.Code, "%s %s: %s", c.method, c.route, r.rec.Body.String())
			after := f.snapshot(0)
			delete(before, "ga_"+f.kind.Portal())
			delete(after, "ga_"+f.kind.Portal())
			require.Equal(t, before, after, "%s %s", c.method, c.route)
		}

		for i, c := range cases {
			e := f.newEnv(fmt.Sprintf("对照%02d", i))
			path, body := c.build(e)
			before := f.snapshot(0)
			f.ok(e.own, c.method, path, body)
			require.NotEqual(t, before, f.snapshot(0), "%s %s 没有改动任何东西", c.method, c.route)
		}
	})
}

// 147. 主体参数守卫（D-067 第 5 条）：主体端每条非公开路由，查询串带 orgId、请求体带 merchantId 一律 3002；
// 不分大小写、忽略 '_' 和 '-'、复数也算，JSON 任意一层、表单、multipart、没标 JSON 的请求体都查；值里出现这些字不算；被拒的不写。
func TestOrgPortal_147_OrgParamsRejected(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		e := f.newEnv("甲")
		fill := strings.NewReplacer(":id", fmt.Sprint(e.staffID), ":sid", e.staffSID)
		before := f.snapshot(0)
		n := 0
		for _, r := range f.app.Routes() {
			if r.Portal != f.kind.Portal() || r.Guard == rbac.GuardPublic {
				continue
			}
			path := fill.Replace(strings.TrimPrefix(r.Path, f.base))
			res := f.raw(e.own, r.Method, path+"?orgId=1", "", nil, clientIP)
			require.Equal(t, httpx.CodeBadRequest, res.env.Code, "%s %s: %s", r.Method, r.Path, res.rec.Body.String())
			require.Equal(t, "scope.orgParam", res.key())
			if r.Method != "GET" {
				res = f.do(e.own, r.Method, path, gin.H{"merchantId": 1})
				require.Equal(t, httpx.CodeBadRequest, res.env.Code, "%s %s: %s", r.Method, r.Path, res.rec.Body.String())
			}
			n++
		}
		require.Greater(t, n, 25, "主体端的已登录路由都要走到")
		require.Equal(t, before, f.snapshot(0), "被拒的请求什么都不写")

		// 各种写法
		reject := func(name, path, contentType string, body []byte) {
			t.Helper()
			r := f.raw(e.own, "POST", path, contentType, body, clientIP)
			require.Equal(t, httpx.CodeBadRequest, r.env.Code, "%s: %s", name, r.rec.Body.String())
			require.Equal(t, "scope.orgParam", r.key(), name)
		}
		for _, q := range []string{"ORGID=1", "org_id=1", "Org-Id=1", "agentIds=1", "merchantid=2&x=1", "orgId[]=1", "org[id]=1", "merchant.id=1"} {
			reject("query "+q, "/org/roles?"+q, "application/json", []byte(`{"code":"q1","name":"q1"}`))
		}
		for _, b := range []string{
			`{"code":"j1","name":"j1","AgentID":1}`,
			`{"code":"j1","name":"j1","org_id":1}`,
			`{"code":"j1","name":"j1","extra":{"deep":[{"merchantIds":[1]}]}}`,
			`{"code":"j1","name":"j1","merchantIdſ":[1]}`, // 长 s：encoding/json 按大小写折叠会把它当成 merchantIds
		} {
			reject("json "+b, "/org/roles", "application/json", []byte(b))
			// 绑定 JSON 不看 Content-Type：标成别的类型（包括表单、没有分隔符的 multipart）的 JSON 请求体照样查
			reject("text/plain "+b, "/org/roles", "text/plain", []byte(b))
			reject("no type "+b, "/org/roles", "", []byte(b))
			reject("form type "+b, "/org/roles", "application/x-www-form-urlencoded", []byte(b))
			reject("multipart no boundary "+b, "/org/roles", "multipart/form-data", []byte(b))
			reject("multipart bad boundary "+b, "/org/roles", "multipart/form-data; boundary=zzz", []byte(b))
		}
		reject("form", "/org/roles", "application/x-www-form-urlencoded", []byte("code=f1&name=f1&orgId=1"))
		var mb bytes.Buffer
		mw := multipart.NewWriter(&mb)
		require.NoError(t, mw.WriteField("name", "m1"))
		require.NoError(t, mw.WriteField("agent_id", "1"))
		require.NoError(t, mw.Close())
		reject("multipart", "/org/roles", mw.FormDataContentType(), mb.Bytes())
		require.Equal(t, before, f.snapshot(0))

		// 值里出现这些字、键只是相近的，照常通过
		f.ok(e.own, "POST", "/org/roles", gin.H{"code": "ok1", "name": "orgId", "remark": "merchantId agentId"})
		f.ok(e.own, "GET", "/org/accounts?keyword=orgId&orgCode=1", nil)
		f.ok(e.own, "GET", "/auth/me", nil)
	})
}

// 148. 概览、主体的 IP 白名单、个人中心。
func TestOrgPortal_148_OverviewIPAllowProfile(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		e := f.newEnv("甲")
		_, st := f.staff(e.own, e.o, "viewer", f.role(e.own, "viewer", f.perm(orgportal.PermAccountList)))

		// 概览：主账号看到全部计数；员工只看到有查看权限的；平台写的备注不给主体看
		r := f.ok(e.own, "GET", "/org/overview", nil)
		require.Equal(t, e.o.code, r.data()["org"].(map[string]any)["code"])
		require.NotContains(t, r.rec.Body.String(), "平台的备注")
		require.NotContains(t, r.data()["org"], "remark")
		require.Equal(t, true, r.data()["owner"])
		require.Equal(t, map[string]any{"accounts": float64(3), "sessions": float64(4), "roles": float64(2)}, r.data()["counts"])
		r = f.ok(st, "GET", "/org/overview", nil)
		require.Equal(t, false, r.data()["owner"])
		require.Equal(t, map[string]any{"accounts": float64(3)}, r.data()["counts"])

		// IP 白名单：只有主账号；名单必须包含主账号自己的地址；名单外的来源 2003
		require.Equal(t, 403, f.do(st, "GET", "/org/ip-allow", nil).rec.Code)
		r = f.do(e.own, "PUT", "/org/ip-allow", gin.H{"items": []gin.H{{"cidr": "198.51.100.0/24"}}})
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		f.ok(e.own, "PUT", "/org/ip-allow", gin.H{"items": []gin.H{{"cidr": "203.0.113.0/24"}}})
		r = f.ok(e.own, "GET", "/org/ip-allow", nil)
		require.Equal(t, clientIP, r.data()["yourIp"])
		require.Len(t, r.data()["items"], 1)
		require.Equal(t, httpx.CodeIPDenied, f.raw(st, "GET", "/org/overview", "", nil, "198.51.100.7").env.Code)
		f.ok(e.own, "PUT", "/org/ip-allow", gin.H{"items": []gin.H{}})
		f.ok(st, "GET", "/org/overview", nil)

		// 个人中心：只读写本人
		ownerName := f.ok(e.own, "GET", "/org/profile", nil).data()["displayName"]
		r = f.ok(st, "GET", "/org/profile", nil)
		require.Equal(t, "viewer", r.data()["username"])
		require.Equal(t, e.o.code, r.data()["orgCode"])
		f.ok(st, "PUT", "/org/profile", gin.H{"displayName": "看看而已", "email": "v@example.com"})
		require.Equal(t, "看看而已", f.ok(st, "GET", "/org/profile", nil).data()["displayName"])
		r = f.do(st, "PUT", "/org/profile", gin.H{"displayName": "坏\u200b名字"})
		require.Equal(t, httpx.CodeValidation, r.env.Code)
		require.Equal(t, "preset:ocean", f.ok(st, "PUT", "/org/profile/avatar", gin.H{"preset": "ocean"}).data()["avatar"])
		r = f.do(st, "PUT", "/org/profile/avatar", gin.H{"preset": "upload:abc"})
		require.Equal(t, httpx.CodeValidation, r.env.Code)
		require.Equal(t, "", f.ok(st, "PUT", "/org/profile/avatar", gin.H{"preset": ""}).data()["avatar"])
		// 下线其他设备：主账号有两个会话，下线一个，当前的还能用
		r = f.ok(e.own, "POST", "/org/profile/revoke-other-sessions", nil)
		require.Equal(t, float64(1), r.data()["revoked"])
		f.ok(e.own, "GET", "/org/profile", nil)
		// 个人中心只按身份改本人：员工改了自己，主账号的资料不变
		require.Equal(t, ownerName, f.ok(e.own, "GET", "/org/profile", nil).data()["displayName"])
	})
}

// hashProbe 包在 auth.Service 外面：数生成密码哈希的次数；hold 不为 nil 时每次进来先报到、等放行再算；
// full 时像进程的密码计算闸门占满那样回 429。
type hashProbe struct {
	auth.Service
	calls atomic.Int64
	hold  atomic.Pointer[hashHold]
	full  atomic.Bool
}

type hashHold struct {
	entered chan struct{}
	release chan struct{}
}

func (p *hashProbe) HashPassword(portalCode, plain string) (string, error) {
	if p.full.Load() {
		return "", httpx.ErrTooManyRequests
	}
	p.calls.Add(1)
	if h := p.hold.Load(); h != nil {
		h.entered <- struct{}{}
		<-h.release
	}
	return p.Service.HashPassword(portalCode, plain)
}

// 151. 建子账号、重置员工密码要算密码哈希（D-068）：便宜的检查先做，注定失败的请求不去算；一个主体同时最多 2 个在算、
// 每分钟最多 30 次（两条接口合起来），超出回 429、什么都不写；别的主体不受影响；没通过便宜检查的请求、
// 被进程的闸门挡回来的请求不占次数。
func TestOrgPortal_151_PasswordHashingLimited(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		deps := f.app.Deps()
		probe := &hashProbe{Service: deps.Auth}
		deps.Auth = probe // 套件每次都从 Deps 取
		a, b := f.newOrg("甲"), f.newOrg("乙")
		ownA, ownB := f.owner(a), f.owner(b)
		staffA, _ := f.staff(ownA, a, "staff")
		staffB, _ := f.staff(ownB, b, "staff")
		reset := func(id uint64) string { return fmt.Sprintf("/org/accounts/%d/reset-password", id) }
		accounts := func(o orgRef) (n int64) {
			require.NoError(t, f.gdb.Table(f.user).Where("org_id = ?", o.id).Count(&n).Error)
			return n
		}

		// 便宜的检查先做：这些请求一次哈希都不算
		base, rows := probe.calls.Load(), accounts(a)
		for _, c := range []struct {
			name, path string
			body       any
			status     int
			code       int
			key        string
		}{
			{"登录名不合法", "/org/accounts", gin.H{"username": "9x", "password": staffPass}, 200, httpx.CodeValidation, "org.username"},
			{"显示名带控制字符", "/org/accounts", gin.H{"username": "newbie", "password": staffPass, "displayName": "a\u0007b"}, 200, httpx.CodeValidation, "org.textChars"},
			{"密码不合策略", "/org/accounts", gin.H{"username": "newbie", "password": "short"}, 200, httpx.CodeValidation, ""},
			{"登录名已被占用", "/org/accounts", gin.H{"username": "STAFF", "password": staffPass}, 200, httpx.CodeConflict, "org.usernameTaken"},
			{"重置不存在的账号", reset(987654), nil, 404, httpx.CodeNotFound, ""},
			{"重置别的主体的账号", reset(staffB), nil, 404, httpx.CodeNotFound, ""},
			{"重置主账号", reset(a.ownerID), nil, 403, httpx.CodeForbidden, "org.account.resetOwner"},
		} {
			r := f.do(ownA, "POST", c.path, c.body)
			require.Equal(t, c.status, r.rec.Code, "%s: %s", c.name, r.rec.Body.String())
			require.Equal(t, c.code, r.env.Code, "%s: %s", c.name, r.rec.Body.String())
			if c.key != "" {
				require.Equal(t, c.key, r.key(), c.name)
			}
			require.Equal(t, base, probe.calls.Load(), "%s：不该去算哈希", c.name)
		}
		require.Equal(t, rows, accounts(a))

		// 同时在算的上限：甲有 2 个停在哈希里，第 3 个建账号、重置密码都立即回 429、不算、不写；乙照常进得去。
		// 放行之后两个位置都让出来：再来一轮，甲照样能有 2 个同时进去（只让出 1 个的话第二轮进不去 2 个）
		for round := 1; round <= 2; round++ {
			hold := &hashHold{entered: make(chan struct{}, 8), release: make(chan struct{})}
			probe.hold.Store(hold)
			released := false
			letGo := func() {
				if !released {
					released = true
					probe.hold.Store(nil)
					close(hold.release)
				}
			}
			defer letGo() // 中途失败也放行，不留停着的请求
			entered := func(msg string) {
				t.Helper()
				select {
				case <-hold.entered:
				case <-time.After(5 * time.Second):
					t.Fatalf("第 %d 轮：%s", round, msg)
				}
			}
			busy1, busy3 := fmt.Sprintf("busy1r%d", round), fmt.Sprintf("busy3r%d", round)
			got := make(chan resp, 3)
			go func() { got <- f.do(ownA, "POST", "/org/accounts", gin.H{"username": busy1, "password": staffPass}) }()
			go func() { got <- f.do(ownA, "POST", reset(staffA), nil) }()
			entered("甲的第 1 个请求没有进去")
			entered("甲的第 2 个请求没有进去")
			base, rows = probe.calls.Load(), accounts(a)
			before := f.snapshot(0)
			for _, c := range []struct {
				path string
				body any
			}{{"/org/accounts", gin.H{"username": busy3, "password": staffPass}}, {reset(staffA), nil}} {
				busy := make(chan resp, 1)
				go func() { busy <- f.do(ownA, "POST", c.path, c.body) }()
				select {
				case r := <-busy:
					require.Equal(t, 429, r.rec.Code, "%s: %s", c.path, r.rec.Body.String())
					require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
				case <-time.After(5 * time.Second):
					t.Fatalf("%s：甲占满之后没有立即回 429", c.path)
				}
			}
			require.Equal(t, base, probe.calls.Load(), "被挡的请求没有去算哈希")
			require.Equal(t, before, f.snapshot(0), "被挡的请求什么都没写")
			go func() { got <- f.do(ownB, "POST", "/org/accounts", gin.H{"username": busy1, "password": staffPass}) }()
			entered("甲占满之后乙的请求没有进去")
			letGo()
			for i := 0; i < 3; i++ {
				select {
				case r := <-got:
					require.Equal(t, 0, r.env.Code, r.rec.Body.String())
				case <-time.After(10 * time.Second):
					t.Fatal("放行之后停着的请求没有完成")
				}
			}
			require.Equal(t, rows+1, accounts(a), "放行之后停着的请求照常完成")
		}

		// 每分钟的次数：一个新主体两条接口合起来 30 次，第 31 次回 429、不算、不写；没通过便宜检查的、
		// 被进程的闸门挡回来的（没有算）都不占次数；别的主体不受影响
		c := f.newOrg("丙")
		ownC := f.owner(c)
		first := f.ok(ownC, "POST", "/org/accounts", gin.H{"username": "u00", "password": staffPass})
		uid := uint64(first.data()["account"].(map[string]any)["id"].(float64))
		free := []struct {
			path string
			body any
			code int
		}{
			{"/org/accounts", gin.H{"username": "u00", "password": staffPass}, httpx.CodeConflict},
			{"/org/accounts", gin.H{"username": "9x", "password": staffPass}, httpx.CodeValidation},
			{"/org/accounts", gin.H{"username": "fresh", "password": "short"}, httpx.CodeValidation},
			{reset(staffB), nil, httpx.CodeNotFound},
			{reset(c.ownerID), nil, httpx.CodeForbidden},
		}
		for i := 1; i < 30; i++ {
			if i%3 == 0 {
				f.ok(ownC, "POST", reset(uid), nil)
			} else {
				f.ok(ownC, "POST", "/org/accounts", gin.H{"username": fmt.Sprintf("u%02d", i), "password": staffPass})
			}
			if i%10 != 0 {
				continue
			}
			for _, fr := range free { // 不占次数的
				require.Equal(t, fr.code, f.do(ownC, "POST", fr.path, fr.body).env.Code, fr.path)
			}
			probe.full.Store(true) // 进程的闸门满了：429、没有算，也不占次数
			for _, path := range []string{"/org/accounts", reset(uid)} {
				r := f.do(ownC, "POST", path, gin.H{"username": "gatefull", "password": staffPass})
				require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
			}
			probe.full.Store(false)
		}
		base = probe.calls.Load()
		before := f.snapshot(0)
		for _, cs := range []struct {
			path string
			body any
		}{{"/org/accounts", gin.H{"username": "over", "password": staffPass}}, {reset(uid), nil}} {
			r := f.do(ownC, "POST", cs.path, cs.body)
			require.Equal(t, 429, r.rec.Code, "%s: %s", cs.path, r.rec.Body.String())
			require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
		}
		require.Equal(t, base, probe.calls.Load(), "超出次数的请求没有去算哈希")
		require.Equal(t, before, f.snapshot(0), "超出次数的请求什么都没写")
		f.ok(ownB, "POST", "/org/accounts", gin.H{"username": "after", "password": staffPass})
		f.ok(ownB, "POST", reset(staffB), nil)
	})
}

// 155. 主体账号表（D-070）：升级前留下的 bcrypt 哈希照常按编号登录，登录成功后库里换成 Argon2id，只动哈希这一列；
// 套件建的子账号、重置的密码写进去的就是 Argon2id。
func TestOrgPortal_155_LegacyHashUpgradedOnLogin(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o := f.newOrg("甲")
		own := f.owner(o)
		staffID, _ := f.staff(own, o, "staff")
		row := func(id uint64) (r struct {
			PasswordHash  string
			MustChangePwd bool
			PwdChangedAt  string
			UpdatedAt     string
		}) {
			require.NoError(t, f.gdb.Raw("SELECT password_hash, must_change_pwd, CAST(pwd_changed_at AS CHAR) AS pwd_changed_at, CAST(updated_at AS CHAR) AS updated_at FROM "+f.user+" WHERE id = ?", id).Scan(&r).Error)
			return r
		}
		require.True(t, strings.HasPrefix(row(o.ownerID).PasswordHash, "$argon2id$"), "主账号改密写进去的是 Argon2id")
		require.True(t, strings.HasPrefix(row(staffID).PasswordHash, "$argon2id$"), "子账号改密写进去的是 Argon2id")

		old, err := bcrypt.GenerateFromPassword([]byte("legacy-pass-123"), bcrypt.MinCost)
		require.NoError(t, err)
		require.NoError(t, f.gdb.Exec("UPDATE "+f.user+" SET password_hash = ? WHERE id = ?", string(old), staffID).Error)
		before := row(staffID)
		r := f.do("", "POST", "/auth/login", gin.H{"org": o.code, "username": "staff", "password": "wrong-pass-123"})
		require.NotEqual(t, 0, r.env.Code)
		require.Equal(t, before, row(staffID), "密码错：什么都不变")

		tok := f.login(o.code, "staff", "legacy-pass-123")
		after := row(staffID)
		require.True(t, strings.HasPrefix(after.PasswordHash, "$argon2id$v=19$"), after.PasswordHash)
		before.PasswordHash, after.PasswordHash = "", ""
		require.Equal(t, before, after, "只换哈希这一列")
		f.ok(tok, "GET", "/org/profile", nil)
		upgraded := row(staffID).PasswordHash
		f.login(o.code, "staff", "legacy-pass-123")
		require.Equal(t, upgraded, row(staffID).PasswordHash, "已经升级过的不再重写")
		f.ok(tok, "GET", "/org/profile", nil)

		// 主账号重置员工密码、新建子账号：写进去的是 Argon2id
		f.ok(own, "POST", fmt.Sprintf("/org/accounts/%d/reset-password", staffID), nil)
		require.True(t, strings.HasPrefix(row(staffID).PasswordHash, "$argon2id$"))
		created := f.ok(own, "POST", "/org/accounts", gin.H{"username": "fresh", "password": staffPass})
		require.True(t, strings.HasPrefix(row(uint64(created.data()["account"].(map[string]any)["id"].(float64))).PasswordHash, "$argon2id$"))
	})
}

// 162. 套件的路由全部"按库核对"（D-073）：独立的平台主体管理服务（共库、不配 Redis，或直接改库，都不经过
// 本程序的认证器）停用主体、吊销会话、重置主账号密码、更换主账号、停用账号、要求改密之后，套件的每一条读接口在
// 下一个请求就按新状态处理，不等状态缓存到期。按路由表遍历、双向核对。对照：同一个令牌访问没标的路由（/auth/me）
// 在这之前照旧放行——缓存确实是热的、改动确实没有经过本程序；按库核对的那一次读到了变化，缓存跟着清掉，
// /auth/me 随后也按新状态处理。
func TestOrgPortal_162_BackOfficeSeesRevocationAtOnce(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		// 模拟另一个程序：管理服务和数据库上下文独立，不接主体端应用的本地失效回调（D-075）。
		orgs := org.New(org.Options{})
		platformCtx := db.WithDB(context.Background(), f.gdb)
		// who 是一次遍历里的角色：主体、主账号和它当前的令牌、一个没有角色的员工和他的令牌、一个角色
		type who struct {
			o        orgRef
			own      string
			staffID  uint64
			staffTok string
			roleID   uint64
		}
		setup := func(name string) *who {
			o := f.newOrg(name)
			own := f.owner(o)
			roleID := f.role(own, "r1", f.perm(orgportal.PermLoginLogList))
			staffID, staffTok := f.staff(own, o, "staff")
			return &who{o: o, own: own, staffID: staffID, staffTok: staffTok, roleID: roleID}
		}
		type routeCase struct {
			route string
			build func(w *who) string
		}
		fixed := func(p string) routeCase { return routeCase{p, func(*who) string { return p }} }
		cases := []routeCase{
			fixed("/org/overview"), fixed("/org/dashboard"), fixed("/org/ip-deny"), fixed("/org/ip-allow"), fixed("/org/accounts"),
			{"/org/accounts/:id", func(w *who) string { return fmt.Sprintf("/org/accounts/%d", w.staffID) }},
			fixed("/org/roles"),
			{"/org/roles/:id/perms", func(w *who) string { return fmt.Sprintf("/org/roles/%d/perms", w.roleID) }},
			fixed("/org/perms/tree"), fixed("/org/sessions"), fixed("/org/login-logs"), fixed("/org/operation-logs"),
			fixed("/org/profile"),
		}
		// 路由表：/org/ 下的每一条都标了（读和写），认证接口没标；用例表和套件的读路由一致
		guards, want := map[string]rbac.GuardKind{}, map[string]bool{}
		for _, r := range f.app.Routes() {
			if r.Portal != f.kind.Portal() {
				continue
			}
			inKit := strings.HasPrefix(r.Path, f.base+"/org/")
			require.Equal(t, inKit, r.LiveAuth, "%s %s", r.Method, r.Path)
			if inKit && r.Method == "GET" {
				want[r.Path], guards[r.Path] = true, r.Guard
			}
		}
		have := map[string]bool{}
		for _, c := range cases {
			require.False(t, have[f.base+c.route], "重复的用例：%s", c.route)
			have[f.base+c.route] = true
		}
		require.Equal(t, want, have, "用例表和套件的读路由不一致")

		me := func(tok string) resp { return f.do(tok, "GET", "/auth/me", nil) }
		meUser := func(tok string) map[string]any {
			u, _ := f.ok(tok, "GET", "/auth/me", nil).data()["user"].(map[string]any)
			require.NotNil(t, u)
			return u
		}
		relogin := func(w *who) { w.own = f.login(w.o.code, "admin", newPass) }
		sql := func(q string, args ...any) { require.NoError(t, f.gdb.Exec(q, args...).Error) }
		modes := []struct {
			name    string
			change  func(w *who)               // 别的程序做的事
			want    func(g rbac.GuardKind) int // 按库核对的路由随后的 HTTP 状态
			code    int                        // 信封里的错误码，0 表示不看
			after   func(w *who)               // 拒绝之后，没标的路由（/auth/me）也跟上了
			restore func(w *who)               // 恢复原状，下一条路由接着用
		}{
			{
				name:   "平台停用主体",
				change: func(w *who) { require.NoError(t, orgs.SetStatus(platformCtx, f.kind, w.o.id, org.StatusDisabled, 1)) },
				want:   func(rbac.GuardKind) int { return 401 },
				after:  func(w *who) { require.Equal(t, 401, me(w.own).rec.Code) },
				restore: func(w *who) {
					require.NoError(t, orgs.SetStatus(platformCtx, f.kind, w.o.id, org.StatusEnabled, 1))
					relogin(w)
				},
			},
			{
				name: "平台吊销会话",
				change: func(w *who) {
					var sids []string
					require.NoError(t, f.gdb.Raw("SELECT sid FROM ga_session WHERE portal = ? AND user_id = ? AND revoked_at IS NULL", f.kind.Portal(), w.o.ownerID).Scan(&sids).Error)
					require.NotEmpty(t, sids)
					for _, sid := range sids {
						require.NoError(t, orgs.RevokeSession(platformCtx, f.kind, w.o.id, sid))
					}
				},
				want:    func(rbac.GuardKind) int { return 401 },
				after:   func(w *who) { require.Equal(t, 401, me(w.own).rec.Code) },
				restore: relogin,
			},
			{
				name: "平台重置主账号密码",
				change: func(w *who) {
					pwd, err := orgs.NewInitialPassword()
					require.NoError(t, err)
					require.NoError(t, orgs.ResetOwnerPassword(platformCtx, f.kind, w.o.id, pwd))
					w.o.password = pwd.Plain()
				},
				want:    func(rbac.GuardKind) int { return 401 },
				after:   func(w *who) { require.Equal(t, 401, me(w.own).rec.Code) },
				restore: func(w *who) { w.own = f.owner(w.o) },
			},
			{
				name:   "平台更换主账号",
				change: func(w *who) { require.NoError(t, orgs.ChangeOwner(platformCtx, f.kind, w.o.id, w.staffID, 1)) },
				// 原来的主账号留下、不再是主账号；它的会话在同一个事务里全部吊销（D-101）：按库核对的路由立即 401
				want: func(rbac.GuardKind) int { return 401 },
				after: func(w *who) {
					require.Equal(t, 401, me(w.own).rec.Code, "原来的主账号")
					// 重新登录后是普通账号：要主账号的接口 403
					w.own = f.login(w.o.code, "admin", newPass)
					require.Equal(t, false, meUser(w.own)["super"], "原来的主账号")
					require.Equal(t, 403, f.do(w.own, "GET", "/org/ip-allow", nil).rec.Code)
					// 换上来的主账号立即生效，原有的会话照常：只有主账号能看的 IP 白名单
					require.Equal(t, 200, f.do(w.staffTok, "GET", "/org/ip-allow", nil).rec.Code)
					require.Equal(t, true, meUser(w.staffTok)["super"])
				},
				restore: func(w *who) {
					require.NoError(t, orgs.ChangeOwner(platformCtx, f.kind, w.o.id, w.o.ownerID, 1))
					require.Equal(t, 401, f.do(w.staffTok, "GET", "/org/ip-allow", nil).rec.Code, "换回去之后，刚才那位主账号的会话同样全部吊销")
					w.staffTok = f.login(w.o.code, "staff", newPass)
					require.Equal(t, 403, f.do(w.staffTok, "GET", "/org/ip-allow", nil).rec.Code, "重新登录后是普通员工")
					// 换回来的主账号原有的会话照常，并且立即是主账号
					require.Equal(t, 200, f.do(w.own, "GET", "/org/ip-allow", nil).rec.Code)
				},
			},
			{
				name:    "账号在库里被停用",
				change:  func(w *who) { sql("UPDATE "+f.user+" SET status = 0 WHERE id = ?", w.o.ownerID) },
				want:    func(rbac.GuardKind) int { return 401 },
				after:   func(w *who) { require.Equal(t, 401, me(w.own).rec.Code) },
				restore: func(w *who) { sql("UPDATE "+f.user+" SET status = 1 WHERE id = ?", w.o.ownerID) },
			},
			{
				name:    "账号在库里被要求改密",
				change:  func(w *who) { sql("UPDATE "+f.user+" SET must_change_pwd = 1 WHERE id = ?", w.o.ownerID) },
				want:    func(rbac.GuardKind) int { return 403 },
				code:    httpx.CodePwdChangeRequired,
				after:   func(w *who) { require.Equal(t, true, meUser(w.own)["mustChangePwd"]) },
				restore: func(w *who) { sql("UPDATE "+f.user+" SET must_change_pwd = 0 WHERE id = ?", w.o.ownerID) },
			},
		}
		for _, m := range modes {
			w := setup(m.name)
			require.Equal(t, 403, f.do(w.staffTok, "GET", "/org/ip-allow", nil).rec.Code, "员工本来看不了 IP 白名单")
			for _, c := range cases {
				path, label := c.build(w), m.name+" GET "+c.route
				// 先各访问一次：路由本身是通的，缓存是热的
				require.Equal(t, 200, f.do(w.own, "GET", path, nil).rec.Code, label)
				before := meUser(w.own)
				require.Equal(t, true, before["super"], label)
				require.Equal(t, false, before["mustChangePwd"], label)

				m.change(w)
				// 对照：没标的路由还在用缓存里的旧状态——会话、账号、主体在库里已经无效了它仍然放行，换下来的主账号
				// 在它看来还是主账号。（/auth/me 里的"必须改密"是它自己读库得到的，不经过状态缓存，这里不看。）
				require.Equal(t, true, meUser(w.own)["super"], "%s：/auth/me 还没跟上才对（否则这条用例什么都没验）", label)

				r := f.do(w.own, "GET", path, nil)
				require.Equal(t, m.want(guards[f.base+c.route]), r.rec.Code, "%s: %s", label, r.rec.Body.String())
				if m.code != 0 {
					require.Equal(t, m.code, r.env.Code, label)
				}
				m.after(w)
				m.restore(w)
			}
		}

		// 有角色的员工（不是主账号，权限码按角色判断）：账号在库里被停用、会话在库里被吊销，要权限码的读接口同样立即 401
		w := setup("有角色的员工")
		f.ok(w.own, "PUT", fmt.Sprintf("/org/accounts/%d/roles", w.staffID), gin.H{"roleIds": []uint64{w.roleID}})
		require.Equal(t, 200, f.do(w.staffTok, "GET", "/org/login-logs", nil).rec.Code)
		require.Equal(t, 403, f.do(w.staffTok, "GET", "/org/accounts", nil).rec.Code, "没有这个权限码")
		sql("UPDATE "+f.user+" SET status = 0 WHERE id = ?", w.staffID)
		require.Equal(t, 200, me(w.staffTok).rec.Code, "对照：没标的路由还在用缓存")
		require.Equal(t, 401, f.do(w.staffTok, "GET", "/org/login-logs", nil).rec.Code)
		require.Equal(t, 401, me(w.staffTok).rec.Code)
		sql("UPDATE "+f.user+" SET status = 1 WHERE id = ?", w.staffID)
		require.Equal(t, 200, f.do(w.staffTok, "GET", "/org/login-logs", nil).rec.Code)
		require.Equal(t, 200, me(w.staffTok).rec.Code)
		require.NoError(t, f.revokeOnly(w.staffID)(f.gdb))
		require.Equal(t, 200, me(w.staffTok).rec.Code, "对照：没标的路由还在用缓存")
		require.Equal(t, 401, f.do(w.staffTok, "GET", "/org/login-logs", nil).rec.Code)
		require.Equal(t, 401, me(w.staffTok).rec.Code)
	})
}
