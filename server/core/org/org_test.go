package org_test

// D-065：主体与主体账号。迁移可重跑；建主体同时建主账号（初始密码只返回一次、必须改）；主体端用户来源的契约；
// 停用主体吊销会话（代理商停用不影响名下商户）；重置、更换主账号；商户改归属后 ByChildOrgs 立即跟上；ByChildOrgs 失败即拒绝。

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/scope"
	"github.com/goalladmin/goalladmin/server/migrations"
)

const platformAdmin = 7 // 平台端操作人的用户 ID

type fixture struct {
	t    *testing.T
	ctx  context.Context
	gdb  *gorm.DB
	svc  *org.Service
	sess *session.Manager
	now  time.Time
}

// testHasher 是参数降到最低的哈希器：测试里生成、核对密码哈希都用它。
var testHasher = password.NewHasher(password.Params{Memory: 64, Time: 1})

func setup(t *testing.T) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	clock := func() time.Time { return now }
	return &fixture{
		t: t, ctx: ctx, gdb: gdb, now: now,
		svc:  org.New(org.Options{Now: clock, Hash: testHasher.Hash}),
		sess: session.NewManager(session.Config{RefreshTTL: time.Hour, Now: clock}),
	}
}

// pwd 生成一份初始密码（真实调用方在拿锁之前生成）。
func (f *fixture) pwd() org.InitialPassword {
	f.t.Helper()
	p, err := f.svc.NewInitialPassword()
	require.NoError(f.t, err)
	return p
}

func (f *fixture) create(k org.Kind, name, owner string, agentID uint64) *org.Created {
	f.t.Helper()
	c, err := f.svc.Create(f.ctx, k, org.CreateInput{Name: name, OwnerUsername: owner, AgentID: agentID}, f.pwd(), platformAdmin)
	require.NoError(f.t, err)
	return c
}

// addAccount 直接往账号表插一个员工账号（不经过 CreateMember，测试只要一个账号行）。
func (f *fixture) addAccount(k org.Kind, orgID uint64, username string, status int) uint64 {
	f.t.Helper()
	table := map[string]string{"agent": "ga_agent_user", "merchant": "ga_merchant_user"}[k.Portal()]
	require.NoError(f.t, f.gdb.Exec("INSERT INTO "+table+" (org_id, username, password_hash, status, created_at, updated_at) VALUES (?, ?, 'x', ?, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", orgID, username, status).Error)
	var id uint64
	require.NoError(f.t, f.gdb.Raw("SELECT id FROM "+table+" WHERE org_id = ? AND username = ?", orgID, username).Scan(&id).Error)
	return id
}

func (f *fixture) login(k org.Kind, orgID, userID uint64) string {
	f.t.Helper()
	s, _, err := f.sess.Create(f.ctx, k.Portal(), orgID, userID, "203.0.113.9", "test")
	require.NoError(f.t, err)
	return s.SID
}

// revoked 返回会话的吊销原因；没吊销返回空串。
func (f *fixture) revoked(sid string) string {
	f.t.Helper()
	s, err := f.sess.Get(f.ctx, sid)
	require.NoError(f.t, err)
	require.NotNil(f.t, s)
	if s.RevokedAt == nil {
		return ""
	}
	return s.RevokeReason
}

func (f *fixture) userCol(k org.Kind, id uint64, col string) any {
	f.t.Helper()
	table := map[string]string{"agent": "ga_agent_user", "merchant": "ga_merchant_user"}[k.Portal()]
	row := map[string]any{}
	require.NoError(f.t, f.gdb.Table(table).Where("id = ?", id).Take(&row).Error)
	return row[col]
}

// fieldKey 取校验错误里第一个字段的翻译键。
func fieldKey(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	require.True(t, errors.As(err, &he), "不是 httpx 错误: %v", err)
	require.NotEmpty(t, he.Fields, "没有字段错误: %v", err)
	return he.Fields[0].Key
}

func isNotFound(err error) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == httpx.CodeNotFound
}

// 迁移 00015 可以重跑：再执行一遍不报错；唯一键是编号、(主体, 账号名)。
func TestMigrate_00015_Rerunnable(t *testing.T) {
	f := setup(t)
	raw, err := fs.ReadFile(migrations.Core(), migrations.CoreDir+"/00015_org_tables.sql")
	require.NoError(t, err)
	for _, stmt := range db.SplitStatements(string(raw)) {
		require.NoError(t, f.gdb.Exec(stmt).Error)
	}
	uniques := func(table string) []string {
		var keys []string
		require.NoError(t, f.gdb.Raw("SELECT DISTINCT index_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND non_unique = 0 ORDER BY index_name", table).Scan(&keys).Error)
		return keys
	}
	require.Equal(t, []string{"PRIMARY", "uk_agent_code"}, uniques("ga_agent"))
	require.Equal(t, []string{"PRIMARY", "uk_merchant_code"}, uniques("ga_merchant"))
	require.Equal(t, []string{"PRIMARY", "uk_agent_user_org_username"}, uniques("ga_agent_user"))
	require.Equal(t, []string{"PRIMARY", "uk_merchant_user_org_username"}, uniques("ga_merchant_user"))

	// 账号名只在主体内唯一
	a := f.create(org.Merchant(), "商户甲", "admin", 0)
	b := f.create(org.Merchant(), "商户乙", "admin", 0)
	require.NotEqual(t, a.Org.ID, b.Org.ID)
	require.Error(t, f.gdb.Exec("INSERT INTO ga_merchant_user (org_id, username, password_hash, created_at, updated_at) VALUES (?, 'ADMIN', 'x', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", a.Org.ID).Error,
		"同一主体里账号名重复（排序规则不分大小写）")
}

