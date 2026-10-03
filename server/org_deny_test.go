package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/modules/agent"
	"github.com/goalladmin/goalladmin/server/modules/merchant"
)

// 规范 §13.2 第 186 条（D-102）：平台查看、整体清空主体自己设的 IP 黑名单。只限 URL 里的这个端、这个主体；
// 查看要列表权限、清空要主账号管理权限（敏感）；清空记操作日志。
func TestPartner_186_OrgDenyListViewAndClear(t *testing.T) {
	f := newPartnerFixture(t)
	root := f.admin("root")
	a1 := f.newOrg(root, "agent", "代理一", nil)
	m1, m2 := f.newOrg(root, "merchant", "商户一", nil), f.newOrg(root, "merchant", "商户二", nil)
	require.Equal(t, a1.id, m1.id, "两个端的主体 ID 相同才验得出按端隔离")
	ipa := f.app.Deps().IPACL
	add := func(tg ipacl.Target, cidr, remark string) {
		_, err := ipa.AddOrgDeny(f.ctx, tg, ipacl.DenyInput{CIDR: cidr, Remark: remark}, 1, "")
		require.NoError(t, err)
	}
	add(ipacl.OrgTarget("merchant", m1.id), "203.0.113.7", "first")
	add(ipacl.OrgTarget("merchant", m1.id), "203.0.113.0/28", "")
	add(ipacl.OrgTarget("merchant", m2.id), "203.0.113.7", "")
	add(ipacl.OrgTarget("agent", a1.id), "203.0.113.7", "")
	_, err := ipa.AddDeny(f.ctx, ipacl.DenyInput{CIDR: "203.0.113.99"}, 1, "")
	require.NoError(t, err)
	_, err = ipa.SetAllow(f.ctx, ipacl.OrgTarget("merchant", m1.id), []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 1, "")
	require.NoError(t, err)
	// 一条已过期的记录：列表里有，清空时一起删
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_ip_rule (kind, scope, portal, org_id, user_id, cidr, expires_at, status, created_at, updated_at) VALUES ('deny', 'org', 'merchant', ?, 0, '203.0.113.200/32', '2000-01-01 00:00:00', 1, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", m1.id).Error)

	mp, ap := fmt.Sprintf("/merchant/merchants/%d/ip-deny", m1.id), fmt.Sprintf("/agent/agents/%d/ip-deny", a1.id)
	count := func(where string, args ...any) int64 {
		var n int64
		require.NoError(t, f.gdb.Table("ga_ip_rule").Where(where, args...).Count(&n).Error)
		return n
	}

	// 查看：只有这个商户的，含已过期的；分页
	r := f.ok(root, "GET", mp, nil)
	require.EqualValues(t, 3, r.data()["total"])
	body := r.rec.Body.String()
	require.Contains(t, body, "203.0.113.0/28")
	require.Contains(t, body, "203.0.113.200/32")
	require.Contains(t, body, `"remark":"first"`)
	require.NotContains(t, body, "203.0.113.99", "全局名单不在里面")
	require.NotContains(t, body, "198.51.100.0/24", "白名单不在里面")
	require.Len(t, f.ok(root, "GET", mp+"?pageSize=2", nil).list(), 2)
	require.EqualValues(t, 1, f.ok(root, "GET", ap, nil).data()["total"])
	require.Equal(t, 404, f.do(root, "GET", "/merchant/merchants/999/ip-deny", nil).rec.Code)

	// 权限：查看要列表权限，清空要主账号管理权限；代理商的权限不管商户
	viewer := f.createRole(root, "viewer", []string{merchant.PermList, agent.PermList, agent.PermOwner})
	_, v := f.createUser(root, "viewer", []uint64{viewer})
	f.ok(v, "GET", mp, nil)
	require.Equal(t, 403, f.do(v, "DELETE", mp, nil).rec.Code)
	nobody := f.createRole(root, "nobody", []string{agent.PermList})
	_, nb := f.createUser(root, "nobody", []uint64{nobody})
	require.Equal(t, 403, f.do(nb, "GET", mp, nil).rec.Code)
	require.EqualValues(t, 3, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = ?", m1.id), "被拒的请求什么都不动")

	// 清空：只动这个端这个主体自己设的黑名单
	require.Equal(t, 404, f.do(root, "DELETE", "/merchant/merchants/999/ip-deny", nil).rec.Code)
	r = f.ok(root, "DELETE", mp, nil)
	require.EqualValues(t, 3, r.data()["removed"])
	require.Zero(t, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = ?", m1.id))
	require.EqualValues(t, 1, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = ?", m2.id))
	require.EqualValues(t, 1, count("kind = 'deny' AND scope = 'org' AND portal = 'agent' AND org_id = ?", a1.id))
	require.EqualValues(t, 1, count("kind = 'deny' AND scope = 'global'"))
	require.EqualValues(t, 1, count("kind = 'allow' AND scope = 'org' AND portal = 'merchant' AND org_id = ?", m1.id))
	require.EqualValues(t, 0, f.ok(root, "GET", mp, nil).data()["total"])
	// 再清一次：没有可删的，照样成功
	require.EqualValues(t, 0, f.ok(root, "DELETE", mp, nil).data()["removed"])

	// 代理商的接口清的是代理商的（有代理商主账号管理权限的人）
	require.EqualValues(t, 1, f.ok(v, "DELETE", ap, nil).data()["removed"])
	require.Zero(t, count("kind = 'deny' AND scope = 'org' AND portal = 'agent' AND org_id = ?", a1.id))
	require.EqualValues(t, 1, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = ?", m2.id))

	// 清空留痕
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE portal = 'platform' AND action = ? AND http_status = 200 AND code = 0", merchant.OpClearIPDeny).Scan(&n).Error)
	require.EqualValues(t, 2, n)
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_operation_log WHERE portal = 'platform' AND action = ? AND http_status = 200 AND code = 0", agent.OpClearIPDeny).Scan(&n).Error)
	require.EqualValues(t, 1, n)
}
