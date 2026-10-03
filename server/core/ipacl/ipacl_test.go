package ipacl_test

// D-062：IP 黑名单与白名单的匹配、到期、边界、防锁死、跨程序生效。

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/goalladmin/goalladmin/server/migrations"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func setup(t *testing.T) (context.Context, *gorm.DB, *clock) {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	return ctx, gdb, &clock{t: time.Now().UTC().Truncate(time.Second)}
}

func newSvc(t *testing.T, ctx context.Context, gdb *gorm.DB, c *clock) *ipacl.Service {
	t.Helper()
	s := ipacl.New(ipacl.Options{DB: gdb, Now: c.Now})
	require.NoError(t, s.Load(ctx))
	return s
}

// fieldKey 取校验错误里第一个字段的翻译键。
func fieldKey(t *testing.T, err error) string {
	t.Helper()
	var he *httpx.Error
	require.True(t, errors.As(err, &he), "不是 httpx 错误: %v", err)
	require.NotEmpty(t, he.Fields, "没有字段错误: %v", err)
	return he.Fields[0].Key
}

func TestParseCIDR(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.5":         "203.0.113.5/32",
		" 203.0.113.77/24 ":   "203.0.113.0/24",
		"::ffff:192.0.2.1":    "192.0.2.1/32",
		"::ffff:10.0.0.0/104": "10.0.0.0/8",
		"2001:db8::1":         "2001:db8::1/128",
		"2001:db8::1/64":      "2001:db8::/64",
	} {
		p, err := ipacl.ParseCIDR(in)
		require.NoError(t, err, in)
		require.Equal(t, want, p.String(), in)
	}
	for _, bad := range []string{"", "abc", "1.2.3.4/33", "1.2.3", "fe80::1%eth0", "300.1.1.1", "::ffff:1.2.3.4/90"} {
		_, err := ipacl.ParseCIDR(bad)
		require.Error(t, err, bad)
	}
}

func TestDeny_MatchExpiryAndBounds(t *testing.T) {
	ctx, gdb, c := setup(t)
	s := newSvc(t, ctx, gdb, c)
	_, err := s.AddDeny(ctx, ipacl.DenyInput{CIDR: "203.0.113.5", ExpiresIn: time.Hour, Remark: "刷登录"}, 1, "192.0.2.10")
	require.NoError(t, err)
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "198.51.100.0/24"}, 1, "192.0.2.10")
	require.NoError(t, err)

	require.True(t, s.Blocked(ctx, "203.0.113.5"))
	require.True(t, s.Blocked(ctx, "::ffff:203.0.113.5"), "IPv4 映射地址按 IPv4 算")
	require.True(t, s.Blocked(ctx, "198.51.100.200"))
	require.False(t, s.Blocked(ctx, "203.0.113.6"))
	require.False(t, s.Blocked(ctx, "not-an-ip"))

	// 到期后自动失效，不用重读
	c.Advance(time.Hour)
	require.False(t, s.Blocked(ctx, "203.0.113.5"), "到期的黑名单不再生效")
	require.True(t, s.Blocked(ctx, "198.51.100.1"), "永久的照常生效")

	// 已过期的默认不列出；同一网段再封一次是更新到期时间
	list, total, err := s.ListDeny(ctx, ipacl.DenyFilter{}, 1, 20)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, "198.51.100.0/24", list[0].CIDR)
	r, err := s.AddDeny(ctx, ipacl.DenyInput{CIDR: "203.0.113.5/32", ExpiresIn: time.Hour}, 1, "")
	require.NoError(t, err)
	require.Equal(t, "203.0.113.5/32", r.CIDR)
	require.True(t, s.Blocked(ctx, "203.0.113.5"))
	var rows int64
	require.NoError(t, gdb.Table("ga_ip_rule").Where("cidr = ?", "203.0.113.5/32").Count(&rows).Error)
	require.EqualValues(t, 1, rows, "同一网段只有一行")

	// 边界：太宽的网段、过去的到期时间、坏备注、无法解析
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "10.0.0.0/7"}, 1, "")
	require.Equal(t, "ipacl.denyTooBroad", fieldKey(t, err))
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "2001::/15"}, 1, "")
	require.Equal(t, "ipacl.denyTooBroad", fieldKey(t, err))
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "10.0.0.0/8"}, 1, "")
	require.NoError(t, err)
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "192.0.2.99", ExpiresIn: -time.Minute}, 1, "")
	require.Equal(t, "ipacl.expiresIn", fieldKey(t, err))
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "192.0.2.99", Remark: "a\u200bb"}, 1, "")
	require.Equal(t, "ipacl.remarkChars", fieldKey(t, err))
	_, err = s.AddDeny(ctx, ipacl.DenyInput{CIDR: "nonsense"}, 1, "")
	require.Equal(t, "ipacl.cidrInvalid", fieldKey(t, err))

	// 删除：只删黑名单；不存在的是 404
	_, err = s.RemoveDeny(ctx, list[0].ID)
	require.NoError(t, err)
	require.False(t, s.Blocked(ctx, "198.51.100.1"), "删掉立即生效")
	_, err = s.RemoveDeny(ctx, list[0].ID)
	require.ErrorIs(t, err, httpx.ErrNotFound)
}

