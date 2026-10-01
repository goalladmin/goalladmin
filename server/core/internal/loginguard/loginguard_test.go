package loginguard

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/portal"
)

func newGuard() (*Guard, *time.Time) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	g := New(portal.DefaultLoginPolicy(), func() time.Time { return now })
	return g, &now
}

func TestCaptchaAfterThreeFailures(t *testing.T) {
	g, _ := newGuard()
	for i := 0; i < 3; i++ {
		require.False(t, g.Check("alice", "1.1.1.1").CaptchaRequired)
		g.Admit("alice", "1.1.1.1").Fail()
	}
	require.True(t, g.Check("alice", "1.1.1.1").CaptchaRequired)
	require.False(t, g.Check("alice", "2.2.2.2").CaptchaRequired, "按账号+IP 计数")
	g.Admit("alice", "1.1.1.1").Succeed()
	require.False(t, g.Check("alice", "1.1.1.1").CaptchaRequired)
}

func TestPairLockAndExpiry(t *testing.T) {
	g, now := newGuard()
	for i := 0; i < 10; i++ {
		g.Admit("alice", "1.1.1.1").Fail()
	}
	d := g.Check("alice", "1.1.1.1")
	require.True(t, d.Locked)
	require.Equal(t, now.Add(15*time.Minute), d.LockedUntil)
	require.False(t, g.Check("alice", "2.2.2.2").Locked, "另一个 IP 不受影响")
	*now = now.Add(16 * time.Minute)
	require.False(t, g.Check("alice", "1.1.1.1").Locked)
}

func TestAccountLockAcrossIPs(t *testing.T) {
	g, _ := newGuard()
	for i := 0; i < 50; i++ {
		g.Admit("alice", fmt.Sprintf("10.0.0.%d", i)).Fail()
	}
	require.True(t, g.Check("alice", "192.168.1.1").Locked, "50 次跨 IP 失败后锁账号")
	g.Unlock("alice")
	require.False(t, g.Check("alice", "192.168.1.1").Locked)
}

func TestRateLimit(t *testing.T) {
	g, now := newGuard()
	for i := 0; i < 20; i++ {
		require.False(t, g.Check(fmt.Sprintf("u%d", i), "1.1.1.1").RateLimited)
	}
	require.True(t, g.Check("u99", "1.1.1.1").RateLimited, "同 IP 第 21 次")
	for i := 0; i < 10; i++ {
		require.False(t, g.Check("bob", fmt.Sprintf("10.0.0.%d", i)).RateLimited)
	}
	require.True(t, g.Check("bob", "10.0.0.99").RateLimited, "同账号第 11 次")
	*now = now.Add(61 * time.Second)
	require.False(t, g.Check("u99", "1.1.1.1").RateLimited)
	require.False(t, g.Check("bob", "10.0.0.99").RateLimited)
}

func TestFailuresExpireWithWindow(t *testing.T) {
	g, now := newGuard()
	for i := 0; i < 3; i++ {
		g.Admit("alice", "1.1.1.1").Fail()
	}
	*now = now.Add(16 * time.Minute)
	require.False(t, g.Check("alice", "1.1.1.1").CaptchaRequired)
}

// 规范 §13.2 第 102 条：登录防护的状态有键数上限（D-056）。表满时新来源按限流拒绝，已有的来源照常计，
// 失败次数和锁定都不被逐出；过期数据清掉以后又能接纳新来源。
func TestGuard_102_BoundedKeys(t *testing.T) {
	g, now := newGuard()
	g.maxKeys = 80
	// 用不同 IP、不同账号把表填满：每次失败的登录占四个键（两个限流键、两条失败记录）
	for i := 0; i < 20; i++ {
		d := g.Check(fmt.Sprintf("u%d", i), fmt.Sprintf("10.0.0.%d", i))
		require.False(t, d.RateLimited, i)
		d.Attempt.Fail()
	}
	require.Equal(t, 80, g.keysLocked())

	// 新 IP 或新账号：拒绝，而且不新建键
	require.True(t, g.Check("newbie", "10.9.9.9").RateLimited)
	require.True(t, g.Check("u0", "10.9.9.9").RateLimited, "账号已有、IP 是新的")
	require.True(t, g.Check("newbie", "10.0.0.0").RateLimited, "IP 已有、账号是新的")
	require.Equal(t, 80, g.keysLocked())

	// 已有的来源照常；它的失败照常记，达到阈值照常锁定
	for i := 0; i < 9; i++ {
		d := g.Check("u1", "10.0.0.1")
		require.False(t, d.RateLimited, i)
		require.False(t, d.Locked, i)
		d.Attempt.Fail()
		*now = now.Add(7 * time.Second) // 账号每分钟 10 次，放慢一点
	}
	require.True(t, g.Check("u1", "10.0.0.1").Locked, "表满时失败次数和锁定不被逐出")
	require.Equal(t, 80, g.keysLocked())

	// 表满时的提前清理有间隔：清过一次之后不到 forcedGCEvery 不再清，不会每个请求都遍历全表
	*now = now.Add(time.Minute)
	g.lastGC = now.Add(-forcedGCEvery + time.Second)
	require.True(t, g.Check("newbie", "10.9.9.9").RateLimited, "距上次清理不到 forcedGCEvery")
	*now = now.Add(time.Second)
	require.False(t, g.Check("newbie", "10.9.9.9").RateLimited, "过期的限流键清掉后接纳新来源")
	require.True(t, g.Check("u1", "10.0.0.1").Locked, "锁定还在")
}

