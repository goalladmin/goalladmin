package main

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 规范 §13.2 第 185 条（D-101）：更换主账号时，原主账号的会话在同一个事务里全部吊销、角色清空；
// 新主账号、别的账号、别的主体、另一个端同 ID 的账号都不受影响。平台程序的真实装配（授权服务接在主体服务上）。
func TestPartner_185_ChangeOwnerRetiresPreviousOwner(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	m1, m2 := f.newOrg(root, "merchant", "商户一", nil), f.newOrg(root, "merchant", "商户二", nil)
	a1 := f.newOrg(root, "agent", "代理一", nil)
	require.Equal(t, a1.ownerID, m1.ownerID, "两个端的账号 ID 相同才验得出按端隔离")
	staff := f.addAccount("merchant", m1.id, "staff")
	clerk := f.addAccount("merchant", m1.id, "clerk")

	// 角色：原主账号事先给自己配了两个，新主账号、另一个员工、别的主体的主账号、代理商端同 ID 的账号各有一个
	addRole := func(portal string, orgID uint64, code string) uint64 {
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_role (portal, org_id, code, name, status, created_at, updated_at) VALUES (?, ?, ?, ?, 1, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", portal, orgID, code, code).Error)
		var id uint64
		require.NoError(t, f.gdb.Raw("SELECT id FROM ga_role WHERE portal = ? AND org_id = ? AND code = ?", portal, orgID, code).Scan(&id).Error)
		return id
	}
	give := func(portal string, userID, roleID uint64) {
		require.NoError(t, f.gdb.Exec("INSERT INTO ga_user_role (portal, user_id, role_id) VALUES (?, ?, ?)", portal, userID, roleID).Error)
	}
	roles := func(portal string, userID uint64) int64 {
		var n int64
		require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user_role WHERE portal = ? AND user_id = ?", portal, userID).Scan(&n).Error)
		return n
	}
	r1, r2 := addRole("merchant", m1.id, "r1"), addRole("merchant", m1.id, "r2")
	rOther := addRole("merchant", m2.id, "r1")
	rAgent := addRole("agent", a1.id, "r1")
	give("merchant", m1.ownerID, r1)
	give("merchant", m1.ownerID, r2)
	give("merchant", staff, r1)
	give("merchant", clerk, r2)
	give("merchant", m2.ownerID, rOther)
	give("agent", a1.ownerID, rAgent)

	sOld1, sOld2 := f.addSession("merchant", m1.id, m1.ownerID), f.addSession("merchant", m1.id, m1.ownerID)
	sStaff, sClerk := f.addSession("merchant", m1.id, staff), f.addSession("merchant", m1.id, clerk)
	sOther, sAgent := f.addSession("merchant", m2.id, m2.ownerID), f.addSession("agent", a1.id, a1.ownerID)

	// 换不成的（别的主体的账号）：什么都不动
	r := f.do(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/owner", m1.id), gin.H{"userId": m2.ownerID})
	require.NotEqual(t, 0, r.env.Code)
	require.Empty(t, f.sessionRevoked(sOld1))
	require.EqualValues(t, 2, roles("merchant", m1.ownerID))

	// 换成自己：什么都不动
	f.ok(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/owner", m1.id), gin.H{"userId": m1.ownerID})
	require.Empty(t, f.sessionRevoked(sOld1))
	require.EqualValues(t, 2, roles("merchant", m1.ownerID))

	f.ok(root, "PUT", fmt.Sprintf("/merchant/merchants/%d/owner", m1.id), gin.H{"userId": staff})
	got := f.ok(root, "GET", fmt.Sprintf("/merchant/merchants/%d", m1.id), nil).data()
	require.Equal(t, "staff", got["ownerUsername"])

	// 原主账号：会话全部吊销，角色清空，账号还在、仍是启用
	require.Equal(t, "admin", f.sessionRevoked(sOld1))
	require.Equal(t, "admin", f.sessionRevoked(sOld2))
	require.Zero(t, roles("merchant", m1.ownerID))
	var status int
	require.NoError(t, f.gdb.Raw("SELECT status FROM ga_merchant_user WHERE id = ? AND org_id = ?", m1.ownerID, m1.id).Scan(&status).Error)
	require.Equal(t, 1, status)

	// 新主账号、另一个员工、别的主体、代理商端同 ID 的账号：会话和角色都不动
	for _, sid := range []string{sStaff, sClerk, sOther, sAgent} {
		require.Empty(t, f.sessionRevoked(sid), sid)
	}
	require.EqualValues(t, 1, roles("merchant", staff))
	require.EqualValues(t, 1, roles("merchant", clerk))
	require.EqualValues(t, 1, roles("merchant", m2.ownerID))
	require.EqualValues(t, 1, roles("agent", a1.ownerID))

	// 角色本身还在（只清了分配）
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_role WHERE id IN ?", []uint64{r1, r2}).Scan(&n).Error)
	require.EqualValues(t, 2, n)
}
