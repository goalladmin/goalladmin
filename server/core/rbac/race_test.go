package rbac

// 缓存与重载的交错（D-043）：用 testHook 在"读库之后、写内存之前"的窗口里插入另一次写入，
// 验证读到的旧数据不会盖住新数据。连真实 MySQL 跑。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func newRaceService(t *testing.T) (*Service, context.Context, *gorm.DB) {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	reg := NewRegistry()
	require.NoError(t, reg.AddPerms("m", []Perm{
		{Code: "m:thing:list", Name: "list", Portal: "platform", Group: "m.thing"},
		{Code: "m:thing:edit", Name: "edit", Portal: "platform", Group: "m.thing"},
	}))
	require.NoError(t, reg.AddDataResources("m", []DataResource{{Code: "m:thing", Name: "thing", Portal: "platform", Perms: []string{"m:thing:list"}, Default: ScopeSelf, Scopes: AllScopes()}}))
	require.NoError(t, reg.Finalize())
	s, err := NewService(Options{Registry: reg, Base: ctx, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), CacheTTL: time.Minute})
	require.NoError(t, err)
	return s, ctx, gdb
}

func createRaceRole(t *testing.T, ctx context.Context, s *Service, code string) *Role {
	t.Helper()
	r := &Role{Portal: "platform", Code: code, Name: code, Status: 1}
	require.NoError(t, s.st.createRole(ctx, r))
	return r
}

// 两次并发 Reload：先读到旧状态的那次不能在后一次之后把旧状态写回内存（D-043；D-053 起成员关系也在快照里）。
func TestReload_ConcurrentReloadKeepsLatestState(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	r := createRaceRole(t, ctx, s, "ops")
	const uid = 42
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", uid, []uint64{r.ID}))
	require.NoError(t, s.Reload())
	require.Len(t, s.snap.Load().enabledRoles("platform", uid), 1)

	// A 读完库（旧状态：uid 有 ops）停在钩子里；这时撤掉 uid 的角色并发起 B 的重载
	bDone := make(chan error, 1)
	fired := false
	s.testHook = func(stage string) {
		if stage != "reload.read" || fired {
			return
		}
		fired = true
		require.NoError(t, s.st.replaceUserRoles(ctx, "platform", uid, nil))
		go func() { bDone <- s.Reload() }()
		time.Sleep(300 * time.Millisecond) // 给 B 机会跑到锁前面
	}
	require.NoError(t, s.Reload())
	require.NoError(t, <-bDone) // 等 B 结束再摘钩子，B 在锁里也会读一次钩子
	s.testHook = nil
	require.Empty(t, s.snap.Load().enabledRoles("platform", uid), "撤销后的状态不能被先读到旧数据的重载盖回去")
}

// 撤销角色、停用角色提交之后，新的判定立即看不到它（D-053：成员关系和角色状态在授权快照里，提交之后重新发布）。
// 以前靠"清缓存 + 读方不回填旧值"保证，现在没有单独的角色缓存了，保证的是同一件事。
func TestEnabledRoles_RevokeAndDisableTakeEffectImmediately(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	root := auth.Principal{Portal: "platform", UserID: 1, Username: "root", Super: true}
	makeSuper(t, ctx, s, root.UserID)
	r := createRaceRole(t, ctx, s, "ops")
	const uid = 42
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.AssignUserRoles(ctx, root, "platform", uid, []uint64{r.ID}))
	ok, err := s.Allowed(ctx, "platform", uid, "m:thing:list")
	require.NoError(t, err)
	require.True(t, ok, "分配角色提交之后立即生效")

	require.NoError(t, s.AssignUserRoles(ctx, root, "platform", uid, nil))
	ok, err = s.Allowed(ctx, "platform", uid, "m:thing:list")
	require.NoError(t, err)
	require.False(t, ok, "撤销角色提交之后立即生效")

	require.NoError(t, s.AssignUserRoles(ctx, root, "platform", uid, []uint64{r.ID}))
	_, err = s.UpdateRole(ctx, root, "platform", r.ID, RoleInput{Name: r.Name, Status: 0})
	require.NoError(t, err)
	ok, err = s.Allowed(ctx, "platform", uid, "m:thing:list")
	require.NoError(t, err)
	require.False(t, ok, "停用角色提交之后立即生效")
}