// 建主体：编号的格式、主账号随主体一起建、初始密码只返回一次（库里只有哈希）、首次登录必须改密；平台操作人记在主体行上。
func TestCreate_OrgWithOwner(t *testing.T) {
	f := setup(t)
	agent, err := f.svc.Create(f.ctx, org.Agent(), org.CreateInput{
		Name: "  华东代理  ", ContactName: "张三", ContactPhone: "+86 138 0000 0000", Sort: 3, Remark: "第一行\n第二行",
		OwnerUsername: "  Boss ", OwnerDisplayName: "老板",
	}, f.pwd(), platformAdmin)
	require.NoError(t, err)
	require.Regexp(t, regexp.MustCompile(`^A\d{8}$`), agent.Org.Code)
	require.Equal(t, "华东代理", agent.Org.Name)
	require.Equal(t, "boss", agent.Org.OwnerUsername, "登录名归一化成小写")
	require.Equal(t, agent.OwnerID, agent.Org.OwnerUserID)
	require.Equal(t, org.StatusEnabled, agent.Org.Status)
	require.EqualValues(t, platformAdmin, agent.Org.CreatedBy)
	require.EqualValues(t, platformAdmin, agent.Org.UpdatedBy)
	require.Len(t, agent.Password, 20)

	// 库里只有哈希，能核对上这次返回的密码；主账号必须改密，操作人是 0（不是这个端的账号建的）
	hash, _ := f.userCol(org.Agent(), agent.OwnerID, "password_hash").(string)
	require.NotContains(t, hash, agent.Password)
	require.True(t, strings.HasPrefix(hash, "$argon2id$"), hash)
	require.True(t, testHasher.Verify(hash, agent.Password))
	require.EqualValues(t, 1, f.userCol(org.Agent(), agent.OwnerID, "must_change_pwd"))
	require.EqualValues(t, 0, f.userCol(org.Agent(), agent.OwnerID, "created_by"))
	require.Equal(t, "老板", f.userCol(org.Agent(), agent.OwnerID, "display_name"))

	got, err := f.svc.Get(f.ctx, org.Agent(), agent.Org.ID)
	require.NoError(t, err)
	require.Equal(t, agent.Org, *got)

	// 商户挂在代理商下面；直属平台的 agent_id 是 0
	m := f.create(org.Merchant(), "商户甲", "admin", agent.Org.ID)
	require.Regexp(t, regexp.MustCompile(`^M\d{8}$`), m.Org.Code)
	require.Equal(t, agent.Org.ID, m.Org.AgentID)
	direct := f.create(org.Merchant(), "商户乙", "admin", 0)
	require.Zero(t, direct.Org.AgentID)
	require.NotEqual(t, m.Password, direct.Password, "每次生成新的随机密码")
	require.Equal(t, "admin", f.userCol(org.Merchant(), m.OwnerID, "display_name"), "没填显示名就用登录名")

	// 两种主体的表互不相干：ID 可以相同，按编号查不到另一种
	_, err = f.svc.Users(org.Merchant()).FindOrgByCode(f.ctx, agent.Org.Code)
	require.ErrorIs(t, err, portal.ErrOrgNotFound)
}

func TestCreate_Validation(t *testing.T) {
	f := setup(t)
	ag := f.create(org.Agent(), "代理", "boss", 0)
	off := f.create(org.Agent(), "停用的代理", "boss", 0)
	require.NoError(t, f.svc.SetStatus(f.ctx, org.Agent(), off.Org.ID, org.StatusDisabled, platformAdmin))

	for name, c := range map[string]struct {
		in    org.CreateInput
		field string
		key   string
	}{
		"空名称":      {org.CreateInput{Name: "  ", OwnerUsername: "admin"}, "name", "org.required"},
		"名称太长":     {org.CreateInput{Name: strings.Repeat("名", 65), OwnerUsername: "admin"}, "name", "org.textLength"},
		"名称有控制字符":  {org.CreateInput{Name: "a\x07b", OwnerUsername: "admin"}, "name", "org.textChars"},
		"名称有不可见字符": {org.CreateInput{Name: "a\u200bb", OwnerUsername: "admin"}, "name", "org.textChars"},
		"名称不是UTF8": {org.CreateInput{Name: "a\xffb", OwnerUsername: "admin"}, "name", "org.textChars"},
		"联系电话太长":   {org.CreateInput{Name: "x", ContactPhone: strings.Repeat("1", 33), OwnerUsername: "admin"}, "contactPhone", "org.textLength"},
		"排序越界":     {org.CreateInput{Name: "x", Sort: org.MaxSort + 1, OwnerUsername: "admin"}, "sort", "org.sort"},
		"备注太长":     {org.CreateInput{Name: "x", Remark: strings.Repeat("r", 256), OwnerUsername: "admin"}, "remark", "org.textLength"},
		"登录名太短":    {org.CreateInput{Name: "x", OwnerUsername: "ab"}, "ownerUsername", "org.username"},
		"登录名数字开头":  {org.CreateInput{Name: "x", OwnerUsername: "1admin"}, "ownerUsername", "org.username"},
		"显示名太长":    {org.CreateInput{Name: "x", OwnerUsername: "admin", OwnerDisplayName: strings.Repeat("d", 65)}, "ownerDisplayName", "org.textLength"},
		"代理商不存在":   {org.CreateInput{Name: "x", OwnerUsername: "admin", AgentID: 999999}, "agentId", "org.agent"},
		"代理商已停用":   {org.CreateInput{Name: "x", OwnerUsername: "admin", AgentID: off.Org.ID}, "agentId", "org.agentDisabled"},
	} {
		_, err := f.svc.Create(f.ctx, org.Merchant(), c.in, f.pwd(), platformAdmin)
		require.Equal(t, c.key, fieldKey(t, err), name)
		var he *httpx.Error
		require.True(t, errors.As(err, &he))
		require.Equal(t, c.field, he.Fields[0].Field, name)
	}
	// 校验失败不留下半个主体
	var n int64
	require.NoError(t, f.gdb.Table("ga_merchant").Count(&n).Error)
	require.Zero(t, n)
	require.NoError(t, f.gdb.Table("ga_merchant_user").Count(&n).Error)
	require.Zero(t, n)

	// 代理商没有所属代理商：传了是调用方的编程错误
	_, err := f.svc.Create(f.ctx, org.Agent(), org.CreateInput{Name: "x", OwnerUsername: "admin", AgentID: ag.Org.ID}, f.pwd(), platformAdmin)
	require.Error(t, err)
	var he *httpx.Error
	require.False(t, errors.As(err, &he), "不是校验错误，是 500")
	// 零值的 Kind 不可用
	_, err = f.svc.Create(f.ctx, org.Kind{}, org.CreateInput{Name: "x", OwnerUsername: "admin"}, f.pwd(), platformAdmin)
	require.Error(t, err)
	// 没有用 NewInitialPassword 生成密码也是编程错误，什么都不建
	_, err = f.svc.Create(f.ctx, org.Merchant(), org.CreateInput{Name: "x", OwnerUsername: "admin"}, org.InitialPassword{}, platformAdmin)
	require.Error(t, err)
	require.False(t, errors.As(err, &he))
	require.Error(t, f.svc.ResetOwnerPassword(f.ctx, org.Agent(), ag.Org.ID, org.InitialPassword{}))
	require.Panics(t, func() { f.svc.Users(org.Kind{}) })
}

