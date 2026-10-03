package rbac

// 主体端的授权（D-063，规范 §13.2 第 127–130 条）：角色按主体隔离、主账号、主体内的授权规则、主体行锁、
// 判定忽略别的主体的成员关系。连真实 MySQL；主体和账号放在测试自建的两张表里，
// 用户来源的 LockOrgByID 是真正的 SELECT ... FOR UPDATE，锁的效果是真实的。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/scope"
	"github.com/goalladmin/goalladmin/server/migrations"
)

const shopP = "shop"

// ---- 测试用的主体端用户来源：两张测试表，读写都走 db.From(ctx)，能参与调用方的事务 ----

type sqlOrgUsers struct{}

type testOrgRow struct {
	ID          uint64 `gorm:"column:id"`
	Code        string `gorm:"column:code"`
	Name        string `gorm:"column:name"`
	Status      int    `gorm:"column:status"`
	OwnerUserID uint64 `gorm:"column:owner_user_id"`
}

type testUserRow struct {
	ID       uint64 `gorm:"column:id"`
	OrgID    uint64 `gorm:"column:org_id"`
	Username string `gorm:"column:username"`
	Status   int    `gorm:"column:status"`
}

func (r testOrgRow) org() *portal.Org {
	return &portal.Org{ID: r.ID, Code: r.Code, Name: r.Name, Status: r.Status, OwnerUserID: r.OwnerUserID}
}

func (sqlOrgUsers) FindByUsername(context.Context, string) (*portal.Account, error) {
	return nil, portal.ErrAccountNotFound
}

func (sqlOrgUsers) FindByID(ctx context.Context, id uint64) (*portal.Account, error) {
	var r testUserRow
	if err := db.From(ctx).Raw("SELECT id, org_id, username, status FROM t_org_user WHERE id = ?", id).Scan(&r).Error; err != nil {
		return nil, err
	}
	if r.ID == 0 {
		return nil, portal.ErrAccountNotFound
	}
	return &portal.Account{ID: r.ID, OrgID: r.OrgID, Username: r.Username, Status: r.Status}, nil
}

func (sqlOrgUsers) UpdatePasswordHash(context.Context, uint64, string, bool) error { return nil }
func (sqlOrgUsers) TouchLogin(context.Context, uint64, string, time.Time) error    { return nil }

func (u sqlOrgUsers) findOrg(ctx context.Context, q string, arg any) (*portal.Org, error) {
	var r testOrgRow
	if err := db.From(ctx).Raw(q, arg).Scan(&r).Error; err != nil {
		return nil, err
	}
	if r.ID == 0 {
		return nil, portal.ErrOrgNotFound
	}
	return r.org(), nil
}

func (u sqlOrgUsers) FindOrgByCode(ctx context.Context, code string) (*portal.Org, error) {
	return u.findOrg(ctx, "SELECT id, code, name, status, owner_user_id FROM t_org WHERE code = ?", code)
}

func (u sqlOrgUsers) FindOrgByID(ctx context.Context, id uint64) (*portal.Org, error) {
	return u.findOrg(ctx, "SELECT id, code, name, status, owner_user_id FROM t_org WHERE id = ?", id)
}

// LockOrgByID 是真正的排他锁（D-063：主体端必须实现，且必须是排他锁）。
func (u sqlOrgUsers) LockOrgByID(ctx context.Context, id uint64) (*portal.Org, error) {
	return u.findOrg(ctx, "SELECT id, code, name, status, owner_user_id FROM t_org WHERE id = ? FOR UPDATE", id)
}

func (sqlOrgUsers) FindByOrgUsername(ctx context.Context, orgID uint64, username string) (*portal.Account, error) {
	var r testUserRow
	if err := db.From(ctx).Raw("SELECT id, org_id, username, status FROM t_org_user WHERE org_id = ? AND username = ?", orgID, username).Scan(&r).Error; err != nil {
		return nil, err
	}
	if r.ID == 0 {
		return nil, portal.ErrAccountNotFound
	}
	return &portal.Account{ID: r.ID, OrgID: r.OrgID, Username: r.Username, Status: r.Status}, nil
}

// ---- 夹具：两个主体，各一个主账号、两个员工 ----

type orgFix struct {
	s                       *Service
	ctx                     context.Context // 不带身份
	users                   sqlOrgUsers
	orgA, orgB              uint64
	ownerA, staffA, staffA2 auth.Principal
	ownerB, staffB          auth.Principal
}

// as 返回带着身份 p 的 ctx（只读方法从这里取主体）。
func (f *orgFix) as(p auth.Principal) context.Context { return auth.WithPrincipal(f.ctx, p) }