// 读方查库期间菜单调整被提交并清了缓存：旧的调整不能再写回缓存。
func TestMenuCustomCached_NoBackfillAfterWrite(t *testing.T) {
	s, ctx, gdb := newRaceService(t)
	insert := func(name, title string) {
		require.NoError(t, gdb.Exec("INSERT INTO ga_menu_custom (portal, name, kind, titles, created_at, updated_at) VALUES ('platform', ?, 'code', ?, NOW(3), NOW(3))", name, title).Error)
	}
	insert("a", `{"zh-CN":"甲"}`)
	fired := false
	s.testHook = func(stage string) {
		if stage != "menu.read" || fired {
			return
		}
		fired = true
		insert("b", `{"zh-CN":"乙"}`)
		s.menuChanged("platform")
	}
	rows, err := s.menuCustomCached(ctx, "platform")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	s.testHook = nil
	_, cached := s.menuCache.Get("platform")
	require.False(t, cached, "写入提交并清缓存之后，读方不能把旧的调整填回去")
	rows, err = s.menuCustomCached(ctx, "platform")
	require.NoError(t, err)
	require.Len(t, rows, 2)
}

// 批量取用户角色时嵌入的必须是导出类型：嵌入不导出的行类型时反射填不进去，扫出来全是零值，
// 会话列表上"超管账号"的标记就没了（D-043 把 gorm 模型改成不导出之后发现的）。
func TestUserRolesBatch_FillsEveryColumn(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	r := createRaceRole(t, ctx, s, "ops")
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", 7, []uint64{r.ID}))
	super, err := s.st.roleByCode(ctx, "platform", SuperRoleCode)
	require.NoError(t, err)
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", 8, []uint64{super.ID}))

	got, err := s.UserRolesBatch(ctx, "platform", []uint64{7, 8})
	require.NoError(t, err)
	require.Len(t, got[7], 1)
	require.Equal(t, Role{ID: r.ID, Portal: "platform", Code: "ops", Name: "ops", Status: 1, CreatedAt: got[7][0].CreatedAt, UpdatedAt: got[7][0].UpdatedAt}, got[7][0])
	require.Len(t, got[8], 1)
	require.True(t, got[8][0].IsSuper, "超管标记要能扫出来")
	require.Equal(t, SuperRoleCode, got[8][0].Code)
}

// 非超管授权、分配角色时"自己有没有这个权限码"要按锁内已提交的状态算，不能用内存里的策略：
// 超管收回 carol 的权限并提交，在重载内存之前 carol 把这个权限授给别的角色——内存里她还有，库里已经没有了。
func TestGrant_ActorPermsReadInsideLock(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	mgr := createRaceRole(t, ctx, s, "mgr")
	target := createRaceRole(t, ctx, s, "target")
	const carolID = 42
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", carolID, []uint64{mgr.ID}))
	root := auth.Principal{Portal: "platform", UserID: 1, Username: "root", Super: true}
	makeSuper(t, ctx, s, root.UserID) // 锁内按库认定超管（D-045）：库里也得真是超管
	carol := auth.Principal{Portal: "platform", UserID: carolID, Username: "carol"}
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list", "m:thing:edit"}))
	ok, err := s.Allowed(ctx, "platform", carolID, "m:thing:edit")
	require.NoError(t, err)
	require.True(t, ok, "前提：carol 现在有 edit")

	// 超管把 edit 从 mgr 收回；提交之后、重载之前，carol 试图把 edit 授给 target
	var grantErr error
	fired := false
	s.testHook = func(stage string) {
		if stage != "grant.committed" || fired {
			return
		}
		fired = true
		grantErr = s.GrantRolePerms(ctx, carol, "platform", target.ID, []string{"m:thing:edit"})
	}
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list"}))
	s.testHook = nil
	require.Error(t, grantErr, "库里已经收回的权限不能再授出去")
	require.ErrorIs(t, grantErr, httpx.ErrForbidden)
	codes, err := s.RolePerms(ctx, "platform", target.ID)
	require.NoError(t, err)
	require.Empty(t, codes, "target 不能拿到 edit")

	// 分配角色同理：超管给 target 授 edit、把 edit 还给 carol 的角色，然后再次收回；
	// 收回提交之后、重载之前，carol 把含 edit 的 target 分配给别人——内存里她还有 edit，库里没有
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", target.ID, []string{"m:thing:edit"}))
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list", "m:thing:edit"}))
	var assignErr error
	fired = false
	s.testHook = func(stage string) {
		if stage != "grant.committed" || fired {
			return
		}
		fired = true
		assignErr = s.AssignUserRoles(ctx, carol, "platform", 43, []uint64{target.ID})
	}
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list"}))
	s.testHook = nil
	require.ErrorIs(t, assignErr, httpx.ErrForbidden, "内存里还有 edit，库里已经没有：不能把含 edit 的角色分配出去")
	rs, err := s.UserRoles(ctx, "platform", 43)
	require.NoError(t, err)
	require.Empty(t, rs)
}

