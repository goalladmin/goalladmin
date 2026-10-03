package app

// D-062：IP 黑名单、端白名单、主体与账号白名单接在请求链路上的效果。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
)

// fromIP 让请求来自某个地址（trustedProxies 为空时 ClientIP 取 RemoteAddr）。
func fromIP(ip string) reqOpt { return func(r *http.Request) { r.RemoteAddr = ip + ":40000" } }

func (f *authFixture) testCtx() context.Context { return f.app.Context(context.Background()) }

func (f *authFixture) countSecurity(kind string) int64 {
	f.app.audit.Flush(context.Background())
	var n int64
	require.NoError(f.t, f.app.deps.DB.Table("ga_security_event").Where("kind = ?", kind).Select("COALESCE(SUM(count), 0)").Scan(&n).Error)
	return n
}

func requireIPDenied(t *testing.T, r resp) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeIPDenied, r.env.Code, r.rec.Body.String())
}

// 黑名单挡住这个来源的每个请求：登录、验证码、刷新、已登录的接口；健康检查照常；别的来源不受影响。
func TestIPACL_BlacklistBlocksEverything(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	access, refresh := f.mustLogin("alice", "correct-horse-9")
	_, err := f.app.deps.IPACL.AddDeny(f.testCtx(), ipacl.DenyInput{CIDR: "203.0.113.0/24"}, 1, "")
	require.NoError(t, err)

	requireIPDenied(t, f.login("alice", "correct-horse-9"))
	requireIPDenied(t, f.do("GET", "/auth/captcha", nil))
	requireIPDenied(t, f.refresh(refresh))
	requireIPDenied(t, f.ping(access))
	requireIPDenied(t, f.do("GET", "/no-such-route", nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, healthPath, nil)
	req.RemoteAddr = "203.0.113.10:5000"
	f.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "健康检查不受黑名单影响")

	// 别的来源照常；被挡的请求没有动会话（刷新凭证换个地方照样能用）
	require.Equal(t, 200, f.do("GET", "/ping", nil, bearerOpt(access), fromIP("198.51.100.7")).rec.Code)
	require.Equal(t, 0, f.refresh(refresh, fromIP("198.51.100.7")).env.Code)
	require.Positive(t, f.countSecurity("ip_denied"), "被拒的请求记安全事件")
}

// 端白名单：这个端的每个接口（含不需要登录的）只放行名单里的来源；别的端不受影响。
func TestIPACL_PortalWhitelist(t *testing.T) {
	f := newOrgFixture(t)
	f.seedTwoOrgs()
	f.addUser("alice", "correct-horse-9")
	_, err := f.app.deps.IPACL.SetAllow(f.testCtx(), ipacl.PortalTarget(testPortal), []ipacl.Entry{{CIDR: "10.0.0.0/8"}}, 1, "")
	require.NoError(t, err)

	requireIPDenied(t, f.do("GET", "/auth/captcha", nil))
	requireIPDenied(t, f.login("alice", "correct-horse-9"))
	r := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "correct-horse-9"}, fromIP("10.1.2.3"))
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	access, _ := r.data()["accessToken"].(string)
	require.Equal(t, 200, f.do("GET", "/ping", nil, bearerOpt(access), fromIP("10.1.2.3")).rec.Code)
	requireIPDenied(t, f.ping(access))

	// shop 端没设，照常
	require.Equal(t, 0, f.shopLogin("M10000001", "admin", "a-secret-pass-1").env.Code)
}

