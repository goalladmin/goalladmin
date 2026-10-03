package ratelimit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type windowClock struct{ millis atomic.Int64 }

func newWindowClock() *windowClock {
	c := &windowClock{}
	c.millis.Store(time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC).UnixMilli())
	return c
}
func (c *windowClock) now() time.Time      { return time.UnixMilli(c.millis.Load()) }
func (c *windowClock) add(d time.Duration) { c.millis.Add(d.Milliseconds()) }

func openWindowClient(t *testing.T, prefix string, proxy *redisx.TestProxy) *redisx.Client {
	t.Helper()
	addr := redisx.TestAddr(t)
	if proxy != nil {
		addr = proxy.Addr()
	}
	c, err := redisx.Open(redisx.Options{
		Addr: addr, KeyPrefix: prefix, ConnectWait: 2 * time.Second, ProbeEvery: 10 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	require.True(t, c.Available())
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

func rawWindowRedis(t *testing.T) *redis.Client {
	t.Helper()
	c := redis.NewClient(&redis.Options{Addr: redisx.TestAddr(t), Protocol: 2, DisableIdentity: true, MaxRetries: -1})
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

func reserveWindow(t *testing.T, w *Window, key string) *Reservation {
	t.Helper()
	r, ok := w.Reserve(context.Background(), key)
	require.True(t, ok, "key=%s", key)
	require.NotNil(t, r)
	return r
}

func windowDenied(t *testing.T, w *Window, key string) {
	t.Helper()
	r, ok := w.Reserve(context.Background(), key)
	require.False(t, ok, "key=%s", key)
	require.Nil(t, r)
}

func TestReservation_165_MemoryGenerationAndIdempotence(t *testing.T) {
	clock := newWindowClock()
	w := New(2, time.Minute, 2, clock.now)
	old := reserveWindow(t, w, "key")
	second := reserveWindow(t, w, "key")
	windowDenied(t, w, "key")
	old.Undo()
	old.Undo()
	old.Reset()
	reserveWindow(t, w, "key")
	windowDenied(t, w, "key")
	clock.add(time.Minute)
	fresh := reserveWindow(t, w, "key")
	second.Reset()
	second.Undo()
	reserveWindow(t, w, "key")
	windowDenied(t, w, "key")
	fresh.Reset()
	newer := reserveWindow(t, w, "key")
	fresh.Reset()
	reserveWindow(t, w, "key")
	windowDenied(t, w, "key")
	newer.Undo()
	reserveWindow(t, w, "key")
	windowDenied(t, w, "key")
}

func TestWindow_165_SharedAggregateFixedWindowAndOldTickets(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	a := openWindowClient(t, prefix, nil)
	b := openWindowClient(t, prefix, nil)
	clock := newWindowClock()
	wa := NewShared(2, time.Minute, 100, clock.now, a, "pwd-tries:platform")
	wb := NewShared(2, time.Minute, 100, clock.now, b, "pwd-tries:platform")
	first := reserveWindow(t, wa, "sid")
	clock.add(30 * time.Second)
	second := reserveWindow(t, wb, "sid")
	require.Equal(t, first.generation, second.generation, "先到的请求决定共享代数")
	windowDenied(t, wa, "sid")
	windowDenied(t, wb, "sid")
	clock.add(30*time.Second - time.Millisecond)
	windowDenied(t, wb, "sid")
	clock.add(time.Millisecond)
	fresh := reserveWindow(t, wb, "sid")
	require.NotEqual(t, first.generation, fresh.generation)
	first.Undo()
	second.Reset()
	other := reserveWindow(t, wa, "sid")
	windowDenied(t, wb, "sid")
	fresh.Reset()
	newer := reserveWindow(t, wb, "sid")
	require.NotEqual(t, fresh.generation, newer.generation)
	other.Reset()
	reserveWindow(t, wa, "sid")
	windowDenied(t, wa, "sid")
	newer.Undo()
	newer.Undo()
	reserveWindow(t, wb, "sid")
	windowDenied(t, wa, "sid")
}

func TestWindow_165_SharedCapacityIsolationAndHashedFields(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	c := openWindowClient(t, prefix, nil)
	clock := newWindowClock()
	a := NewShared(2, time.Minute, 2, clock.now, c, "captcha:agent")
	b := NewShared(2, time.Minute, 2, clock.now, c, "captcha:merchant")
	otherPrefix := openWindowClient(t, redisx.TestPrefix(t), nil)
	isolated := NewShared(1, time.Minute, 2, clock.now, otherPrefix, "captcha:agent")
	reserveWindow(t, a, "client-private-a")
	refund := reserveWindow(t, a, "client-private-b")
	refund.Undo()
	windowDenied(t, a, "client-private-c")
	sameNamespace := NewShared(2, time.Minute, 2, clock.now, c, "captcha:agent")
	windowDenied(t, sameNamespace, "client-private-c")
	reserveWindow(t, a, "client-private-a")
	windowDenied(t, a, "client-private-a")
	reserveWindow(t, b, "client-private-a")
	reserveWindow(t, b, "client-private-c")
	reserveWindow(t, isolated, "client-private-a")
	windowDenied(t, isolated, "client-private-a")
	raw := rawWindowRedis(t)
	ctx := context.Background()
	fields, err := raw.HGetAll(ctx, c.Key("{counts}", "windows")).Result()
	require.NoError(t, err)
	require.Len(t, fields, 4)
	for field := range fields {
		require.Len(t, field, 129)
		require.NotContains(t, field, "client-private")
		require.NotContains(t, field, "captcha")
	}
	keys, err := raw.Keys(ctx, prefix+"*").Result()
	require.NoError(t, err)
	for _, key := range keys {
		if key == prefix+"probe" { // redisx 的短连接探测键
			continue
		}
		require.Contains(t, key, "{counts}")
		require.NotContains(t, key, "client-private")
	}
	clock.add(time.Minute)
	reserveWindow(t, a, "client-private-c")
	count, err := raw.HGet(ctx, c.Key("{counts}", "window-capacity"), digest("captcha:agent")).Int()
	require.NoError(t, err)
	require.Equal(t, 1, count, "过期窗口释放本用途预算")
	reserveWindow(t, b, "client-private-c")
}

func TestWindow_165_SharedParallelAdmission(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	clock := newWindowClock()
	clients := []*redisx.Client{openWindowClient(t, prefix, nil), openWindowClient(t, prefix, nil)}
	windows := []*Window{
		NewShared(7, time.Minute, 20, clock.now, clients[0], "org-password:agent"),
		NewShared(7, time.Minute, 20, clock.now, clients[1], "org-password:agent"),
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 80 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := windows[i%2].Reserve(context.Background(), "org:17"); ok {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 7, admitted.Load())
	shadowTotal := 0
	for _, w := range windows {
		w.mu.Lock()
		shadowTotal += w.m["org:17"].n
		require.Zero(t, w.m["org:17"].pending)
		w.mu.Unlock()
	}
	require.Equal(t, 7, shadowTotal, "乱序完成不能漏记共享成功的影子")
	for _, c := range clients {
		require.True(t, c.Available(), "并发测试必须走共享路径")
	}
}

func TestWindow_165_ShadowSaturationDoesNotRefundEarlierCharge(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	proxy := redisx.NewTestProxy(t, redisx.TestAddr(t))
	c := openWindowClient(t, prefix, proxy)
	clock := newWindowClock()
	a := NewShared(2, time.Minute, 20, clock.now, c, "captcha:platform")
	b := NewShared(2, time.Minute, 20, clock.now, c, "captcha:platform")
	reserveWindow(t, a, "ip")
	clock.add(30 * time.Second)
	reserveWindow(t, b, "ip")
	clock.add(30 * time.Second)
	reserveWindow(t, b, "ip")
	saturated := reserveWindow(t, b, "ip")
	require.False(t, saturated.localIncrement)
	saturated.Undo()
	proxy.Cut()
	require.Error(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error { return rdb.Ping(ctx).Err() }))
	windowDenied(t, b, "ip")
}

func TestWindow_165_SharedOutageRecoveryAndBackendBinding(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	proxy := redisx.NewTestProxy(t, redisx.TestAddr(t))
	c := openWindowClient(t, prefix, proxy)
	clock := newWindowClock()
	w := NewShared(2, time.Minute, 20, clock.now, c, "pwd-changes:platform")
	raw := rawWindowRedis(t)
	ctx := context.Background()
	count := func(key string) int {
		t.Helper()
		value, err := raw.HGet(ctx, c.Key("{counts}", "windows"), digest("pwd-changes:platform")+":"+digest(key)).Result()
		require.NoError(t, err)
		var n int
		_, err = fmt.Sscanf(value[strings.LastIndex(value, "|")+1:], "%d", &n)
		require.NoError(t, err)
		return n
	}
	shared := reserveWindow(t, w, "account")
	blocked := reserveWindow(t, w, "keep")
	reserveWindow(t, w, "keep")
	reserveWindow(t, w, "reset")
	proxy.Cut()
	fallback := reserveWindow(t, w, "account")
	require.False(t, fallback.shared)
	require.False(t, c.Available())
	windowDenied(t, w, "account")
	blocked.Undo()
	fallbackReset := reserveWindow(t, w, "reset")
	fallbackReset.Reset()
	require.Equal(t, 2, count("keep"), "断网退款不重试、不清共享记录")
	proxy.Restore()
	require.Eventually(t, c.Available, 3*time.Second, 10*time.Millisecond)
	fallback.Undo()
	require.Equal(t, 1, count("account"), "本地票据恢复后仍只退本地")
	require.Equal(t, 1, count("reset"), "本地清零恢复后不能清共享窗口")
	shared.Undo()
	require.Equal(t, 0, count("account"), "共享票据恢复后仍退共享代数")
	reserveWindow(t, w, "account")
	reserveWindow(t, w, "account")
	windowDenied(t, w, "account")
	windowDenied(t, w, "keep")
	reserveWindow(t, w, "reset")
	windowDenied(t, w, "reset")
}

func TestWindow_165_CancellationDoesNotFallbackOrConsume(t *testing.T) {
	clock := newWindowClock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	memory := New(1, time.Minute, 10, clock.now)
	r, ok := memory.Reserve(ctx, "key")
	require.False(t, ok)
	require.Nil(t, r)
	require.Empty(t, memory.m)
	reserveWindow(t, memory, "key")
	prefix := redisx.TestPrefix(t)
	proxy := redisx.NewTestProxy(t, redisx.TestAddr(t))
	c := openWindowClient(t, prefix, proxy)
	w := NewShared(1, time.Minute, 10, clock.now, c, "pwd-tries:agent")
	r, ok = w.Reserve(ctx, "key")
	require.False(t, ok)
	require.Nil(t, r)
	require.Empty(t, w.m)
	// 保留已建立的连接并丢掉回复，确保测到调用方超时。
	// Stall 会先关闭旧连接，提前收到 EOF 时按设计可以退回本地。
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.Ping(ctx).Err()
	}))
	proxy.Freeze()
	deadline, end := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer end()
	started := time.Now()
	r, ok = w.Reserve(deadline, "key")
	require.False(t, ok)
	require.Nil(t, r)
	require.Less(t, time.Since(started), 500*time.Millisecond)
	require.True(t, c.Available(), "调用方取消不改变 Redis 健康状态")
	proxy.Restore()
	reserveWindow(t, w, "key")
	windowDenied(t, w, "key")
}

func TestWindow_165_ProbeChecksEveryPermissionOnFixedKeys(t *testing.T) {
	admin := rawWindowRedis(t)
	ctx := context.Background()
	for _, forbidden := range []string{"eval", "hget", "hset", "hdel", "hincrby", "zadd", "zrangebyscore", "zrem"} {
		t.Run(forbidden, func(t *testing.T) {
			prefix := redisx.TestPrefix(t)
			username := "window_" + digest(prefix)[:12]
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">test_pwd", "~"+prefix+"*", "+@all", "-"+forbidden).Err())
			t.Cleanup(func() { require.NoError(t, admin.Do(ctx, "ACL", "DELUSER", username).Err()) })
			rdb := redis.NewClient(&redis.Options{Addr: redisx.TestAddr(t), Protocol: 2, DisableIdentity: true, Username: username, Password: "test_pwd", MaxRetries: -1})
			defer func() { require.NoError(t, rdb.Close()) }()
			err := Probe(ctx, rdb, func(parts ...string) string { return prefix + strings.Join(parts, ":") })
			require.Error(t, err, "缺少 %s 必须被探测到", forbidden)
			message := strings.ToUpper(err.Error())
			require.True(t, strings.Contains(message, "NOPERM") || strings.Contains(message, "THE USER EXECUTING THE SCRIPT CAN'T RUN THIS COMMAND OR SUBCOMMAND"), "unexpected ACL error: %v", err)
		})
	}
	prefix := redisx.TestPrefix(t)
	key := func(parts ...string) string { return prefix + strings.Join(parts, ":") }
	username := "window_" + digest(prefix)[:12]
	args := make([]interface{}, 0, 10)
	args = append(args, "ACL", "SETUSER", username, "reset", "on", ">test_pwd", "+@all")
	for _, k := range windowKeys(key) {
		args = append(args, "~"+k)
	}
	require.NoError(t, admin.Do(ctx, args...).Err())
	t.Cleanup(func() { require.NoError(t, admin.Do(ctx, "ACL", "DELUSER", username).Err()) })
	rdb := redis.NewClient(&redis.Options{Addr: redisx.TestAddr(t), Protocol: 2, DisableIdentity: true, Username: username, Password: "test_pwd", MaxRetries: -1})
	defer func() { require.NoError(t, rdb.Close()) }()
	require.NoError(t, Probe(ctx, rdb, key), "只允许三个固定键即可执行探测")
	for _, k := range windowKeys(key) {
		exists, err := admin.Exists(ctx, k).Result()
		require.NoError(t, err)
		require.Zero(t, exists, "探测完成只清自己的字段")
	}
}