// 主体端用户来源的契约（portal.OrgUserProvider、UserLocker、OrgLocker）。
func TestProvider_Contract(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户甲", "admin", 0)
	other := f.create(org.Merchant(), "商户乙", "admin", 0)
	staff := f.addAccount(org.Merchant(), m.Org.ID, "staff", org.StatusEnabled)
	users := f.svc.Users(org.Merchant())

	// 注册：主体端要求 OrgUserProvider + OrgLocker
	require.NoError(t, portal.NewRegistry().Register(portal.Portal{Code: "merchant", Users: users, Scoped: true}))
	_, ok := users.(portal.UserLocker)
	require.True(t, ok)

	o, err := users.FindOrgByCode(f.ctx, portal.NormalizeOrgCode(strings.ToLower(m.Org.Code)))
	require.NoError(t, err)
	require.Equal(t, portal.Org{ID: m.Org.ID, Code: m.Org.Code, Name: "商户甲", Status: 1, OwnerUserID: m.OwnerID}, *o)
	_, err = users.FindOrgByCode(f.ctx, "M00000000")
	require.ErrorIs(t, err, portal.ErrOrgNotFound)
	o, err = users.FindOrgByID(f.ctx, other.Org.ID)
	require.NoError(t, err)
	require.Equal(t, other.Org.Code, o.Code)
	_, err = users.FindOrgByID(f.ctx, 999999)
	require.ErrorIs(t, err, portal.ErrOrgNotFound)

	acc, err := users.FindByOrgUsername(f.ctx, m.Org.ID, "admin")
	require.NoError(t, err)
	require.Equal(t, m.OwnerID, acc.ID)
	require.Equal(t, m.Org.ID, acc.OrgID, "账号必须带上主体")
	require.True(t, acc.MustChangePwd)
	acc, err = users.FindByOrgUsername(f.ctx, other.Org.ID, "admin")
	require.NoError(t, err)
	require.Equal(t, other.OwnerID, acc.ID, "同名账号各属各的主体")
	_, err = users.FindByOrgUsername(f.ctx, other.Org.ID, "staff")
	require.ErrorIs(t, err, portal.ErrAccountNotFound, "别的主体的账号查不到")
	_, err = users.FindByUsername(f.ctx, "admin")
	require.ErrorIs(t, err, portal.ErrAccountNotFound, "主体端不按账号名单独查")

	acc, err = users.FindByID(f.ctx, staff)
	require.NoError(t, err)
	require.Equal(t, m.Org.ID, acc.OrgID)
	require.Equal(t, "staff", acc.Username)
	_, err = users.FindByID(f.ctx, 999999)
	require.ErrorIs(t, err, portal.ErrAccountNotFound)

	// 改密、记录登录
	require.NoError(t, users.UpdatePasswordHash(f.ctx, staff, "new-hash", false))
	require.Equal(t, "new-hash", f.userCol(org.Merchant(), staff, "password_hash"))
	require.EqualValues(t, 0, f.userCol(org.Merchant(), staff, "must_change_pwd"))
	require.NotNil(t, f.userCol(org.Merchant(), staff, "pwd_changed_at"))
	require.ErrorIs(t, users.UpdatePasswordHash(f.ctx, 999999, "h", false), portal.ErrAccountNotFound)
	require.NoError(t, users.TouchLogin(f.ctx, staff, "203.0.113.7", f.now))
	require.Equal(t, "203.0.113.7", f.userCol(org.Merchant(), staff, "last_login_ip"))

	// 两把锁都是排他锁：锁住之后，别的连接连共享锁都拿不到（D-063 第 4 条）
	require.NoError(t, db.Tx(f.ctx, func(ctx context.Context) error {
		o, err := users.(portal.OrgLocker).LockOrgByID(ctx, m.Org.ID)
		require.NoError(t, err)
		require.Equal(t, m.Org.ID, o.ID)
		a, err := users.(portal.UserLocker).LockByID(ctx, staff)
		require.NoError(t, err)
		require.Equal(t, m.Org.ID, a.OrgID)
		require.Error(t, f.gdb.Exec("SELECT id FROM ga_merchant WHERE id = ? FOR SHARE NOWAIT", m.Org.ID).Error, "主体行是排他锁")
		require.Error(t, f.gdb.Exec("SELECT id FROM ga_merchant_user WHERE id = ? FOR SHARE NOWAIT", staff).Error, "账号行是排他锁")
		require.NoError(t, f.gdb.Exec("SELECT id FROM ga_merchant WHERE id = ? FOR SHARE NOWAIT", other.Org.ID).Error, "别的主体不受影响")
		_, err = users.(portal.OrgLocker).LockOrgByID(ctx, 999999)
		require.ErrorIs(t, err, portal.ErrOrgNotFound)
		return nil
	}))
}

