package agentportal_test

// 代理商只读名下商户（D-065 第 6 条、D-067）：规范 §13.2 第 149 条。用代理商程序真实注册的模块跑完整的 HTTP 链路。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/orgportal"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/agentportal"
)

const base = "/api/agent/v1"

type fixture struct {
	t   *testing.T
	app *app.App
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Portals = map[string]conf.Portal{"agent": {
		AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: "agent-portal-test-secret-0123456789abcdef01",
		Login: conf.PortalLogin{IPRatePerMinute: 600, AccountRatePerMinute: 120},
	}}
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1), app.WithoutMigrations())
	require.NoError(t, err)
	a.Register(agentportal.Module())
	require.NoError(t, a.Setup())
	return &fixture{t: t, app: a, ctx: a.Context(context.Background())}
}

type resp struct {
	code int
	env  httpx.Envelope
	body string
}

func (f *fixture) do(tok, method, path string, body any) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, base+path, rd)
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
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return resp{code: rec.Code, env: env, body: rec.Body.String()}
}

func (f *fixture) ok(tok, method, path string, body any) resp {
	f.t.Helper()
	r := f.do(tok, method, path, body)
	require.Equal(f.t, 0, r.env.Code, "%s %s: %s", method, path, r.body)
	return r
}

func (r resp) data() map[string]any { m, _ := r.env.Data.(map[string]any); return m }

func (r resp) codes() []string {
	l, _ := r.data()["list"].([]any)
	out := make([]string, 0, len(l))
	for _, v := range l {
		out = append(out, v.(map[string]any)["code"].(string))
	}
	return out
}

// open 开一个主体；kind 为商户时 agentID 是所属代理商。返回主体和主账号登录、改密之后的令牌。
func (f *fixture) open(kind org.Kind, name string, agentID uint64) (*org.Created, string) {
	f.t.Helper()
	orgs := f.app.Deps().Orgs
	pwd, err := orgs.NewInitialPassword()
	require.NoError(f.t, err)
	c, err := orgs.Create(f.ctx, kind, org.CreateInput{Name: name, OwnerUsername: "admin", AgentID: agentID, Remark: "平台的备注"}, pwd, 1)
	require.NoError(f.t, err)
	if kind != org.Agent() {
		return c, ""
	}
	r := f.ok("", "POST", "/auth/login", gin.H{"org": c.Org.Code, "username": "admin", "password": c.Password})
	tok := r.data()["accessToken"].(string)
	f.ok(tok, "PUT", "/auth/password", gin.H{"oldPassword": c.Password, "newPassword": "changed-pass-9"})
	return c, tok
}

// 149. 代理商只读名下商户：列表、详情只有 agent_id 是自己的商户；商户换了代理商，原代理商下一次请求就看不到；
// 代理商端没有写商户数据的路由；没有查看权限的员工 403；平台的备注、排序、主账号不给代理商看。
func TestAgentPortal_149_ChildMerchantsReadOnly(t *testing.T) {
	f := newFixture(t)
	a1, tok1 := f.open(org.Agent(), "代理一", 0)
	a2, tok2 := f.open(org.Agent(), "代理二", 0)
	m1, _ := f.open(org.Merchant(), "商户一", a1.Org.ID)
	m2, _ := f.open(org.Merchant(), "商户二", a1.Org.ID)
	m3, _ := f.open(org.Merchant(), "商户三", a2.Org.ID)
	direct, _ := f.open(org.Merchant(), "直属商户", 0)

	r := f.ok(tok1, "GET", "/merchants?pageSize=100", nil)
	require.ElementsMatch(t, []string{m1.Org.Code, m2.Org.Code}, r.codes())
	require.NotContains(t, r.body, "平台的备注")
	for _, k := range []string{"remark", "sort", "agentId", "ownerUserId", "ownerUsername"} {
		require.NotContains(t, r.body, `"`+k+`"`, k)
	}
	require.ElementsMatch(t, []string{m3.Org.Code}, f.ok(tok2, "GET", "/merchants", nil).codes())
	f.ok(tok1, "GET", fmt.Sprintf("/merchants/%d", m1.Org.ID), nil)
	for _, id := range []uint64{m3.Org.ID, direct.Org.ID, 999999} {
		require.Equal(t, 404, f.do(tok1, "GET", fmt.Sprintf("/merchants/%d", id), nil).code, id)
	}
	// 按状态、关键字筛选
	require.NoError(t, f.app.Deps().Orgs.SetStatus(f.ctx, org.Merchant(), m2.Org.ID, org.StatusDisabled, 1))
	require.ElementsMatch(t, []string{m2.Org.Code}, f.ok(tok1, "GET", "/merchants?status=0", nil).codes())
	require.ElementsMatch(t, []string{m1.Org.Code}, f.ok(tok1, "GET", "/merchants?keyword="+m1.Org.Code, nil).codes())
	// 概览的"名下商户"计数
	require.Equal(t, float64(2), f.ok(tok1, "GET", "/org/overview", nil).data()["counts"].(map[string]any)["merchants"])

	// 商户换了代理商：原代理商下一次请求就看不到，新代理商看得到
	require.NoError(t, f.app.Deps().Orgs.SetAgent(f.ctx, m1.Org.ID, a2.Org.ID, 1))
	require.ElementsMatch(t, []string{m2.Org.Code}, f.ok(tok1, "GET", "/merchants", nil).codes())
	require.Equal(t, 404, f.do(tok1, "GET", fmt.Sprintf("/merchants/%d", m1.Org.ID), nil).code)
	require.ElementsMatch(t, []string{m1.Org.Code, m3.Org.Code}, f.ok(tok2, "GET", "/merchants", nil).codes())

	// 代理商端没有写商户数据的路由
	for _, rt := range f.app.Routes() {
		if strings.HasPrefix(rt.Path, base+"/merchants") {
			require.Equal(t, "GET", rt.Method, rt.Path)
			require.Equal(t, agentportal.PermMerchantList, rt.Perm, rt.Path)
		}
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		code := f.do(tok1, m, fmt.Sprintf("/merchants/%d", m2.Org.ID), gin.H{"name": "x"}).code
		require.Contains(t, []int{404, 405}, code, m)
	}
	// 请求里带商户 ID 按主体参数守卫拒绝（D-067 第 5 条），不是悄悄当筛选条件
	require.Equal(t, httpx.CodeBadRequest, f.do(tok1, "GET", fmt.Sprintf("/merchants?merchantId=%d", m3.Org.ID), nil).env.Code)
	require.Equal(t, httpx.CodeBadRequest, f.do(tok1, "GET", fmt.Sprintf("/merchants?agentId=%d", a2.Org.ID), nil).env.Code)

	// 没有查看权限的员工 403；授了权限之后能看
	r = f.ok(tok1, "POST", "/org/roles", gin.H{"code": "viewer", "name": "viewer"})
	roleID := uint64(r.data()["id"].(float64))
	r = f.ok(tok1, "POST", "/org/accounts", gin.H{"username": "staff", "password": "staff-pass-123"})
	staffID := uint64(r.data()["account"].(map[string]any)["id"].(float64))
	st := f.ok("", "POST", "/auth/login", gin.H{"org": a1.Org.Code, "username": "staff", "password": "staff-pass-123"}).data()["accessToken"].(string)
	f.ok(st, "PUT", "/auth/password", gin.H{"oldPassword": "staff-pass-123", "newPassword": "changed-pass-9"})
	require.Equal(t, 403, f.do(st, "GET", "/merchants", nil).code)
	_, has := f.ok(st, "GET", "/org/overview", nil).data()["counts"].(map[string]any)["merchants"]
	require.False(t, has, "没有查看权限，概览里也没有这个计数")
	f.ok(tok1, "PUT", fmt.Sprintf("/org/roles/%d/perms", roleID), gin.H{"codes": []string{agentportal.PermMerchantList, "agent:" + orgportal.PermAccountList}})
	f.ok(tok1, "PUT", fmt.Sprintf("/org/accounts/%d/roles", staffID), gin.H{"roleIds": []uint64{roleID}})
	require.ElementsMatch(t, []string{m2.Org.Code}, f.ok(st, "GET", "/merchants", nil).codes())
}

