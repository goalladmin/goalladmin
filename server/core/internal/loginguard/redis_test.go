package loginguard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func sharedClients(t *testing.T, proxy *redisx.TestProxy) (*redisx.Client, *redisx.Client) {
	t.Helper()
	addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
	if proxy != nil {
		addr = proxy.Addr()
	}
	open := func() *redisx.Client {
		c, err := redisx.Open(redisx.Options{Addr: addr, KeyPrefix: prefix,
			ProbeEvery: 20 * time.Millisecond, Check: Probe,
			Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, c.Close()) })
		return c
	}
	return open(), open()
}

func sharedGuards(t *testing.T) (*Guard, *Guard, *time.Time, *redisx.Client) {
	t.Helper()
	a, b := sharedClients(t, nil)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	return NewShared(portal.DefaultLoginPolicy(), clock, a, "platform"), NewShared(portal.DefaultLoginPolicy(), clock, b, "platform"), &now, a
}

func guardData(t *testing.T, c *redisx.Client) map[string]string {
	t.Helper()
	var fields map[string]string
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		var err error
		fields, err = rdb.HGetAll(ctx, guardKeys(c.Key)[0]).Result()
		return err
	}))
	return fields
}

func logicalCount(t *testing.T, c *redisx.Client, g *Guard) int64 {
	t.Helper()
	var count int64
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		var err error
		count, err = rdb.HGet(ctx, guardKeys(c.Key)[2], g.shared.ns).Int64()
		return err
	}))
	return count
}

// 规范 §13.2 第 165 条：两实例共用固定窗口，IP 先于账号计数，IPv6 按 /64；端与部署隔离。
func TestShared_165_RatesAndIsolation(t *testing.T) {
	a, b, now, c := sharedGuards(t)
	for i := 0; i < 20; i++ {
		g := []*Guard{a, b}[i%2]
		d := g.Check(fmt.Sprintf("u%d", i), "2001:db8:1234:5678::1")
		require.False(t, d.RateLimited)
		require.NotNil(t, d.Attempt)
		d.Attempt.Done()
	}
	require.True(t, b.Check("other", "2001:db8:1234:5678:abcd::2").RateLimited)
	for i := 0; i < 10; i++ {
		d := []*Guard{a, b}[i%2].Check("alice", fmt.Sprintf("10.0.0.%d", i))
		require.NotNil(t, d.Attempt)
		require.False(t, d.CaptchaRequired)
		d.Attempt.Done()
	}
	// 同账号第 11 次：不拒绝，改为必须带验证码（D-103）
	over := a.Check("alice", "10.0.0.99")
	require.False(t, over.RateLimited)
	require.True(t, over.CaptchaRequired)
	require.NotNil(t, over.Attempt)
	over.Attempt.Done()
	other := NewShared(a.policy, a.now, c, "merchant")
	d := other.Check("alice", "10.0.0.99")
	require.NotNil(t, d.Attempt)
	require.False(t, d.CaptchaRequired, "别的端各算各的")
	d.Attempt.Done()
	// 上一轮 IP 拒绝没有创建新账号计数。
	for field := range guardData(t, c) {
		require.NotEqual(t, a.shared.ns+":r:a:"+digest(accountKey("other")), field)
		require.NotContains(t, field, "alice")
		require.NotContains(t, field, "2001:db8")
	}
	*now = now.Add(time.Minute)
	d = b.Check("alice", "10.0.0.99")
	require.NotNil(t, d.Attempt)
	require.False(t, d.CaptchaRequired, "换了窗口，次数重新计")
	d.Attempt.Done()
	otherClient := redisx.OpenTest(t, nil)
	otherDeployment := NewShared(a.policy, a.now, otherClient, "platform")
	d = otherDeployment.Check("alice", "10.0.0.99")
	require.NotNil(t, d.Attempt)
	d.Attempt.Done()
}