// 账号白名单：名单外的来源登录按"账号或密码错误"回、登录日志记 ip_denied；已登录后换到名单外：接口 2003，
// 登出照常，刷新 2003 且不轮换、不吊销；回到名单里一切照旧。
func TestIPACL_AccountWhitelist(t *testing.T) {
	f := newAuthFixture(t)
	id := f.addUser("alice", "correct-horse-9")
	f.addUser("bob", "correct-horse-9")
	_, err := f.app.deps.IPACL.SetAllow(f.testCtx(), ipacl.UserTarget(testPortal, 0, id), []ipacl.Entry{{CIDR: "203.0.113.10"}}, 1, "")
	require.NoError(t, err)

	// 名单外：和密码错完全一样（正确密码也一样）
	out := fromIP("198.51.100.1")
	denied := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "correct-horse-9"}, out)
	wrong := f.do("POST", "/auth/login", gin.H{"username": "bob", "password": "wrong-pass-9"}, out)
	require.Equal(t, httpx.CodeLoginFailed, denied.env.Code, denied.rec.Body.String())
	require.Equal(t, wrong.env.Code, denied.env.Code)
	require.Equal(t, wrong.env.Msg, denied.env.Msg)
	require.Equal(t, wrong.data(), denied.data())
	require.Empty(t, denied.cookie("ga_rt_"+testPortal))
	var last session.LoginLog
	require.NoError(t, f.app.deps.DB.Where("portal = ? AND username = ?", testPortal, "alice").Order("id DESC").First(&last).Error)
	require.Equal(t, "ip_denied", last.Reason)
	require.Equal(t, id, last.UserID)
	// 别的账号不受影响
	require.Equal(t, 0, f.do("POST", "/auth/login", gin.H{"username": "bob", "password": "correct-horse-9"}, out).env.Code)

	// 名单里：照常登录；换到名单外：接口 2003
	access, refresh := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(access).rec.Code)
	requireIPDenied(t, f.do("GET", "/ping", nil, bearerOpt(access), out))
	requireIPDenied(t, f.do("GET", "/auth/me", nil, bearerOpt(access), out))
	rr := f.refresh(refresh, out)
	requireIPDenied(t, rr)
	require.Empty(t, rr.cookie("ga_rt_"+testPortal), "被拒的刷新不动 Cookie")
	// 刷新被拒没有轮换、没有吊销：回到名单里，同一个凭证照样能刷新
	ok := f.refresh(refresh)
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())
	newAccess, _ := ok.data()["accessToken"].(string)
	require.Equal(t, 200, f.ping(newAccess).rec.Code)
	// 登出不受账号白名单限制（只会吊销自己的会话）
	lo := f.do("POST", "/auth/logout", nil, bearerOpt(newAccess), webClient(), out)
	require.Equal(t, 200, lo.rec.Code, lo.rec.Body.String())
	require.Equal(t, 401, f.ping(newAccess).rec.Code)
}

// 主体白名单：主体下所有账号都受约束；账号白名单再收窄；别的主体不受影响。
func TestIPACL_OrgWhitelist(t *testing.T) {
	f := newOrgFixture(t)
	orgA, _, _, staffA, _ := f.seedTwoOrgs()
	_, err := f.app.deps.IPACL.SetAllow(f.testCtx(), ipacl.OrgTarget(shopPortal, orgA), []ipacl.Entry{{CIDR: "203.0.113.0/24"}}, 1, "")
	require.NoError(t, err)
	out := fromIP("198.51.100.1")

	r := f.shop("POST", "/auth/login", gin.H{"org": "M10000001", "username": "admin", "password": "a-secret-pass-1"}, out)
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
	r = f.shop("POST", "/auth/login", gin.H{"org": "M10000002", "username": "admin", "password": "b-secret-pass-1"}, out)
	require.Equal(t, 0, r.env.Code, "别的主体不受影响：%s", r.rec.Body.String())

	staff, _ := f.mustShopLogin("M10000001", "staff", "staff-pass-1")
	require.Equal(t, 200, f.whoami(staff).rec.Code)
	requireIPDenied(t, f.shop("GET", "/whoami", nil, bearerOpt(staff), out))

	// 员工自己的名单再收窄：主体名单里、员工名单外的地址也不行
	_, err = f.app.deps.IPACL.SetAllow(f.testCtx(), ipacl.UserTarget(shopPortal, orgA, staffA), []ipacl.Entry{{CIDR: "203.0.113.99"}}, 1, "")
	require.NoError(t, err)
	requireIPDenied(t, f.whoami(staff)) // 203.0.113.10 在主体名单里、不在员工名单里
	require.Equal(t, 200, f.shop("GET", "/whoami", nil, bearerOpt(staff), fromIP("203.0.113.99")).rec.Code)
}
