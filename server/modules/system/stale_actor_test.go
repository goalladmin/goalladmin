package system_test

// 规范 §13.2 第 68–70 条（D-045）：操作人的身份和范围在锁内按已提交的状态重新认定。
// 每个用例都让一个请求在"认证已经通过、还没拿到锁"时等着，另一条事务在这期间收回它的超管角色（或收窄范围、
// 重置密码、停用负责人）并提交；请求拿到锁后必须按提交后的状态判断，而不是认证时的旧身份。

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// holdTx 在另一条事务里执行 during，等 release 关闭后提交；返回的通道在提交后收到结果。
func (f *fixture) holdTx(during func(tx *gorm.DB), release <-chan struct{}) <-chan error {
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- f.gdb.Transaction(func(tx *gorm.DB) error {
			during(tx)
			close(ready)
			<-release
			return nil
		})
	}()
	<-ready
	return done
}

// inFlight 先让 hold 占住锁并改动数据，再发请求；请求走到锁前面以后放行 hold，返回请求的结果。
func (f *fixture) inFlight(hold func(release <-chan struct{}) <-chan error, req func() resp) resp {
	f.t.Helper()
	release := make(chan struct{})
	held := hold(release)
	var r resp
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r = req()
	}()
	time.Sleep(300 * time.Millisecond) // 让请求通过认证、走到锁前面
	close(release)
	require.NoError(f.t, <-held)
	wg.Wait()
	return r
}

func (f *fixture) holdsSuper(userID uint64) bool {
	var n int64
	require.NoError(f.t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user_role WHERE user_id = ? AND role_id = ?", userID, f.superRoleID()).Scan(&n).Error)
	return n > 0
}

// staleSuper 建第二个超管 sam；返回的 revoke 在占住超管锁的事务里收回 sam 的超管角色。
func (f *fixture) staleSuper(root string) (samID uint64, sam string, revoke func(release <-chan struct{}) <-chan error) {
	f.t.Helper()
	sup := f.superRoleID()
	samID, sam = f.createUser(root, "sam", []uint64{sup})
	revoke = func(release <-chan struct{}) <-chan error {
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(f.t, tx.Exec("DELETE FROM ga_user_role WHERE user_id = ? AND role_id = ?", samID, sup).Error)
		}, release)
	}
	return samID, sam, revoke
}

// 77a. 超管角色被收回后，在途的请求不能再给自己分配超管角色。
func TestStaleActor_68a_RevokedSuperCannotRegrantItself(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	samID, sam, revoke := f.staleSuper(root)
	r := f.inFlight(revoke, func() resp {
		return f.do(sam, "PUT", fmt.Sprintf("/system/users/%d/roles", samID), gin.H{"roleIds": []uint64{f.superRoleID()}})
	})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.False(t, f.holdsSuper(samID), "超管角色不能被授回")
}

// 77b. 超管角色被收回后，在途的请求不能再重置别人的密码。
func TestStaleActor_68b_RevokedSuperCannotResetPassword(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	daveID, _ := f.createUser(root, "dave", nil)
	_, sam, revoke := f.staleSuper(root)
	r := f.inFlight(revoke, func() resp {
		return f.do(sam, "POST", fmt.Sprintf("/system/users/%d/reset-password", daveID), nil)
	})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.NotEmpty(t, f.login("dave", "user-pass-456"), "dave 的密码没被换掉")
}

// 77c. 超管角色被收回后，在途的请求不能再让超管的会话下线。
func TestStaleActor_68c_RevokedSuperCannotEndSuperSessions(t *testing.T) {
	f := newFixture(t)
	root, rootID := f.admin("root")
	sid := f.mySID(root, rootID)
	_, sam, revoke := f.staleSuper(root)
	r := f.inFlight(revoke, func() resp {
		return f.do(sam, "POST", "/system/sessions/"+sid+"/revoke", nil)
	})
	require.NotEqual(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, 200, f.do(root, "GET", "/auth/me", nil).rec.Code, "root 的会话还在")
}

