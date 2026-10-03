package captcha

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

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
)

func openCaptchaClient(t *testing.T, addr, prefix, username string) *redisx.Client {
	t.Helper()
	opts := redisx.Options{Addr: addr, KeyPrefix: prefix, Username: username,
		ProbeEvery: 20 * time.Millisecond, Check: Probe, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if username != "" {
		opts.Password = "captcha_test_pwd"
	}
	c, err := redisx.Open(opts)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	require.True(t, c.Available())
	return c
}

func rawCaptchaClient(t *testing.T, username string) *redis.Client {
	t.Helper()
	opts := &redis.Options{Addr: redisx.TestAddr(t), Protocol: 2, DisableIdentity: true, MaxRetries: -1, Username: username}
	if username != "" {
		opts.Password = "captcha_test_pwd"
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	return c
}

func sharedCaptchaPair(t *testing.T) (*Captcha, *Captcha, *time.Time) {
	t.Helper()
	addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
	now := time.Now().UTC()
	clock := func() time.Time { return now }
	a := NewShared(clock, openCaptchaClient(t, addr, prefix, ""))
	b := NewShared(clock, openCaptchaClient(t, addr, prefix, ""))
	return a, b, &now
}

func generateShared(t *testing.T, c *Captcha, scope string) (string, string) {
	t.Helper()
	id, img, err := c.GenerateFor(context.Background(), scope)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(img, "data:image/png;base64,"))
	answer := c.Peek(id)
	require.Len(t, answer, Length)
	return id, answer
}

// 166：跨实例可用、跨端及前缀隔离，对错和空答案均只消费一次。
func TestShared_166_OnceAndIsolation(t *testing.T) {
	a, b, _ := sharedCaptchaPair(t)
	ctx := context.Background()
	id, answer := generateShared(t, a, "platform")
	require.True(t, validID(id, 'r'))
	require.Zero(t, a.size(), "共享答案不留可验证的本地副本")
	require.Equal(t, answer, b.Peek(id))
	require.False(t, b.VerifyFor(ctx, "merchant", id, answer))
	other := NewShared(nil, openCaptchaClient(t, redisx.TestAddr(t), redisx.TestPrefix(t), ""))
	require.False(t, other.VerifyFor(ctx, "platform", id, answer))
	require.True(t, b.VerifyFor(ctx, "platform", id, " "+answer+" "))
	require.False(t, a.VerifyFor(ctx, "platform", id, answer))
	for _, wrong := range []string{"wrong", "", "  "} {
		id, answer = generateShared(t, a, "agent")
		require.False(t, b.VerifyFor(ctx, "agent", id, wrong))
		require.Empty(t, a.Peek(id))
		require.False(t, a.VerifyFor(ctx, "agent", id, answer))
	}
	for _, bad := range []string{"", "r", "r" + strings.Repeat("g", 40), "x" + strings.Repeat("0", 40), strings.Repeat("0", 1000)} {
		require.False(t, a.VerifyFor(ctx, "platform", bad, "12345"))
	}
}

func TestShared_166_ConcurrentConsumeAndCapacity(t *testing.T) {
	a, b, _ := sharedCaptchaPair(t)
	ctx := context.Background()
	id, answer := generateShared(t, a, "platform")
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			c := []*Captcha{a, b}[i%2]
			if c.VerifyFor(ctx, "platform", id, answer) {
				passed.Add(1)
			}
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, passed.Load())
	a.max, b.max = 5, 5
	var failures atomic.Int32
	for i := range 20 {
		wg.Go(func() {
			id, _, err := []*Captcha{a, b}[i%2].GenerateFor(ctx, "platform")
			if err != nil || !validID(id, 'r') {
				failures.Add(1)
			}
		})
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	raw := rawCaptchaClient(t, "")
	keys := captchaKeys(a.shared.client.Key)
	require.EqualValues(t, 5, raw.HLen(ctx, keys[0]).Val())
	require.EqualValues(t, 5, raw.ZCard(ctx, keys[1]).Val())
}

func TestShared_166_ExpiryEvictionAndBoundedGC(t *testing.T) {
	a, b, now := sharedCaptchaPair(t)
	a.max, b.max = 3, 3
	ctx := context.Background()
	old, answer := generateShared(t, a, "platform")
	for range 3 {
		*now = now.Add(time.Millisecond)
		generateShared(t, b, "platform")
	}
	require.False(t, a.VerifyFor(ctx, "platform", old, answer), "满了丢最早到期的")
	id, answer := generateShared(t, a, "platform")
	*now = now.Add(defaultTTL)
	require.False(t, b.VerifyFor(ctx, "platform", id, answer), "恰好到期不能使用")
	raw := rawCaptchaClient(t, "")
	keys := captchaKeys(a.shared.client.Key)
	require.NoError(t, raw.Del(ctx, keys...).Err())
	a.max = defaultMax
	values := make(map[string]interface{}, 384)
	members := make([]redis.Z, 0, 384)
	for i := range 384 {
		field := fmt.Sprintf("expired-%d", i)
		values[field] = `{"s":"platform","e":0,"a":"12345"}`
		members = append(members, redis.Z{Score: 0, Member: field})
	}
	require.NoError(t, raw.HSet(ctx, keys[0], values).Err())
	require.NoError(t, raw.ZAdd(ctx, keys[1], members...).Err())
	generateShared(t, a, "platform")
	require.EqualValues(t, 384-128+1, raw.HLen(ctx, keys[0]).Val())
	for _, key := range keys {
		ttl, err := raw.PTTL(ctx, key).Result()
		require.NoError(t, err)
		require.Greater(t, ttl, time.Duration(0))
		require.LessOrEqual(t, ttl, defaultTTL)
	}
}

func TestShared_166_IdleContainersExpire(t *testing.T) {
	a, _, _ := sharedCaptchaPair(t)
	a.ttl = 80 * time.Millisecond
	id, _, err := a.GenerateFor(context.Background(), "platform")
	require.NoError(t, err)
	require.True(t, validID(id, 'r'))
	raw := rawCaptchaClient(t, "")
	keys := captchaKeys(a.shared.client.Key)
	require.Eventually(t, func() bool { return raw.Exists(context.Background(), keys...).Val() == 0 }, 2*time.Second, 10*time.Millisecond)
}

func TestShared_166_FallbackAndRecovery(t *testing.T) {
	for _, mode := range []string{"cut", "stall"} {
		t.Run(mode, func(t *testing.T) {
			addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
			proxy := redisx.NewTestProxy(t, addr)
			a := NewShared(nil, openCaptchaClient(t, proxy.Addr(), prefix, ""))
			b := NewShared(nil, openCaptchaClient(t, proxy.Addr(), prefix, ""))
			ctx := context.Background()
			shared, answer := generateShared(t, a, "platform")
			if mode == "cut" {
				proxy.Cut()
			} else {
				proxy.Stall()
			}
			start := time.Now()
			require.False(t, a.VerifyFor(ctx, "platform", shared, answer))
			require.Less(t, time.Since(start), 2*time.Second)
			local, localAnswer := generateShared(t, a, "platform")
			require.True(t, validID(local, 'l'))
			require.False(t, b.VerifyFor(ctx, "platform", local, localAnswer))
			other, otherAnswer := generateShared(t, b, "platform")
			require.True(t, validID(other, 'l'))
			require.True(t, b.VerifyFor(ctx, "platform", other, otherAnswer))
			proxy.Restore()
			require.Eventually(t, func() bool { return a.shared.client.Available() && b.shared.client.Available() }, 5*time.Second, 10*time.Millisecond)
			require.False(t, b.VerifyFor(ctx, "platform", local, localAnswer))
			require.True(t, a.VerifyFor(ctx, "platform", local, localAnswer), "恢复后原本地答案仍只在本机消费")
			require.False(t, a.VerifyFor(ctx, "platform", local, localAnswer))
			require.True(t, b.VerifyFor(ctx, "platform", shared, answer), "没到 Redis 的请求没消费共享答案")
			require.False(t, a.VerifyFor(ctx, "platform", shared, answer))
			fresh, freshAnswer := generateShared(t, b, "platform")
			require.True(t, a.VerifyFor(ctx, "platform", fresh, freshAnswer))
		})
	}
}

func TestShared_166_CancellationAndLocalScope(t *testing.T) {
	a, b, _ := sharedCaptchaPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id, _, err := a.GenerateFor(ctx, "platform")
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, id)
	id, answer := generateShared(t, a, "platform")
	require.False(t, b.VerifyFor(ctx, "platform", id, answer))
	require.True(t, b.shared.client.Available())
	require.True(t, a.VerifyFor(context.Background(), "platform", id, answer))
	local := NewShared(nil, nil)
	id, answer = generateShared(t, local, "agent")
	require.Len(t, id, 2*idBytes)
	require.False(t, local.VerifyFor(context.Background(), "merchant", id, answer))
	require.False(t, local.VerifyFor(context.Background(), "agent", id, ""))
	require.False(t, local.VerifyFor(context.Background(), "agent", id, answer))
}