// 规范 §13.2 第 165 条：失败与锁定跨实例共用，成功只清组合；锁屏准入不消耗登录请求次数。
func TestShared_165_FailureSuccessAndLocks(t *testing.T) {
	a, b, now, _ := sharedGuards(t)
	for i := 0; i < 3; i++ {
		at := []*Guard{a, b}[i%2].Admit("alice", "1.1.1.1")
		require.NotNil(t, at)
		captcha, locked := at.Fail()
		require.Equal(t, i == 2, captcha)
		require.False(t, locked)
	}
	d := b.Check("alice", "1.1.1.1")
	require.True(t, d.CaptchaRequired)
	d.Attempt.Succeed()
	d = a.Check("alice", "1.1.1.1")
	require.False(t, d.CaptchaRequired)
	d.Attempt.Done()
	for i := 0; i < 10; i++ {
		at := []*Guard{a, b}[i%2].Admit("alice", "1.1.1.1")
		require.NotNil(t, at)
		_, locked := at.Fail()
		require.Equal(t, i == 9, locked)
	}
	d = a.Check("alice", "1.1.1.1")
	require.True(t, d.Locked)
	require.False(t, d.RateLimited)
	require.Equal(t, now.Add(a.policy.LockDuration), d.LockedUntil)
	// 成功前的 3 次账号失败没有被清掉；再记 37 次就是账号阈值 50。
	for i := 0; i < 37; i++ {
		at := []*Guard{a, b}[i%2].Admit("alice", fmt.Sprintf("10.1.0.%d", i))
		require.NotNil(t, at)
		_, locked := at.Fail()
		require.Equal(t, i == 36, locked)
	}
	require.True(t, b.Check("alice", "2.2.2.2").Locked)
	*now = now.Add(16 * time.Minute)
	d = b.Check("alice", "2.2.2.2")
	require.False(t, d.Locked)
	require.NotNil(t, d.Attempt)
	d.Attempt.Done()
}

// 不同主机时间可能乱序；按时间值清理失败，不能只丢掉数组头部的旧值。
func TestShared_165_FailureTimesAreFilteredByValue(t *testing.T) {
	c1, c2 := sharedClients(t, nil)
	base := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	nowA, nowB := base.Add(time.Minute), base
	policy := portal.DefaultLoginPolicy()
	policy.CaptchaAfterFailures = 2
	a := NewShared(policy, func() time.Time { return nowA }, c1, "platform")
	b := NewShared(policy, func() time.Time { return nowB }, c2, "platform")
	a.Admit("alice", "1.1.1.1").Fail()
	b.Admit("alice", "1.1.1.1").Fail()
	nowB = base.Add(policy.Window + 10*time.Second)
	d := b.Check("alice", "1.1.1.1")
	require.False(t, d.CaptchaRequired, "较旧的失败虽后到达也必须过期")
	require.NotNil(t, d.Attempt)
	requires, locked := d.Attempt.Fail()
	require.True(t, requires, "较新的第一次失败仍在滑动窗口")
	require.False(t, locked)
}

// 规范 §13.2 第 103、165 条：容量是两个实例的合计；结果结束不新建记录，幂等且不逐出锁定。
func TestShared_165_CapacityAndIdempotentEnd(t *testing.T) {
	a, b, now, c := sharedGuards(t)
	a.maxKeys, b.maxKeys = 8, 8
	first := a.Check("alice", "1.1.1.1")
	require.NotNil(t, first.Attempt)
	first.Attempt.Fail()
	second := b.Check("bob", "2.2.2.2")
	require.NotNil(t, second.Attempt)
	second.Attempt.Fail()
	require.EqualValues(t, 8, logicalCount(t, c, a))
	require.True(t, a.Check("carol", "3.3.3.3").RateLimited)
	require.Nil(t, b.Admit("carol", "3.3.3.3"))
	at := b.Admit("alice", "1.1.1.1")
	require.NotNil(t, at)
	remote := at.remote
	at.Fail()
	at.Fail()
	at.Succeed()
	at.Done()
	result, err := remote.run(context.Background(), "fail", *now, 0, "")
	require.NoError(t, err)
	require.Zero(t, result.code)
	require.EqualValues(t, 8, logicalCount(t, c, a))
	for _, g := range []*Guard{a, b} {
		g.mu.Lock()
		require.LessOrEqual(t, g.keysLocked(), g.maxKeys)
		for _, state := range g.state {
			require.Zero(t, state.inflight)
		}
		g.mu.Unlock()
	}
}

