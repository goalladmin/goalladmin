// Package loginguard 实现登录防护：限流、失败计数、锁定（规范 §5.7）。
package loginguard

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// 状态的容量（D-056、D-057）。键数（限流键 + 失败/锁定记录）合计不超过 maxKeys，这是硬上限：
//
//   - 键只在准入时新建。一次登录（Check）或解锁（Admit）被接纳时，就把它可能用到的键都建好——两个限流键、
//     "账号 + IP"和"账号"两条失败记录——并把两条失败记录钉住，直到这次尝试结束（Fail、Succeed 或 Done）。
//     之后记失败、加锁只改已有的记录，不再新建键；锁定是失败记录上的一个字段，不单独占键。
//   - 放不下就不接纳：登录按限流回 429，解锁同样回 429，都在核对密码之前。已有的计数和锁定一个都不逐出——
//     逐出活跃记录等于替攻击者清掉失败次数和锁定；钉住的记录清理时也跳过。
//
// 表满时先提前清一次过期数据，但两次提前清理至少隔 forcedGCEvery，避免表满时每个请求都遍历一遍全表。
const (
	defaultMaxKeys = 300_000
	forcedGCEvery  = 10 * time.Second
	gcEvery        = 5 * time.Minute
)

// Guard 按端保存计数器。所有方法并发安全。
type Guard struct {
	mu      sync.Mutex
	policy  portal.LoginPolicy
	now     func() time.Time
	maxKeys int
	rate    map[string]*rateWindow // 每分钟固定窗口：按 IP、按账号
	state   map[string]*record     // 失败与锁定：按"账号 + IP"、按账号
	lastGC  time.Time
	shared  *sharedGuard
}

type rateWindow struct {
	start    time.Time
	count    int
	reserved int // 共享准入往返期间保留容量；只在 Guard 锁内改动
}

// record 是一个维度（"账号 + IP"或账号）的失败时间点和锁定截止时间。inflight 是钉住它的在途尝试数。
type record struct {
	fails    []time.Time
	until    time.Time
	inflight int
}

func (r *record) idle() bool { return r.inflight == 0 && len(r.fails) == 0 && r.until.IsZero() }

// New 创建守卫。
func New(policy portal.LoginPolicy, now func() time.Time) *Guard {
	if now == nil {
		now = time.Now
	}
	return &Guard{
		policy:  policy.Normalized(),
		now:     now,
		maxKeys: defaultMaxKeys,
		rate:    map[string]*rateWindow{},
		state:   map[string]*record{},
		lastGC:  now(),
	}
}

// Policy 返回生效的策略。
func (g *Guard) Policy() portal.LoginPolicy { return g.policy }

// 键的构造。IP 先归成限流用的键：IPv6 按 /64 网段（D-058）。
func ipKey(ip string) string             { return "ip:" + ratelimit.IPKey(ip) }
func accountKey(username string) string  { return "acct:" + username }
func pairKey(username, ip string) string { return "pair:" + username + "@" + ratelimit.IPKey(ip) }

// Decision 是登录前的判定结果。
type Decision struct {
	RateLimited     bool          // 来源 IP 触发限流，或容量已满：应返回 429
	Locked          bool          // 被锁定，应返回 CodeLocked
	LockedUntil     time.Time     // 锁定截止
	CaptchaRequired bool          // 需要验证码：策略要求、这个来源失败过几次，或者这个账号的请求超过了每分钟的次数（D-103）
	RetryAfter      time.Duration // 限流时建议的等待时间
	// Attempt 是这次被接纳的尝试，只在既没限流也没锁定时非空。调用方必须以 Fail、Succeed 或 Done 结束它
	// （通常 defer Done）：它钉住的记录在结束前不会被清理。
	Attempt *Attempt
}

// Attempt 是一次被接纳的密码核对。结束方法只有第一次调用生效。
type Attempt struct {
	g          *Guard
	pair, acct string
	closed     bool
	remote     *sharedAttempt
	// acctWindow 是这次请求记进去的那个账号窗口的起点（没记的为零值）：验证码没过时凭它把这一次退回去（D-103）
	acctWindow time.Time
}

// Check 在验证密码之前调用：统计本次请求并给出判定。
func (g *Guard) Check(username, ip string) Decision {
	return g.CheckContext(context.Background(), username, ip)
}

// CheckContext 使用共享计数；未配置 Redis 或本次操作失败时使用本实例的影子。
func (g *Guard) CheckContext(ctx context.Context, username, ip string) Decision {
	if g.shared != nil {
		return g.checkShared(ctx, username, ip)
	}
	return g.checkMemory(username, ip)
}

