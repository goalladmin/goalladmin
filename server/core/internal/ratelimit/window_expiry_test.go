package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/stretchr/testify/require"
)

func windowAdmission(w *Window, reserve bool) func(string) bool {
	if reserve {
		return func(key string) bool {
			_, ok := w.Reserve(context.Background(), key)
			return ok
		}
	}
	return w.Allow
}

// 规范 §13.2 第 171 条：满表分批清理，到期立即可用，活跃计数不能被逐出。
func TestWindow_171_BoundedLocalExpiry(t *testing.T) {
	for _, reserve := range []bool{false, true} {
		t.Run(fmt.Sprintf("reserve=%t", reserve), func(t *testing.T) {
			clock := newWindowClock()
			const oldCount = windowGC * 3
			w := New(2, time.Minute, oldCount+1, clock.now)
			admit := windowAdmission(w, reserve)
			for i := range oldCount {
				require.True(t, admit(fmt.Sprintf("old:%d", i)))
			}
			clock.add(30 * time.Second)
			require.True(t, admit("active"))
			clock.add(30*time.Second - time.Millisecond)
			require.False(t, admit("early"))
			require.Len(t, w.m, oldCount+1)
			clock.add(time.Millisecond)
			for batch := range 3 {
				require.True(t, admit(fmt.Sprintf("batch:%d", batch)))
				require.Len(t, w.m, oldCount+2-windowGC, "每次只清一批")
				for i := range windowGC - 1 {
					require.True(t, admit(fmt.Sprintf("fresh:%d:%d", batch, i)))
				}
				require.Len(t, w.m, oldCount+1)
			}
			require.False(t, admit("full"))
			require.Len(t, w.expiry, len(w.m))
			require.True(t, admit("active"), "已有键仍可使用剩余次数")
			require.False(t, admit("active"), "原次数必须保留")
			w.Reset("active")
			require.True(t, admit("replacement"), "重置立即释放容量")
		})
	}
}

func TestWindow_171_ExpiredTicketsCannotChangeReusedCapacity(t *testing.T) {
	clock := newWindowClock()
	w := New(1, time.Minute, 2, clock.now)
	oldA := reserveWindow(t, w, "a")
	oldB := reserveWindow(t, w, "b")
	clock.add(time.Minute)
	reserveWindow(t, w, "c") // 容量清理删除旧 a、b。
	reserveWindow(t, w, "a")
	oldA.Reset()
	oldB.Undo()
	oldA.Undo()
	oldB.Reset()
	windowDenied(t, w, "a")
	windowDenied(t, w, "c")
	windowDenied(t, w, "b")
}

func TestWindow_171_ParallelLocalCapacity(t *testing.T) {
	clock := newWindowClock()
	w := New(3, time.Minute, 32, clock.now)
	for range 3 {
		var accepted atomic.Int64
		var wg sync.WaitGroup
		for i := range 512 {
			wg.Go(func() {
				if windowAdmission(w, i/64%2 == 0)(fmt.Sprintf("key:%d", i%64)) {
					accepted.Add(1)
				}
			})
		}
		wg.Wait()
		require.EqualValues(t, 32*3, accepted.Load())
		require.Len(t, w.m, 32)
		require.Len(t, w.expiry, 32)
		for _, e := range w.m {
			require.Equal(t, 3, e.n)
		}
		clock.add(time.Minute)
	}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			w.Reset(fmt.Sprintf("key:%d", i))
			w.Reset(fmt.Sprintf("key:%d", i))
		})
	}
	wg.Wait()
	require.Empty(t, w.m)
	require.Empty(t, w.expiry)
}

func TestWindow_171_ResetDoesNotAccumulateExpiryEntries(t *testing.T) {
	clock := newWindowClock()
	w := New(1, time.Minute, 32, clock.now)
	tickets := make([]*Reservation, 32)
	for i := range tickets {
		tickets[i] = reserveWindow(t, w, fmt.Sprintf("key:%d", i))
		clock.add(time.Millisecond)
	}
	for i := range 1_024 {
		n := i * 17 % len(tickets)
		key := fmt.Sprintf("key:%d", n)
		old := tickets[n]
		if i%2 == 0 {
			w.Reset(key)
		} else {
			old.Reset()
		}
		tickets[n] = reserveWindow(t, w, key)
		old.Undo()
		old.Reset()
		windowDenied(t, w, key)
		require.Len(t, w.m, 32)
		require.Len(t, w.expiry, 32, "重复重置不得积累过期索引节点")
		clock.add(time.Millisecond)
	}
	clock.add(time.Minute)
	reserveWindow(t, w, "fresh")
	require.Len(t, w.m, 1)
	require.Len(t, w.expiry, 1, "不同顺序的重置不能阻挡过期回收")
	for _, ticket := range tickets {
		ticket.Reset()
	}
	windowDenied(t, w, "fresh")
}

func TestWindow_171_SharedShadowExpiry(t *testing.T) {
	client := openWindowClient(t, redisx.TestPrefix(t), nil)
	clock := newWindowClock()
	const capacity = windowGC * 2
	w := NewShared(1, time.Minute, capacity, clock.now, client, "bounded-shadow")
	for i := range capacity {
		reserveWindow(t, w, fmt.Sprintf("old:%d", i))
	}
	clock.add(time.Minute)
	reserveWindow(t, w, "fresh")
	require.True(t, client.Available())
	require.Len(t, w.m, capacity-windowGC+1)
	require.Len(t, w.expiry, len(w.m))
	windowDenied(t, w, "fresh")
}

// 模拟截止时间已到、context 的取消定时器尚未执行的瞬间。
type windowPendingDeadline struct {
	context.Context
	deadline time.Time
}

func (c windowPendingDeadline) Deadline() (time.Time, bool) { return c.deadline, true }

func TestWindow_171_ElapsedDeadlineDoesNotConsume(t *testing.T) {
	w := New(1, time.Minute, 1, nil)
	ctx := windowPendingDeadline{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	require.NoError(t, ctx.Err())
	r, ok := w.Reserve(ctx, "key")
	require.False(t, ok)
	require.Nil(t, r)
	require.Empty(t, w.m)
	require.Empty(t, w.expiry)
	reserveWindow(t, w, "key")
}

func BenchmarkWindowFull(b *testing.B) {
	for _, reserve := range []bool{false, true} {
		for _, capacity := range []int{1_000, 10_000, 100_000} {
			b.Run(fmt.Sprintf("reserve=%t/keys=%d", reserve, capacity), func(b *testing.B) {
				now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
				w := New(1, time.Minute, capacity, func() time.Time { return now })
				admit := windowAdmission(w, reserve)
				for i := range capacity {
					if !admit(fmt.Sprintf("key:%d", i)) {
						b.Fatal("initial admission denied")
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					if admit("new-key") {
						b.Fatal("full window admitted a new key")
					}
				}
			})
		}
	}
}