// 租期之后的旧结果不得重建旧记录，也不得修改下一次准入保留的新记录。
func TestShared_165_LeaseAndConcurrentSuccess(t *testing.T) {
	a, b, now, c := sharedGuards(t)
	old := a.Admit("alice", "1.1.1.1")
	require.NotNil(t, old)
	*now = now.Add(attemptLease + time.Millisecond)
	current := b.Admit("alice", "1.1.1.1")
	require.NotNil(t, current)
	before := guardData(t, c)
	old.Fail()
	require.Equal(t, before, guardData(t, c), "超期结果只结束原本的内存影子")
	current.Done()
	require.Empty(t, guardData(t, c))
	// 成功仅清当时组合历史，仍在途的尝试可以继续合法记失败。
	first, success, later := a.Admit("alice", "1.1.1.1"), b.Admit("alice", "1.1.1.1"), a.Admit("alice", "1.1.1.1")
	first.Fail()
	success.Succeed()
	later.Fail()
	fields := guardData(t, c)
	var pair, acct struct {
		F []int64 `json:"f"`
		P int     `json:"p"`
	}
	require.NoError(t, json.Unmarshal([]byte(fields[a.shared.ns+":s:p:"+digest(pairKey("alice", "1.1.1.1"))]), &pair))
	require.NoError(t, json.Unmarshal([]byte(fields[a.shared.ns+":s:a:"+digest(accountKey("alice"))]), &acct))
	require.Len(t, pair.F, 1)
	require.Len(t, acct.F, 2)
	require.Zero(t, pair.P)
	require.Zero(t, acct.P)
}

func TestShared_165_ConcurrentAdmissionIsAtomic(t *testing.T) {
	a, b, _, _ := sharedGuards(t)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := []*Guard{a, b}[i%2].Check(fmt.Sprintf("u%d", i), "1.1.1.1")
			if d.Attempt != nil {
				allowed.Add(1)
				d.Attempt.Done()
			}
		}(i)
	}
	wg.Wait()
	require.EqualValues(t, 20, allowed.Load())
}

func TestShared_165_ConcurrentCapacityIsAtomic(t *testing.T) {
	a, b, _, c := sharedGuards(t)
	a.maxKeys, b.maxKeys = 20, 20
	var wg sync.WaitGroup
	attempts := make(chan *Attempt, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d := []*Guard{a, b}[i%2].Check(fmt.Sprintf("user%d", i), fmt.Sprintf("10.2.0.%d", i))
			if d.Attempt != nil {
				attempts <- d.Attempt
			}
		}(i)
	}
	wg.Wait()
	close(attempts)
	require.Len(t, attempts, 5, "五次合法在途准入合计正好占满 20 个逻辑键")
	require.EqualValues(t, 20, logicalCount(t, c, a))
	for at := range attempts {
		at.Fail()
	}
	require.EqualValues(t, 20, logicalCount(t, c, a), "失败收尾只修改原本预留的记录")
	for _, g := range []*Guard{a, b} {
		g.mu.Lock()
		require.LessOrEqual(t, g.keysLocked(), g.maxKeys)
		g.mu.Unlock()
	}
}

func TestShared_165_CaptchaAlways(t *testing.T) {
	a, _, _, _ := sharedGuards(t)
	a.policy.CaptchaAlways = true
	d := a.Check("alice", "1.1.1.1")
	require.True(t, d.CaptchaRequired)
	require.NotNil(t, d.Attempt)
	requires, _ := d.Attempt.Fail()
	require.True(t, requires)
}

// 故障使用已保留的影子；恢复不清内存历史，也不把故障期间的在途票据补写到 Redis。
func TestShared_165_FallbackAndRecoveryKeepBackend(t *testing.T) {
	proxy := redisx.NewTestProxy(t, redisx.TestAddr(t))
	c, _ := sharedClients(t, proxy)
	clock := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	g := NewShared(portal.DefaultLoginPolicy(), func() time.Time { return clock }, c, "platform")
	at := g.Admit("alice", "1.1.1.1")
	require.NotNil(t, at.remote)
	proxy.Cut()
	at.Fail()
	require.False(t, c.Available())
	for i := 0; i < 9; i++ {
		g.Admit("alice", "1.1.1.1").Fail()
	}
	require.True(t, g.Check("alice", "1.1.1.1").Locked)
	memory := g.Admit("bob", "2.2.2.2")
	require.NotNil(t, memory)
	require.Nil(t, memory.remote)
	proxy.Restore()
	require.Eventually(t, c.Available, 3*time.Second, 5*time.Millisecond)
	before := guardData(t, c)
	memory.Fail()
	require.Equal(t, before, guardData(t, c))
	// 共享没有这次故障期间的账号锁定；其结果覆盖本地旧锁，但本地仍保留旧失败。
	d := g.Check("alice", "1.1.1.1")
	require.False(t, d.Locked)
	require.NotNil(t, d.Attempt)
	d.Attempt.Done()
	proxy.Cut()
	// 显式报告本轮网络故障后，新检查立即走旧的本地锁定。
	require.Error(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error { return rdb.Ping(ctx).Err() }))
	require.True(t, g.Check("alice", "1.1.1.1").Locked)
}