func (f *orgFix) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	require.NoError(t, db.From(f.ctx).Exec(q, args...).Error)
}

func newOrgFix(t *testing.T) *orgFix {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	// 测试库是每个测试临时建的，这两张表只给测试用的用户来源用
	require.NoError(t, gdb.Exec("CREATE TABLE t_org (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, code varchar(16) NOT NULL, name varchar(64) NOT NULL, status tinyint NOT NULL, owner_user_id bigint unsigned NOT NULL DEFAULT 0)").Error)
	require.NoError(t, gdb.Exec("CREATE TABLE t_org_user (id bigint unsigned NOT NULL AUTO_INCREMENT PRIMARY KEY, org_id bigint unsigned NOT NULL, username varchar(64) NOT NULL, status tinyint NOT NULL)").Error)

	reg := NewRegistry()
	require.NoError(t, reg.AddPerms("shop", []Perm{
		{Code: "shop:thing:list", Name: "list", Portal: shopP, Group: "shop.thing"},
		{Code: "shop:thing:edit", Name: "edit", Portal: shopP, Group: "shop.thing"},
		{Code: "shop:staff:admin", Name: "admin", Portal: shopP, Group: "shop.staff", Sensitive: true},
		{Code: "shop:role:list", Name: "roles", Portal: shopP, Group: "shop.staff", RoleView: true},
	}))
	require.NoError(t, reg.AddDataResources("shop", []DataResource{{Code: "shop:thing", Name: "thing", Portal: shopP, Perms: []string{"shop:thing:list"}, Default: ScopeSelf, Scopes: AllScopes()}}))
	require.NoError(t, reg.AddMenus("shop", []MenuNode{{Portal: shopP, Name: "shop-thing", Path: "/thing", Component: "thing/index", TitleKey: "menu.thing", Perm: "shop:thing:list", Sort: 10}}))
	require.NoError(t, reg.Finalize())
	f := &orgFix{ctx: ctx}
	pr := portal.NewRegistry()
	require.NoError(t, pr.Register(portal.Portal{Code: shopP, Users: f.users, Scoped: true}))
	f.s, err = NewService(Options{Registry: reg, Base: ctx, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), CacheTTL: time.Minute, Portals: pr})
	require.NoError(t, err)

	addOrg := func(code string) uint64 {
		res := db.From(ctx).Exec("INSERT INTO t_org (code, name, status) VALUES (?, ?, 1)", code, code)
		require.NoError(t, res.Error)
		var id uint64
		require.NoError(t, db.From(ctx).Raw("SELECT id FROM t_org WHERE code = ?", code).Scan(&id).Error)
		return id
	}
	addUser := func(org uint64, name string, owner bool) auth.Principal {
		require.NoError(t, db.From(ctx).Exec("INSERT INTO t_org_user (org_id, username, status) VALUES (?, ?, 1)", org, name).Error)
		var id uint64
		require.NoError(t, db.From(ctx).Raw("SELECT id FROM t_org_user WHERE org_id = ? AND username = ?", org, name).Scan(&id).Error)
		if owner {
			f.exec(t, "UPDATE t_org SET owner_user_id = ? WHERE id = ?", id, org)
		}
		return auth.Principal{Portal: shopP, UserID: id, OrgID: org, Username: name, Super: owner}
	}
	f.orgA, f.orgB = addOrg("M10000001"), addOrg("M10000002")
	f.ownerA, f.staffA, f.staffA2 = addUser(f.orgA, "admin", true), addUser(f.orgA, "staff", false), addUser(f.orgA, "staff2", false)
	f.ownerB, f.staffB = addUser(f.orgB, "admin", true), addUser(f.orgB, "staff", false)
	return f
}

func (f *orgFix) role(t *testing.T, owner auth.Principal, code string, perms ...string) *Role {
	t.Helper()
	r, err := f.s.CreateRole(f.as(owner), owner, shopP, RoleInput{Code: code, Name: code, Status: 1})
	require.NoError(t, err)
	if len(perms) > 0 {
		require.NoError(t, f.s.GrantRolePerms(f.as(owner), owner, shopP, r.ID, perms))
	}
	return r
}

