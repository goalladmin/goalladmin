package ratelimit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWindow_LimitResetAndExpiry(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w := New(3, time.Minute, 100, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		require.True(t, w.Allow("a"))
	}
	require.False(t, w.Allow("a"), "窗口内第 4 次拒绝")
	require.True(t, w.Allow("b"), "键之间互不影响")
	w.Reset("a")
	require.True(t, w.Allow("a"), "重置之后重新计")
	require.True(t, w.Allow("a"))
	require.True(t, w.Allow("a"))
	require.False(t, w.Allow("a"))
	now = now.Add(time.Minute)
	require.True(t, w.Allow("a"), "窗口过了重新计")
}

func TestWindow_MaxKeys(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	w := New(1, time.Minute, 2, func() time.Time { return now })
	require.True(t, w.Allow("a"))
	require.True(t, w.Allow("b"))
	require.False(t, w.Allow("c"), "键满了拒绝新键")
	require.False(t, w.Allow("a"), "已有的键照常计数")
	now = now.Add(time.Minute)
	require.True(t, w.Allow("c"), "过期的键清掉之后新键可以进来")
}

func TestGate(t *testing.T) {
	g := NewGate(2)
	require.True(t, g.TryEnter())
	require.True(t, g.TryEnter())
	require.False(t, g.TryEnter())
	g.Leave()
	require.True(t, g.TryEnter())
}

// 规范 §13.2 第 110 条（D-058）：IPv6 按 /64 网段限流，IPv4 和 IPv4 映射地址按单个地址。
func TestIPKey_110(t *testing.T) {
	require.Equal(t, "203.0.113.7", IPKey("203.0.113.7"))
	require.Equal(t, "203.0.113.7", IPKey("::ffff:203.0.113.7"))
	require.Equal(t, IPKey("2001:db8:1:2::1"), IPKey("2001:db8:1:2:ffff:ffff:ffff:ffff"), "同一个 /64")
	require.NotEqual(t, IPKey("2001:db8:1:2::1"), IPKey("2001:db8:1:3::1"), "不同的 /64")
	require.Equal(t, IPKey("fe80::1%eth0"), IPKey("fe80::2"))
	require.Equal(t, "not-an-ip", IPKey("not-an-ip"))
}