func TestShared_165_ProbeDoesNotAlterRecords(t *testing.T) {
	a, _, _, c := sharedGuards(t)
	at := a.Admit("alice", "1.1.1.1")
	before := guardData(t, c)
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error { return Probe(ctx, rdb, c.Key) }))
	require.Equal(t, before, guardData(t, c))
	at.Done()
	require.Empty(t, guardData(t, c))
}

// Lua 错误不会回滚：缺内部权限时反复探测也必须有界，其他正常客户端不能受探测残留影响。
func TestShared_165_DeniedProbeDoesNotPolluteGC(t *testing.T) {
	a, _, _, c := sharedGuards(t)
	admin := redis.NewClient(&redis.Options{Addr: redisx.TestAddr(t), Protocol: 2, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	username := "guard-acl-" + strings.ReplaceAll(c.Key(""), ":", "")
	t.Cleanup(func() { require.NoError(t, admin.Do(context.Background(), "ACL", "DELUSER", username).Err()) })
	ctx := context.Background()
	for i, command := range []string{"hdel", "zrem", "hset", "hincrby", "zadd", "zrangebyscore", "hget"} {
		t.Run(command, func(t *testing.T) {
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">acl-test", "~*", "+@all", "-"+command).Err())
			limited := redis.NewClient(&redis.Options{Addr: redisx.TestAddr(t), Username: username, Password: "acl-test", Protocol: 2, DisableIdentity: true})
			defer func() { require.NoError(t, limited.Close()) }()
			before := len(guardData(t, c))
			for j := 0; j < 3; j++ {
				require.Error(t, Probe(ctx, limited, c.Key))
				require.LessOrEqual(t, len(guardData(t, c)), before+1, "固定探测字段不能累积")
			}
			d := a.Check(fmt.Sprintf("user%d", i), fmt.Sprintf("10.8.0.%d", i))
			require.NotNil(t, d.Attempt, "正常客户端的 GC 和准入不受残留字段影响")
			d.Attempt.Fail()
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+"+command).Err())
			require.NoError(t, Probe(ctx, limited, c.Key))
			require.NotContains(t, guardData(t, c), "_probe")
			_, err := admin.HGet(ctx, guardKeys(c.Key)[2], "_probe").Result()
			require.ErrorIs(t, err, redis.Nil)
			require.Zero(t, admin.ZScore(ctx, guardKeys(c.Key)[1], "_probe").Val())
		})
	}
}