func (g *Guard) checkMemory(username, ip string) Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.gcLocked(now, false)

	// 先看锁定：被锁的账号直接告知锁定时间，比 429 更有信息量
	if until, ok := g.lockedLocked(pairKey(username, ip), now); ok {
		return Decision{Locked: true, LockedUntil: until}
	}
	if until, ok := g.lockedLocked(accountKey(username), now); ok {
		return Decision{Locked: true, LockedUntil: until}
	}
	// 放不下这次尝试要用的键：不接纳（D-056、D-057），这时一个键都不建
	if !g.roomLocked(now, []string{ipKey(ip), accountKey(username)}, []string{pairKey(username, ip), accountKey(username)}) {
		return Decision{RateLimited: true, RetryAfter: time.Minute}
	}
	// 限流按请求次数计，先记再判。来源超限直接拒绝；账号超限不拒绝，改为这次必须带验证码（D-103）：
	// 账号的次数是所有来源合计的，据此拒绝等于让任何一个来源都能把这个账号的登录挡住。
	if !g.allowLocked(ipKey(ip), g.policy.IPRatePerMinute, now) {
		return Decision{RateLimited: true, RetryAfter: time.Minute}
	}
	over := !g.allowLocked(accountKey(username), g.policy.AccountRatePerMinute, now)
	at := g.pinLocked(username, ip)
	at.acctWindow = g.rate[accountKey(username)].start
	return Decision{
		CaptchaRequired: over || g.policy.CaptchaAlways || g.failuresLocked(at.pair, now) >= g.policy.CaptchaAfterFailures,
		Attempt:         at,
	}
}

// Admit 为不经过登录限流的密码核对（锁屏解锁）预留失败记录。放不下时返回 nil，调用方应在核对密码之前拒绝。
func (g *Guard) Admit(username, ip string) *Attempt {
	return g.AdmitContext(context.Background(), username, ip)
}

// AdmitContext 只预留失败记录，不占登录请求次数。
func (g *Guard) AdmitContext(ctx context.Context, username, ip string) *Attempt {
	if g.shared != nil {
		return g.admitShared(ctx, username, ip)
	}
	return g.admitMemory(username, ip)
}

func (g *Guard) admitMemory(username, ip string) *Attempt {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	g.gcLocked(now, false)
	if !g.roomLocked(now, nil, []string{pairKey(username, ip), accountKey(username)}) {
		return nil
	}
	return g.pinLocked(username, ip)
}

// pinLocked 建好（或取到）两条失败记录并钉住。调用前已确认放得下。
func (g *Guard) pinLocked(username, ip string) *Attempt {
	at := &Attempt{g: g, pair: pairKey(username, ip), acct: accountKey(username)}
	for _, k := range []string{at.pair, at.acct} {
		r := g.state[k]
		if r == nil {
			r = &record{}
			g.state[k] = r
		}
		r.inflight++
	}
	return at
}

// Fail 记录一次失败，并在达到阈值时加锁。返回本次失败后是否需要验证码、是否已锁定。
func (a *Attempt) Fail() (captchaRequired, locked bool) {
	g := a.g
	g.mu.Lock()
	if a.closed {
		g.mu.Unlock()
		return false, false
	}
	now := g.now()
	pr, ar := g.state[a.pair], g.state[a.acct] // 钉住的记录一定还在
	pr.fails = append(pr.fails, now)
	ar.fails = append(ar.fails, now)
	pair := g.failuresLocked(a.pair, now)
	acct := g.failuresLocked(a.acct, now)
	if pair >= g.policy.LockAfterFailures {
		pr.until = now.Add(g.policy.LockDuration)
		locked = true
	}
	if acct >= g.policy.AccountLockAfter {
		ar.until = now.Add(g.policy.LockDuration)
		locked = true
	}
	a.releaseLocked()
	captchaRequired = g.policy.CaptchaAlways || pair >= g.policy.CaptchaAfterFailures
	g.mu.Unlock()
	if a.remote != nil {
		if result, ok := a.remote.finish("fail", now); ok {
			return result.captcha, result.locked
		}
	}
	return captchaRequired, locked
}

// Succeed 登录成功：清掉该账号 + IP 的失败计数和锁定（账号级累计计数保留，防止用成功登录洗掉分布式尝试）。
func (a *Attempt) Succeed() {
	g := a.g
	g.mu.Lock()
	if a.closed {
		g.mu.Unlock()
		return
	}
	r := g.state[a.pair]
	r.fails, r.until = nil, time.Time{}
	a.releaseLocked()
	now := g.now()
	g.mu.Unlock()
	if a.remote != nil {
		a.remote.finish("succeed", now)
	}
}

// Done 结束尝试而不记结果（验证码不对、出错）。可以 defer，在 Fail、Succeed 之后调用不再有作用；nil 也可以调用。
func (a *Attempt) Done() {
	if a == nil {
		return
	}
	a.g.mu.Lock()
	if a.closed {
		a.g.mu.Unlock()
		return
	}
	a.releaseLocked()
	now := a.g.now()
	a.g.mu.Unlock()
	if a.remote != nil {
		a.remote.finish("done", now)
	}
}

