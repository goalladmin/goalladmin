package system_test

// 规范 §13.2 第 62 条：角色的状态判断、引用计数和授权都在锁内按已提交的状态做（D-043）。
// 每个用例都用另一条事务先占住锁并改动数据、再让请求进来，验证请求看到的是提交后的状态。

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// holdSuperLock 在另一条事务里锁住超管角色行（AssignUserRoles / UpdateRole / DeleteRole 都先拿这把锁），
// 执行 during，然后等 release 关闭后提交。
func (f *fixture) holdSuperLock(during func(tx *gorm.DB), release <-chan struct{}) <-chan error {
	done := make(chan error, 1)
	locked := make(chan struct{})
	go func() {
		done <- f.gdb.Transaction(func(tx *gorm.DB) error {
			var rows []map[string]any
			if err := tx.Raw("SELECT id FROM ga_role WHERE portal = 'platform' AND is_super = 1 FOR UPDATE").Scan(&rows).Error; err != nil {
				close(locked)
				return err
			}
			during(tx)
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked
	return done
}

func (f *fixture) roleStatus(id uint64) int {
	var st int
	require.NoError(f.t, f.gdb.Raw("SELECT status FROM ga_role WHERE id = ?", id).Scan(&st).Error)
	return st
}

// 74a. 非超管改角色时，超管同时把角色停用：非超管的写入必须看到"已停用"，走重新启用的检查并被拒。
func TestRoleRace_62a_UpdateSeesCommittedStatus(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	ops := f.createRole(admin, "ops", []string{system.PermUserCreate}) // 含敏感权限码：非超管不能把它重新启用
	mgr := f.createRole(admin, "mgr", []string{system.PermRoleUpdate, system.PermRoleList})
	_, carol := f.createUser(admin, "carol", []uint64{mgr})

	release := make(chan struct{})
	held := f.holdSuperLock(func(tx *gorm.DB) {
		require.NoError(t, tx.Exec("UPDATE ga_role SET status = 0 WHERE id = ?", ops).Error)
	}, release)

	var wg sync.WaitGroup
	var status, code int
	wg.Add(1)
	go func() {
		defer wg.Done()
		// carol 只是改个名字、状态照旧填 1：在她看来角色是启用的
		status, code = f.try(carol, "PUT", fmt.Sprintf("/system/roles/%d", ops), gin.H{"name": "ops-renamed", "status": 1})
	}()
	time.Sleep(300 * time.Millisecond) // 让请求走到锁前面
	close(release)
	require.NoError(t, <-held)
	wg.Wait()
	require.Equal(t, 403, status, "超管已停用的含敏感权限的角色，非超管不能顺手写回启用（code %d）", code)
	require.Equal(t, 0, f.roleStatus(ops), "角色必须仍是停用")
}

// 74b. 删角色时另一边刚把它分配给了用户：删除必须看到已提交的分配并拒绝，不留悬空的用户角色行。
func TestRoleRace_62b_DeleteSeesCommittedAssignment(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	tmp := f.createRole(admin, "tmp", nil)
	bobID, _ := f.createUser(admin, "bob", nil)

	release := make(chan struct{})
	held := f.holdSuperLock(func(tx *gorm.DB) {
		require.NoError(t, tx.Exec("INSERT INTO ga_user_role (portal, user_id, role_id) VALUES ('platform', ?, ?)", bobID, tmp).Error)
	}, release)

	var wg sync.WaitGroup
	var code int
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, code = f.try(admin, "DELETE", fmt.Sprintf("/system/roles/%d", tmp), nil)
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)
	require.NoError(t, <-held)
	wg.Wait()
	require.Equal(t, httpx.CodeConflict, code, "已被分配的角色不能删")
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_role WHERE id = ?", tmp).Scan(&n).Error)
	require.EqualValues(t, 1, n, "角色还在")
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user_role WHERE role_id = ? AND user_id = ?", tmp, bobID).Scan(&n).Error)
	require.EqualValues(t, 1, n, "分配也还在，没有悬空")
}

// 74c. 授权时角色刚被并发删除：授权必须报 404，不给已删除的角色留下孤儿策略。
func TestRoleRace_62c_GrantAfterDeleteIs404(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	tmp := f.createRole(admin, "tmp", nil)

	release := make(chan struct{})
	done := make(chan error, 1)
	locked := make(chan struct{})
	go func() {
		done <- f.gdb.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("DELETE FROM ga_role WHERE id = ?", tmp).Error; err != nil {
				close(locked)
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	var wg sync.WaitGroup
	var status int
	wg.Add(1)
	go func() {
		defer wg.Done()
		status, _ = f.try(admin, "PUT", fmt.Sprintf("/system/roles/%d/perms", tmp), gin.H{"codes": []string{system.PermUserList}})
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)
	require.NoError(t, <-done)
	wg.Wait()
	require.Equal(t, 404, status)
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_casbin_rule WHERE v0 = ?", fmt.Sprintf("role:%d", tmp)).Scan(&n).Error)
	require.EqualValues(t, 0, n, "被删角色不能留下策略行")
}

// 74d. 分配角色时角色刚被并发删除：分配必须在锁内看到"角色已不存在"并拒绝，不留悬空的用户角色行。
// 直接调 rbac 服务（系统模块的接口在外层已经拿了超管锁，测不出这一层）。
func TestRoleRace_62d_AssignSeesCommittedDelete(t *testing.T) {
	f := newFixture(t)
	admin, adminID := f.admin("admin")
	tmp := f.createRole(admin, "tmp", nil)
	bobID, _ := f.createUser(admin, "bob", nil)
	ctx := f.app.Context(context.Background())
	actor := auth.Principal{Portal: "platform", UserID: adminID, Username: "admin", Super: true}

	release := make(chan struct{})
	held := f.holdSuperLock(func(tx *gorm.DB) {
		require.NoError(t, tx.Exec("DELETE FROM ga_role WHERE id = ?", tmp).Error)
	}, release)

	var wg sync.WaitGroup
	var err error
	wg.Add(1)
	go func() {
		defer wg.Done()
		err = f.app.Deps().RBAC.AssignUserRoles(ctx, actor, "platform", bobID, []uint64{tmp})
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)
	require.NoError(t, <-held)
	wg.Wait()
	require.Error(t, err, "角色已被删除，分配必须被拒")
	var links int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user_role WHERE role_id = ?", tmp).Scan(&links).Error)
	require.EqualValues(t, 0, links, "不能留下指向已删角色的用户角色行")
}

// ============ 63b. 真实用户表的排序规则忽略重音：别名登录不能碰到账号 ============

// ga_user 用的是不区分重音的排序规则，`ádmin` 能查到 `admin`；登录必须按"账号不存在"处理，
// 拿着正确密码也进不来，别名上的失败也不算到本账号头上（内核层面的规则见 §13.2 第 63 条）。
func TestLogin_63b_AccentAliasOnRealUserTable(t *testing.T) {
	f := newFixture(t)
	_, _ = f.admin("admin") // 密码改成了 changed-pass-9
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user WHERE username = ?", "ádmin").Scan(&n).Error)
	require.EqualValues(t, 1, n, "前提：排序规则让别名查到同一个账号")

	_, code := f.try("", "POST", "/auth/login", gin.H{"username": "ádmin", "password": "changed-pass-9"})
	require.Equal(t, httpx.CodeLoginFailed, code, "正确密码配别名也不能登录")
	for i := 0; i < 2; i++ {
		_, code = f.try("", "POST", "/auth/login", gin.H{"username": "Ádmin", "password": "wrong-9"})
		require.Equal(t, httpx.CodeLoginFailed, code)
	}
	_, code = f.try("", "POST", "/auth/login", gin.H{"username": "ádmin", "password": "changed-pass-9"})
	require.Equal(t, httpx.CodeCaptchaRequired, code, "别名自己的键累计三次失败后要验证码")
	r := f.do("", "POST", "/auth/login", gin.H{"username": "admin", "password": "changed-pass-9"})
	require.Equal(t, 0, r.env.Code, "别名上的失败不让本账号进入验证码阶段："+r.rec.Body.String())
}
