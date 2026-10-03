package app

// D-065：主体服务（core/org）接上真的主体端。平台在 Deps.Orgs 上建商户，主账号拿一次性密码按编号登录、首次登录必须改密；
// 平台停用、重置主账号密码、更换主账号之后，主体端按库认定。用 merchant 端（用户来源是 Deps.Orgs.Users）跑完整的 HTTP 链路。

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

const merchantSecret = "merchant-secret-0123456789abcdef0123456789abcd"

// merchantTestModule 注册 merchant 端，用户来源是内核的主体服务。
type merchantTestModule struct{}

func (merchantTestModule) Name() string { return "merchanttest" }
func (merchantTestModule) Init(d *Deps) error {
	return d.Portals.Register(portal.Portal{Code: org.Merchant().Portal(), Users: d.Orgs.Users(org.Merchant()), Scoped: true})
}
func (merchantTestModule) Perms() []rbac.Perm     { return nil }
func (merchantTestModule) Menus() []rbac.MenuNode { return nil }
func (merchantTestModule) Routes(r *Router) {
	r.Portal(org.Merchant().Portal()).GET("/whoami", rbac.AuthOnly(), func(c *gin.Context) {
		p := auth.MustFromCtx(c.Request.Context())
		httpx.OK(c, gin.H{"user": p.Username, "userId": p.UserID, "orgId": p.OrgID, "super": p.Super})
	})
}
func (merchantTestModule) Start(context.Context) error { return nil }
func (merchantTestModule) Stop(context.Context) error  { return nil }

type orgSvcFixture struct {
	t     *testing.T
	app   *App
	clock *fakeClock
	ctx   context.Context
}

func newOrgSvcFixture(t *testing.T) *orgSvcFixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Server.AllowedOrigins = []string{testOrigin}
	cfg.Portals[org.Merchant().Portal()] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: merchantSecret}
	clock := &fakeClock{t: time.Now().UTC().Truncate(time.Second)}
	a, err := New(cfg, WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithClock(clock.Now), WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	require.NotNil(t, a.Deps().Orgs, "有数据库时 New 就建好主体服务")
	a.Register(merchantTestModule{})
	require.NoError(t, a.Setup())
	return &orgSvcFixture{t: t, app: a, clock: clock, ctx: a.Context(context.Background())}
}

func (f *orgSvcFixture) do(method, path string, body any, opts ...reqOpt) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, PortalPrefix(org.Merchant().Portal())+path, rd)
	req.RemoteAddr = "203.0.113.10:5000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	webClient()(req)
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return resp{rec: rec, env: env}
}

func (f *orgSvcFixture) login(code, username, password string) resp {
	return f.do("POST", "/auth/login", gin.H{"org": code, "username": username, "password": password})
}

func (f *orgSvcFixture) mustLogin(code, username, password string) (access, refresh string, must bool) {
	f.t.Helper()
	r := f.login(code, username, password)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	access, _ = r.data()["accessToken"].(string)
	must, _ = r.data()["mustChangePwd"].(bool)
	return access, r.cookie("ga_rt_" + org.Merchant().Portal()), must
}

func (f *orgSvcFixture) whoami(access string) resp {
	return f.do("GET", "/whoami", nil, bearerOpt(access))
}