func TestShared_166_DeadlineDoesNotFallbackOrDisable(t *testing.T) {
	proxy := redisx.NewTestProxy(t, redisx.TestAddr(t))
	c := NewShared(nil, openCaptchaClient(t, proxy.Addr(), redisx.TestPrefix(t), ""))
	id, answer := generateShared(t, c, "platform")
	proxy.Freeze()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	require.False(t, c.VerifyFor(ctx, "platform", id, answer))
	deadline, _ := ctx.Deadline()
	require.False(t, time.Now().Before(deadline), "连接调用不可提前结束")
	<-ctx.Done() // 连接时限与 context 定时器可能先后唤醒。
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.True(t, c.shared.client.Available(), "调用方超时不算 Redis 故障")
	proxy.Restore()
	require.True(t, c.VerifyFor(context.Background(), "platform", id, answer))
	proxy.Freeze()
	ctx, stop := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer stop()
	id, _, err := c.GenerateFor(ctx, "platform")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, id)
	require.Zero(t, c.size(), "取消后不生成可用的本地回退答案")
	require.True(t, c.shared.client.Available())
	proxy.Restore()
}

func TestShared_166_PermissionsAndRecovery(t *testing.T) {
	ctx := context.Background()
	admin := rawCaptchaClient(t, "")
	for _, forbidden := range []string{"eval", "hget", "hset", "hdel", "hlen", "zadd", "zrem", "zrange", "zrangebyscore", "pexpire"} {
		t.Run(forbidden, func(t *testing.T) {
			prefix := redisx.TestPrefix(t)
			username := "captcha_" + strings.ReplaceAll(prefix, ":", "_")
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">captcha_test_pwd", "~"+prefix+"*", "+@all").Err())
			t.Cleanup(func() { require.NoError(t, admin.Do(ctx, "ACL", "DELUSER", username).Err()) })
			a := NewShared(nil, openCaptchaClient(t, redisx.TestAddr(t), prefix, ""))
			b := NewShared(nil, openCaptchaClient(t, redisx.TestAddr(t), prefix, username))
			writer := NewShared(nil, openCaptchaClient(t, redisx.TestAddr(t), prefix, username))
			id, answer := generateShared(t, a, "platform")
			keys := captchaKeys(a.shared.client.Key)
			before, err := admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "-"+forbidden).Err())
			require.False(t, b.VerifyFor(ctx, "platform", id, answer), "撤权不能消费答案")
			local, _ := generateShared(t, writer, "platform")
			require.True(t, validID(local, 'l'))
			raw := rawCaptchaClient(t, username)
			for range 3 {
				require.Error(t, Probe(ctx, raw, a.shared.client.Key))
			}
			after, err := admin.HGetAll(ctx, keys[0]).Result()
			require.NoError(t, err)
			delete(after, "_probe")
			require.Equal(t, before, after, "失败探测和消费不半写用户答案")
			require.LessOrEqual(t, admin.HLen(ctx, keys[0]).Val(), int64(len(before)+1))
			rejected, err := redisx.Open(redisx.Options{Addr: redisx.TestAddr(t), KeyPrefix: prefix, Username: username, Password: "captcha_test_pwd", Check: Probe,
				Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
			if rejected != nil {
				_ = rejected.Close()
			}
			var rejection *redisx.RejectedError
			require.ErrorAs(t, err, &rejection)
			require.NotContains(t, err.Error(), "captcha_test_pwd")
			require.Equal(t, answer, a.Peek(id), "正常客户端仍能读，且清除残留探测字段")
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+"+forbidden).Err())
			require.Eventually(t, func() bool { return b.shared.client.Available() }, 5*time.Second, 10*time.Millisecond)
			require.True(t, b.VerifyFor(ctx, "platform", id, answer))
			require.Zero(t, admin.HLen(ctx, keys[0]).Val())
			require.Zero(t, admin.ZCard(ctx, keys[1]).Val())
		})
	}
}
