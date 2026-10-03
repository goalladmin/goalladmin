package system_test

// 个人中心（D-038，规范 §13.2 第 56 条）：本人资料接口只读写调用者本人；只能改允许的几项，夹带别的字段整体拒绝；
// 下线其他设备只影响本人、保留当前会话。

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

func (f *fixture) profileOf(token string) map[string]any {
	f.t.Helper()
	r := f.do(token, "GET", "/system/profile", nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	return r.data()
}

func TestProfile_56_OnlyTouchesCaller(t *testing.T) {
	f := newFixture(t)
	root, rootID := f.admin("root")
	role := f.createRole(root, "clerk", []string{system.PermDictList})
	aliceID, alice := f.createUser(root, "alice", []uint64{role})
	bobID, bob := f.createUser(root, "bob", nil)

	// 没有任何权限码的账号也能看、能改自己的资料：这是登录即可的接口
	p := f.profileOf(bob)
	require.Equal(t, "bob", p["username"])
	require.Equal(t, float64(bobID), p["id"])
	require.Equal(t, false, p["super"])
	require.Equal(t, float64(1), p["sessions"], "只有本次登录一个会话")
	require.Empty(t, p["roles"])
	_, hasRemark := p["remark"]
	require.False(t, hasRemark, "管理员的备注不给本人看")

	p = f.profileOf(alice)
	roles := p["roles"].([]any)
	require.Len(t, roles, 1)
	require.Equal(t, "clerk", roles[0].(map[string]any)["code"])
	require.NotNil(t, p["pwdChangedAt"], "首次改密后有上次改密时间")

	// 改自己的显示名、邮箱、手机、简介（简介允许换行）
	r := f.do(alice, "PUT", "/system/profile", gin.H{"displayName": "  Alice Liddell ", "email": "alice@example.com", "phone": "+65 8000 0000", "bio": "第一行\n第二行"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "Alice Liddell", r.data()["displayName"])
	require.Equal(t, "第一行\n第二行", r.data()["bio"])
	me := f.do(alice, "GET", "/auth/me", nil)
	require.Equal(t, "Alice Liddell", me.data()["user"].(map[string]any)["displayName"], "改完 /auth/me 立即看到新显示名")
	// 管理员那边看到的是同一份数据，别人的资料一个字都没动
	view := f.do(root, "GET", fmt.Sprintf("/system/users/%d", aliceID), nil).data()
	require.Equal(t, "alice@example.com", view["email"])
	require.Equal(t, "第一行\n第二行", view["bio"])
	require.Equal(t, "bob", f.do(root, "GET", fmt.Sprintf("/system/users/%d", bobID), nil).data()["displayName"])
	require.Equal(t, "root", f.do(root, "GET", fmt.Sprintf("/system/users/%d", rootID), nil).data()["displayName"])

	// 夹带不允许改的字段：整体拒绝（3002），库里什么都没变
	for _, body := range []gin.H{
		{"displayName": "Mallory", "username": "root"},
		{"displayName": "Mallory", "status": 0},
		{"displayName": "Mallory", "roleIds": []uint64{f.superRoleID()}},
		{"displayName": "Mallory", "id": rootID},
		{"displayName": "Mallory", "userId": rootID},
		{"displayName": "Mallory", "avatar": "https://example.com/a.png"},
		{"displayName": "Mallory", "deptId": 1},
		{"displayName": "Mallory", "password": "Hacked-pass-1"},
		{"displayName": "Mallory", "mustChangePwd": false},
		{"displayName": "Mallory", "remark": "x"},
	} {
		r := f.do(alice, "PUT", "/system/profile", body)
		require.Equal(t, httpx.CodeBadRequest, r.env.Code, "%v: %s", body, r.rec.Body.String())
	}
	view = f.do(root, "GET", fmt.Sprintf("/system/users/%d", aliceID), nil).data()
	require.Equal(t, "Alice Liddell", view["displayName"])
	require.Equal(t, "alice", view["username"])
	require.Equal(t, float64(1), view["status"])
	require.Len(t, view["roles"].([]any), 1)
	require.Equal(t, "", view["avatar"])
	f.login("alice", "user-pass-456")

	// 校验：显示名必填、邮箱格式、简介的控制字符和长度、显示名的不可见字符
	for _, tc := range []struct {
		name  string
		body  gin.H
		field string
	}{
		{"显示名必填", gin.H{"displayName": "", "email": ""}, "displayName"},
		{"显示名全是空白", gin.H{"displayName": "   "}, "displayName"},
		{"显示名带零宽字符", gin.H{"displayName": "Ali\u200bce"}, "displayName"},
		{"邮箱格式", gin.H{"displayName": "Alice", "email": "not-an-email"}, "email"},
		{"简介带控制字符", gin.H{"displayName": "Alice", "bio": "hi\x07"}, "bio"},
		{"简介太长", gin.H{"displayName": "Alice", "bio": strings.Repeat("长", 256)}, "bio"},
		{"手机太长", gin.H{"displayName": "Alice", "phone": strings.Repeat("1", 33)}, "phone"},
	} {
		r := f.do(alice, "PUT", "/system/profile", tc.body)
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%s: %s", tc.name, r.rec.Body.String())
		fields := r.data()["fields"].([]any)
		require.NotEmpty(t, fields, tc.name)
		require.Equal(t, tc.field, fields[0].(map[string]any)["field"], tc.name)
	}
	require.Equal(t, "Alice Liddell", f.profileOf(alice)["displayName"], "被拒的请求不改任何东西")

	// 操作日志：本人改资料留痕，记在本人名下
	logs := f.oplogs(system.OpProfileUpdate)
	require.NotEmpty(t, logs)
	require.Equal(t, "alice", logs[len(logs)-1].Username)
	require.Equal(t, aliceID, logs[len(logs)-1].UserID)

	// 必须改密的账号连个人中心也进不去（除 /auth/* 外一律 2002）；锁屏后一律 1006
	r = f.do(root, "POST", "/system/users", gin.H{"username": "newbie", "password": "user-pass-123"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	newbie := f.login("newbie", "user-pass-123")
	r = f.do(newbie, "GET", "/system/profile", nil)
	require.Equal(t, 403, r.rec.Code)
	require.Equal(t, httpx.CodePwdChangeRequired, r.env.Code)
	require.Equal(t, 0, f.do(alice, "POST", "/auth/lock", nil).env.Code)
	r = f.do(alice, "PUT", "/system/profile", gin.H{"displayName": "Locked"})
	require.Equal(t, 423, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeSessionLocked, r.env.Code)
}

func TestProfile_56b_RevokeOtherSessionsKeepsCurrent(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	_, alice1 := f.createUser(root, "alice", nil)
	_, bob := f.createUser(root, "bob", nil)
	alice2 := f.login("alice", "user-pass-456")
	alice3 := f.login("alice", "user-pass-456")
	require.Equal(t, float64(3), f.profileOf(alice1)["sessions"])

	// 请求体里指定别人的用户 ID 也没用：接口不读它，只按本人的身份处理
	r := f.do(alice2, "POST", "/system/profile/revoke-other-sessions", gin.H{"userId": 1, "username": "bob"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, float64(2), r.data()["revoked"])

	// 发起的那个会话还活着，另外两个立刻失效；别人的会话不受影响
	require.Equal(t, 0, f.do(alice2, "GET", "/auth/me", nil).env.Code)
	require.Equal(t, 401, f.do(alice1, "GET", "/auth/me", nil).rec.Code)
	require.Equal(t, 401, f.do(alice3, "GET", "/auth/me", nil).rec.Code)
	require.Equal(t, 0, f.do(bob, "GET", "/auth/me", nil).env.Code)
	require.Equal(t, 0, f.do(root, "GET", "/auth/me", nil).env.Code)
	require.Equal(t, float64(1), f.profileOf(alice2)["sessions"])

	// 只剩自己时再点一次：什么都不吊销，当前会话照常
	r = f.do(alice2, "POST", "/system/profile/revoke-other-sessions", nil)
	require.Equal(t, 0, r.env.Code)
	require.Equal(t, float64(0), r.data()["revoked"])
	require.Equal(t, 0, f.do(alice2, "GET", "/auth/me", nil).env.Code)

	logs := f.oplogs(system.OpProfileRevokeOthers)
	require.Len(t, logs, 2)
	require.Equal(t, "alice", logs[0].Username)

	// 路由守卫：三条都是"登录即可"，没有一条对未登录开放
	n := 0
	for _, rt := range f.app.Routes() {
		if strings.Contains(rt.Path, "/system/profile") {
			n++
			require.Equal(t, rbac.GuardAuthOnly, rt.Guard, rt.Method+" "+rt.Path)
		}
	}
	require.Equal(t, 3, n)
	require.Equal(t, 401, f.do("", "GET", "/system/profile", nil).rec.Code)
}

// 轮次用尽时最后一轮刚好取满一页且全部吊销掉：不能误报"会话太多"（D-043）。
// 直接写会话表造出 50 轮 × 200 个别的会话（正常使用不会有这么多）。
func TestProfile_56c_RevokeOtherSessionsExactBoundary(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	aliceID, alice := f.createUser(root, "alice", nil)
	var sb strings.Builder
	sb.WriteString("INSERT INTO ga_session (sid, portal, user_id, refresh_hash, rotated_at, expires_at, ip, user_agent, last_seen_at, created_at) VALUES ")
	for i := 0; i < 50*200; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "('%032d', 'platform', %d, '%064d', UTC_TIMESTAMP(3), DATE_ADD(UTC_TIMESTAMP(3), INTERVAL 1 DAY), '', '', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", i+1, aliceID, i+1)
	}
	require.NoError(t, f.gdb.Exec(sb.String()).Error)

	r := f.do(alice, "POST", "/system/profile/revoke-other-sessions", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, float64(50*200), r.data()["revoked"])
	require.Equal(t, 0, f.do(alice, "GET", "/auth/me", nil).env.Code, "当前会话还在")
	require.Equal(t, float64(1), f.profileOf(alice)["sessions"])
}

// 规范 §13.2 第 109 条（D-058）：只需登录的本人写操作（改资料、换头像、下线其他设备）不排队等全端共用的超管锁——
// 任何账号都能调这些接口，排在超管锁上的话，一个普通账号高并发调用就能拖慢全端的管理写操作。
// 它们只锁本人的账号行；账号被停用、会话被吊销照样作废（第 72 条的路由表用例覆盖）。
func TestProfile_109_SelfWritesDoNotTakePortalLock(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	_, bob := f.createUser(root, "bob", nil)

	release := make(chan struct{})
	held := f.holdSuperLock(func(*gorm.DB) {}, release) // 另一条事务占着超管锁不放
	calls := []func() resp{
		func() resp {
			return f.do(bob, "PUT", "/system/profile", gin.H{"displayName": "bob2", "email": "bob@example.com"})
		},
		func() resp { return f.do(bob, "PUT", "/system/avatar", gin.H{"preset": "ocean"}) },
		func() resp { return f.do(bob, "DELETE", "/system/avatar", nil) },
		func() resp { return f.do(bob, "POST", "/system/profile/revoke-other-sessions", nil) },
	}
	for i, call := range calls {
		done := make(chan resp, 1)
		go func() { done <- call() }()
		select {
		case r := <-done:
			require.Equal(t, 0, r.env.Code, "%d: %s", i, r.rec.Body.String())
		case <-time.After(3 * time.Second):
			close(release)
			<-held
			t.Fatalf("第 %d 个本人写操作在等全端的超管锁", i)
		}
	}
	close(release)
	require.NoError(t, <-held)
}

// 规范 §13.2 第 112 条（D-058）：管理员建、改用户时，显示名和简介与本人在个人中心改的同一套字符校验（不能有控制字符、
// 不可见字符）；用户、角色、字典的排序值有上限，超出时回校验错误而不是 500。
func TestProfile_112_AdminInputsValidatedLikeSelfService(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	r := f.do(root, "POST", "/system/users", gin.H{"username": "eve", "displayName": "eve\u202egnp.exe"})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	require.Contains(t, r.rec.Body.String(), "system.org.nameChars")
	id, _ := f.createUser(root, "frank", nil)
	r = f.do(root, "PUT", fmt.Sprintf("/system/users/%d", id), gin.H{"displayName": "frank", "bio": "hi\u0007there"})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	r = f.do(root, "PUT", fmt.Sprintf("/system/users/%d", id), gin.H{"displayName": "  frank  ", "bio": "多行\n简介"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	for _, c := range []struct {
		method, path string
		body         gin.H
	}{
		{"POST", "/system/users", gin.H{"username": "george", "sort": 5_000_000_000}},
		{"POST", "/system/roles", gin.H{"code": "george", "name": "george", "sort": 5_000_000_000}},
		{"POST", "/system/dicts", gin.H{"code": "george", "name": "george", "sort": 5_000_000_000}},
	} {
		body := c.body
		r := f.do(root, c.method, c.path, body)
		require.Equal(t, 200, r.rec.Code, c.path+" "+r.rec.Body.String())
		require.Equal(t, httpx.CodeValidation, r.env.Code, c.path+" "+r.rec.Body.String())
	}
}

// 规范 §13.2 第 114 条（D-059）：本人写操作锁住自己的会话行再确认有效。吊销会话（管理员下线、改密、重置、停用）
// 写的也是这一行：吊销已经写下、还没提交时，本人写操作排在后面，等吊销提交后看到"已吊销"回 401，什么都不写——
// 吊销返回之后不会再有拿着这个会话的本人写操作提交。
func TestProfile_114_SelfWriteWaitsForPendingRevoke(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, bob := f.createUser(root, "bob", nil)
	var sid string
	require.NoError(t, f.gdb.Raw("SELECT sid FROM ga_session WHERE user_id = ? AND revoked_at IS NULL ORDER BY id DESC LIMIT 1", bobID).Scan(&sid).Error)

	revoke := func(release <-chan struct{}) <-chan error {
		return f.holdTx(func(tx *gorm.DB) {
			require.NoError(t, tx.Exec("UPDATE ga_session SET revoked_at = NOW(3), revoke_reason = 'admin' WHERE sid = ?", sid).Error)
		}, release)
	}
	r := f.inFlight(revoke, func() resp {
		return f.do(bob, "PUT", "/system/profile", gin.H{"displayName": "改名", "email": "bob@example.com"})
	})
	require.Equal(t, 401, r.rec.Code, r.rec.Body.String())
	var name string
	require.NoError(t, f.gdb.Raw("SELECT display_name FROM ga_user WHERE id = ?", bobID).Scan(&name).Error)
	require.NotEqual(t, "改名", name, "被吊销的会话不能再写")
}