// 77d. 超管角色被收回后，在途的请求不能再授出敏感权限码，也不能改内置超管角色。
func TestStaleActor_68d_RevokedSuperCannotGrantOrEditSuperRole(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	role := f.createRole(root, "plain", nil)
	_, sam, revoke := f.staleSuper(root)
	r := f.inFlight(revoke, func() resp {
		return f.do(sam, "PUT", fmt.Sprintf("/system/roles/%d/perms", role), gin.H{"codes": []string{system.PermUserCreate}})
	})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	perms, err := f.app.Deps().RBAC.RolePerms(f.app.Context(context.Background()), "platform", role)
	require.NoError(t, err)
	require.Empty(t, perms, "角色没有被授权")

	// 再来一次：sam 重新成为超管，然后在途时又被收回，改内置超管角色的名字被拒
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/users/%d/roles", f.userID("sam")), gin.H{"roleIds": []uint64{f.superRoleID()}}).env.Code)
	sam = f.login("sam", "user-pass-456")
	_, _, revoke = f.revokeOf("sam")
	r = f.inFlight(revoke, func() resp {
		return f.do(sam, "PUT", fmt.Sprintf("/system/roles/%d", f.superRoleID()), gin.H{"name": "hijacked", "status": 1})
	})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
}

func (f *fixture) userID(username string) uint64 {
	var id uint64
	require.NoError(f.t, f.gdb.Raw("SELECT id FROM ga_user WHERE username = ?", username).Scan(&id).Error)
	return id
}

func (f *fixture) revokeOf(username string) (uint64, string, func(release <-chan struct{}) <-chan error) {
	id, sup := f.userID(username), f.superRoleID()
	return id, username, func(release <-chan struct{}) <-chan error {
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(f.t, tx.Exec("DELETE FROM ga_user_role WHERE user_id = ? AND role_id = ?", id, sup).Error)
		}, release)
	}
}

// 78. 本人改密的请求已经验证过旧密码，这时管理员重置了密码、吊销了会话：改密不能再把密码改回自己选的。
func TestStaleActor_69_PasswordChangeCannotOverrideReset(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, bob := f.createUser(root, "bob", nil)
	resetHash, err := f.app.Deps().Auth.HashPassword("platform", "Reset-By-Admin-42!")
	require.NoError(t, err)
	reset := func(release <-chan struct{}) <-chan error {
		// 和 resetPassword 一样：写新密码、要求改密、吊销全部会话，在一个事务里
		return f.holdTx(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET password_hash = ?, must_change_pwd = 1 WHERE id = ?", resetHash, bobID).Error)
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'pwd_change' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", bobID).Error)
		}, release)
	}
	r := f.inFlight(reset, func() resp {
		return f.do(bob, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-456", "newPassword": "bobs-own-choice-9"})
	})
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	l := f.do("", "POST", "/auth/login", gin.H{"username": "bob", "password": "bobs-own-choice-9"})
	require.NotEqual(t, 0, l.env.Code, "改密请求不能覆盖管理员的重置")
	var must bool
	require.NoError(t, f.gdb.Raw("SELECT must_change_pwd FROM ga_user WHERE id = ?", bobID).Scan(&must).Error)
	require.True(t, must, "强制改密的标记还在")
}

// 79a. 收窄范围的授权刚提交、内存还没重载时，在途的请求不能再按旧的"全部"挪部门。
func TestStaleActor_70a_DeptMoveUsesCommittedScope(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	east, west := f.createDept(root, 0, "东区"), f.createDept(root, 0, "西区")
	perms := []string{system.PermUserList, system.PermUserCreate, system.PermUserUpdate, system.PermUserStatus,
		system.PermUserAssignRole, system.PermSessionList, system.PermSessionRevoke, system.PermDeptUpdate, system.PermDeptList}
	role := f.scopedRole(root, "mover", perms, "all")
	_, mover := f.userIn(root, "mover", east, []uint64{role})
	narrow := func(release <-chan struct{}) <-chan error {
		// 和授权一样拿超管锁、写范围；内存里的范围要等授权提交之后才重载，这里干脆不重载
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("INSERT INTO ga_role_data_scope (role_id, resource, scope) VALUES (?, ?, 'self') ON DUPLICATE KEY UPDATE scope = 'self'", role, system.DataUser).Error)
		}, release)
	}
	r := f.inFlight(narrow, func() resp {
		return f.do(mover, "PUT", fmt.Sprintf("/system/depts/%d", west), gin.H{"parentId": east, "name": "西区"})
	})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "system.dept.moveNeedsAll", fieldKey(r))
	var parent uint64
	require.NoError(t, f.gdb.Raw("SELECT parent_id FROM ga_dept WHERE id = ?", west).Scan(&parent).Error)
	require.Zero(t, parent, "部门没有被挪动")
}

// 79b. 选负责人的检查和建部门在一个事务里：检查期间负责人被停用，部门不能带着这个负责人建出来。
func TestStaleActor_70b_DeptLeaderCheckedInsideTx(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	leaderID, _ := f.createUser(root, "leader", nil)
	disable := func(release <-chan struct{}) <-chan error {
		// 停用账号也在超管锁里
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET status = 0 WHERE id = ?", leaderID).Error)
		}, release)
	}
	r := f.inFlight(disable, func() resp {
		return f.do(root, "POST", "/system/depts", gin.H{"name": "新部门", "leaderUserId": leaderID})
	})
	require.NotEqual(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "system.dept.leader", fieldKey(r))
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_dept WHERE leader_user_id = ?", leaderID).Scan(&n).Error)
	require.Zero(t, n, "没有部门带着停用的负责人建出来")
}

