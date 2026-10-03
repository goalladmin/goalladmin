package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/modules/agentportal"
	"github.com/goalladmin/goalladmin/server/modules/merchantportal"
	"github.com/goalladmin/goalladmin/server/modules/onboarding"
	"github.com/stretchr/testify/require"
)

func newApplication(t *testing.T, f *partnerFixture) *org.Application {
	t.Helper()
	v, err := f.app.Deps().Orgs.Apply(f.ctx, org.Merchant(), org.ApplicationInput{Name: "Applicant", ContactName: "Owner", ContactPhone: "12345678", OwnerUsername: "owner"})
	require.NoError(t, err)
	return v
}
func TestOnboardingHTTP_169_ReviewPermissionsAndRevocation(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	a := newApplication(t, f)
	_, staff := f.createUser(root, "staff", nil)
	path := fmt.Sprintf("/onboarding/applications/%d/review", a.ID)
	body := gin.H{"decision": "approve", "note": "verified"}
	require.Equal(t, 403, f.do(staff, "POST", path, body).rec.Code)
	require.Equal(t, httpx.CodeBadRequest, f.do(root, "POST", path, gin.H{"decision": "approve", "agentId": 123}).env.Code)
	// 在密码计算处暂停，保证请求已经通过认证；吊销完成后才进入 WithActor。
	actorID, actor := f.createUser(root, "reviewer", []uint64{f.superRoleID()})
	entered, release := make(chan struct{}), make(chan struct{})
	original := f.app.Deps().Orgs
	f.app.Deps().Orgs = org.New(org.Options{Hash: func(string) (string, error) { close(entered); <-release; return "unused", nil }})
	done := make(chan partnerResp, 1)
	go func() { done <- f.do(actor, "POST", path, body) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review never reached password calculation")
	}
	require.NoError(t, f.app.Deps().Auth.RevokeUserSessions(f.ctx, "platform", actorID, "admin"))
	close(release)
	result := <-done
	require.Equal(t, 401, result.rec.Code)
	f.app.Deps().Orgs = original
	rows, _, err := original.Applications(f.ctx, "pending", httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	result = f.ok(root, "POST", path, body)
	require.NotEmpty(t, result.data()["password"])
	require.NotEqual(t, 0, f.do(root, "POST", path, body).env.Code)
	list := f.ok(root, "GET", "/onboarding/applications", nil)
	require.NotContains(t, list.rec.Body.String(), "password")
}

func TestOnboardingHTTP_169_PublicBoundary(t *testing.T) {
	f := newPartnerFixture(t)
	cfg := conf.Default()
	cfg.Portals = map[string]conf.Portal{"merchant": {JWTSecret: "test-merchant-key-0123456789012345678901", AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour}}
	cfg.Onboarding.MerchantOrigin = "https://merchant.example.com"
	cfg.Server.AllowedOrigins = []string{"https://merchant.example.com"}
	a, err := app.New(cfg, app.WithDB(f.gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(merchantportal.Module(), onboarding.Module("merchant"))
	require.NoError(t, a.Setup())
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	// 此用例隔离入口契约；真实一次性消费由 core/app 的 169 用例验证。
	a.Deps().VerifyCaptcha = func(_ context.Context, p, id, answer string) bool {
		return p == "merchant" && id == "challenge" && answer == "12345"
	}
	request := func(origin, ip string, body gin.H) partnerResp {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/api/merchant/v1/onboarding/applications", strings.NewReader(string(b)))
		req.RemoteAddr = ip + ":5000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GA-Client", "web")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		return partnerResp{rec, env}
	}
	body := gin.H{"name": "Shop", "contactName": "Owner", "contactPhone": "12345678", "ownerUsername": "owner", "captchaId": "challenge", "captchaCode": "12345"}
	goodOrigin := "https://merchant.example.com"
	require.Equal(t, 404, request(goodOrigin, "203.0.113.11", body).rec.Code)
	cfg.Onboarding.MerchantEnabled = true
	require.Equal(t, 403, request("https://other.example.com", "203.0.113.11", body).rec.Code)
	body["agentId"] = 7
	require.Equal(t, httpx.CodeBadRequest, request(goodOrigin, "203.0.113.11", body).env.Code)
	delete(body, "agentId")
	body["captchaCode"] = "wrong"
	require.Equal(t, httpx.CodeCaptchaRequired, request(goodOrigin, "203.0.113.11", body).env.Code)
	body["captchaCode"] = "12345"
	success := request(goodOrigin, "203.0.113.11", body)
	require.Zero(t, success.env.Code)
	require.Len(t, success.data(), 1)
	require.NotEmpty(t, success.data()["reference"])
	for range 2 {
		require.Zero(t, request(goodOrigin, "203.0.113.11", body).env.Code)
	}
	require.Equal(t, 429, request(goodOrigin, "203.0.113.11", body).rec.Code)
	var n int64
	require.NoError(t, f.gdb.Table("ga_merchant_user").Count(&n).Error)
	require.Zero(t, n)
	// 平台不会开放公共申请路由。
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/platform/v1/onboarding/applications", io.NopCloser(strings.NewReader("{}"))))
	require.Equal(t, 405, rec.Code)
}

// 使用真实三端模块与账号/会话；验证码消费本身由 core/app 的 169 用例覆盖。
func TestOnboardingHTTP_169_InviteApprovalFlow(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	agent := f.newOrg(root, "agent", "Inviter", nil)
	build := func(code string) *app.App {
		cfg := conf.Default()
		cfg.Portals = map[string]conf.Portal{code: {AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour}}
		cfg.Onboarding = conf.Onboarding{AgentEnabled: true, MerchantEnabled: true, MerchantOrigin: "https://merchant.example.com"}
		a, err := app.New(cfg, app.WithDB(f.gdb), app.WithoutMigrations(), app.WithPasswordHashParams(64, 1), app.WithLogger(logx.New("error", "text", io.Discard)))
		require.NoError(t, err)
		if code == "agent" {
			a.Register(agentportal.Module())
		} else {
			a.Register(merchantportal.Module())
		}
		a.Register(onboarding.Module(code))
		require.NoError(t, a.Setup())
		t.Cleanup(func() { _ = a.Stop(context.Background()) })
		a.Deps().VerifyCaptcha = func(context.Context, string, string, string) bool { return true }
		return a
	}
	agentApp, merchantApp := build("agent"), build("merchant")
	request := func(a *app.App, code, token, method, path string, body any) partnerResp {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/api/"+code+"/v1"+path, strings.NewReader(string(b)))
		req.Header.Set("X-GA-Client", "web")
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		return partnerResp{rec, env}
	}
	login := request(agentApp, "agent", "", "POST", "/auth/login", gin.H{"org": agent.code, "username": "admin", "password": agent.password})
	require.Zero(t, login.env.Code)
	token := login.data()["accessToken"].(string)
	require.Zero(t, request(agentApp, "agent", token, "PUT", "/auth/password", gin.H{"oldPassword": agent.password, "newPassword": "owner-password-42"}).env.Code)
	invite := request(agentApp, "agent", token, "POST", "/onboarding/invitations", nil)
	require.Zero(t, invite.env.Code, invite.rec.Body.String())
	url := invite.data()["url"].(string)
	require.True(t, strings.HasPrefix(url, "https://merchant.example.com/register#invite="))
	secret := strings.Split(url, "#invite=")[1]
	form := gin.H{"name": "Invited shop", "contactName": "Contact", "contactPhone": "12345678", "ownerUsername": "owner", "invitationToken": secret, "captchaId": "id", "captchaCode": "code"}
	apply := request(merchantApp, "merchant", "", "POST", "/onboarding/applications", form)
	require.Zero(t, apply.env.Code, apply.rec.Body.String())
	require.NotZero(t, request(merchantApp, "merchant", "", "POST", "/onboarding/applications", form).env.Code)
	rows, _, err := f.app.Deps().Orgs.Applications(f.ctx, "pending", httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	approved := f.ok(root, "POST", fmt.Sprintf("/onboarding/applications/%d/review", rows[0].ID), gin.H{"decision": "approve", "note": "verified"})
	info := approved.data()["org"].(map[string]any)
	require.EqualValues(t, agent.id, info["agentId"])
	login = request(merchantApp, "merchant", "", "POST", "/auth/login", gin.H{"org": info["code"], "username": "owner", "password": approved.data()["password"]})
	require.Zero(t, login.env.Code)
	require.Equal(t, true, login.data()["mustChangePwd"])
	token = login.data()["accessToken"].(string)
	require.Equal(t, httpx.CodePwdChangeRequired, request(merchantApp, "merchant", token, "GET", "/org/overview", nil).env.Code)
	require.Zero(t, request(merchantApp, "merchant", token, "PUT", "/auth/password", gin.H{"oldPassword": approved.data()["password"], "newPassword": "merchant-password-42"}).env.Code)
	require.Zero(t, request(merchantApp, "merchant", token, "GET", "/org/overview", nil).env.Code)
}

// 183（D-099）：邀请菜单只有主账号看得到；重复撤销不改第一次的撤销时间。
func TestOnboardingHTTP_183_InvitationMenuAndRevokeOnce(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	agent := f.newOrg(root, "agent", "Inviter", nil)
	cfg := conf.Default()
	cfg.Portals = map[string]conf.Portal{"agent": {AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour}}
	cfg.Onboarding = conf.Onboarding{MerchantEnabled: true, MerchantOrigin: "https://merchant.example.com"}
	a, err := app.New(cfg, app.WithDB(f.gdb), app.WithoutMigrations(), app.WithPasswordHashParams(64, 1), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.Register(agentportal.Module())
	a.Register(onboarding.Module("agent"))
	require.NoError(t, a.Setup())
	t.Cleanup(func() { _ = a.Stop(context.Background()) })
	request := func(token, method, path string, body any) partnerResp {
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		}
		req := httptest.NewRequest(method, "/api/agent/v1"+path, rd)
		req.Header.Set("X-GA-Client", "web")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		var env httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
		return partnerResp{rec, env}
	}
	signIn := func(username, password, next string) string {
		login := request("", "POST", "/auth/login", gin.H{"org": agent.code, "username": username, "password": password})
		require.Zero(t, login.env.Code, login.rec.Body.String())
		token := login.data()["accessToken"].(string)
		require.Zero(t, request(token, "PUT", "/auth/password", gin.H{"oldPassword": password, "newPassword": next}).env.Code)
		return token
	}
	owner := signIn("admin", agent.password, "owner-password-42")
	require.Zero(t, request(owner, "POST", "/org/accounts", gin.H{"username": "clerk", "password": "clerk-password-41"}).env.Code)
	staff := signIn("clerk", "clerk-password-41", "clerk-password-42")

	hasMenu := func(token string) bool {
		me := request(token, "GET", "/auth/me", nil)
		require.Zero(t, me.env.Code, me.rec.Body.String())
		return strings.Contains(me.rec.Body.String(), `"name":"onboarding-invitations"`)
	}
	require.True(t, hasMenu(owner), "主账号看得到邀请菜单")
	require.False(t, hasMenu(staff), "员工看不到邀请菜单")
	require.Equal(t, 403, request(staff, "GET", "/onboarding/invitations", nil).rec.Code)

	invite := request(owner, "POST", "/onboarding/invitations", nil)
	require.Zero(t, invite.env.Code, invite.rec.Body.String())
	id := uint64(invite.data()["invitation"].(map[string]any)["id"].(float64))
	revokedAt := func() time.Time {
		var at *time.Time
		require.NoError(t, f.gdb.Raw("SELECT revoked_at FROM ga_org_invitation WHERE id = ?", id).Scan(&at).Error)
		require.NotNil(t, at)
		return *at
	}
	path := fmt.Sprintf("/onboarding/invitations/%d", id)
	require.Zero(t, request(owner, "DELETE", path, nil).env.Code)
	// 把第一次的撤销时间改成一个固定的旧值，再撤销一次：这个值不能被盖掉
	first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, f.gdb.Exec("UPDATE ga_org_invitation SET revoked_at = ? WHERE id = ?", first, id).Error)
	again := request(owner, "DELETE", path, nil)
	require.Zero(t, again.env.Code, again.rec.Body.String())
	require.True(t, first.Equal(revokedAt()), "重复撤销不改第一次的时间：%s", revokedAt())
	require.Equal(t, 404, request(owner, "DELETE", fmt.Sprintf("/onboarding/invitations/%d", id+1000), nil).rec.Code)
}