// holdOrgLock 在另一个事务里拿住主体行的排他锁，直到调用 release（拿着锁时执行 then，可为 nil）。
// 测试中途失败时 Cleanup 也会放锁：否则拿着锁的事务一直不结束，删测试库会一直等下去。
func (f *orgFix) holdOrgLock(t *testing.T, org uint64, then func(tx context.Context) error) (release func(), done chan error) {
	t.Helper()
	locked, rel := make(chan struct{}), make(chan struct{})
	done = make(chan error, 1)
	go func() {
		done <- db.Tx(f.ctx, func(tx context.Context) error {
			if _, err := f.users.LockOrgByID(tx, org); err != nil {
				close(locked)
				return err
			}
			close(locked)
			<-rel
			if then != nil {
				return then(tx)
			}
			return nil
		})
	}()
	<-locked
	release = sync.OnceFunc(func() { close(rel) })
	t.Cleanup(release)
	return release, done
}

func fieldKeyOf(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) && len(he.Fields) > 0 {
		return he.Fields[0].Key
	}
	return ""
}

// 127. 角色按主体隔离：同一个编码在不同主体里各有一个；别的主体的角色列不到、读不到、改不了、删不了、授权不了（404），
// 分配别的主体的角色按校验失败；给别的主体的账号分配角色 404、什么都不写；没有主体端身份时失败即拒绝。
func TestOrgRBAC_127_RolesAreIsolatedByOrg(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	ctxA, ctxB := f.as(f.ownerA), f.as(f.ownerB)
	ra := f.role(t, f.ownerA, "ops", "shop:thing:list")
	rb := f.role(t, f.ownerB, "ops", "shop:thing:edit")
	require.Equal(t, f.orgA, ra.OrgID)
	require.Equal(t, f.orgB, rb.OrgID)
	_, err := s.CreateRole(ctxA, f.ownerA, shopP, RoleInput{Code: "ops", Name: "again", Status: 1})
	require.ErrorIs(t, err, httpx.ErrConflict, "同一主体里编码仍然唯一")

	roles, err := s.Roles(ctxA, shopP)
	require.NoError(t, err)
	require.Len(t, roles, 1)
	require.Equal(t, ra.ID, roles[0].ID)
	got, err := s.RoleByCode(ctxB, shopP, "ops")
	require.NoError(t, err)
	require.Equal(t, rb.ID, got.ID)

	// 读：别的主体的角色就是不存在
	_, err = s.Role(ctxA, shopP, rb.ID)
	require.ErrorIs(t, err, httpx.ErrNotFound)
	_, err = s.RolePerms(ctxA, shopP, rb.ID)
	require.ErrorIs(t, err, httpx.ErrNotFound)
	_, err = s.RoleDataScopes(ctxA, shopP, rb.ID)
	require.ErrorIs(t, err, httpx.ErrNotFound)
	// 写：同样 404，B 的角色不变
	_, err = s.UpdateRole(ctxA, f.ownerA, shopP, rb.ID, RoleInput{Name: "hijacked", Status: 0})
	require.ErrorIs(t, err, httpx.ErrNotFound)
	require.ErrorIs(t, s.GrantRole(ctxA, f.ownerA, shopP, rb.ID, []string{"shop:staff:admin"}, nil), httpx.ErrNotFound)
	require.ErrorIs(t, s.DeleteRole(ctxA, f.ownerA, shopP, rb.ID), httpx.ErrNotFound)
	after, err := s.Role(ctxB, shopP, rb.ID)
	require.NoError(t, err)
	require.Equal(t, "ops", after.Name)
	require.Equal(t, 1, after.Status)
	codes, err := s.RolePerms(ctxB, shopP, rb.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"shop:thing:edit"}, codes)

	// 分配：别的主体的角色按校验失败；别的主体的账号、不存在的账号 404；都不写
	err = s.AssignUserRoles(ctxA, f.ownerA, shopP, f.staffA.UserID, []uint64{rb.ID})
	require.ErrorIs(t, err, httpx.ErrValidation)
	require.Equal(t, "rbac.role.notFound", fieldKeyOf(err))
	require.ErrorIs(t, s.AssignUserRoles(ctxA, f.ownerA, shopP, f.staffB.UserID, []uint64{ra.ID}), httpx.ErrNotFound)
	require.ErrorIs(t, s.AssignUserRoles(ctxA, f.ownerA, shopP, 999999, []uint64{ra.ID}), httpx.ErrNotFound)
	var n int64
	require.NoError(t, db.From(f.ctx).Table("ga_user_role").Where("portal = ? AND user_id IN ?", shopP, []uint64{f.staffA.UserID, f.staffB.UserID, 999999}).Count(&n).Error)
	require.Zero(t, n)

	// 本主体照常
	require.NoError(t, s.AssignUserRoles(ctxA, f.ownerA, shopP, f.staffA.UserID, []uint64{ra.ID}))
	mine, err := s.UserRoles(ctxA, shopP, f.staffA.UserID)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	theirs, err := s.UserRoles(ctxB, shopP, f.staffA.UserID)
	require.NoError(t, err)
	require.Empty(t, theirs, "B 看 A 的员工：A 的角色对 B 不存在")
	batch, err := s.UserRolesBatch(ctxB, shopP, []uint64{f.staffA.UserID})
	require.NoError(t, err)
	require.Empty(t, batch[f.staffA.UserID])

	// 失败即拒绝：没有身份、身份是别的端、主体端身份不带主体，都不能退化成"不加条件"
	_, err = s.Roles(f.ctx, shopP)
	require.ErrorIs(t, err, scope.ErrNoOrg)
	_, err = s.Roles(f.as(auth.Principal{Portal: "platform", UserID: 1, Super: true}), shopP)
	require.ErrorIs(t, err, scope.ErrNoOrg)
	_, err = s.UserRoles(f.ctx, shopP, f.staffA.UserID)
	require.ErrorIs(t, err, scope.ErrNoOrg)
	zero := f.staffA
	zero.OrgID = 0
	_, err = s.Roles(f.as(zero), shopP)
	require.ErrorIs(t, err, scope.ErrNoOrg, "主体端身份不带主体：读同样拒绝")
	ok, err := s.Allowed(f.ctx, shopP, f.staffA.UserID, "shop:thing:list")
	require.ErrorIs(t, err, scope.ErrNoOrg)
	require.False(t, ok)
	noOrg := f.ownerA
	noOrg.OrgID = 0
	_, err = s.CreateRole(ctxA, noOrg, shopP, RoleInput{Code: "xx", Name: "xx", Status: 1})
	require.ErrorIs(t, err, scope.ErrNoOrg)
	require.ErrorIs(t, s.WithActor(ctxA, noOrg, func(context.Context, auth.Principal) error { return nil }), scope.ErrNoOrg)
	// 主体端没有"服务器命令"的身份：主体和主账号都要按库认定
	_, err = s.CreateRole(ctxA, auth.Principal{Portal: shopP, OrgID: f.orgA, Super: true}, shopP, RoleInput{Code: "cli", Name: "cli", Status: 1})
	require.ErrorIs(t, err, httpx.ErrTokenInvalid)
	// 平台端的身份带了主体：拼出来的身份，拒绝
	_, err = s.CreateRole(f.ctx, auth.Principal{Portal: "platform", UserID: 1, OrgID: f.orgA, Super: true}, "platform", RoleInput{Code: "xx", Name: "xx", Status: 1})
	require.ErrorIs(t, err, httpx.ErrForbidden)
}