// CaptchaFailed 结束一次没通过验证码的尝试：不记失败，并把它占的那一次账号请求次数退回去（D-103）——
// 账号的次数超限之后要验证码，没过验证码的请求如果也算数，不解验证码的人照样能让这个账号的每次登录都要验证码。
// 来源 IP 的次数不退。nil 也可以调用；在别的结束方法之后调用不再有作用。
func (a *Attempt) CaptchaFailed() {
	if a == nil {
		return
	}
	g := a.g
	g.mu.Lock()
	if a.closed {
		g.mu.Unlock()
		return
	}
	if w := g.rate[a.acct]; w != nil && !a.acctWindow.IsZero() && w.start.Equal(a.acctWindow) && w.count > 0 {
		w.count--
	}
	a.releaseLocked()
	now := g.now()
	g.mu.Unlock()
	if a.remote != nil {
		a.remote.finish("refund", now)
	}
}

// releaseLocked 解除钉住；没有内容的记录顺手删掉。
func (a *Attempt) releaseLocked() {
	a.closed = true
	for _, k := range []string{a.pair, a.acct} {
		if r := a.g.state[k]; r != nil {
			r.inflight--
			if r.idle() {
				delete(a.g.state, k)
			}
		}
	}
}

// Unlock 清掉本实例账号和它所有"账号 + IP"的失败计数与锁定。
func (g *Guard) Unlock(username string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	prefix := "pair:" + username + "@"
	for k, r := range g.state {
		if k == accountKey(username) || strings.HasPrefix(k, prefix) {
			r.fails, r.until = nil, time.Time{}
			if r.idle() {
				delete(g.state, k)
			}
		}
	}
}

func (g *Guard) allowLocked(key string, limit int, now time.Time) bool {
	w := g.rate[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		reserved := 0
		if w != nil {
			reserved = w.reserved
		}
		g.rate[key] = &rateWindow{start: now, count: 1, reserved: reserved}
		return true
	}
	w.count++
	return w.count <= limit
}

func (g *Guard) lockedLocked(key string, now time.Time) (time.Time, bool) {
	r := g.state[key]
	if r == nil || r.until.IsZero() {
		return time.Time{}, false
	}
	if now.After(r.until) {
		r.until = time.Time{}
		g.dropIfIdleLocked(key, now)
		return time.Time{}, false
	}
	return r.until, true
}

func (g *Guard) failuresLocked(key string, now time.Time) int {
	r := g.state[key]
	if r == nil {
		return 0
	}
	cut := now.Add(-g.policy.Window)
	i := 0
	for i < len(r.fails) && r.fails[i].Before(cut) {
		i++
	}
	if i > 0 {
		r.fails = r.fails[i:]
		if len(r.fails) == 0 {
			r.fails = nil
		}
	}
	return len(r.fails)
}

// dropIfIdleLocked 删掉过期、没人钉住的记录。
func (g *Guard) dropIfIdleLocked(key string, now time.Time) {
	r := g.state[key]
	if r == nil {
		return
	}
	g.failuresLocked(key, now)
	if !r.until.IsZero() && now.After(r.until) {
		r.until = time.Time{}
	}
	if r.idle() {
		delete(g.state, key)
	}
}

// keysLocked 是键数合计。
func (g *Guard) keysLocked() int { return len(g.rate) + len(g.state) }

// roomLocked 报告放不放得下这次尝试要新建的键（限流键 rateKeys、失败记录 stateKeys 里还不存在的）。
// 放不下时先提前清一次过期数据再看。
func (g *Guard) roomLocked(now time.Time, rateKeys, stateKeys []string) bool {
	need := func() int {
		n := 0
		for _, k := range rateKeys {
			if _, ok := g.rate[k]; !ok {
				n++
			}
		}
		for _, k := range stateKeys {
			if _, ok := g.state[k]; !ok {
				n++
			}
		}
		return n
	}
	n := need()
	if n == 0 || g.keysLocked()+n <= g.maxKeys {
		return true
	}
	g.gcLocked(now, true)
	n = need()
	return n == 0 || g.keysLocked()+n <= g.maxKeys
}

// gcLocked 每隔一段时间清一次过期数据，避免内存无限增长。force 表示表满了要提前清（仍受 forcedGCEvery 限制）。
func (g *Guard) gcLocked(now time.Time, force bool) {
	every := gcEvery
	if force {
		every = forcedGCEvery
	}
	if now.Sub(g.lastGC) < every {
		return
	}
	g.lastGC = now
	for k, w := range g.rate {
		if w.reserved == 0 && now.Sub(w.start) >= time.Minute {
			delete(g.rate, k)
		}
	}
	for k := range g.state {
		g.dropIfIdleLocked(k, now)
	}
}