// 运行期间撤销任何脚本内部命令时，预检必须在用户状态半写之前失败。
func TestShared_165_RuntimeACLChangesDoNotPartiallyWrite(t *testing.T) {
	addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
	admin := redis.NewClient(&redis.Options{Addr: addr, Protocol: 2, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	username := "guard-runtime-" + strings.ReplaceAll(prefix, ":", "")
	ctx := context.Background()
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">acl-test", "~*", "+@all").Err())
	t.Cleanup(func() { require.NoError(t, admin.Do(context.Background(), "ACL", "DELUSER", username).Err()) })
	c, err := redisx.Open(redisx.Options{Addr: addr, Username: username, Password: "acl-test", KeyPrefix: prefix, Check: Probe,
		ProbeEvery: 20 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	g := NewShared(portal.DefaultLoginPolicy(), nil, c, "platform")
	read := func() map[string]string {
		data, err := admin.HGetAll(ctx, guardKeys(c.Key)[0]).Result()
		require.NoError(t, err)
		delete(data, "_probe")
		return data
	}
	for i, command := range []string{"hdel", "zrem", "hset", "hincrby", "zadd", "zrangebyscore", "hget"} {
		t.Run(command, func(t *testing.T) {
			user, ip := fmt.Sprintf("user%d", i), fmt.Sprintf("10.9.0.%d", i)
			g.Admit(user, ip).Fail()
			pending := g.Admit(user, ip)
			require.NotNil(t, pending.remote)
			before := read()
			count := admin.HGet(ctx, guardKeys(c.Key)[2], g.shared.ns).Val()
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "-"+command).Err())
			pending.Fail()
			require.False(t, c.Available())
			require.Equal(t, before, read(), "结束预检失败不能删除票据或半记失败")
			require.Equal(t, count, admin.HGet(ctx, guardKeys(c.Key)[2], g.shared.ns).Val())
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+"+command).Err())
			require.Eventually(t, c.Available, 3*time.Second, 5*time.Millisecond)
			before = read()
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "-"+command).Err())
			d := g.Check("new"+user, ip)
			require.NotNil(t, d.Attempt, "本次准入失败直接使用已保留的影子")
			d.Attempt.Done()
			require.False(t, c.Available())
			require.Equal(t, before, read(), "准入预检失败不能新建用户计数或泄漏预算")
			require.Equal(t, count, admin.HGet(ctx, guardKeys(c.Key)[2], g.shared.ns).Val())
			require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "+"+command).Err())
			require.Eventually(t, c.Available, 3*time.Second, 5*time.Millisecond)
			// 两次被拒绝的共享收尾没有写入；此时共享只保留第一笔失败。
			d = g.Check(user, ip)
			require.False(t, d.CaptchaRequired)
			require.NotNil(t, d.Attempt)
			requires, _ := d.Attempt.Fail()
			require.False(t, requires)
		})
	}
}

// 延迟清理的旧票据必须带原记录代数；即使历史还保留，也不能释放新准入的 pin。
func TestShared_165_ExpiredTicketCannotReleaseNewPin(t *testing.T) {
	a, b, now, c := sharedGuards(t)
	a.policy.Window, b.policy.Window = time.Hour, time.Hour
	a.Admit("alice", "1.1.1.1").Fail()
	old := a.Admit("alice", "1.1.1.1")
	ticket := a.shared.ns + ":t:" + old.remote.id
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.ZRem(ctx, guardKeys(c.Key)[1], ticket).Err()
	}))
	*now = now.Add(attemptLease + time.Millisecond)
	current := b.Admit("alice", "1.1.1.1")
	require.NotNil(t, current)
	// 旧索引迟到，记录仍有失败历史；新票据保留的是同一记录的新代数。
	require.NoError(t, c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.ZAdd(ctx, guardKeys(c.Key)[1], redis.Z{Score: float64(now.UnixMilli() - 1), Member: ticket}).Err()
	}))
	b.Admit("bob", "2.2.2.2").Done()
	fields := guardData(t, c)
	for _, key := range []string{a.shared.ns + ":s:p:" + digest(pairKey("alice", "1.1.1.1")), a.shared.ns + ":s:a:" + digest(accountKey("alice"))} {
		var record struct {
			P int `json:"p"`
			V int `json:"v"`
		}
		require.NoError(t, json.Unmarshal([]byte(fields[key]), &record))
		require.Equal(t, 1, record.P)
		require.Equal(t, 1, record.V)
	}
	old.Fail()
	current.Fail()
	fields = guardData(t, c)
	var record struct {
		P int     `json:"p"`
		F []int64 `json:"f"`
	}
	require.NoError(t, json.Unmarshal([]byte(fields[a.shared.ns+":s:p:"+digest(pairKey("alice", "1.1.1.1"))]), &record))
	require.Zero(t, record.P)
	require.Len(t, record.F, 2)
}

func TestShared_165_NoRedisKeepsMemoryBehavior(t *testing.T) {
	g := NewShared(portal.DefaultLoginPolicy(), nil, nil, "platform")
	for i := 0; i < 3; i++ {
		g.AdmitContext(context.Background(), "alice", "1.1.1.1").Fail()
	}
	d := g.CheckContext(context.Background(), "alice", "1.1.1.1")
	require.True(t, d.CaptchaRequired)
	require.NotNil(t, d.Attempt)
	d.Attempt.Done()
}