// 162. 名下商户的两条路由和套件的后台一样"按库核对"（D-073）：平台程序停用这个代理商之后，下一个请求就是 401，
// 不等状态缓存到期。对照：没标的 /auth/me 在这之前照旧放行。
func TestAgentPortal_162_MerchantRoutesSeeRevocationAtOnce(t *testing.T) {
	f := newFixture(t)
	// 模拟同库的另一个平台程序，Redis 未配置。它使用独立服务，不能共享代理商实例的本地失效回调（D-075）。
	orgs := org.New(org.Options{})
	a1, tok := f.open(org.Agent(), "代理一", 0)
	m1, _ := f.open(org.Merchant(), "商户一", a1.Org.ID)

	n := 0
	for _, rt := range f.app.Routes() {
		if strings.HasPrefix(rt.Path, base+"/merchants") {
			require.True(t, rt.LiveAuth, rt.Path)
			n++
		}
	}
	require.Equal(t, 2, n)

	gdb := f.app.Deps().DB
	relogin := func() {
		tok = f.ok("", "POST", "/auth/login", gin.H{"org": a1.Org.Code, "username": "admin", "password": "changed-pass-9"}).data()["accessToken"].(string)
	}
	// 三种改动分开各走一遍：停用主体（会话也被吊销）；只吊销会话（账号、主体都还启用）；只停用账号（会话没吊销）。
	// 认证的两步（会话、账号）都得是按库核对的，只有其中一步是的话后两种会漏掉一种
	modes := []struct {
		name            string
		change, restore func()
	}{
		{"停用主体",
			func() { require.NoError(t, orgs.SetStatus(f.ctx, org.Agent(), a1.Org.ID, org.StatusDisabled, 1)) },
			func() {
				require.NoError(t, orgs.SetStatus(f.ctx, org.Agent(), a1.Org.ID, org.StatusEnabled, 1))
				relogin()
			}},
		{"只吊销会话",
			func() {
				require.NoError(t, gdb.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'admin' WHERE portal = 'agent' AND user_id = ? AND revoked_at IS NULL", a1.OwnerID).Error)
			},
			relogin},
		{"只停用账号",
			func() {
				require.NoError(t, gdb.Exec("UPDATE ga_agent_user SET status = 0 WHERE id = ?", a1.OwnerID).Error)
			},
			func() {
				require.NoError(t, gdb.Exec("UPDATE ga_agent_user SET status = 1 WHERE id = ?", a1.OwnerID).Error)
			}},
	}
	for _, m := range modes {
		for _, path := range []string{"/merchants", fmt.Sprintf("/merchants/%d", m1.Org.ID)} {
			f.ok(tok, "GET", path, nil)
			f.ok(tok, "GET", "/auth/me", nil)
			m.change()
			f.ok(tok, "GET", "/auth/me", nil) // 缓存里还是改动之前的状态
			require.Equal(t, 401, f.do(tok, "GET", path, nil).code, "%s %s", m.name, path)
			require.Equal(t, 401, f.do(tok, "GET", "/auth/me", nil).code, "%s：按库核对读到了变化，缓存跟着清掉", m.name)
			m.restore()
		}
	}
}