// 调用方的事务在拿超管锁之前就做过读（建用户时先写用户再分配角色就是这样）：一致性快照早已建立，
// 锁内的判断必须用锁定读看到最新已提交的状态，否则超管刚收回的权限在这个事务里还"存在"（D-043）。
func TestAssign_LockedReadsSeePastSnapshot(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	mgr := createRaceRole(t, ctx, s, "mgr")
	target := createRaceRole(t, ctx, s, "target")
	const carolID = 42
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", carolID, []uint64{mgr.ID}))
	root := auth.Principal{Portal: "platform", UserID: 1, Username: "root", Super: true}
	makeSuper(t, ctx, s, root.UserID) // 锁内按库认定超管（D-045）：库里也得真是超管
	carol := auth.Principal{Portal: "platform", UserID: carolID, Username: "carol"}
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list", "m:thing:edit"}))
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", target.ID, []string{"m:thing:edit"}))

	var assignErr error
	err := db.Tx(ctx, func(txCtx context.Context) error {
		// 先做一次普通读，让事务的快照建立在超管收回权限之前
		_, err := s.st.role(txCtx, "platform", mgr.ID)
		require.NoError(t, err)
		// 超管在事务外收回 carol 的 edit 并提交（另一条连接）
		require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list"}))
		// 事务里再分配含 edit 的角色：快照里 carol 还有 edit，锁内的锁定读必须看到她已经没有了
		assignErr = s.AssignUserRoles(txCtx, carol, "platform", 43, []uint64{target.ID})
		return assignErr
	})
	require.Error(t, err)
	require.ErrorIs(t, assignErr, httpx.ErrForbidden, "快照里的旧权限不能用来通过检查")
	rs, err := s.UserRoles(ctx, "platform", 43)
	require.NoError(t, err)
	require.Empty(t, rs)

	// 反过来：超管在快照建立之后给 target 加了敏感权限码，事务里分配 target 也必须看到并拒绝
	require.NoError(t, s.reg.AddPerms("m", []Perm{{Code: "m:thing:admin", Name: "admin", Portal: "platform", Group: "m.thing", Sensitive: true}}))
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", mgr.ID, []string{"m:thing:list", "m:thing:edit"}))
	err = db.Tx(ctx, func(txCtx context.Context) error {
		_, err := s.st.role(txCtx, "platform", target.ID)
		require.NoError(t, err)
		require.NoError(t, s.GrantRolePerms(ctx, root, "platform", target.ID, []string{"m:thing:edit", "m:thing:admin"}))
		assignErr = s.AssignUserRoles(txCtx, carol, "platform", 44, []uint64{target.ID})
		return assignErr
	})
	require.Error(t, err)
	require.ErrorIs(t, assignErr, httpx.ErrForbidden, "快照里目标角色还没有敏感权限码，锁定读必须看到已提交的授权")
}

