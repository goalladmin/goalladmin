package ratelimit

import (
	"context"
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

func TestWindow_Undo(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	w := New(2, time.Minute, 100, func() time.Time { return now })
	require.True(t, w.Allow("a"))
	require.True(t, w.Allow("a"))
	require.False(t, w.Allow("a"))
	w.Undo("a")
	require.True(t, w.Allow("a"), "退回一次之后可以再占一次")
	require.False(t, w.Allow("a"), "只退回了一次")
	w.Undo("b") // 没有记录的键：什么都不做
	w.Undo("a")
	w.Undo("a")
	w.Undo("a") // 退到 0 为止，不会变成负数
	require.True(t, w.Allow("a"))
	require.True(t, w.Allow("a"))
	require.False(t, w.Allow("a"), "上限没有因为多退而变大")
	require.True(t, w.Allow("b"))
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

// Enter：有位置直接进；满了在时限内等到让出来的位置；等够了、ctx 取消了返回 false；不等（wait 为 0）时和 TryEnter 一样。
func TestGate_EnterWaits(t *testing.T) {
	ctx := context.Background()
	g := NewGate(1)
	require.True(t, g.Enter(ctx, time.Second), "有位置：直接进")
	require.False(t, g.Enter(ctx, 0), "不等")

	start := time.Now()
	require.False(t, g.Enter(ctx, 80*time.Millisecond), "等够了也没有位置")
	require.GreaterOrEqual(t, time.Since(start), 70*time.Millisecond, "确实等了")

	go func() { time.Sleep(50 * time.Millisecond); g.Leave() }()
	start = time.Now()
	require.True(t, g.Enter(ctx, 5*time.Second), "等到了让出来的位置")
	require.Less(t, time.Since(start), 2*time.Second, "位置一让出来就进，不用等满")

	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start = time.Now()
	require.False(t, g.Enter(cctx, 5*time.Second), "请求被取消")
	require.Less(t, time.Since(start), 2*time.Second)
	require.Zero(t, g.waiting.Load(), "不等了的都从等待人数里减掉")
	g.Leave()
	require.True(t, g.TryEnter(), "没等到的没有占位置")
}

// 在等的人等到让出来的位置之后确实占着它：别人进不来，直到它让出。
func TestGate_WaiterTakesTheFreedSlot(t *testing.T) {
	g := NewGate(1)
	require.True(t, g.TryEnter())
	got := make(chan bool, 1)
	go func() { got <- g.Enter(context.Background(), 10*time.Second) }()
	require.Eventually(t, func() bool { return g.waiting.Load() == 1 }, 5*time.Second, time.Millisecond)
	g.Leave()
	require.True(t, <-got, "等到了")
	require.False(t, g.TryEnter(), "位置在刚才等的那个手里")
	require.Zero(t, g.waiting.Load())
	g.Leave()
	require.True(t, g.TryEnter())
}

// 同时在等的最多是位置数的 4 倍：再多的不等，直接返回 false。
func TestGate_WaitersAreBounded(t *testing.T) {
	g := NewGate(2)
	require.True(t, g.TryEnter())
	require.True(t, g.TryEnter())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool, 16)
	for range maxWaitersPerSlot * 2 {
		go func() { done <- g.Enter(ctx, 10*time.Second) }()
	}
	require.Eventually(t, func() bool { return int(g.waiting.Load()) == maxWaitersPerSlot*2 }, time.Second, time.Millisecond)
	start := time.Now()
	require.False(t, g.Enter(context.Background(), 10*time.Second), "等的人满了")
	require.Less(t, time.Since(start), time.Second, "没有等")
	cancel()
	for range maxWaitersPerSlot * 2 {
		require.False(t, <-done)
	}
	require.Zero(t, g.waiting.Load())
}

// 规范 §13.2 第 157 条（D-071）：位置分两类用——后一类最多占一半、满了直接拒绝；前一类可以用全部、满了等一小会儿；
// 全部位置满了时后一类即使自己的那一半还有余量也进不去；让出位置的函数多调几次只让出一次。
func TestBudget_157_FrontAndBackground(t *testing.T) {
	ctx := context.Background()
	const wait = 300 * time.Millisecond
	b := NewBudget(4, wait)
	l1, ok := b.EnterBackground()
	require.True(t, ok)
	l2, ok := b.EnterBackground()
	require.True(t, ok)
	_, ok = b.EnterBackground()
	require.False(t, ok, "后一类最多占一半")
	f1, ok := b.EnterFront(ctx)
	require.True(t, ok, "另一半留给前一类")
	f2, ok := b.TryEnterFront()
	require.True(t, ok)
	start := time.Now()
	_, ok = b.EnterFront(ctx)
	require.False(t, ok, "全部满了：前一类等一小会儿，等不到才拒绝")
	require.GreaterOrEqual(t, time.Since(start), wait*9/10)
	all, back := b.InUse()
	require.Equal(t, []int{4, 2}, []int{all, back})
	_, ok = b.TryEnterFront()
	require.False(t, ok)

	l1()
	l1() // 多调一次不会多让出一个位置
	f3, ok := b.TryEnterFront()
	require.True(t, ok, "后一类让出来的位置前一类可以用")
	_, ok = b.TryEnterFront()
	require.False(t, ok, "只让出了一个")
	start = time.Now()
	_, ok = b.EnterBackground()
	require.False(t, ok, "后一类自己的一半有余量，但全部位置满了：进不去，也不等")
	require.Less(t, time.Since(start), wait/2)
	l3, ok := func() (func(), bool) { f1(); return b.EnterBackground() }()
	require.True(t, ok)
	_, ok = b.EnterBackground()
	require.False(t, ok, "刚才没进去的那次没有占掉后一类的位置：现在正好又是 2 个")

	for _, leave := range []func(){l2, l3, f2, f3} {
		leave()
	}
	all, back = b.InUse()
	require.Equal(t, []int{0, 0}, []int{all, back}, "都让出来了")
	for range 4 {
		_, ok := b.TryEnterFront()
		require.True(t, ok, "前一类可以用全部 4 个")
	}

	// 一半向下取整、至少 1 个；总数不到 1 按 1
	for n, back := range map[int]int{1: 1, 2: 1, 3: 1, 5: 2, 8: 4, 0: 1, -3: 1} {
		b := NewBudget(n, 0)
		for i := range back {
			_, ok := b.EnterBackground()
			require.True(t, ok, "n=%d 第 %d 个", n, i)
		}
		_, ok := b.EnterBackground()
		require.False(t, ok, "n=%d", n)
	}
}

func TestKeyedGate(t *testing.T) {
	g := NewKeyedGate(2)
	require.True(t, g.TryEnter("a"))
	require.True(t, g.TryEnter("a"))
	require.False(t, g.TryEnter("a"), "这个键满了")
	require.True(t, g.TryEnter("b"), "键之间互不影响")
	g.Leave("a")
	require.True(t, g.TryEnter("a"), "让出一个之后可以再进")
	require.False(t, g.TryEnter("a"))
	g.Leave("a")
	g.Leave("a")
	g.Leave("b")
	require.Empty(t, g.m, "没有在用的键不留在表里")
	require.True(t, g.TryEnter("a"))
	require.True(t, g.TryEnter("a"))
	require.False(t, g.TryEnter("a"), "清掉之后上限不变")
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