// 停用主体：同一个事务里吊销它的全部会话，别的主体、另一个端同 ID 的主体不受影响；启用后不会恢复旧会话。
func TestSetStatus_DisableRevokesOrgSessions(t *testing.T) {
	f := setup(t)
	a := f.create(org.Merchant(), "商户甲", "admin", 0)
	b := f.create(org.Merchant(), "商户乙", "admin", 0)
	ag := f.create(org.Agent(), "代理", "boss", 0)
	require.Equal(t, a.Org.ID, ag.Org.ID, "前提：两种主体的 ID 撞在一起")
	staff := f.addAccount(org.Merchant(), a.Org.ID, "staff", org.StatusEnabled)
	sOwner, sStaff := f.login(org.Merchant(), a.Org.ID, a.OwnerID), f.login(org.Merchant(), a.Org.ID, staff)
	sB, sAgent := f.login(org.Merchant(), b.Org.ID, b.OwnerID), f.login(org.Agent(), ag.Org.ID, ag.OwnerID)

	require.NoError(t, f.svc.SetStatus(f.ctx, org.Merchant(), a.Org.ID, org.StatusDisabled, 9))
	got, err := f.svc.Get(f.ctx, org.Merchant(), a.Org.ID)
	require.NoError(t, err)
	require.Equal(t, org.StatusDisabled, got.Status)
	require.EqualValues(t, 9, got.UpdatedBy)
	require.Equal(t, auth.RevokeDisabled, f.revoked(sOwner))
	require.Equal(t, auth.RevokeDisabled, f.revoked(sStaff))
	require.Empty(t, f.revoked(sB), "别的主体不受影响")
	require.Empty(t, f.revoked(sAgent), "另一个端同 ID 的主体不受影响")

	o, err := f.svc.Users(org.Merchant()).FindOrgByID(f.ctx, a.Org.ID)
	require.NoError(t, err)
	require.False(t, o.Enabled(), "登录按主体停用处理")

	require.NoError(t, f.svc.SetStatus(f.ctx, org.Merchant(), a.Org.ID, org.StatusEnabled, 9))
	require.Equal(t, auth.RevokeDisabled, f.revoked(sOwner), "启用不恢复旧会话")

	require.Equal(t, "org.status", fieldKey(t, f.svc.SetStatus(f.ctx, org.Merchant(), a.Org.ID, 2, 9)))
	require.True(t, isNotFound(f.svc.SetStatus(f.ctx, org.Merchant(), 999999, 0, 9)))

	// 停用失败（事务回滚）时会话也不吊销：在外层事务里停用，然后回滚
	s2 := f.login(org.Merchant(), b.Org.ID, b.OwnerID)
	boom := errors.New("boom")
	require.ErrorIs(t, db.Tx(f.ctx, func(ctx context.Context) error {
		require.NoError(t, f.svc.SetStatus(ctx, org.Merchant(), b.Org.ID, org.StatusDisabled, 9))
		return boom
	}), boom)
	require.Empty(t, f.revoked(s2))
	got, err = f.svc.Get(f.ctx, org.Merchant(), b.Org.ID)
	require.NoError(t, err)
	require.Equal(t, org.StatusEnabled, got.Status)
}

// 停用主体先锁它的账号行、再吊销会话（"主体行 → 账号行 → 会话行"）：主体端的本人改密拿着自己的账号行时，停用排在它后面，
// 排队期间还没碰任何会话行。跳过账号行直接按主体批量改会话行的话，两边锁会话行的顺序不同，会死锁。
func TestSetStatus_LocksAccountsBeforeSessions(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户", "admin", 0)
	staff := f.addAccount(org.Merchant(), m.Org.ID, "staff", org.StatusEnabled)
	sOwner, sStaff := f.login(org.Merchant(), m.Org.ID, m.OwnerID), f.login(org.Merchant(), m.Org.ID, staff)

	// 另一个事务（像本人改密那样）锁住员工的账号行；测试失败时也要放掉，免得卡住清理
	tx := f.gdb.Begin()
	release := sync.OnceFunc(func() { tx.Rollback() })
	t.Cleanup(release)
	require.NoError(t, tx.Exec("SELECT id FROM ga_merchant_user WHERE id = ? FOR UPDATE", staff).Error)

	done := make(chan error, 1)
	go func() { done <- f.svc.SetStatus(f.ctx, org.Merchant(), m.Org.ID, org.StatusDisabled, 9) }()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("停用没有等账号行的锁就完成了: %v", err)
	default:
	}
	for _, sid := range []string{sOwner, sStaff} {
		require.NoError(t, f.gdb.Exec("SELECT id FROM ga_session WHERE sid = ? FOR UPDATE NOWAIT", sid).Error, "等账号行时还不应锁会话行")
	}
	require.Empty(t, f.revoked(sOwner))
	release()
	require.NoError(t, <-done)
	require.Equal(t, auth.RevokeDisabled, f.revoked(sOwner))
	require.Equal(t, auth.RevokeDisabled, f.revoked(sStaff))
}

// 代理商停用不影响名下商户：商户照常启用，会话照常有效。
func TestSetStatus_AgentDisableKeepsMerchants(t *testing.T) {
	f := setup(t)
	ag := f.create(org.Agent(), "代理", "boss", 0)
	m := f.create(org.Merchant(), "商户", "admin", ag.Org.ID)
	sm := f.login(org.Merchant(), m.Org.ID, m.OwnerID)
	sa := f.login(org.Agent(), ag.Org.ID, ag.OwnerID)

	require.NoError(t, f.svc.SetStatus(f.ctx, org.Agent(), ag.Org.ID, org.StatusDisabled, platformAdmin))
	require.Equal(t, auth.RevokeDisabled, f.revoked(sa))
	require.Empty(t, f.revoked(sm))
	got, err := f.svc.Get(f.ctx, org.Merchant(), m.Org.ID)
	require.NoError(t, err)
	require.Equal(t, org.StatusEnabled, got.Status)
	require.Equal(t, ag.Org.ID, got.AgentID, "归属不变")
}