// 80a. 账号在请求认证之后被停用（会话一并吊销）：在途的写请求拿到锁后整个作废（401），什么都不写（D-046）。
func TestStaleActor_71a_DisabledAccountInFlightWriteIsRejected(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	role := f.createRole(root, "deptmaker", []string{system.PermDeptCreate, system.PermDeptList})
	annID, ann := f.createUser(root, "ann", []uint64{role})
	disable := func(release <-chan struct{}) <-chan error {
		// 和停用账号一样：拿超管锁、改状态、吊销会话
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET status = 0 WHERE id = ?", annID).Error)
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'disabled' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", annID).Error)
		}, release)
	}
	r := f.inFlight(disable, func() resp {
		return f.do(ann, "POST", "/system/depts", gin.H{"name": "停用之后建的部门"})
	})
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_dept WHERE name = ?", "停用之后建的部门").Scan(&n).Error)
	require.Zero(t, n)
}

// 80b. 会话在请求认证之后被吊销（账号仍启用）：在途的写请求同样作废（D-046）。
func TestStaleActor_71b_RevokedSessionInFlightWriteIsRejected(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	role := f.createRole(root, "editor", []string{system.PermUserList, system.PermUserUpdate})
	_, ed := f.createUser(root, "eddie", []uint64{role})
	targetID, _ := f.createUser(root, "target", nil)
	edID := f.userID("eddie")
	revoke := func(release <-chan struct{}) <-chan error {
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'admin' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", edID).Error)
		}, release)
	}
	r := f.inFlight(revoke, func() resp {
		return f.do(ed, "PUT", fmt.Sprintf("/system/users/%d", targetID), gin.H{"displayName": "被改的名字"})
	})
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	require.NotEqual(t, "被改的名字", f.userDisplayName(targetID))
}

// 80c. 本人改密和管理员重置同时发生：两边加锁顺序相同（用户行 → 会话行），不会死锁；
// 无论谁先，最后都是管理员重置后的状态或改密后又被重置的状态，都要求下次登录改密（D-046）。
func TestStaleActor_71c_PasswordChangeAndResetDoNotDeadlock(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("dl%d", i)
		uid, tok := f.createUser(root, name, nil)
		var wg sync.WaitGroup
		var a, b resp
		wg.Add(2)
		go func() {
			defer wg.Done()
			a = f.do(tok, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-456", "newPassword": "self-chosen-789"})
		}()
		go func() {
			defer wg.Done()
			b = f.do(root, "POST", fmt.Sprintf("/system/users/%d/reset-password", uid), nil)
		}()
		wg.Wait()
		require.Less(t, a.rec.Code, 500, "改密不能因为死锁失败: %s", a.rec.Body.String())
		require.Less(t, b.rec.Code, 500, "重置不能因为死锁失败: %s", b.rec.Body.String())
		require.Equal(t, 0, b.env.Code, b.rec.Body.String())
		var must bool
		require.NoError(t, f.gdb.Raw("SELECT must_change_pwd FROM ga_user WHERE id = ?", uid).Scan(&must).Error)
		require.True(t, must, "第 %d 轮：管理员重置的结果留着", i)
	}
}

func (f *fixture) activeSessions(userID uint64) int64 {
	var n int64
	require.NoError(f.t, f.gdb.Raw("SELECT COUNT(*) FROM ga_session WHERE user_id = ? AND revoked_at IS NULL", userID).Scan(&n).Error)
	return n
}

// 82a. 登录校验完密码、还没建会话时，管理员重置了密码：这次登录作废，不能带着旧密码建出一个漏网的会话（D-047）。
func TestStaleActor_73a_LoginRacingResetGetsNoSession(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, _ := f.createUser(root, "bob", nil)
	resetHash, err := f.app.Deps().Auth.HashPassword("platform", "Reset-By-Admin-42!")
	require.NoError(t, err)
	reset := func(release <-chan struct{}) <-chan error {
		return f.holdTx(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET password_hash = ?, must_change_pwd = 1 WHERE id = ?", resetHash, bobID).Error)
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'pwd_change' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", bobID).Error)
		}, release)
	}
	r := f.inFlight(reset, func() resp {
		return f.do("", "POST", "/auth/login", gin.H{"username": "bob", "password": "user-pass-456"})
	})
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
	require.Zero(t, f.activeSessions(bobID), "旧密码不能换来会话")
}