// 非超管不能把带通配规则（或未注册权限码）的角色分配出去：通配规则不算操作人"拥有"的权限码，
// 哪怕操作人自己的角色里也有同样的规则（通配规则只能由超管写进库里，规范 §6.3）。
func TestAssign_WildcardRoleNeedsSuper(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	mgr := createRaceRole(t, ctx, s, "mgr")
	target := createRaceRole(t, ctx, s, "target")
	const carolID = 42
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", carolID, []uint64{mgr.ID}))
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", mgr.ID, []string{"m:*"}))
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", target.ID, []string{"m:*"}))
	require.NoError(t, s.Reload())
	carol := auth.Principal{Portal: "platform", UserID: carolID, Username: "carol"}
	ok, err := s.Allowed(ctx, "platform", carolID, "m:thing:edit")
	require.NoError(t, err)
	require.True(t, ok, "前提：通配规则让 carol 拥有 edit")

	err = s.AssignUserRoles(ctx, carol, "platform", 43, []uint64{target.ID})
	require.ErrorIs(t, err, httpx.ErrForbidden)
	rs, err := s.UserRoles(ctx, "platform", 43)
	require.NoError(t, err)
	require.Empty(t, rs)
	// 超管可以
	root := auth.Principal{Portal: "platform", UserID: 1, Username: "root", Super: true}
	makeSuper(t, ctx, s, root.UserID) // 锁内按库认定超管（D-045）：库里也得真是超管
	require.NoError(t, s.AssignUserRoles(ctx, root, "platform", 43, []uint64{target.ID}))
}

// makeSuper 把用户设成超管：Principal 上的 Super 只是认证时的结论，锁内的判断按库里的角色算（D-045）。
func makeSuper(t *testing.T, ctx context.Context, s *Service, userID uint64) {
	t.Helper()
	super, err := s.st.roleByCode(ctx, "platform", SuperRoleCode)
	require.NoError(t, err)
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", userID, []uint64{super.ID}))
}

// 规范 §13.2 第 84 条：授权提交之后重载失败，不能继续用旧快照判定——旧快照里还有刚撤掉的权限码（D-051）。
// 重试有间隔；库恢复后第一次判定就用上新状态。数据范围按请求的数据库视图读（D-054），收窄提交之后立即生效，不等重载。
func TestPolicy_84a_ReloadFailureFailsClosed(t *testing.T) {
	s, ctx, gdb := newRaceService(t)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	r := createRaceRole(t, ctx, s, "ops")
	const uid = 42
	p := auth.Principal{Portal: "platform", UserID: uid}
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", uid, []uint64{r.ID}))
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list", "m:thing:edit"}))
	require.NoError(t, s.st.setRoleScopes(ctx, r.ID, map[string]DataScope{"m:thing": ScopeAll}))
	require.NoError(t, s.Reload())
	sc, err := s.DataScopeOf(ctx, p, "m:thing", "m:thing:list")
	require.NoError(t, err)
	require.Equal(t, ScopeAll, sc)

	// 收窄范围、撤掉 edit 并提交：范围立即按库里的新状态算
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.st.setRoleScopes(ctx, r.ID, map[string]DataScope{"m:thing": ScopeSelf}))
	sc, err = s.DataScopeOf(ctx, p, "m:thing", "m:thing:list")
	require.NoError(t, err)
	require.Equal(t, ScopeSelf, sc, "范围不等内存重载")

	// 紧接着的重载失败（这里让策略表暂时不可读）
	require.NoError(t, gdb.Exec("RENAME TABLE ga_casbin_rule TO ga_casbin_rule_off").Error)
	require.Error(t, s.Reload())
	_, err = s.Allowed(ctx, "platform", uid, "m:thing:edit")
	require.ErrorIs(t, err, httpx.ErrUnavailable, "重载失败后不能再按旧策略放行刚撤掉的权限码")
	_, err = s.Perms(ctx, p)
	require.ErrorIs(t, err, httpx.ErrUnavailable)

	// 库恢复了，但还没到重试时间：仍然不可用（不在每个请求里重试）
	require.NoError(t, gdb.Exec("RENAME TABLE ga_casbin_rule_off TO ga_casbin_rule").Error)
	_, err = s.Allowed(ctx, "platform", uid, "m:thing:edit")
	require.ErrorIs(t, err, httpx.ErrUnavailable)

	// 到了重试时间：这次判定先重载，用的是库里的新状态
	clock = clock.Add(2 * policyRetryInterval)
	ok, err := s.Allowed(ctx, "platform", uid, "m:thing:edit")
	require.NoError(t, err)
	require.False(t, ok)
	ok, err = s.Allowed(ctx, "platform", uid, "m:thing:list")
	require.NoError(t, err)
	require.True(t, ok)
}