// 规范 §13.2 第 103 条：键数上限是硬上限（D-057）。一次尝试会用到的键在准入时全部建好，记失败、加锁不再新建键；
// 限流键都在、只缺失败记录的尝试同样要放得下才接纳；锁屏解锁的失败走同一套准入。
func TestGuard_103_EveryNewKeyIsAdmitted(t *testing.T) {
	g, now := newGuard()
	g.maxKeys = 8
	a := g.Check("a", "1.1.1.1")
	b := g.Check("b", "2.2.2.2")
	require.NotNil(t, a.Attempt)
	require.NotNil(t, b.Attempt)
	a.Attempt.Fail()
	b.Attempt.Fail()
	require.Equal(t, 8, g.keysLocked())

	// IP 2.2.2.2 和账号 a 的限流键都在，但"a@2.2.2.2"的失败记录没有：放不下，在核对密码之前拒绝
	d := g.Check("a", "2.2.2.2")
	require.True(t, d.RateLimited)
	require.Nil(t, d.Attempt)
	require.Nil(t, g.Admit("a", "2.2.2.2"), "解锁同样要放得下")
	require.Equal(t, 8, g.keysLocked())
	// 已有的组合照常
	d = g.Check("a", "1.1.1.1")
	require.NotNil(t, d.Attempt)
	d.Attempt.Fail()
	at := g.Admit("b", "2.2.2.2")
	require.NotNil(t, at)
	at.Fail()
	require.Equal(t, 8, g.keysLocked())

	// 在途尝试钉住的记录不会被清理和解锁删掉，结束时记失败不会新建键
	g.maxKeys = 12
	d = g.Check("c", "3.3.3.3")
	require.NotNil(t, d.Attempt)
	require.Equal(t, 12, g.keysLocked())
	*now = now.Add(time.Hour)
	g.gcLocked(*now, false)
	g.Unlock("c")
	require.Equal(t, 2, g.keysLocked(), "限流键和失败记录都过期了，只剩在途的两条")
	d.Attempt.Fail()
	d.Attempt.Fail() // 第二次不再有作用
	require.Equal(t, 2, g.keysLocked())
	d.Attempt.Done()

	// 随机交错：任何时刻键数都不超过上限，结束后没有钉住的记录
	g2, now2 := newGuard()
	g2.maxKeys = 30
	var open []*Attempt
	for i := 0; i < 5000; i++ {
		u, ip := fmt.Sprintf("u%d", i*7%13), fmt.Sprintf("10.0.%d.%d", i%3, i*5%11)
		switch i % 5 {
		case 0, 1:
			if d := g2.Check(u, ip); d.Attempt != nil {
				open = append(open, d.Attempt)
			}
		case 2:
			if at := g2.Admit(u, ip); at != nil {
				open = append(open, at)
			}
		case 3:
			if len(open) > 0 {
				open[0].Fail()
				open = open[1:]
			}
		case 4:
			if len(open) > 0 {
				j := len(open) - 1
				if i%2 == 0 {
					open[j].Succeed()
				} else {
					open[j].Done()
				}
				open = open[:j]
			}
			*now2 = now2.Add(3 * time.Second)
		}
		require.LessOrEqual(t, g2.keysLocked(), g2.maxKeys, i)
	}
	for _, at := range open {
		at.Done()
	}
	for k, r := range g2.state {
		require.Zero(t, r.inflight, k)
	}
}

// 规范 §13.2 第 110 条（D-058）：IPv6 的限流、"账号 + IP"的失败计数都按 /64 网段算，换网段内的地址不是新配额。
func TestGuard_110_IPv6CountedPer64(t *testing.T) {
	g, _ := newGuard()
	for i := 0; i < 20; i++ {
		require.False(t, g.Check(fmt.Sprintf("u%d", i), fmt.Sprintf("2001:db8:1:2::%x", i+1)).RateLimited, i)
	}
	require.True(t, g.Check("u99", "2001:db8:1:2:abcd::1").RateLimited, "同一个 /64 的第 21 次")
	require.False(t, g.Check("u99", "2001:db8:1:3::1").RateLimited, "另一个 /64")

	for i := 0; i < 3; i++ {
		g.Admit("bob", fmt.Sprintf("2001:db8:9:9::%x", i+1)).Fail()
	}
	require.True(t, g.Check("bob", "2001:db8:9:9::ffff").CaptchaRequired, "换地址不重置"+"账号 + 网段"+"的失败次数")
}