// 128. 主账号与主体内的授权：主账号是主体内的超管（锁内按主体行认定，不看身份里的 Super）；员工授不出敏感权限码和
// 自己没有的权限码，改不了主账号的角色；"受超管保护""最后一个超管"在主体端都指主账号；主账号换人、主体停用、账号被挪到
// 别的主体之后，在途的写操作按新状态判断；主体内的人不能改全端共用的菜单。
func TestOrgRBAC_128_OwnerAndInOrgGrantRules(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	ctxA := f.as(f.ownerA)
	mgr := f.role(t, f.ownerA, "mgr", "shop:thing:list")
	target := f.role(t, f.ownerA, "target")
	require.NoError(t, s.AssignUserRoles(ctxA, f.ownerA, shopP, f.staffA.UserID, []uint64{mgr.ID}))

	// 主账号：身份里的 Super 不算数，锁内按主体行重新认定
	plain := f.ownerA
	plain.Super = false
	require.NoError(t, s.WithActor(ctxA, plain, func(_ context.Context, cur auth.Principal) error {
		require.True(t, cur.Super, "主账号在锁内被认定为主体内的超管")
		return nil
	}))
	require.NoError(t, s.GrantRolePerms(ctxA, plain, shopP, target.ID, []string{"shop:staff:admin"}), "主账号能授敏感权限码")
	require.NoError(t, s.GrantRolePerms(ctxA, f.ownerA, shopP, target.ID, nil))

	// 员工：敏感权限码、自己没有的权限码都授不出去；自己有的可以
	ctxS := f.as(f.staffA)
	err := s.GrantRolePerms(ctxS, f.staffA, shopP, target.ID, []string{"shop:staff:admin"})
	require.ErrorIs(t, err, httpx.ErrForbidden)
	require.Equal(t, "rbac.perm.sensitive", fieldKeyOf(err))
	err = s.GrantRolePerms(ctxS, f.staffA, shopP, target.ID, []string{"shop:thing:edit"})
	require.ErrorIs(t, err, httpx.ErrForbidden)
	require.Equal(t, "rbac.perm.notOwned", fieldKeyOf(err))
	require.NoError(t, s.GrantRolePerms(ctxS, f.staffA, shopP, target.ID, []string{"shop:thing:list"}))
	// 员工能把自己权限以内的角色分给同事，但改不了主账号的角色
	require.NoError(t, s.AssignUserRoles(ctxS, f.staffA, shopP, f.staffA2.UserID, []uint64{target.ID}))
	err = s.AssignUserRoles(ctxS, f.staffA, shopP, f.ownerA.UserID, []uint64{target.ID})
	require.ErrorIs(t, err, httpx.ErrForbidden)
	require.Equal(t, "rbac.role.superOnlyChange", fieldKeyOf(err))
	// 员工身份里谎称 Super 也没用
	liar := f.staffA
	liar.Super = true
	require.ErrorIs(t, s.GrantRolePerms(ctxS, liar, shopP, target.ID, []string{"shop:staff:admin"}), httpx.ErrForbidden)

	// "受超管保护""最后一个超管""超管名单"在主体端都指主账号；主体端没有超管角色
	held, err := s.HoldsSuperRole(f.ctx, shopP, f.ownerA.UserID)
	require.NoError(t, err)
	require.True(t, held)
	held, err = s.HoldsSuperRole(f.ctx, shopP, f.staffA.UserID)
	require.NoError(t, err)
	require.False(t, held)
	require.NoError(t, s.WithActor(ctxA, f.ownerA, func(ctx context.Context, _ auth.Principal) error {
		last, err := s.IsLastSuper(ctx, shopP, f.ownerA.UserID)
		require.NoError(t, err)
		require.True(t, last, "主体内不能停用主账号")
		last, err = s.IsLastSuper(ctx, shopP, f.staffA.UserID)
		require.NoError(t, err)
		require.False(t, last)
		_, err = s.IsLastSuper(ctx, shopP, f.ownerB.UserID)
		require.ErrorIs(t, err, httpx.ErrNotFound, "别的主体的账号就是不存在")
		require.ErrorIs(t, s.CheckEnableUser(ctx, f.ownerA, shopP, f.staffB.UserID), httpx.ErrNotFound, "主账号也不能启用别的主体的账号")
		require.NoError(t, s.CheckEnableUser(ctx, f.ownerA, shopP, f.staffA.UserID))
		return nil
	}))
	// 锁内的认定必须在 WithActor 里：不自己去锁主体行（在别的锁里自己加锁会把加锁顺序倒过来）
	require.NoError(t, db.TxReadCommitted(ctxA, func(ctx context.Context) error {
		_, err := s.IsLastSuper(ctx, shopP, f.ownerA.UserID)
		require.ErrorIs(t, err, errNotInOrgLock)
		_, err = s.AllowedLocked(ctx, f.ownerA, "shop:thing:list")
		require.ErrorIs(t, err, errNotInOrgLock)
		require.ErrorIs(t, s.CheckEnableUser(ctx, f.ownerA, shopP, f.staffA.UserID), errNotInOrgLock)
		return nil
	}))
	ids, err := s.SuperUserIDs(ctxA, shopP)
	require.NoError(t, err)
	require.Equal(t, []uint64{f.ownerA.UserID}, ids)
	isSuper, err := s.IsSuper(f.ctx, shopP, f.ownerA.UserID)
	require.NoError(t, err)
	require.False(t, isSuper, "主体端的超管不来自角色")
	// 哪怕库里被直接写进一个不属于任何主体的超管角色、挂在员工身上
	stray := &Role{Portal: shopP, Code: "super", Name: "super", IsSuper: true, Status: 1}
	require.NoError(t, s.st.createRole(f.ctx, stray))
	require.NoError(t, db.From(f.ctx).Exec("INSERT INTO ga_user_role (portal, user_id, role_id) VALUES (?, ?, ?)", shopP, f.staffA2.UserID, stray.ID).Error)
	require.NoError(t, s.Reload())
	isSuper, err = s.IsSuper(f.ctx, shopP, f.staffA2.UserID)
	require.NoError(t, err)
	require.False(t, isSuper)

	// 菜单调整对全端生效：主账号也不能改
	require.ErrorIs(t, s.ResetMenu(ctxA, f.ownerA, "shop-thing"), httpx.ErrForbidden)

	// 主账号换人：旧主账号在途的写操作按新状态判断
	f.exec(t, "UPDATE t_org SET owner_user_id = ? WHERE id = ?", f.staffA.UserID, f.orgA)
	require.ErrorIs(t, s.GrantRolePerms(ctxA, f.ownerA, shopP, target.ID, []string{"shop:staff:admin"}), httpx.ErrForbidden)
	require.NoError(t, s.GrantRolePerms(ctxS, f.staffA, shopP, target.ID, []string{"shop:staff:admin"}), "新的主账号可以")
	held, err = s.HoldsSuperRole(f.ctx, shopP, f.ownerA.UserID)
	require.NoError(t, err)
	require.False(t, held)

	// 账号被挪到别的主体（数据被改过）：在途写操作 401，本人写操作（WithSelf）也 401
	require.NoError(t, s.WithSelf(f.as(f.staffA2), f.staffA2, func(context.Context) error { return nil }))
	f.exec(t, "UPDATE t_org_user SET org_id = ? WHERE id = ?", f.orgB, f.staffA2.UserID)
	require.ErrorIs(t, s.WithActor(f.as(f.staffA2), f.staffA2, func(context.Context, auth.Principal) error { return nil }), httpx.ErrTokenInvalid)
	require.ErrorIs(t, s.WithSelf(f.as(f.staffA2), f.staffA2, func(context.Context) error { return nil }), httpx.ErrTokenInvalid)

	// 主体停用：主账号在途的写操作、本人写操作都 401；别的主体照常
	f.exec(t, "UPDATE t_org SET status = 0 WHERE id = ?", f.orgB)
	_, err = s.CreateRole(f.as(f.ownerB), f.ownerB, shopP, RoleInput{Code: "late", Name: "late", Status: 1})
	require.ErrorIs(t, err, httpx.ErrTokenInvalid)
	require.ErrorIs(t, s.WithSelf(f.as(f.ownerB), f.ownerB, func(context.Context) error { return nil }), httpx.ErrTokenInvalid)
	_, err = s.CreateRole(ctxS, f.staffA, shopP, RoleInput{Code: "fine", Name: "fine", Status: 1})
	require.NoError(t, err)
}