// 重置主账号密码：新的随机密码只返回一次，下次登录必须改；主账号的会话全部吊销，员工的会话不受影响。
func TestResetOwnerPassword(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户", "admin", 0)
	staff := f.addAccount(org.Merchant(), m.Org.ID, "staff", org.StatusEnabled)
	users := f.svc.Users(org.Merchant())
	require.NoError(t, users.UpdatePasswordHash(f.ctx, m.OwnerID, "changed-by-owner", false))
	s1, s2 := f.login(org.Merchant(), m.Org.ID, m.OwnerID), f.login(org.Merchant(), m.Org.ID, m.OwnerID)
	sStaff := f.login(org.Merchant(), m.Org.ID, staff)

	pwd := f.pwd()
	require.NoError(t, f.svc.ResetOwnerPassword(f.ctx, org.Merchant(), m.Org.ID, pwd))
	plain := pwd.Plain()
	require.Len(t, plain, 20)
	require.NotEqual(t, m.Password, plain)
	acc, err := users.FindByID(f.ctx, m.OwnerID)
	require.NoError(t, err)
	require.True(t, testHasher.Verify(acc.PasswordHash, plain))
	require.True(t, acc.MustChangePwd)
	require.EqualValues(t, 0, f.userCol(org.Merchant(), m.OwnerID, "updated_by"), "平台改的，账号行的操作人为 0")
	require.Equal(t, auth.RevokePwdChange, f.revoked(s1))
	require.Equal(t, auth.RevokePwdChange, f.revoked(s2))
	require.Empty(t, f.revoked(sStaff), "只吊销主账号的会话")

	require.True(t, isNotFound(f.svc.ResetOwnerPassword(f.ctx, org.Merchant(), 999999, f.pwd())))
	require.True(t, isNotFound(f.svc.ResetOwnerPassword(f.ctx, org.Agent(), m.Org.ID, f.pwd())), "按种类查对应的表")
}

