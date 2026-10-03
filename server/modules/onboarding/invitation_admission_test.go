package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
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

// 此模块只注册真实的主体用户来源，邀请处理仍使用生产的 RBAC 和事务。
type inviteTestPortal struct{}

func (inviteTestPortal) Name() string { return "invite-test-portal" }
func (inviteTestPortal) Init(d *app.Deps) error {
	return d.Portals.Register(portal.Portal{Code: "agent", Users: d.Orgs.Users(org.Agent()), Scoped: true})
}
func (inviteTestPortal) Perms() []rbac.Perm          { return nil }
func (inviteTestPortal) Menus() []rbac.MenuNode      { return nil }
func (inviteTestPortal) Routes(*app.Router)          {}
func (inviteTestPortal) Start(context.Context) error { return nil }
func (inviteTestPortal) Stop(context.Context) error  { return nil }

type inviteQuota struct {
	allow   bool
	keys    []string
	inTx    []bool
	refunds int
}

func (q *inviteQuota) Reserve(ctx context.Context, key string) (bool, func()) {
	q.keys = append(q.keys, key)
	q.inTx = append(q.inTx, db.InTx(ctx))
	if !q.allow {
		return false, nil
	}
	return true, func() { q.refunds++ }
}

type inviteAdmissionFixture struct {
	app   *app.App
	m     *module
	ctx   context.Context
	owner auth.Principal
	quota *inviteQuota
}

func newInviteAdmissionFixture(t *testing.T) *inviteAdmissionFixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.TestContext(t, gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Portals = map[string]conf.Portal{"agent": {JWTSecret: "invite-test-key-0123456789abcdef0123456789", AccessTTL: 15 * time.Minute, RefreshTTL: 24 * time.Hour}}
	cfg.Onboarding = conf.Onboarding{MerchantEnabled: true, MerchantOrigin: "https://merchant.example.test"}
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	m := &module{portal: "agent"}
	a.Register(inviteTestPortal{}, m)
	require.NoError(t, a.Setup())
	t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
	ctx := a.Context(context.Background())
	pwd, err := a.Deps().Orgs.NewInitialPassword()
	require.NoError(t, err)
	created, err := a.Deps().Orgs.Create(ctx, org.Agent(), org.CreateInput{Name: "邀请测试主体", OwnerUsername: "owner"}, pwd, 1)
	require.NoError(t, err)
	quota := &inviteQuota{allow: true}
	m.invites = quota
	return &inviteAdmissionFixture{app: a, m: m, ctx: ctx, quota: quota,
		owner: auth.Principal{Portal: "agent", OrgID: created.Org.ID, UserID: created.OwnerID, Username: "owner", Super: true}}
}

// 请求身份代表已经通过认证但仍在途的状态；处理函数必须重新通过真实 WithActor 认定。
func (f *inviteAdmissionFixture) request(t *testing.T, actor auth.Principal) (int, httpx.Envelope) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/api/agent/v1/onboarding/invitations", nil).WithContext(auth.WithPrincipal(f.ctx, actor))
	f.m.createInvite(c)
	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env), rec.Body.String())
	return rec.Code, env
}

func (f *inviteAdmissionFixture) invitationCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.app.Deps().DB.Table("ga_org_invitation").Where("agent_id = ?", f.owner.OrgID).Count(&n).Error)
	return n
}

func TestInvitationAdmissionRejectsStaleOwnerBeforeQuota(t *testing.T) {
	f := newInviteAdmissionFixture(t)
	member, err := f.app.Deps().Orgs.CreateMember(f.ctx, org.Agent(), f.owner.OrgID, org.MemberInput{Username: "next-owner"}, "test-hash", f.owner.UserID)
	require.NoError(t, err)
	require.NoError(t, f.app.Deps().Orgs.ChangeOwner(f.ctx, org.Agent(), f.owner.OrgID, member.ID, 1))
	status, env := f.request(t, f.owner)
	require.Equal(t, 403, status)
	require.Equal(t, httpx.CodeForbidden, env.Code)
	require.Empty(t, f.quota.keys, "缓存里的旧主账号身份不占现任主账号的邀请额度")
	require.Zero(t, f.invitationCount(t))
}

func TestInvitationAdmissionRejectsInvalidSessionBeforeQuota(t *testing.T) {
	f := newInviteAdmissionFixture(t)
	actor := f.owner
	actor.SessionID = strings.Repeat("a", 32)
	status, _ := f.request(t, actor)
	require.Equal(t, 401, status)
	require.Empty(t, f.quota.keys, "WithActor 拒绝会话时不预留配额")
	require.Zero(t, f.invitationCount(t))
}

func TestInvitationAdmissionQuotaDenialDoesNotCreate(t *testing.T) {
	f := newInviteAdmissionFixture(t)
	f.quota.allow = false
	status, _ := f.request(t, f.owner)
	require.Equal(t, 429, status)
	require.Equal(t, []string{strconv.FormatUint(f.owner.OrgID, 10)}, f.quota.keys)
	require.Equal(t, []bool{true}, f.quota.inTx)
	require.Zero(t, f.invitationCount(t))
	require.Zero(t, f.quota.refunds)
}

func TestInvitationAdmissionQualifiedCreateFailureKeepsAttempt(t *testing.T) {
	f := newInviteAdmissionFixture(t)
	const callbackName = "invite-admission:test-create-failure"
	callbacks := f.app.Deps().DB.Callback().Create()
	require.NoError(t, callbacks.Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "ga_org_invitation" {
			_ = tx.AddError(errors.New("invitation test storage failure"))
		}
	}))
	t.Cleanup(func() { require.NoError(t, callbacks.Remove(callbackName)) })
	status, _ := f.request(t, f.owner)
	require.Equal(t, 500, status)
	require.Equal(t, []string{strconv.FormatUint(f.owner.OrgID, 10)}, f.quota.keys)
	require.Equal(t, []bool{true}, f.quota.inTx)
	require.Zero(t, f.invitationCount(t))
	require.Zero(t, f.quota.refunds, "通过资格后的创建尝试仍然计数")
}

func TestInvitationAdmissionCurrentOwnerCreatesUsableInvitation(t *testing.T) {
	f := newInviteAdmissionFixture(t)
	status, env := f.request(t, f.owner)
	require.Equal(t, 200, status)
	require.Zero(t, env.Code)
	require.Equal(t, []string{strconv.FormatUint(f.owner.OrgID, 10)}, f.quota.keys)
	require.Equal(t, []bool{true}, f.quota.inTx)
	require.Zero(t, f.quota.refunds)
	require.EqualValues(t, 1, f.invitationCount(t))
	url := env.Data.(map[string]any)["url"].(string)
	secret, ok := strings.CutPrefix(url, "https://merchant.example.test/register#invite=")
	require.True(t, ok)
	require.Len(t, secret, 64)
	application, err := f.app.Deps().Orgs.Apply(f.ctx, org.Merchant(), org.ApplicationInput{
		Name: "有效邀请申请", ContactName: "联系人", ContactPhone: "12345678", OwnerUsername: "applicant", InvitationToken: secret,
	})
	require.NoError(t, err)
	require.Equal(t, f.owner.OrgID, application.AgentID)
	_, err = f.app.Deps().Orgs.Apply(f.ctx, org.Merchant(), org.ApplicationInput{
		Name: "再次申请", ContactName: "联系人", ContactPhone: "12345678", OwnerUsername: "applicant", InvitationToken: secret,
	})
	require.Error(t, err, "邀请仍是一次性使用")
}