// 129. 主体行锁：主体端的写操作拿的是主体行的排他锁——同一主体的写操作排队，别的主体不受影响；
// 锁内按已提交的状态重新认定操作人：排队期间主账号换了人，旧主账号的写操作按新状态被拒绝。
func TestOrgRBAC_129_OrgRowLockSerializesWithinOrg(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	ra := f.role(t, f.ownerA, "ra")
	rb := f.role(t, f.ownerB, "rb")

	// 另一个事务拿着 A 的主体行锁；拿着锁把主账号换成 staffA，提交之后排队的写操作才能继续
	release, held := f.holdOrgLock(t, f.orgA, func(tx context.Context) error {
		return db.From(tx).Exec("UPDATE t_org SET owner_user_id = ? WHERE id = ?", f.staffA.UserID, f.orgA).Error
	})

	grantA := make(chan error, 1)
	go func() {
		grantA <- s.GrantRolePerms(f.as(f.ownerA), f.ownerA, shopP, ra.ID, []string{"shop:staff:admin"})
	}()
	// 别的主体不等
	start := time.Now()
	require.NoError(t, s.GrantRolePerms(f.as(f.ownerB), f.ownerB, shopP, rb.ID, []string{"shop:staff:admin"}))
	require.Less(t, time.Since(start), 3*time.Second, "B 的写操作不该等 A 的主体行锁")
	select {
	case err := <-grantA:
		t.Fatalf("A 的写操作应该在等主体行锁，却已经返回：%v", err)
	case <-time.After(400 * time.Millisecond):
	}
	// 加锁顺序（D-063 第 5 条）：主体行是第一把锁。排队时它还没碰角色行——先锁角色行再等主体行，
	// 就会和"先主体行、后角色行"的写操作互相等待
	require.NoError(t, db.Tx(f.ctx, func(tx context.Context) error {
		var id uint64
		return db.From(tx).Raw("SELECT id FROM ga_role WHERE id = ? FOR UPDATE NOWAIT", ra.ID).Scan(&id).Error
	}), "排队中的写操作不该已经锁住角色行")

	release()
	require.NoError(t, <-held)
	err := <-grantA
	require.ErrorIs(t, err, httpx.ErrForbidden, "排队期间主账号换了人：锁内按库认定，旧主账号不能再授敏感权限码")
	codes, err := s.RolePerms(f.as(f.ownerA), shopP, ra.ID)
	require.NoError(t, err)
	require.Empty(t, codes)

	// WithActor（模块的写操作都走它）同样排队
	release, held = f.holdOrgLock(t, f.orgB, nil)
	ran := make(chan struct{})
	go func() {
		_ = s.WithActor(f.as(f.ownerB), f.ownerB, func(context.Context, auth.Principal) error { close(ran); return nil })
	}()
	select {
	case <-ran:
		t.Fatal("WithActor 应该在等主体行锁")
	case <-time.After(400 * time.Millisecond):
	}
	release()
	require.NoError(t, <-held)
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("锁释放之后 WithActor 应该继续执行")
	}
}