// 规范 §13.2 第 94 条：数据范围的全部输入（角色、角色状态、权限码、范围）在同一个数据库视图里读（D-054）。
// 读完角色、还没读角色的范围时，另一个连接成套提交"把 U 从 R 撤下、同时把 R 放宽到全部"：
// 算出来的必须是成套的旧状态（仅本人），不能是"旧成员关系 + 新的全部范围"。
func TestDataScope_94a_OneViewForAllInputs(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	r := createRaceRole(t, ctx, s, "r")
	const uid = 42
	p := auth.Principal{Portal: "platform", UserID: uid}
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.st.setRoleScopes(ctx, r.ID, map[string]DataScope{"m:thing": ScopeSelf}))
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", uid, []uint64{r.ID}))

	fired := false
	s.testHook = func(stage string) {
		if stage != "view.roles" || fired {
			return
		}
		fired = true
		require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
			if err := s.st.replaceUserRoles(ctx, "platform", uid, nil); err != nil {
				return err
			}
			return s.st.setRoleScopes(ctx, r.ID, map[string]DataScope{"m:thing": ScopeAll})
		}))
	}
	sc, err := s.DataScopeOf(ctx, p, "m:thing", "m:thing:list")
	s.testHook = nil
	require.NoError(t, err)
	require.True(t, fired)
	require.Equal(t, ScopeSelf, sc, "不能拼出从来没有过的全部范围")

	sc, err = s.DataScopeOf(ctx, p, "m:thing", "m:thing:list")
	require.NoError(t, err)
	require.Equal(t, ScopeSelf, sc, "撤下之后没有角色拥有这个权限码：仅本人")
}

// 规范 §13.2 第 91 条：成员关系、角色状态、权限码取自同一个授权快照（D-053）。重载读完策略、还没读成员关系时，
// 另一个连接成套提交"把 U 加进角色 R、同时撤掉 R 的权限码"：这次重载得到的必须是成套的旧状态（U 不在 R 里），
// 不能是"新成员关系 + R 旧的权限码"——那样 U 会拿到库里从来没有过的权限。
func TestPolicy_91_MembershipInSameSnapshot(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	r := createRaceRole(t, ctx, s, "r")
	const uid = 42
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:edit"}))
	require.NoError(t, s.Reload())

	fired := false
	s.testHook = func(stage string) {
		if stage != "reload.policies" || fired {
			return
		}
		fired = true
		require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
			if err := s.st.replaceRolePerms(ctx, "platform", r.ID, nil); err != nil {
				return err
			}
			return s.st.replaceUserRoles(ctx, "platform", uid, []uint64{r.ID})
		}))
	}
	require.NoError(t, s.Reload())
	s.testHook = nil
	require.True(t, fired)
	ok, err := s.Allowed(ctx, "platform", uid, "m:thing:edit")
	require.NoError(t, err)
	require.False(t, ok, "成员关系必须和策略来自同一个快照，不能拼出从来没有过的权限")

	require.NoError(t, s.Reload())
	ok, err = s.Allowed(ctx, "platform", uid, "m:thing:edit")
	require.NoError(t, err)
	require.False(t, ok)
	require.Len(t, s.snap.Load().enabledRoles("platform", uid), 1)
}