// 82b. 登录校验完密码、还没建会话时，账号被停用：同样作废（D-047）。
func TestStaleActor_73b_LoginRacingDisableGetsNoSession(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, _ := f.createUser(root, "bob", nil)
	disable := func(release <-chan struct{}) <-chan error {
		return f.holdTx(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET status = 0 WHERE id = ?", bobID).Error)
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = UTC_TIMESTAMP(3), revoke_reason = 'disabled' WHERE portal = 'platform' AND user_id = ? AND revoked_at IS NULL", bobID).Error)
		}, release)
	}
	r := f.inFlight(disable, func() resp {
		return f.do("", "POST", "/auth/login", gin.H{"username": "bob", "password": "user-pass-456"})
	})
	require.Equal(t, httpx.CodeLoginFailed, r.env.Code, r.rec.Body.String())
	require.Zero(t, f.activeSessions(bobID), "停用的账号不能换来会话")
}

// 83. 同一个会话里两个改密请求并发：后一个验证旧密码时前一个还没提交，拿到锁后发现密码已经换了，
// 按旧密码不对拒绝，不能把前一个改好的密码覆盖掉（D-047）。
func TestStaleActor_74_ConcurrentPasswordChangesInOneSession(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, bob := f.createUser(root, "bob", nil)
	firstHash, err := f.app.Deps().Auth.HashPassword("platform", "first-change-77")
	require.NoError(t, err)
	first := func(release <-chan struct{}) <-chan error {
		// 前一个改密请求：写了新密码、吊销了别的会话，还没提交
		return f.holdTx(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_user SET password_hash = ? WHERE id = ?", firstHash, bobID).Error)
		}, release)
	}
	r := f.inFlight(first, func() resp {
		return f.do(bob, "PUT", "/auth/password", gin.H{"oldPassword": "user-pass-456", "newPassword": "second-change-88"})
	})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	require.Equal(t, "password.oldIncorrect", fieldKey(r))
	require.NotEmpty(t, f.login("bob", "first-change-77"), "前一个改密的结果留着")
	l := f.do("", "POST", "/auth/login", gin.H{"username": "bob", "password": "second-change-88"})
	require.Equal(t, httpx.CodeLoginFailed, l.env.Code)
}

// 86. 请求通过了路由的权限守卫，等锁期间这个权限被收回：拿到锁后按收回之后的授权再核一遍，回 403，什么都不写（D-048）。
func TestStaleActor_77_RouteGuardRecheckedInsideLock(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	role := f.createRole(root, "dictops", []string{system.PermDictList, system.PermDictCreate})
	_, ops := f.createUser(root, "ops", []uint64{role})
	revoke := func(release <-chan struct{}) <-chan error {
		// 和授权一样拿超管锁，把这个角色的权限全部收回
		return f.holdSuperLock(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("DELETE FROM ga_casbin_rule WHERE v0 = ?", fmt.Sprintf("role:%d", role)).Error)
		}, release)
	}
	r := f.inFlight(revoke, func() resp {
		return f.do(ops, "POST", "/system/dicts", gin.H{"code": "city", "name": "城市", "portal": "platform"})
	})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_dict WHERE code = 'city'").Scan(&n).Error)
	require.Zero(t, n, "字典没有建出来")
}

// 87. 超管在途中被降级：写入照常（剩下的角色够），但回显按降级后的"查看用户"范围，看不到的人不带资料（D-048）。
func TestStaleActor_78_EchoUsesActorAfterRecheck(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	daveID, _ := f.createUser(root, "dave", nil)
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/users/%d", daveID), gin.H{"displayName": "dave", "email": "dave@example.com", "phone": "13800000000"}).env.Code)
	editor := f.createRole(root, "editor", []string{system.PermUserUpdate}) // 修改：全部
	viewer := f.scopedRole(root, "viewer", []string{system.PermUserList}, "self")
	f.createUser(root, "sam", []uint64{f.superRoleID(), editor, viewer})
	sam := f.login("sam", "user-pass-456")
	_, _, revoke := f.revokeOf("sam")
	r := f.inFlight(revoke, func() resp {
		return f.do(sam, "PUT", fmt.Sprintf("/system/users/%d", daveID), gin.H{"displayName": "改名", "email": "dave@example.com", "phone": "13800000000"})
	})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.NotContains(t, r.rec.Body.String(), "dave@example.com", "降级后看不到 dave，回显不能带他的资料")
	require.NotContains(t, r.rec.Body.String(), "13800000000")
}