// 130. 判定忽略别的主体的成员关系和主体里的超管角色：库里被直接写进一条跨主体的成员关系、或者主体里出现了超管角色，
// 判定、权限列表、用户角色、锁内认定、数据范围都不因此多出任何东西。
func TestOrgRBAC_130_CrossOrgMembershipIgnored(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	rb := f.role(t, f.ownerB, "editor", "shop:thing:edit", "shop:thing:list")
	require.NoError(t, s.st.setRoleScopes(f.ctx, rb.ID, map[string]DataScope{"shop:thing": ScopeAll}))
	// 绕过授权服务直接写库：A 的员工挂上 B 的角色
	require.NoError(t, s.st.replaceUserRoles(f.ctx, shopP, f.staffA.UserID, []uint64{rb.ID}))
	// 再在主体 A 里直接造一个超管角色，也挂给 A 的员工
	rogue := &Role{Portal: shopP, OrgID: f.orgA, Code: "root", Name: "root", IsSuper: true, Status: 1}
	require.NoError(t, s.st.createRole(f.ctx, rogue))
	require.NoError(t, db.From(f.ctx).Exec("INSERT INTO ga_user_role (portal, user_id, role_id) VALUES (?, ?, ?)", shopP, f.staffA.UserID, rogue.ID).Error)
	require.NoError(t, s.Reload())

	ctxS := f.as(f.staffA)
	for _, perm := range []string{"shop:thing:edit", "shop:thing:list", "shop:staff:admin"} {
		ok, err := s.Allowed(ctxS, shopP, f.staffA.UserID, perm)
		require.NoError(t, err)
		require.False(t, ok, "%s 不能来自别的主体的角色或主体里的超管角色", perm)
	}
	perms, err := s.Perms(ctxS, f.staffA)
	require.NoError(t, err)
	require.Empty(t, perms)
	rs, err := s.UserRoles(ctxS, shopP, f.staffA.UserID)
	require.NoError(t, err)
	for _, r := range rs {
		require.NotEqual(t, rb.ID, r.ID, "别的主体的角色不出现在本主体的用户角色里")
	}
	require.NoError(t, s.WithActor(ctxS, f.staffA, func(ctx context.Context, cur auth.Principal) error {
		require.False(t, cur.Super, "主体里的超管角色不让人成为超管")
		ok, err := s.AllowedLocked(ctx, cur, "shop:thing:edit")
		require.NoError(t, err)
		require.False(t, ok)
		return nil
	}))
	sc, err := s.DataScopeOf(ctxS, f.staffA, "shop:thing", "shop:thing:list")
	require.NoError(t, err)
	require.Equal(t, ScopeSelf, sc, "别的主体角色上的全部范围不算数")
	// 走菜单也一样：只剩不要权限码的节点（这里没有）
	menus, err := s.Menus(ctxS, f.staffA)
	require.NoError(t, err)
	require.Empty(t, menus)

	// 对照：同样的权限码来自本主体的角色就算数
	ra := f.role(t, f.ownerA, "editor", "shop:thing:edit")
	require.NoError(t, s.AssignUserRoles(f.as(f.ownerA), f.ownerA, shopP, f.staffA2.UserID, []uint64{ra.ID}))
	ok, err := s.Allowed(f.as(f.staffA2), shopP, f.staffA2.UserID, "shop:thing:edit")
	require.NoError(t, err)
	require.True(t, ok)
}

