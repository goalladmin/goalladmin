package loginguard

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/redis/go-redis/v9"
)

const attemptLease = 15 * time.Minute

type sharedGuard struct {
	client *redisx.Client
	ns     string
}

type sharedAttempt struct {
	guard    *sharedGuard
	policy   portal.LoginPolicy
	id       string
	pair     string
	acct     string
	deadline int64
}

type sharedResult struct {
	code    int64 // 0 票据已结束或过期；1 准入；2 锁定；3 限流；4 容量不足
	until   int64
	captcha bool
	locked  bool
}

// NewShared 创建共享守卫。每端独立预算；无 Redis 时与 New 一致。
func NewShared(policy portal.LoginPolicy, now func() time.Time, client *redisx.Client, namespace string) *Guard {
	g := New(policy, now)
	if client != nil {
		g.shared = &sharedGuard{client: client, ns: digest(namespace)}
	}
	return g
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomTicket() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func guardKeys(key func(...string) string) []string {
	return []string{key("guard", "{counts}", "data"), key("guard", "{counts}", "expiry"), key("guard", "{counts}", "capacity")}
}

// prepareShadow 先保留合法的本地容量。共享结果可以覆盖旧的本地锁定，但不能绕过本地硬预算。
// 即使本地策略已拒绝，也暂时钉住两条记录，以便共享准入后收尾不新建任何键。
func (g *Guard) prepareShadow(username, ip string, login bool) (Decision, *Attempt, time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.gcLocked(now, false)
	var rates []string
	if login {
		rates = []string{ipKey(ip), accountKey(username)}
	}
	states := []string{pairKey(username, ip), accountKey(username)}
	var local Decision
	if login {
		if until, ok := g.lockedLocked(states[0], now); ok {
			local = Decision{Locked: true, LockedUntil: until}
		} else if until, ok := g.lockedLocked(states[1], now); ok {
			local = Decision{Locked: true, LockedUntil: until}
		}
	}
	if !g.roomLocked(now, rates, states) {
		if !local.Locked {
			local = Decision{RateLimited: true, RetryAfter: time.Minute}
		}
		return local, nil, now
	}
	// 零次数的窗口也占预算；在 Redis 往返期间，其他请求不能用掉这份容量。
	for _, k := range rates {
		if g.rate[k] == nil {
			g.rate[k] = &rateWindow{start: now}
		}
		g.rate[k].reserved++
	}
	at := g.pinLocked(username, ip)
	over := false
	if login && !local.Locked {
		// 和 checkMemory 同一条规则（D-103）：来源超限拒绝，账号超限改为要求验证码
		if !g.allowLocked(rates[0], g.policy.IPRatePerMinute, now) {
			local = Decision{RateLimited: true, RetryAfter: time.Minute}
		} else {
			over = !g.allowLocked(rates[1], g.policy.AccountRatePerMinute, now)
			at.acctWindow = g.rate[rates[1]].start
		}
	}
	if !local.Locked && !local.RateLimited {
		local.Attempt = at
		local.CaptchaRequired = over || g.policy.CaptchaAlways || g.failuresLocked(at.pair, now) >= g.policy.CaptchaAfterFailures
	}
	return local, at, now
}

func (g *Guard) checkShared(ctx context.Context, username, ip string) Decision {
	if !g.shared.client.Available() {
		return g.checkMemory(username, ip)
	}
	local, shadow, now := g.prepareShadow(username, ip, true)
	if shadow == nil {
		return local
	}
	defer g.releaseRateReservations(username, ip)
	result, remote, err := g.begin(ctx, username, ip, now, "check")
	if err != nil {
		if local.Attempt == nil {
			shadow.Done()
		}
		return local
	}
	if result.code != 1 {
		shadow.Done()
		return result.decision()
	}
	if local.Locked {
		// 远端允许时仍记录本实例请求次数，保留已有失败与锁定供故障回退。
		g.mu.Lock()
		if g.allowLocked(ipKey(ip), g.policy.IPRatePerMinute, now) {
			g.allowLocked(accountKey(username), g.policy.AccountRatePerMinute, now)
			shadow.acctWindow = g.rate[accountKey(username)].start
		}
		g.mu.Unlock()
	}
	shadow.remote = remote
	d := result.decision()
	d.Attempt = shadow
	return d
}

func (g *Guard) releaseRateReservations(username, ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, key := range []string{ipKey(ip), accountKey(username)} {
		if w := g.rate[key]; w != nil {
			w.reserved--
		}
	}
}

func (g *Guard) admitShared(ctx context.Context, username, ip string) *Attempt {
	if !g.shared.client.Available() {
		return g.admitMemory(username, ip)
	}
	_, shadow, now := g.prepareShadow(username, ip, false)
	if shadow == nil {
		return nil
	}
	result, remote, err := g.begin(ctx, username, ip, now, "admit")
	if err != nil {
		return shadow
	}
	if result.code != 1 {
		shadow.Done()
		return nil
	}
	shadow.remote = remote
	return shadow
}

func (r sharedResult) decision() Decision {
	if r.code == 2 {
		return Decision{Locked: true, LockedUntil: time.UnixMilli(r.until).UTC()}
	}
	if r.code == 3 || r.code == 4 {
		return Decision{RateLimited: true, RetryAfter: time.Minute}
	}
	return Decision{CaptchaRequired: r.captcha}
}

func (g *Guard) begin(ctx context.Context, username, ip string, now time.Time, op string) (sharedResult, *sharedAttempt, error) {
	id, err := randomTicket()
	if err != nil {
		return sharedResult{}, nil, err
	}
	remote := &sharedAttempt{
		guard: g.shared, policy: g.policy, id: id,
		pair: digest(pairKey(username, ip)), acct: digest(accountKey(username)),
		deadline: now.Add(attemptLease).UnixMilli(),
	}
	result, err := remote.run(ctx, op, now, g.maxKeys, digest(ipKey(ip)))
	return result, remote, err
}

// finish 只使用原来的共享票据；故障期间的内存尝试不会在恢复后新建共享状态。
func (a *sharedAttempt) finish(op string, now time.Time) (sharedResult, bool) {
	if now.UnixMilli() >= a.deadline {
		return sharedResult{}, false
	}
	// 调用方可能因请求取消而走 Done；以有时限的独立上下文释放原票据。
	r, err := a.run(context.Background(), op, now, 0, "")
	return r, err == nil && r.code == 1
}

func (a *sharedAttempt) run(ctx context.Context, op string, now time.Time, maxKeys int, ip string) (sharedResult, error) {
	var value any
	err := a.guard.client.Do(ctx, func(ctx context.Context, rdb redis.Cmdable) error {
		var err error
		value, err = rdb.Eval(ctx, guardScript, guardKeys(a.guard.client.Key),
			op, a.guard.ns, now.UnixMilli(), maxKeys, ip, a.acct, a.pair, a.id,
			a.policy.Window.Milliseconds(), a.policy.LockDuration.Milliseconds(),
			a.policy.IPRatePerMinute, a.policy.AccountRatePerMinute,
			a.policy.CaptchaAfterFailures, a.policy.LockAfterFailures, a.policy.AccountLockAfter,
			a.policy.CaptchaAlways, attemptLease.Milliseconds()).Result()
		return err
	})
	if err != nil {
		return sharedResult{}, err
	}
	items, ok := value.([]any)
	if !ok || len(items) != 4 {
		return sharedResult{}, errors.New("invalid login guard script result")
	}
	var numbers [4]int64
	for i := range items {
		var ok bool
		numbers[i], ok = items[i].(int64)
		if !ok {
			return sharedResult{}, errors.New("invalid login guard script value")
		}
	}
	if numbers[0] < 0 || numbers[0] > 4 || ((op == "check" || op == "admit") && numbers[0] == 0) ||
		(numbers[2] != 0 && numbers[2] != 1) || (numbers[3] != 0 && numbers[3] != 1) {
		return sharedResult{}, fmt.Errorf("invalid login guard status %d", numbers[0])
	}
	return sharedResult{code: numbers[0], until: numbers[1], captcha: numbers[2] != 0, locked: numbers[3] != 0}, nil
}

// Probe 在业务固定键上验证脚本内权限；固定专用字段不占用户预算，成功后清除。
func Probe(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
	return rdb.Eval(ctx, guardScript, guardKeys(key), "probe", "_probe").Err()
}