func TestWindow_165_BoundedExpiryGCAndSharedBudgetRelease(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	c := openWindowClient(t, prefix, nil)
	clock := newWindowClock()
	raw := rawWindowRedis(t)
	ctx := context.Background()
	ns := digest("captcha:platform")
	const oldCount = windowGC * 3
	values := make(map[string]interface{}, oldCount)
	members := make([]redis.Z, 0, oldCount)
	for i := range oldCount {
		field := ns + ":" + digest(fmt.Sprintf("old:%d", i))
		values[field] = fmt.Sprintf("%d|old|1", clock.now().UnixMilli()-1)
		members = append(members, redis.Z{Score: float64(clock.now().UnixMilli() - 1), Member: field})
	}
	require.NoError(t, raw.HSet(ctx, c.Key("{counts}", "windows"), values).Err())
	require.NoError(t, raw.ZAdd(ctx, c.Key("{counts}", "window-expiry"), members...).Err())
	require.NoError(t, raw.HSet(ctx, c.Key("{counts}", "window-capacity"), ns, oldCount).Err())
	w := NewShared(2, time.Minute, oldCount, clock.now, c, "captcha:platform")
	first := reserveWindow(t, w, "fresh")
	n, err := raw.HLen(ctx, c.Key("{counts}", "windows")).Result()
	require.NoError(t, err)
	require.EqualValues(t, oldCount-windowGC+1, n, "一次只清固定批次")
	first.Reset()
	for i := range 3 {
		reserveWindow(t, w, fmt.Sprintf("fresh:%d", i))
	}
	n, err = raw.HLen(ctx, c.Key("{counts}", "windows")).Result()
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	capacity, err := raw.HGet(ctx, c.Key("{counts}", "window-capacity"), ns).Int()
	require.NoError(t, err)
	require.Equal(t, 3, capacity, "分批过期和清零都准确释放容量")
}