// 132. 不同主体的写操作互不影响：两个主体同时授权、同时分配角色（各自的新角色、新账号），不等待、不死锁
// （主体端的写事务用 READ COMMITTED，共用的角色、授权、成员关系表上没有间隙锁，D-063 第 4 条）。
func TestOrgRBAC_132_ConcurrentWritesInDifferentOrgs(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	// 本用例验证跨主体并发；每轮推进独立窗口，重复 25 轮不占同一分钟的配额。
	now := time.Now()
	s.roleWrites = ratelimit.New(120, time.Minute, 100_000, func() time.Time { return now })
	newUser := func(org uint64, name string) uint64 {
		f.exec(t, "INSERT INTO t_org_user (org_id, username, status) VALUES (?, ?, 1)", org, name)
		var id uint64
		require.NoError(t, db.From(f.ctx).Raw("SELECT id FROM t_org_user WHERE org_id = ? AND username = ?", org, name).Scan(&id).Error)
		return id
	}
	both := func(a, b func() error) {
		t.Helper()
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for i, fn := range []func() error{a, b} {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; errs[i] = fn() }()
		}
		close(start)
		wg.Wait()
		require.NoError(t, errs[0])
		require.NoError(t, errs[1])
	}
	// 员工也来：员工的写操作要在锁内读自己的角色、授权、范围（锁定读），再做"授不出自己没有的"检查
	for _, o := range []struct {
		owner, staff auth.Principal
	}{{f.ownerA, f.staffA}, {f.ownerB, f.staffB}} {
		mgr := f.role(t, o.owner, "mgr", "shop:thing:list")
		require.NoError(t, s.AssignUserRoles(f.as(o.owner), o.owner, shopP, o.staff.UserID, []uint64{mgr.ID}))
	}
	for i := 0; i < 25; i++ {
		now = now.Add(time.Minute)
		sa := f.role(t, f.ownerA, fmt.Sprintf("sa%d", i))
		sb := f.role(t, f.ownerB, fmt.Sprintf("sb%d", i))
		both(
			func() error {
				return s.GrantRolePerms(f.as(f.staffA), f.staffA, shopP, sa.ID, []string{"shop:thing:list"})
			},
			func() error {
				return s.GrantRolePerms(f.as(f.staffB), f.staffB, shopP, sb.ID, []string{"shop:thing:list"})
			},
		)
		ra := f.role(t, f.ownerA, fmt.Sprintf("ra%d", i))
		rb := f.role(t, f.ownerB, fmt.Sprintf("rb%d", i))
		both(
			func() error {
				return s.GrantRole(f.as(f.ownerA), f.ownerA, shopP, ra.ID, []string{"shop:thing:list"}, map[string]DataScope{"shop:thing": ScopeAll})
			},
			func() error {
				return s.GrantRole(f.as(f.ownerB), f.ownerB, shopP, rb.ID, []string{"shop:thing:list"}, map[string]DataScope{"shop:thing": ScopeAll})
			},
		)
		ua, ub := newUser(f.orgA, fmt.Sprintf("ua%d", i)), newUser(f.orgB, fmt.Sprintf("ub%d", i))
		both(
			func() error { return s.AssignUserRoles(f.as(f.ownerA), f.ownerA, shopP, ua, []uint64{ra.ID}) },
			func() error { return s.AssignUserRoles(f.as(f.ownerB), f.ownerB, shopP, ub, []uint64{rb.ID}) },
		)
	}
}