// Redis 往返期间过期清理不能收回影子准入已保留的限流键容量。
func TestShared_165_ShadowCapacityRemainsReservedDuringGC(t *testing.T) {
	g, clock := newGuard()
	g.maxKeys = 4
	for i := 0; i < 10; i++ {
		g.Admit("alice", "1.1.1.1").Fail()
	}
	local, at, started := g.prepareShadow("alice", "1.1.1.1", true)
	require.True(t, local.Locked)
	require.NotNil(t, at)
	*clock = clock.Add(time.Hour)
	require.True(t, g.Check("bob", "2.2.2.2").RateLimited)
	g.mu.Lock()
	require.Equal(t, 4, g.keysLocked())
	// 另一次相同维度的窗口重置必须保留准入保留数。
	require.True(t, g.allowLocked(ipKey("1.1.1.1"), g.policy.IPRatePerMinute, *clock))
	require.Equal(t, 1, g.rate[ipKey("1.1.1.1")].reserved)
	g.allowLocked(ipKey("1.1.1.1"), g.policy.IPRatePerMinute, started)
	require.LessOrEqual(t, g.keysLocked(), g.maxKeys)
	g.mu.Unlock()
	g.releaseRateReservations("alice", "1.1.1.1")
	at.Done()
	g.mu.Lock()
	for _, w := range g.rate {
		require.Zero(t, w.reserved)
	}
	g.mu.Unlock()
}

// 规范 §13.2 第 187 条（D-103）：两个实例共用计数时同一条规则——账号的请求次数超限改为要求验证码；
// 没过验证码的请求把它占的那一次退回去，另一个实例上的别的来源不受影响；退回只退自己记进去的那一次、不新建记录。
func TestShared_187_AccountRateRequiresCaptchaAndRefund(t *testing.T) {
	a, b, now, c := sharedGuards(t)
	for i := 0; i < 3; i++ {
		d := a.Check("alice", "198.51.100.7")
		require.False(t, d.CaptchaRequired)
		d.Attempt.Fail()
	}
	for i := 0; i < 15; i++ {
		d := []*Guard{a, b}[i%2].Check("alice", "198.51.100.7")
		require.False(t, d.RateLimited)
		require.True(t, d.CaptchaRequired)
		d.Attempt.CaptchaFailed()
	}
	victim := b.Check("alice", "203.0.113.5")
	require.False(t, victim.RateLimited)
	require.False(t, victim.CaptchaRequired, "不解验证码的来源不能让别的来源多出一道验证码")
	victim.Attempt.Succeed()

	for i := 0; i < 6; i++ {
		d := []*Guard{a, b}[i%2].Check("alice", fmt.Sprintf("192.0.2.%d", i))
		require.False(t, d.CaptchaRequired, i)
		d.Attempt.Fail()
	}
	over := a.Check("alice", "192.0.2.99")
	require.False(t, over.RateLimited)
	require.True(t, over.CaptchaRequired)
	over.Attempt.CaptchaFailed()
	over.Attempt.CaptchaFailed()
	over.Attempt.Done()
	again := b.Check("alice", "192.0.2.98")
	require.True(t, again.CaptchaRequired, "只退了一次：这一次仍是第 11 次")
	again.Attempt.Done()

	// 窗口换了之后再退回：不动新窗口里的次数，也不新建窗口
	*now = now.Add(61 * time.Second)
	stale := a.Check("carol", "192.0.2.1")
	*now = now.Add(61 * time.Second)
	for i := 0; i < 10; i++ {
		d := []*Guard{a, b}[i%2].Check("carol", fmt.Sprintf("192.0.2.%d", 10+i))
		require.False(t, d.CaptchaRequired, i)
		d.Attempt.Done()
	}
	stale.Attempt.CaptchaFailed()
	d := b.Check("carol", "192.0.2.50")
	require.True(t, d.CaptchaRequired, "旧窗口的尝试退回不能减掉新窗口的次数")
	d.Attempt.Done()

	// 不经过登录限流的准入没有记次数：退回不建账号窗口
	before := logicalCount(t, c, a)
	a.Admit("dave", "192.0.2.1").CaptchaFailed()
	require.Equal(t, before, logicalCount(t, c, a))
	for field := range guardData(t, c) {
		require.NotEqual(t, a.shared.ns+":r:a:"+digest(accountKey("dave")), field)
	}
	// 过了验证码的失败照样累计：这个来源此前的 3 次失败还在，锁定的规则不变
	for i := 0; i < 7; i++ {
		at := []*Guard{a, b}[i%2].Admit("alice", "198.51.100.7")
		require.NotNil(t, at)
		at.Fail()
	}
	require.True(t, b.Check("alice", "198.51.100.7").Locked)
}