func TestWindow_165_ProbeRejectedAtOpenWithoutChangingLiveCounts(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	admin := rawWindowRedis(t)
	ctx := context.Background()
	key := func(parts ...string) string { return prefix + strings.Join(parts, ":") }
	clock := newWindowClock()
	client := openWindowClient(t, prefix, nil)
	w := NewShared(2, time.Minute, 20, clock.now, client, "captcha:agent")
	reserveWindow(t, w, "live")
	before, err := admin.HGetAll(ctx, key("{counts}", "windows")).Result()
	require.NoError(t, err)
	require.NoError(t, Probe(ctx, admin, key))
	after, err := admin.HGetAll(ctx, key("{counts}", "windows")).Result()
	require.NoError(t, err)
	require.Equal(t, before, after, "权限探测不得改变活跃记录")
	username := "window_" + digest(prefix)[:12]
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">test_pwd", "~"+prefix+"*", "+@all", "-hincrby").Err())
	t.Cleanup(func() { require.NoError(t, admin.Do(ctx, "ACL", "DELUSER", username).Err()) })
	bad, err := redisx.Open(redisx.Options{
		Addr: redisx.TestAddr(t), Username: username, Password: "test_pwd", KeyPrefix: prefix,
		Check: Probe, ConnectWait: 50 * time.Millisecond,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if bad != nil {
		_ = bad.Close()
	}
	var rejection *redisx.RejectedError
	require.ErrorAs(t, err, &rejection, "实际窗口脚本缺权限应拒绝配置")
}

func TestWindow_165_ParallelCapacityCannotAllocatePastSharedBudget(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	clock := newWindowClock()
	clients := []*redisx.Client{openWindowClient(t, prefix, nil), openWindowClient(t, prefix, nil)}
	windows := []*Window{
		NewShared(100, time.Minute, 5, clock.now, clients[0], "captcha:merchant"),
		NewShared(100, time.Minute, 5, clock.now, clients[1], "captcha:merchant"),
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := windows[i%2].Reserve(context.Background(), fmt.Sprintf("client:%d", i)); ok {
				admitted.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	require.EqualValues(t, 5, admitted.Load())
	raw := rawWindowRedis(t)
	count, err := raw.HGet(context.Background(), clients[0].Key("{counts}", "window-capacity"), digest("captcha:merchant")).Int()
	require.NoError(t, err)
	require.Equal(t, 5, count)
	for _, c := range clients {
		require.True(t, c.Available())
	}
}

func TestWindow_165_StartupOutageKeepsLocalHistoryOnRecovery(t *testing.T) {
	prefix := redisx.TestPrefix(t)
	proxy := redisx.NewTestProxy(t, redisx.TestAddr(t))
	proxy.Cut()
	c, err := redisx.Open(redisx.Options{
		Addr: proxy.Addr(), KeyPrefix: prefix, ConnectWait: 25 * time.Millisecond, ProbeEvery: 10 * time.Millisecond,
		Check: Probe, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	require.False(t, c.Available())
	clock := newWindowClock()
	w := NewShared(2, time.Minute, 2, clock.now, c, "org-password:merchant")
	old := reserveWindow(t, w, "org:19")
	reserveWindow(t, w, "org:19")
	windowDenied(t, w, "org:19")
	proxy.Restore()
	require.Eventually(t, c.Available, 3*time.Second, 10*time.Millisecond)
	first := reserveWindow(t, w, "org:19")
	reserveWindow(t, w, "org:19")
	windowDenied(t, w, "org:19")
	require.True(t, first.shared)
	require.False(t, first.localIncrement, "恢复不迁移或清空已满的本地历史")
	old.Reset()
	windowDenied(t, w, "org:19")
	clock.add(time.Minute)
	reserveWindow(t, w, "org:19")
}

func TestWindow_165_RuntimePermissionFailureCannotPartiallyWriteBusiness(t *testing.T) {
	admin := rawWindowRedis(t)
	ctx := context.Background()
	for _, command := range []string{"eval", "hget", "hset", "hdel", "hincrby", "zadd", "zrangebyscore", "zrem"} {
		t.Run(command, func(t *testing.T) {
			prefix := redisx.TestPrefix(t)
			username := "window_" + digest(prefix)[:12]
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">test_pwd", "~"+prefix+"*", "+@all").Err())
			t.Cleanup(func() { require.NoError(t, admin.Do(ctx, "ACL", "DELUSER", username).Err()) })
			c, err := redisx.Open(redisx.Options{
				Addr: redisx.TestAddr(t), Username: username, Password: "test_pwd", KeyPrefix: prefix,
				Check: Probe, ConnectWait: time.Second, ProbeEvery: 10 * time.Millisecond,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, c.Close()) })
			clock := newWindowClock()
			w := NewShared(3, time.Minute, 2, clock.now, c, "captcha:platform")
			reserveWindow(t, w, "existing")
			keys := windowKeys(c.Key)
			ns := digest("captcha:platform")
			before, err := admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "-"+command).Err())
			fallback := reserveWindow(t, w, "new")
			require.False(t, fallback.shared)
			require.False(t, c.Available(), "实际命令权限故障标成不可用")
			after, err := admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			delete(after, windowProbeField)
			require.Equal(t, before, after, "命令缺权限不得修改任何业务数据")
			capacity, err := admin.HGet(ctx, keys[2], ns).Int()
			require.NoError(t, err)
			require.Equal(t, 1, capacity)
			index, err := admin.ZCard(ctx, keys[1]).Result()
			require.NoError(t, err)
			require.LessOrEqual(t, index, int64(2), "失败探测只允许一个固定残留")
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+"+command).Err())
			require.Eventually(t, c.Available, 3*time.Second, 10*time.Millisecond)
			after, err = admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			require.Equal(t, before, after, "恢复探测清除残留，保留业务状态")
			fresh := NewShared(3, time.Minute, 2, clock.now, c, "captcha:platform")
			reserveWindow(t, fresh, "new")
			windowDenied(t, fresh, "third")
			reserveWindow(t, w, "existing")
			reserveWindow(t, w, "existing")
			windowDenied(t, w, "existing")
			capacity, err = admin.HGet(ctx, keys[2], ns).Int()
			require.NoError(t, err)
			require.Equal(t, 2, capacity, "恢复后不能因之前半写低算硬预算")
			business, err := admin.HLen(ctx, keys[0]).Result()
			require.NoError(t, err)
			require.EqualValues(t, 2, business)
		})
	}
}