// 132（续）：主体端的写操作不能加入别的事务——外层事务是可重复读、可能已经锁了账号行；要在一个事务里做几步，放进 WithActor。
func TestOrgRBAC_132b_NoJoiningForeignTransactions(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	ra := f.role(t, f.ownerA, "ra")
	err := db.Tx(f.ctx, func(tx context.Context) error {
		return s.AssignUserRoles(auth.WithPrincipal(tx, f.ownerA), f.ownerA, shopP, f.staffA.UserID, []uint64{ra.ID})
	})
	require.ErrorIs(t, err, errNotInOrgLock)
	err = db.Tx(f.ctx, func(tx context.Context) error {
		return s.WithActor(auth.WithPrincipal(tx, f.ownerA), f.ownerA, func(context.Context, auth.Principal) error { return nil })
	})
	require.ErrorIs(t, err, errNotInOrgLock)
	// 拿着别的主体的锁也不行
	err = s.WithActor(f.as(f.ownerB), f.ownerB, func(ctx context.Context, _ auth.Principal) error {
		return s.AssignUserRoles(auth.WithPrincipal(ctx, f.ownerA), f.ownerA, shopP, f.staffA.UserID, []uint64{ra.ID})
	})
	require.ErrorIs(t, err, errNotInOrgLock)
	// 放进 WithActor：建账号再分配角色在同一个事务里
	var uid uint64
	require.NoError(t, s.WithActor(f.as(f.ownerA), f.ownerA, func(ctx context.Context, cur auth.Principal) error {
		if err := db.From(ctx).Exec("INSERT INTO t_org_user (org_id, username, status) VALUES (?, 'fresh', 1)", f.orgA).Error; err != nil {
			return err
		}
		if err := db.From(ctx).Raw("SELECT LAST_INSERT_ID()").Scan(&uid).Error; err != nil {
			return err
		}
		return s.AssignUserRoles(ctx, cur, shopP, uid, []uint64{ra.ID})
	}))
	rs, err := s.UserRoles(f.as(f.ownerA), shopP, uid)
	require.NoError(t, err)
	require.Len(t, rs, 1)
}
