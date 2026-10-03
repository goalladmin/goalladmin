// Package onboarding 提供主体申请、平台审核和代理商邀请入口（D-080）。
package onboarding

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

const (
	PermList   = "onboarding:application:list"
	PermReview = "onboarding:application:review"
)

type module struct {
	portal  string
	deps    *app.Deps
	rate    app.RateWindow
	invites app.RateWindow
}

// Module 仅为指定的已注册端装配路由，不注册新端。
func Module(portal string) app.Module { return &module{portal: portal} }
func (m *module) Name() string        { return "onboarding" }
func (m *module) Init(d *app.Deps) error {
	if m.portal != "platform" && m.portal != "agent" && m.portal != "merchant" {
		return errors.New("onboarding: unknown portal")
	}
	m.deps = d
	m.rate = d.NewRateWindow("onboarding:"+m.portal, 5, time.Hour, 100000)
	m.invites = d.NewRateWindow("onboarding-invite:"+m.portal, 20, 24*time.Hour, 100000)
	return nil
}
func (m *module) Perms() []rbac.Perm {
	if m.portal != "platform" {
		return nil
	}
	return []rbac.Perm{
		{Code: PermList, Name: "perm.onboarding.application.list", Portal: m.portal, Group: "onboarding.application"},
		{Code: PermReview, Name: "perm.onboarding.application.review", Portal: m.portal, Group: "onboarding.application", Sensitive: true},
	}
}
func (m *module) Menus() []rbac.MenuNode {
	if m.portal == "platform" {
		return []rbac.MenuNode{{Portal: m.portal, Name: "onboarding", Path: "/onboarding", Component: "onboarding/index", TitleKey: "menu.onboarding", Icon: "DocumentChecked", Perm: PermList, Sort: 120}}
	}
	if m.portal == "agent" {
		return []rbac.MenuNode{{Portal: m.portal, Name: "onboarding-invitations", Path: "/invitations", Component: "invitations/index", TitleKey: "menu.invitations", Icon: "Link", Sort: 20, SuperOnly: true}}
	}
	return nil
}
func (m *module) Start(context.Context) error { return nil }
func (m *module) Stop(context.Context) error  { return nil }
func (m *module) Routes(r *app.Router) {
	g := r.Portal(m.portal).Group("/onboarding")
	if m.portal == "platform" {
		g.LiveAuth().GET("/applications", rbac.Require(PermList), m.list)
		g.POST("/applications/:id/review", rbac.Require(PermReview), m.review, oplog.Record("onboarding.review"))
		return
	}
	g.GET("/config", rbac.Public(), func(c *gin.Context) {
		httpx.OK(c, gin.H{"enabled": m.enabled(), "invitationsEnabled": m.portal == "agent" && m.deps.Conf.Onboarding.MerchantEnabled})
	})
	g.POST("/applications", rbac.Public(), m.apply)
	if m.portal == "agent" {
		g.LiveAuth().GET("/invitations", rbac.RequireSuper(), m.listInvites)
		g.POST("/invitations", rbac.RequireSuper(), m.createInvite, oplog.Record("onboarding.invite"))
		g.DELETE("/invitations/:id", rbac.RequireSuper(), m.revokeInvite, oplog.Record("onboarding.revoke-invite"))
	}
}
func (m *module) enabled() bool {
	o := m.deps.Conf.Onboarding
	return m.portal == "agent" && o.AgentEnabled || m.portal == "merchant" && o.MerchantEnabled
}
func (m *module) apply(c *gin.Context) {
	if !m.enabled() {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	if !m.clientOK(c) {
		httpx.Fail(c, httpx.ErrForbidden)
		return
	}
	if ok, _ := m.rate.Reserve(c.Request.Context(), app.RateIPKey(c.ClientIP())); !ok {
		httpx.Fail(c, httpx.ErrTooManyRequests)
		return
	}
	var in struct {
		org.ApplicationInput
		CaptchaID   string `json:"captchaId"`
		CaptchaCode string `json:"captchaCode"`
	}
	if err := httpx.BindJSONStrict(c, &in); err != nil {
		httpx.Fail(c, err)
		return
	}
	if !m.deps.VerifyCaptcha(c.Request.Context(), m.portal, in.CaptchaID, in.CaptchaCode) {
		httpx.Fail(c, httpx.New(httpx.CodeCaptchaRequired, ""))
		return
	}
	kind := org.Agent()
	if m.portal == "merchant" {
		kind = org.Merchant()
	}
	row, err := m.deps.Orgs.Apply(c.Request.Context(), kind, in.ApplicationInput)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"reference": row.Reference})
}
func (m *module) clientOK(c *gin.Context) bool {
	if c.GetHeader("X-GA-Client") != "web" || c.ContentType() != "application/json" {
		return false
	}
	origins := m.deps.Conf.Server.AllowedOrigins
	if len(origins) == 0 {
		return !m.deps.Conf.Server.IsRelease()
	}
	got := strings.ToLower(strings.TrimRight(c.GetHeader("Origin"), "/"))
	for _, origin := range origins {
		if got == strings.ToLower(strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}
func (m *module) list(c *gin.Context) {
	q := httpx.BindPage(c)
	rows, n, err := m.deps.Orgs.Applications(c.Request.Context(), c.Query("state"), q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, n, q.Page, q.PageSize)
}
func id(c *gin.Context) (uint64, bool) {
	v, e := strconv.ParseUint(c.Param("id"), 10, 64)
	if e != nil || v == 0 {
		httpx.Fail(c, httpx.ErrNotFound)
		return 0, false
	}
	return v, true
}
func (m *module) review(c *gin.Context) {
	id, ok := id(c)
	if !ok {
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if err := httpx.BindJSONStrict(c, &in); err != nil {
		httpx.Fail(c, err)
		return
	}
	if in.Decision != "approve" && in.Decision != "reject" {
		httpx.Fail(c, httpx.New(httpx.CodeValidation, "").WithFields(httpx.NewField("decision", "onboarding.decision", "choose approve or reject")))
		return
	}
	if err := m.deps.Orgs.CheckApplicationReview(c.Request.Context(), id, in.Decision == "approve", in.Note); err != nil {
		httpx.Fail(c, err)
		return
	}
	var pwd org.InitialPassword
	var err error
	if in.Decision == "approve" {
		pwd, err = m.deps.Orgs.NewInitialPassword()
		if err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	var out *org.Created
	err = m.deps.RBAC.WithActor(c.Request.Context(), auth.MustFromCtx(c.Request.Context()), func(ctx context.Context, actor auth.Principal) error {
		var e error
		out, e = m.deps.Orgs.ReviewApplication(ctx, actor, id, in.Decision == "approve", in.Note, pwd)
		return e
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}
func (m *module) listInvites(c *gin.Context) {
	q := httpx.BindPage(c)
	ctx := c.Request.Context()
	rows, n, err := m.deps.Orgs.Invitations(ctx, auth.MustFromCtx(ctx), q)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OKPage(c, rows, n, q.Page, q.PageSize)
}
func (m *module) createInvite(c *gin.Context) {
	if !m.deps.Conf.Onboarding.MerchantEnabled {
		httpx.Fail(c, httpx.ErrNotFound)
		return
	}
	ctx := c.Request.Context()
	actor := auth.MustFromCtx(ctx)
	var row *org.Invitation
	var token string
	err := m.deps.RBAC.WithActor(ctx, actor, func(ctx context.Context, a auth.Principal) error {
		if a.Portal != "agent" || a.OrgID == 0 || !a.Super {
			return httpx.ErrForbidden
		}
		// 配额只属于锁内确认的当前主账号；等待期间身份失效或已换主账号的不占次数。
		if ok, _ := m.invites.Reserve(ctx, strconv.FormatUint(a.OrgID, 10)); !ok {
			return httpx.ErrTooManyRequests
		}
		var e error
		row, token, e = m.deps.Orgs.CreateInvitation(ctx, a)
		return e
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, gin.H{"invitation": row, "url": strings.TrimRight(m.deps.Conf.Onboarding.MerchantOrigin, "/") + "/register#invite=" + token})
}
func (m *module) revokeInvite(c *gin.Context) {
	id, ok := id(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	err := m.deps.RBAC.WithActor(ctx, auth.MustFromCtx(ctx), func(ctx context.Context, a auth.Principal) error { return m.deps.Orgs.RevokeInvitation(ctx, a, id) })
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nil)
}