func TestWindow_165_RuntimePermissionFailureCannotPartiallyFinish(t *testing.T) {
	admin := rawWindowRedis(t)
	ctx := context.Background()
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%v", reset), func(t *testing.T) {
			prefix := redisx.TestPrefix(t)
			username := "window_" + digest(prefix)[:12]
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">test_pwd", "~"+prefix+"*", "+@all").Err())
			t.Cleanup(func() { require.NoError(t, admin.Do(ctx, "ACL", "DELUSER", username).Err()) })
			c, err := redisx.Open(redisx.Options{
				Addr: redisx.TestAddr(t), Username: username, Password: "test_pwd", KeyPrefix: prefix,
				Check: Probe, ConnectWait: time.Second, ProbeEvery: 10 * time.Millisecond,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, c.Close()) })
			clock := newWindowClock()
			w := NewShared(1, time.Minute, 1, clock.now, c, "pwd-tries:merchant")
			ticket := reserveWindow(t, w, "existing")
			keys := windowKeys(c.Key)
			before, err := admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "-hincrby").Err())
			if reset {
				ticket.Reset()
			} else {
				ticket.Undo()
			}
			require.False(t, c.Available())
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+hincrby").Err())
			require.Eventually(t, c.Available, 3*time.Second, 10*time.Millisecond)
			after, err := admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			require.Equal(t, before, after, "失败收尾不重试，不部分清零或退款")
			fresh := NewShared(1, time.Minute, 1, clock.now, c, "pwd-tries:merchant")
			windowDenied(t, fresh, "existing")
			windowDenied(t, fresh, "new")
		})
	}
}