// 防锁死：黑名单不能盖住操作人当前的地址；对操作人自己生效的白名单必须包含它。
func TestSelfLockoutGuards(t *testing.T) {
	ctx, gdb, c := setup(t)
	s := newSvc(t, ctx, gdb, c)
	_, err := s.AddDeny(ctx, ipacl.DenyInput{CIDR: "192.0.2.0/24"}, 1, "192.0.2.10")
	require.Equal(t, "ipacl.selfLockout", fieldKey(t, err))
	require.False(t, s.Blocked(ctx, "192.0.2.10"))

	_, err = s.SetAllow(ctx, ipacl.PortalTarget("platform"), []ipacl.Entry{{CIDR: "10.0.0.0/8"}}, 1, "192.0.2.10")
	require.Equal(t, "ipacl.selfLockout", fieldKey(t, err))
	require.True(t, s.PortalAllows(ctx, "platform", "192.0.2.10"), "被拒的写入不生效")

	_, err = s.SetAllow(ctx, ipacl.PortalTarget("platform"), []ipacl.Entry{{CIDR: "10.0.0.0/8"}, {CIDR: "192.0.2.10"}}, 1, "192.0.2.10")
	require.NoError(t, err)
	// 清空（不再限制）不需要包含自己
	_, err = s.SetAllow(ctx, ipacl.PortalTarget("platform"), nil, 1, "203.0.113.1")
	require.NoError(t, err)
	require.True(t, s.PortalAllows(ctx, "platform", "203.0.113.1"))
}

func TestAllow_PortalOrgUser(t *testing.T) {
	ctx, gdb, c := setup(t)
	s := newSvc(t, ctx, gdb, c)

	// 端白名单：空 = 不限制；设了只放行名单里的；地址解析不了时不放行
	require.True(t, s.PortalAllows(ctx, "platform", "203.0.113.1"))
	rules, err := s.SetAllow(ctx, ipacl.PortalTarget("platform"), []ipacl.Entry{{CIDR: "10.0.0.0/8", Remark: "办公网"}, {CIDR: "10.1.2.3"}, {CIDR: "10.0.0.0/8"}}, 1, "10.9.9.9")
	require.NoError(t, err)
	require.Len(t, rules, 2, "重复的网段合并")
	require.True(t, s.PortalAllows(ctx, "platform", "10.200.0.1"))
	require.False(t, s.PortalAllows(ctx, "platform", "203.0.113.1"))
	require.False(t, s.PortalAllows(ctx, "platform", ""))
	require.True(t, s.PortalAllows(ctx, "merchant", "203.0.113.1"), "别的端不受影响")

	// 主体白名单和账号白名单：都设了就都要满足
	_, err = s.SetAllow(ctx, ipacl.OrgTarget("merchant", 7), []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 1, "")
	require.NoError(t, err)
	require.True(t, s.AccountAllows(ctx, "merchant", 7, 70, "198.51.100.5"))
	require.False(t, s.AccountAllows(ctx, "merchant", 7, 70, "203.0.113.5"))
	require.True(t, s.AccountAllows(ctx, "merchant", 8, 80, "203.0.113.5"), "别的主体不受影响")
	_, err = s.SetAllow(ctx, ipacl.UserTarget("merchant", 7, 70), []ipacl.Entry{{CIDR: "198.51.100.5"}}, 1, "")
	require.NoError(t, err)
	require.True(t, s.AccountAllows(ctx, "merchant", 7, 70, "198.51.100.5"))
	require.False(t, s.AccountAllows(ctx, "merchant", 7, 70, "198.51.100.6"), "在主体名单里、不在账号名单里")
	require.True(t, s.AccountAllows(ctx, "merchant", 7, 71, "198.51.100.6"), "同主体的别的账号只受主体名单约束")
	require.True(t, s.AccountAllows(ctx, "platform", 0, 70, "203.0.113.5"), "端不同，用户 ID 相同也不相干")

	// 容量和清空
	many := make([]ipacl.Entry, ipacl.MaxAllowEntries+1)
	for i := range many {
		many[i] = ipacl.Entry{CIDR: fmt.Sprintf("10.0.%d.%d", i/250, i%250+1)}
	}
	_, err = s.SetAllow(ctx, ipacl.OrgTarget("merchant", 7), many, 1, "")
	require.Equal(t, "ipacl.allowTooMany", fieldKey(t, err))
	n, err := s.ClearAllow(ctx, ipacl.OrgTarget("merchant", 7))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	require.True(t, s.AccountAllows(ctx, "merchant", 7, 71, "203.0.113.5"))

	// 归属不合法
	_, err = s.SetAllow(ctx, ipacl.Target{Scope: ipacl.ScopeOrg, Portal: "merchant"}, nil, 1, "")
	require.Error(t, err)
}

// 一个程序改了名单，另一个程序最晚一个刷新间隔后跟上；之前沿用自己的快照，不每个请求都查库。
func TestChangesReachOtherProcesses(t *testing.T) {
	ctx, gdb, c := setup(t)
	platform := newSvc(t, ctx, gdb, c)
	merchant := newSvc(t, ctx, gdb, c)

	_, err := platform.AddDeny(ctx, ipacl.DenyInput{CIDR: "203.0.113.7"}, 1, "")
	require.NoError(t, err)
	require.True(t, platform.Blocked(ctx, "203.0.113.7"), "本程序提交后立即生效")
	require.False(t, merchant.Blocked(ctx, "203.0.113.7"), "别的程序在刷新间隔内沿用旧快照")
	c.Advance(ipacl.DefaultRefresh)
	require.True(t, merchant.Blocked(ctx, "203.0.113.7"), "过了刷新间隔就跟上")

	_, err = merchant.SetAllow(ctx, ipacl.OrgTarget("merchant", 3), []ipacl.Entry{{CIDR: "198.51.100.0/24"}}, 9, "")
	require.NoError(t, err)
	c.Advance(ipacl.DefaultRefresh)
	require.False(t, platform.AccountAllows(ctx, "merchant", 3, 30, "203.0.113.1"))
}
