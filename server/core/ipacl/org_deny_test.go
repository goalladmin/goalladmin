package ipacl_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/stretchr/testify/require"
)

func TestOrgDeny_176_IsolationExpiryCapacity(t *testing.T) {
	ctx, gdb, c := setup(t)
	s := newSvc(t, ctx, gdb, c)
	peer := newSvc(t, ctx, gdb, c)
	target := ipacl.OrgTarget("merchant", 1)
	add := func(cidr string, ttl time.Duration) (ipacl.Rule, error) {
		return s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: cidr, ExpiresIn: ttl}, 1, "192.0.2.1")
	}
	_, err := add("192.0.2.0/24", 0)
	require.Equal(t, "ipacl.selfLockout", fieldKey(t, err))
	_, err = add("0.0.0.0/0", 0)
	require.Equal(t, "ipacl.denyTooBroad", fieldKey(t, err))
	_, err = s.AddOrgDeny(ctx, ipacl.OrgTarget("merchant", 0), ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "")
	require.Error(t, err)
	r, err := add("198.51.100.0/24", time.Hour)
	require.NoError(t, err)
	_, err = s.SetAllow(ctx, target, []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 1, "")
	require.NoError(t, err)
	require.False(t, s.AccountAllows(ctx, "merchant", 1, 1, "198.51.100.8"))
	require.True(t, s.AccountAllows(ctx, "merchant", 2, 2, "198.51.100.8"))
	require.True(t, s.AccountAllows(ctx, "agent", 1, 1, "198.51.100.8"))
	require.False(t, s.Blocked(ctx, "198.51.100.8"))
	c.Advance(ipacl.DefaultRefresh)
	require.False(t, peer.AccountAllows(ctx, "merchant", 1, 1, "198.51.100.8"))
	_, err = s.RemoveOrgDeny(ctx, ipacl.OrgTarget("merchant", 2), r.ID)
	require.ErrorIs(t, err, httpx.ErrNotFound)
	_, err = s.RemoveDeny(ctx, r.ID)
	require.ErrorIs(t, err, httpx.ErrNotFound)
	list, n, err := s.ListDeny(ctx, ipacl.DenyFilter{}, 1, 20)
	require.NoError(t, err)
	require.Empty(t, list)
	require.Zero(t, n)
	c.Advance(time.Hour)
	require.True(t, s.AccountAllows(ctx, "merchant", 1, 1, "198.51.100.8"))
	r2, err := add("198.51.100.0/24", 0)
	require.NoError(t, err)
	require.Equal(t, r.ID, r2.ID)
	sentinel := errors.New("rollback")
	err = db.Tx(ctx, func(tx context.Context) error {
		_, err := s.RemoveOrgDeny(tx, target, r.ID)
		if err != nil {
			return err
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.False(t, s.AccountAllows(ctx, "merchant", 1, 1, "198.51.100.8"))
	_, err = s.RemoveOrgDeny(ctx, target, r.ID)
	require.NoError(t, err)
	require.True(t, s.AccountAllows(ctx, "merchant", 1, 1, "198.51.100.8"))
	for i := 0; i < ipacl.MaxOrgDenyRules; i++ {
		_, err = add(fmt.Sprintf("203.0.113.%d", i), 0)
		require.NoError(t, err)
	}
	_, err = add("203.0.113.200", 0)
	require.Equal(t, "ipacl.denyFull", fieldKey(t, err))
	_, err = add("203.0.113.0", time.Hour)
	require.NoError(t, err, "续期不多占名额")
	_, err = s.AddOrgDeny(ctx, ipacl.OrgTarget("merchant", 2), ipacl.DenyInput{CIDR: "203.0.113.200"}, 1, "")
	require.NoError(t, err)
	_, n, err = s.ListOrgDeny(ctx, target, ipacl.DenyFilter{}, 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, 100, n)
}

func TestOrgDeny_178_StoredLimitIncludesExpired(t *testing.T) {
	ctx, gdb, clock := setup(t)
	s := newSvc(t, ctx, gdb, clock)
	target := ipacl.OrgTarget("merchant", 1)
	// 使用固定数量的过期夹具，验证存储边界；不运行流量或资源耗尽测试。
	rows := make([]map[string]any, ipacl.MaxOrgDenyStoredRules)
	for i := range rows {
		rows[i] = map[string]any{"kind": "deny", "scope": "org", "portal": "merchant", "org_id": 1, "user_id": 0,
			"cidr": fmt.Sprintf("10.1.%d.%d/32", i/256, i%256), "expires_at": time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), "status": 1,
			"created_at": clock.Now(), "updated_at": clock.Now()}
	}
	require.NoError(t, gdb.Table("ga_ip_rule").CreateInBatches(rows, 100).Error)
	_, err := s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "192.0.2.1")
	require.Equal(t, "ipacl.denyStoredFull", fieldKey(t, err))
	renewed, err := s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "10.1.0.0"}, 1, "192.0.2.1")
	require.NoError(t, err)
	_, count, err := s.ListOrgDeny(ctx, target, ipacl.DenyFilter{IncludeExpired: true}, 1, 1)
	require.NoError(t, err)
	require.EqualValues(t, ipacl.MaxOrgDenyStoredRules, count)
	_, err = s.AddOrgDeny(ctx, ipacl.OrgTarget("merchant", 2), ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "192.0.2.1")
	require.NoError(t, err)
	_, err = s.RemoveOrgDeny(ctx, target, renewed.ID)
	require.NoError(t, err)
	_, err = s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "192.0.2.1")
	require.NoError(t, err)
}