// 规范 §13.2 第 91 条：进程外的改动（命令行建管理员、直接改库）不经过本进程的写入，快照过了有效期后下一次判定就重新读库。
func TestPolicy_91b_SnapshotRefreshesAfterMaxAge(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }
	require.NoError(t, s.Reload())
	r := createRaceRole(t, ctx, s, "ops")
	const uid = 42
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", uid, []uint64{r.ID})) // 绕过本进程：不重新发布
	ok, err := s.Allowed(ctx, "platform", uid, "m:thing:list")
	require.NoError(t, err)
	require.False(t, ok, "有效期内用的是已发布的快照")
	clock = clock.Add(s.maxAge)
	ok, err = s.Allowed(ctx, "platform", uid, "m:thing:list")
	require.NoError(t, err)
	require.True(t, ok, "过了有效期，下一次判定重新读库")
}

// 规范 §13.2 第 96 条：撤权提交之后、新快照发布之前的判定不能按旧快照放行（D-055）：判定先等发布完成。
// 撤掉成员关系、撤掉权限码、停用角色三种写入都一样。
func TestPolicy_96_NoOldSnapshotBetweenCommitAndPublish(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	root := auth.Principal{Portal: "platform", UserID: 1, Username: "root", Super: true}
	makeSuper(t, ctx, s, root.UserID)
	r := createRaceRole(t, ctx, s, "ops")
	const uid = 42
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", r.ID, []string{"m:thing:list"}))

	cases := map[string]func() error{
		"撤掉成员关系": func() error { return s.AssignUserRoles(ctx, root, "platform", uid, nil) },
		"撤掉权限码":  func() error { return s.GrantRolePerms(ctx, root, "platform", r.ID, nil) },
		"停用角色": func() error {
			_, err := s.UpdateRole(ctx, root, "platform", r.ID, RoleInput{Name: r.Name, Status: 0})
			return err
		},
	}
	for name, revoke := range cases {
		// 每种情况先恢复成"有权限"
		_, err := s.UpdateRole(ctx, root, "platform", r.ID, RoleInput{Name: r.Name, Status: 1})
		require.NoError(t, err)
		require.NoError(t, s.GrantRolePerms(ctx, root, "platform", r.ID, []string{"m:thing:list"}))
		require.NoError(t, s.AssignUserRoles(ctx, root, "platform", uid, []uint64{r.ID}))
		ok, err := s.Allowed(ctx, "platform", uid, "m:thing:list")
		require.NoError(t, err)
		require.True(t, ok, name)

		got := make(chan bool, 1)
		fired := false
		s.testHook = func(stage string) {
			if stage != "grant.committed" || fired {
				return
			}
			fired = true
			// 已提交、还没发布：这时来一个判定
			go func() {
				ok, err := s.Allowed(ctx, "platform", uid, "m:thing:list")
				require.NoError(t, err)
				got <- ok
			}()
			time.Sleep(100 * time.Millisecond)
		}
		require.NoError(t, revoke(), name)
		s.testHook = nil
		require.True(t, fired, name)
		require.False(t, <-got, "%s：提交之后、发布之前的判定不能按旧快照放行", name)
	}
}

// 规范 §13.2 第 96 条：改授权的事务回滚时，"待发布"标记照样撤掉，之后的判定不被卡住（D-055）。
func TestPolicy_96b_RollbackReleasesPublishHold(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	root := auth.Principal{Portal: "platform", UserID: 1, Username: "root", Super: true}
	makeSuper(t, ctx, s, root.UserID)
	r := createRaceRole(t, ctx, s, "ops")
	boom := errors.New("caller fails after assigning")
	err := db.Tx(ctx, func(ctx context.Context) error {
		require.NoError(t, s.AssignUserRoles(ctx, root, "platform", 42, []uint64{r.ID}))
		require.EqualValues(t, 1, s.publishing.Load(), "写完、提交之前就标记待发布")
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Zero(t, s.publishing.Load())
	start := time.Now()
	_, err = s.Allowed(ctx, "platform", 42, "m:thing:list")
	require.NoError(t, err)
	require.Less(t, time.Since(start), time.Second)
}