func TestOrgService_CreatedOwnerLogsInAndPlatformControls(t *testing.T) {
	f := newOrgSvcFixture(t)
	orgs := f.app.Deps().Orgs
	newPwd := func() org.InitialPassword {
		p, err := orgs.NewInitialPassword()
		require.NoError(t, err)
		return p
	}
	m, err := orgs.Create(f.ctx, org.Merchant(), org.CreateInput{Name: "商户甲", OwnerUsername: "admin"}, newPwd(), 1)
	require.NoError(t, err)
	other, err := orgs.Create(f.ctx, org.Merchant(), org.CreateInput{Name: "商户乙", OwnerUsername: "admin"}, newPwd(), 1)
	require.NoError(t, err)

	// 主账号用一次性密码按编号登录，首次登录必须改密；改完才能用别的接口
	access, _, must := f.mustLogin(strings.ToLower(m.Org.Code), "admin", m.Password)
	require.True(t, must)
	require.Equal(t, httpx.CodePwdChangeRequired, f.whoami(access).env.Code)
	r := f.do("PUT", "/auth/password", gin.H{"oldPassword": m.Password, "newPassword": "brand-new-pass-1"}, bearerOpt(access))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	access, refresh, must := f.mustLogin(m.Org.Code, "admin", "brand-new-pass-1")
	require.False(t, must)
	who := f.whoami(access)
	require.Equal(t, 0, who.env.Code, who.rec.Body.String())
	require.EqualValues(t, m.Org.ID, who.data()["orgId"])
	require.EqualValues(t, m.OwnerID, who.data()["userId"])
	require.Equal(t, true, who.data()["super"], "主账号是主体内的超管")
	// 初始密码已经不能用了；别的主体的同名账号是另一个账号
	require.Equal(t, httpx.CodeLoginFailed, f.login(m.Org.Code, "admin", m.Password).env.Code)
	require.Equal(t, httpx.CodeLoginFailed, f.login(other.Org.Code, "admin", "brand-new-pass-1").env.Code)

	// 平台重置主账号密码：旧会话立即吊销（刷新按库认定），旧密码不能登录，新密码能登录且必须改密
	reset := newPwd()
	require.NoError(t, orgs.ResetOwnerPassword(f.ctx, org.Merchant(), m.Org.ID, reset))
	plain := reset.Plain()
	require.Equal(t, 401, f.do("POST", "/auth/refresh", nil, cookieFor(refresh)).rec.Code)
	require.Equal(t, httpx.CodeLoginFailed, f.login(m.Org.Code, "admin", "brand-new-pass-1").env.Code)
	_, refreshA, must := f.mustLogin(m.Org.Code, "admin", plain)
	require.True(t, must)

	// 平台停用主体：已有的会话吊销（刷新立即 401），登录失败；别的主体照常
	ob, refreshB, _ := f.mustLogin(other.Org.Code, "admin", other.Password)
	require.NoError(t, orgs.SetStatus(f.ctx, org.Merchant(), m.Org.ID, org.StatusDisabled, 1))
	require.Equal(t, 401, f.do("POST", "/auth/refresh", nil, cookieFor(refreshA)).rec.Code)
	require.Equal(t, httpx.CodeLoginFailed, f.login(m.Org.Code, "admin", plain).env.Code)
	f.clock.Advance(16 * time.Second) // 状态缓存最长 15 秒
	require.Equal(t, httpx.CodePwdChangeRequired, f.whoami(ob).env.Code, "别的主体的会话还在（只是还没改密）")
	require.Equal(t, 0, f.do("POST", "/auth/refresh", nil, cookieFor(refreshB)).env.Code)

	// 重新启用后能登录；更换主账号之后，原主账号不再是主体内的超管
	require.NoError(t, orgs.SetStatus(f.ctx, org.Merchant(), m.Org.ID, org.StatusEnabled, 1))
	require.NoError(t, f.app.Deps().DB.Exec("INSERT INTO ga_merchant_user (org_id, username, password_hash, status, created_at, updated_at) SELECT org_id, 'staff', password_hash, 1, created_at, updated_at FROM ga_merchant_user WHERE id = ?", m.OwnerID).Error)
	var staff uint64
	require.NoError(t, f.app.Deps().DB.Raw("SELECT id FROM ga_merchant_user WHERE org_id = ? AND username = 'staff'", m.Org.ID).Scan(&staff).Error)
	require.NoError(t, orgs.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, staff, 1))
	r = f.login(m.Org.Code, "admin", plain)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do("PUT", "/auth/password", gin.H{"oldPassword": plain, "newPassword": "another-pass-22"}, bearerOpt(r.data()["accessToken"].(string)))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	access, _, _ = f.mustLogin(m.Org.Code, "admin", "another-pass-22")
	require.Equal(t, false, f.whoami(access).data()["super"], "不再是主账号")
}

func cookieFor(v string) reqOpt {
	return func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "ga_rt_" + org.Merchant().Portal(), Value: v})
	}
}