// 规范 §13.2 第 186 条（D-102）：容量按归属各自计数——全局名单数全局的，主体名单数这个主体的；
// ClearOrgDeny 只清这一个主体自己设的黑名单（含已过期的记录）。
func TestOrgDeny_186_SeparateQuotasAndClear(t *testing.T) {
	ctx, gdb, clock := setup(t)
	s := newSvc(t, ctx, gdb, clock)
	m1, m2, a1 := ipacl.OrgTarget("merchant", 1), ipacl.OrgTarget("merchant", 2), ipacl.OrgTarget("agent", 1)
	fill := func(scope, portal string, org uint64, n int, prefix string, expires any) {
		rows := make([]map[string]any, n)
		for i := range rows {
			rows[i] = map[string]any{"kind": "deny", "scope": scope, "portal": portal, "org_id": org, "user_id": 0,
				"cidr": fmt.Sprintf("%s.%d.%d/32", prefix, i/256, i%256), "expires_at": expires, "status": 1,
				"created_at": clock.Now(), "updated_at": clock.Now()}
		}
		require.NoError(t, gdb.Table("ga_ip_rule").CreateInBatches(rows, 500).Error)
	}

	// 一个主体写满自己的 100 条：它自己加不了，全局和别的主体照常
	fill("org", "merchant", 1, ipacl.MaxOrgDenyRules, "10.1", nil)
	_, err := s.AddOrgDeny(ctx, m1, ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "192.0.2.1")
	require.Equal(t, "ipacl.denyFull", fieldKey(t, err))
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "192.0.2.1")
	require.NoError(t, err)
	_, err = s.AddOrgDeny(ctx, m2, ipacl.DenyInput{CIDR: "198.51.100.1"}, 1, "192.0.2.1")
	require.NoError(t, err)

	// 全局名单只差一条到上限、主体的条目另有一百多条：全局还能加最后一条（主体的不占全局额度），再加才满
	fill("global", "", 0, ipacl.MaxDenyRules-2, "10.2", nil)
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "198.51.100.2"}, 1, "192.0.2.1")
	require.NoError(t, err, "主体的条目不占全局额度")
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "198.51.100.3"}, 1, "192.0.2.1")
	require.Equal(t, "ipacl.denyFull", fieldKey(t, err))
	// 全局满了：主体照常能加自己的
	_, err = s.AddOrgDeny(ctx, a1, ipacl.DenyInput{CIDR: "198.51.100.3"}, 1, "192.0.2.1")
	require.NoError(t, err, "全局名单满了不影响主体的名单")
	_, err = s.AddOrgDeny(ctx, m2, ipacl.DenyInput{CIDR: "198.51.100.3"}, 1, "192.0.2.1")
	require.NoError(t, err)

	// 清空商户一：含已过期的记录；别的主体、另一个端同 ID 的主体、全局名单、商户一的白名单都不动
	fill("org", "merchant", 1, 5, "10.3", time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	_, err = s.SetAllow(ctx, m1, []ipacl.Entry{{CIDR: "192.0.2.0/24"}, {CIDR: "10.1.0.0/16"}}, 1, "192.0.2.1")
	require.NoError(t, err)
	require.False(t, s.AccountAllows(ctx, "merchant", 1, 1, "10.1.0.7"), "在白名单里，但被自己的黑名单挡住")
	count := func(where string, args ...any) int64 {
		var n int64
		require.NoError(t, gdb.Table("ga_ip_rule").Where(where, args...).Count(&n).Error)
		return n
	}
	globals := count("kind = 'deny' AND scope = 'global'")
	_, err = s.ClearOrgDeny(ctx, ipacl.OrgTarget("merchant", 0))
	require.Error(t, err, "没有主体的归属不接受")
	_, err = s.ClearOrgDeny(ctx, ipacl.PortalTarget("merchant"))
	require.Error(t, err)
	n, err := s.ClearOrgDeny(ctx, m1)
	require.NoError(t, err)
	require.EqualValues(t, ipacl.MaxOrgDenyRules+5, n)
	require.Zero(t, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = 1"))
	require.EqualValues(t, 2, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = 2"))
	require.EqualValues(t, 1, count("kind = 'deny' AND scope = 'org' AND portal = 'agent' AND org_id = 1"))
	require.Equal(t, globals, count("kind = 'deny' AND scope = 'global'"))
	require.EqualValues(t, 2, count("kind = 'allow' AND scope = 'org' AND portal = 'merchant' AND org_id = 1"))
	// 清空立即生效（本实例）
	require.True(t, s.AccountAllows(ctx, "merchant", 1, 1, "10.1.0.7"))
	require.False(t, s.AccountAllows(ctx, "merchant", 2, 1, "198.51.100.1"), "别的主体的黑名单照旧")
	// 回滚的清空不生效
	sentinel := errors.New("rollback")
	err = db.Tx(ctx, func(tx context.Context) error {
		if _, err := s.ClearOrgDeny(tx, m2); err != nil {
			return err
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)
	require.EqualValues(t, 2, count("kind = 'deny' AND scope = 'org' AND portal = 'merchant' AND org_id = 2"))
}