// 更换主账号：只能换成本主体里启用的账号；别的主体的账号和不存在的一样。
func TestChangeOwner(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户", "admin", 0)
	other := f.create(org.Merchant(), "别家", "admin", 0)
	staff := f.addAccount(org.Merchant(), m.Org.ID, "staff", org.StatusEnabled)
	off := f.addAccount(org.Merchant(), m.Org.ID, "off", org.StatusDisabled)

	require.Equal(t, "org.ownerNotInOrg", fieldKey(t, f.svc.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, other.OwnerID, 9)))
	require.Equal(t, "org.ownerNotInOrg", fieldKey(t, f.svc.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, 999999, 9)))
	require.Equal(t, "org.ownerDisabled", fieldKey(t, f.svc.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, off, 9)))
	require.True(t, isNotFound(f.svc.ChangeOwner(f.ctx, org.Merchant(), 999999, staff, 9)))
	// 原主账号、新主账号、别的主体的主账号各有一个会话；清角色的回调记下被清的账号（D-101）
	sidOld, sidNew, sidOther := f.login(org.Merchant(), m.Org.ID, m.OwnerID), f.login(org.Merchant(), m.Org.ID, staff), f.login(org.Merchant(), other.Org.ID, other.OwnerID)
	var cleared []string
	svc := org.New(org.Options{Now: func() time.Time { return f.now }, Hash: testHasher.Hash,
		ClearRoles: func(ctx context.Context, portal string, userID uint64) error {
			require.True(t, db.InTx(ctx), "在更换主账号的事务里清")
			cleared = append(cleared, fmt.Sprintf("%s/%d", portal, userID))
			return nil
		}})
	require.NoError(t, svc.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, m.OwnerID, 9), "换成自己：什么都不做")
	require.Empty(t, cleared)
	require.Empty(t, f.revoked(sidOld))

	require.NoError(t, svc.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, staff, 9))
	require.Equal(t, []string{fmt.Sprintf("merchant/%d", m.OwnerID)}, cleared, "只清原主账号的角色")
	require.Equal(t, auth.RevokeAdmin, f.revoked(sidOld), "原主账号的会话吊销")
	require.Empty(t, f.revoked(sidNew), "新主账号的会话不动")
	require.Empty(t, f.revoked(sidOther), "别的主体不动")
	// 清角色失败：整个更换回滚
	failing := org.New(org.Options{Now: func() time.Time { return f.now }, Hash: testHasher.Hash,
		ClearRoles: func(context.Context, string, uint64) error { return errors.New("boom") }})
	require.Error(t, failing.ChangeOwner(f.ctx, org.Merchant(), m.Org.ID, m.OwnerID, 9))
	require.Empty(t, f.revoked(sidNew), "回滚：会话没有被吊销")
	o, err := f.svc.Users(org.Merchant()).FindOrgByID(f.ctx, m.Org.ID)
	require.NoError(t, err)
	require.Equal(t, staff, o.OwnerUserID)
	got, err := f.svc.Get(f.ctx, org.Merchant(), m.Org.ID)
	require.NoError(t, err)
	require.Equal(t, "staff", got.OwnerUsername)
	require.EqualValues(t, 9, got.UpdatedBy)

	accs, total, err := f.svc.Accounts(f.ctx, org.Merchant(), m.Org.ID, nil, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
	owners := map[string]bool{}
	for _, a := range accs {
		owners[a.Username] = a.Owner
		require.Equal(t, m.Org.ID, a.OrgID)
	}
	require.Equal(t, map[string]bool{"admin": false, "staff": true, "off": false}, owners)
	enabled := org.StatusEnabled
	_, total, err = f.svc.Accounts(f.ctx, org.Merchant(), m.Org.ID, &enabled, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	_, _, err = f.svc.Accounts(f.ctx, org.Merchant(), 999999, nil, httpx.PageQuery{Page: 1, PageSize: 20})
	require.True(t, isNotFound(err))
}

// agentCtx 是代理商端的已登录身份。
func agentCtx(parent context.Context, orgID uint64) context.Context {
	return auth.WithPrincipal(parent, auth.Principal{Portal: "agent", UserID: 1, OrgID: orgID})
}

func childMerchants(t *testing.T, ctx context.Context) []uint64 {
	t.Helper()
	ids := []uint64{}
	require.NoError(t, db.From(ctx).Table("ga_merchant AS m").Scopes(org.ByChildOrgs(ctx, "m.id")).Order("m.id").Pluck("m.id", &ids).Error)
	return ids
}

// 商户改归属：只能挂到存在且启用的代理商下；改了之后原代理商下一次查询就看不到（ByChildOrgs 每次现算）。
func TestSetAgent_ReflectedInByChildOrgs(t *testing.T) {
	f := setup(t)
	a1 := f.create(org.Agent(), "代理一", "boss", 0)
	a2 := f.create(org.Agent(), "代理二", "boss", 0)
	off := f.create(org.Agent(), "停用的代理", "boss", 0)
	require.NoError(t, f.svc.SetStatus(f.ctx, org.Agent(), off.Org.ID, org.StatusDisabled, platformAdmin))
	m1 := f.create(org.Merchant(), "商户一", "admin", a1.Org.ID)
	m2 := f.create(org.Merchant(), "商户二", "admin", a2.Org.ID)
	f.create(org.Merchant(), "直属商户", "admin", 0)

	require.Equal(t, []uint64{m1.Org.ID}, childMerchants(t, agentCtx(f.ctx, a1.Org.ID)))
	require.Equal(t, []uint64{m2.Org.ID}, childMerchants(t, agentCtx(f.ctx, a2.Org.ID)))

	require.NoError(t, f.svc.SetAgent(f.ctx, m1.Org.ID, a2.Org.ID, 9))
	require.Empty(t, childMerchants(t, agentCtx(f.ctx, a1.Org.ID)), "原代理商立即看不到")
	require.Equal(t, []uint64{m1.Org.ID, m2.Org.ID}, childMerchants(t, agentCtx(f.ctx, a2.Org.ID)))
	got, err := f.svc.Get(f.ctx, org.Merchant(), m1.Org.ID)
	require.NoError(t, err)
	require.Equal(t, a2.Org.ID, got.AgentID)
	require.EqualValues(t, 9, got.UpdatedBy)

	require.Equal(t, "org.agent", fieldKey(t, f.svc.SetAgent(f.ctx, m1.Org.ID, 999999, 9)))
	require.Equal(t, "org.agentDisabled", fieldKey(t, f.svc.SetAgent(f.ctx, m1.Org.ID, off.Org.ID, 9)))
	require.True(t, isNotFound(f.svc.SetAgent(f.ctx, 999999, a1.Org.ID, 9)))

	require.NoError(t, f.svc.SetAgent(f.ctx, m1.Org.ID, 0, 9), "改为直属平台")
	require.Equal(t, []uint64{m2.Org.ID}, childMerchants(t, agentCtx(f.ctx, a2.Org.ID)))

	// 按代理商筛选商户列表
	zero := uint64(0)
	list, total, err := f.svc.List(f.ctx, org.Merchant(), org.Filter{AgentID: &zero}, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	for _, it := range list {
		require.Zero(t, it.AgentID)
		require.Equal(t, "admin", it.OwnerUsername)
	}
	_, _, err = f.svc.List(f.ctx, org.Agent(), org.Filter{AgentID: &zero}, httpx.PageQuery{Page: 1, PageSize: 20})
	require.Error(t, err, "代理商没有所属代理商")
}

// ByChildOrgs 失败即拒绝：不是代理商端的已登录身份时，查询直接报错，绝不退化成"不加条件"。
func TestByChildOrgs_FailsClosed(t *testing.T) {
	f := setup(t)
	ag := f.create(org.Agent(), "代理", "boss", 0)
	f.create(org.Merchant(), "商户", "admin", ag.Org.ID)

	for name, ctx := range map[string]context.Context{
		"没有身份":      f.ctx,
		"平台端身份":     auth.WithPrincipal(f.ctx, auth.Principal{Portal: "platform", UserID: 1}),
		"商户端身份":     auth.WithPrincipal(f.ctx, auth.Principal{Portal: "merchant", UserID: 1, OrgID: ag.Org.ID}),
		"代理商端但没有主体": auth.WithPrincipal(f.ctx, auth.Principal{Portal: "agent", UserID: 1}),
	} {
		var ids []uint64
		err := db.From(ctx).Table("ga_merchant").Scopes(org.ByChildOrgs(ctx, "id")).Pluck("id", &ids).Error
		require.ErrorIs(t, err, scope.ErrNoOrg, name)
		require.Empty(t, ids, name)
	}
	ctx := agentCtx(f.ctx, ag.Org.ID)
	var ids []uint64
	err := db.From(ctx).Table("ga_merchant").Scopes(org.ByChildOrgs(ctx, "id) OR (1=1")).Pluck("id", &ids).Error
	require.Error(t, err, "列名只认小写字母、数字、下划线")
	require.Empty(t, ids)
}

// 列出、下线主体的会话：只看得到、下得了这个端这个主体的会话。
func TestSessions_ListAndRevoke(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户", "admin", 0)
	other := f.create(org.Merchant(), "别家", "admin", 0)
	ag := f.create(org.Agent(), "代理", "boss", 0)
	require.Equal(t, m.Org.ID, ag.Org.ID)
	staff := f.addAccount(org.Merchant(), m.Org.ID, "staff", org.StatusEnabled)
	s1, s2 := f.login(org.Merchant(), m.Org.ID, m.OwnerID), f.login(org.Merchant(), m.Org.ID, staff)
	sOther, sAgent := f.login(org.Merchant(), other.Org.ID, other.OwnerID), f.login(org.Agent(), ag.Org.ID, ag.OwnerID)

	list, total, err := f.svc.Sessions(f.ctx, org.Merchant(), m.Org.ID, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	got := map[string]string{}
	for _, s := range list {
		got[s.SID] = s.Username
	}
	require.Equal(t, map[string]string{s1: "admin", s2: "staff"}, got)

	require.True(t, isNotFound(f.svc.RevokeSession(f.ctx, org.Merchant(), m.Org.ID, sOther)), "别的主体的会话")
	require.True(t, isNotFound(f.svc.RevokeSession(f.ctx, org.Merchant(), m.Org.ID, sAgent)), "另一个端同 ID 主体的会话")
	require.True(t, isNotFound(f.svc.RevokeSession(f.ctx, org.Merchant(), m.Org.ID, strings.Repeat("0", 32))))
	require.Empty(t, f.revoked(sOther))
	require.Empty(t, f.revoked(sAgent))

	require.NoError(t, f.svc.RevokeSession(f.ctx, org.Merchant(), m.Org.ID, s2))
	require.Equal(t, auth.RevokeAdmin, f.revoked(s2))
	require.NoError(t, f.svc.RevokeSession(f.ctx, org.Merchant(), m.Org.ID, s2), "再下线一次不算错")
	_, total, err = f.svc.Sessions(f.ctx, org.Merchant(), m.Org.ID, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	_, _, err = f.svc.Sessions(f.ctx, org.Merchant(), 999999, httpx.PageQuery{Page: 1, PageSize: 20})
	require.True(t, isNotFound(err))
}

// 175. 会话和账号必须属于同一主体；列表、分页总数都排除归属矛盾的历史记录。
func TestSessions_175_OrgAndAccountMustAgree(t *testing.T) {
	for _, kind := range []org.Kind{org.Agent(), org.Merchant()} {
		t.Run(kind.Portal(), func(t *testing.T) {
			f := setup(t)
			current := f.create(kind, "当前", "admin", 0)
			other := f.create(kind, "其他", "admin", 0)
			want := []string{f.login(kind, current.Org.ID, current.OwnerID), f.login(kind, current.Org.ID, current.OwnerID)}
			require.NoError(t, f.gdb.Model(&session.Session{}).Where("sid = ?", want[0]).Update("last_seen_at", f.now.Add(-time.Minute)).Error)
			f.login(kind, other.Org.ID, current.OwnerID)
			f.login(kind, 0, current.OwnerID)
			f.login(kind, current.Org.ID, other.OwnerID)
			var got []string
			for page := 1; page <= 2; page++ {
				rows, total, err := f.svc.Sessions(f.ctx, kind, current.Org.ID, httpx.PageQuery{Page: page, PageSize: 1})
				require.NoError(t, err)
				require.EqualValues(t, 2, total)
				require.Len(t, rows, 1)
				got = append(got, rows[0].SID)
			}
			require.ElementsMatch(t, want, got)
			rows, total, err := f.svc.Sessions(f.ctx, kind, other.Org.ID, httpx.PageQuery{Page: 1, PageSize: 20})
			require.NoError(t, err)
			require.Zero(t, total)
			require.Empty(t, rows)
			_, _, err = f.sess.ListActivePageInOrg(f.ctx, kind.Portal(), 0, nil, 1, 20)
			require.Error(t, err, "主体 ID 缺失时不允许退化为全端查询")
		})
	}
}

// 列表：关键字按字面匹配（% 和 _ 不是通配符）、按状态筛选；改资料整份替换。
func TestListAndUpdate(t *testing.T) {
	f := setup(t)
	a := f.create(org.Merchant(), "100%纯", "admin", 0)
	b := f.create(org.Merchant(), "1000纯", "admin", 0)
	list, total, err := f.svc.List(f.ctx, org.Merchant(), org.Filter{}, httpx.PageQuery{Page: 1, PageSize: 20, Keyword: "0%"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, a.Org.ID, list[0].ID)
	_, total, err = f.svc.List(f.ctx, org.Merchant(), org.Filter{}, httpx.PageQuery{Page: 1, PageSize: 20, Keyword: b.Org.Code})
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "按编号查")

	require.NoError(t, f.svc.Update(f.ctx, org.Merchant(), a.Org.ID, org.UpdateInput{Name: " 新名字 ", ContactName: "李四", Sort: 5}, 11))
	got, err := f.svc.Get(f.ctx, org.Merchant(), a.Org.ID)
	require.NoError(t, err)
	require.Equal(t, "新名字", got.Name)
	require.Equal(t, "李四", got.ContactName)
	require.EqualValues(t, 5, got.Sort)
	require.Equal(t, a.Org.Code, got.Code, "编号不可改")
	require.Equal(t, a.OwnerID, got.OwnerUserID)
	require.EqualValues(t, 11, got.UpdatedBy)
	require.EqualValues(t, platformAdmin, got.CreatedBy)
	require.Equal(t, "org.required", fieldKey(t, f.svc.Update(f.ctx, org.Merchant(), a.Org.ID, org.UpdateInput{}, 11)))
	require.True(t, isNotFound(f.svc.Update(f.ctx, org.Merchant(), 999999, org.UpdateInput{Name: "x"}, 11)))

	off := org.StatusDisabled
	require.NoError(t, f.svc.SetStatus(f.ctx, org.Merchant(), b.Org.ID, off, 11))
	list, total, err = f.svc.List(f.ctx, org.Merchant(), org.Filter{Status: &off}, httpx.PageQuery{Page: 1, PageSize: 20})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, b.Org.ID, list[0].ID)

	briefs, err := f.svc.Briefs(f.ctx, org.Merchant(), []uint64{a.Org.ID, b.Org.ID, 999999})
	require.NoError(t, err)
	require.Equal(t, map[uint64]org.Brief{
		a.Org.ID: {ID: a.Org.ID, Code: a.Org.Code, Name: "新名字", Status: 1},
		b.Org.ID: {ID: b.Org.ID, Code: b.Org.Code, Name: "1000纯", Status: 0},
	}, briefs)
}

// 算哈希之前的便宜检查（D-068）：CheckCreate 只做字段校验、不读库；CheckResetOwner 看主体在不在、有没有主账号；
// CheckMember 看字段和登录名在本主体内是否已被占用。结论和真正去做时一致。
func TestChecksBeforeHashing(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户", "admin", 0)
	other := f.create(org.Merchant(), "别的商户", "admin", 0)
	f.addAccount(org.Merchant(), m.Org.ID, "staff", org.StatusEnabled)

	require.NoError(t, f.svc.CheckCreate(org.Merchant(), org.CreateInput{Name: "x", OwnerUsername: "Admin"}))
	require.NoError(t, f.svc.CheckCreate(org.Merchant(), org.CreateInput{Name: "x", OwnerUsername: "admin", AgentID: 999999}), "不读库：代理商在不在由 Create 认定")
	require.Equal(t, "org.required", fieldKey(t, f.svc.CheckCreate(org.Merchant(), org.CreateInput{Name: " ", OwnerUsername: "admin"})))
	require.Equal(t, "org.username", fieldKey(t, f.svc.CheckCreate(org.Agent(), org.CreateInput{Name: "x", OwnerUsername: "1admin"})))
	require.Equal(t, "org.textLength", fieldKey(t, f.svc.CheckCreate(org.Agent(), org.CreateInput{Name: "x", OwnerUsername: "admin", OwnerDisplayName: strings.Repeat("d", 65)})))
	require.Error(t, f.svc.CheckCreate(org.Agent(), org.CreateInput{Name: "x", OwnerUsername: "admin", AgentID: 1}), "代理商没有上级")
	require.Error(t, f.svc.CheckCreate(org.Kind{}, org.CreateInput{Name: "x", OwnerUsername: "admin"}))

	require.NoError(t, f.svc.CheckResetOwner(f.ctx, org.Merchant(), m.Org.ID))
	require.True(t, isNotFound(f.svc.CheckResetOwner(f.ctx, org.Merchant(), 999999)))
	require.True(t, isNotFound(f.svc.CheckResetOwner(f.ctx, org.Agent(), m.Org.ID)), "按种类查对应的表")
	require.NoError(t, f.gdb.Exec("UPDATE ga_merchant SET owner_user_id = 0 WHERE id = ?", other.Org.ID).Error)
	require.Equal(t, "org.noOwner", fieldKey(t, f.svc.CheckResetOwner(f.ctx, org.Merchant(), other.Org.ID)))
	require.Equal(t, "org.noOwner", fieldKey(t, f.svc.ResetOwnerPassword(f.ctx, org.Merchant(), other.Org.ID, f.pwd())), "和真正重置时的结论一致")

	check := func(orgID uint64, in org.MemberInput) error {
		return f.svc.CheckMember(f.ctx, org.Merchant(), orgID, in)
	}
	require.NoError(t, check(m.Org.ID, org.MemberInput{Username: "newbie"}))
	require.NoError(t, check(other.Org.ID, org.MemberInput{Username: "staff"}), "登录名只在主体内唯一")
	require.Equal(t, "org.username", fieldKey(t, check(m.Org.ID, org.MemberInput{Username: "9x"})))
	require.Equal(t, "org.textChars", fieldKey(t, check(m.Org.ID, org.MemberInput{Username: "newbie", DisplayName: "a\x07b"})))
	require.Equal(t, "org.sort", fieldKey(t, check(m.Org.ID, org.MemberInput{Username: "newbie", Sort: org.MaxSort + 1})))
	for _, name := range []string{"staff", " STAFF ", "admin"} {
		err := check(m.Org.ID, org.MemberInput{Username: name})
		var he *httpx.Error
		require.True(t, errors.As(err, &he), "%q: %v", name, err)
		require.Equal(t, httpx.CodeConflict, he.Code, name)
		require.Equal(t, "org.usernameTaken", he.Key, name)
	}
	_, err := f.svc.CreateMember(f.ctx, org.Merchant(), m.Org.ID, org.MemberInput{Username: "staff"}, "hash", 1)
	var he *httpx.Error
	require.True(t, errors.As(err, &he))
	require.Equal(t, "org.usernameTaken", he.Key, "和真正去建时的结论一致")
	require.Error(t, f.svc.CheckMember(f.ctx, org.Kind{}, m.Org.ID, org.MemberInput{Username: "newbie"}))
}

// 初始密码的哈希由调用方给的函数算（进程的哈希器加并发闸门，D-068、D-070）：它回 429 时原样交出去，没有密码；
// 没给函数时用默认参数自己算。
func TestNewInitialPassword_UsesGivenHash(t *testing.T) {
	var calls int
	full := true
	svc := org.New(org.Options{Hash: func(plain string) (string, error) {
		if full {
			return "", httpx.ErrTooManyRequests
		}
		calls++
		return testHasher.Hash(plain)
	}})
	pwd, err := svc.NewInitialPassword()
	require.ErrorIs(t, err, httpx.ErrTooManyRequests)
	require.Empty(t, pwd.Plain())
	require.Zero(t, calls)
	full = false
	pwd, err = svc.NewInitialPassword()
	require.NoError(t, err)
	require.Len(t, pwd.Plain(), 20)
	require.Equal(t, 1, calls)

	pwd, err = org.New(org.Options{}).NewInitialPassword()
	require.NoError(t, err)
	require.Len(t, pwd.Plain(), 20)
}

// 用户来源的 RehashPassword（portal.PasswordRehasher，D-070）：只换哈希这一列，必须改密、改密时间、更新时间都不动；
// 库里的哈希已经不是给的那个旧哈希时什么都不做；按种类写对应的表。
func TestProvider_RehashPassword(t *testing.T) {
	f := setup(t)
	m := f.create(org.Merchant(), "商户", "admin", 0)
	a := f.create(org.Agent(), "代理", "admin", 0)
	users := f.svc.Users(org.Merchant())
	rh, ok := users.(portal.PasswordRehasher)
	require.True(t, ok, "主体账号的用户来源实现了 PasswordRehasher")

	cols := func(k org.Kind, id uint64) []any {
		return []any{f.userCol(k, id, "must_change_pwd"), f.userCol(k, id, "pwd_changed_at"), f.userCol(k, id, "updated_at"), f.userCol(k, id, "updated_by")}
	}
	old, _ := f.userCol(org.Merchant(), m.OwnerID, "password_hash").(string)
	before := cols(org.Merchant(), m.OwnerID)
	agentBefore, _ := f.userCol(org.Agent(), a.OwnerID, "password_hash").(string)

	require.NoError(t, rh.RehashPassword(f.ctx, m.OwnerID, "not-the-current-hash", "new-hash-1"))
	require.Equal(t, old, f.userCol(org.Merchant(), m.OwnerID, "password_hash"), "旧哈希对不上：什么都不写")

	require.NoError(t, rh.RehashPassword(f.ctx, m.OwnerID, old, "new-hash-1"))
	require.Equal(t, "new-hash-1", f.userCol(org.Merchant(), m.OwnerID, "password_hash"))
	require.Equal(t, before, cols(org.Merchant(), m.OwnerID), "只换哈希这一列")
	require.Equal(t, agentBefore, f.userCol(org.Agent(), a.OwnerID, "password_hash"), "代理商表里同 ID 的账号不受影响")

	require.NoError(t, rh.RehashPassword(f.ctx, 999999, "x", "y"), "账号不存在也不报错：没有可换的")
}
