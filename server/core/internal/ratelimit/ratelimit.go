// Package ratelimit 提供简单限流（D-055、D-076）：按键的固定窗口计数，和进程内的并发上限。
// 只用在登录防护覆盖不到、又会做昂贵计算的入口（核对密码、生成密码哈希、生成验证码图片）。
package ratelimit

import (
	"context"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

// IPKey 把客户端 IP 归成限流用的键（D-058）：IPv4 按单个地址；IPv6 按 /64 网段——一个 IPv6 用户通常拥有整个 /64，
// 按单个地址计数的话，换一个地址就是一份新配额。IPv4 映射的 IPv6 地址按 IPv4 算；解析不了的原样返回。
func IPKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// Window 是按键的固定窗口计数：每个键在一个窗口内最多 limit 次。键的数量有上限，满了先清掉过期的，仍然满就拒绝新键
// （不让大量不同的键把内存撑满；拒绝新键只影响限流对象本身，不会放过已经在计数的键）。
type Window struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time
	m       map[string]*entry
	expiry  windowExpiry
	shared  *sharedWindow
}

type entry struct {
	n       int
	pending int // 共享准入在途占用；故障回退也受它约束
	start   time.Time
	index   int // 本地过期堆位置，移除后为 -1
}

// New 创建计数器。now 为 nil 时用 time.Now。
func New(limit int, window time.Duration, maxKeys int, now func() time.Time) *Window {
	if now == nil {
		now = time.Now
	}
	return &Window{limit: limit, window: window, maxKeys: maxKeys, now: now, m: map[string]*entry{}}
}

// Allow 为 key 占用一次机会：窗口内还有余量时计一次并返回 true，否则返回 false。先占用、后做事，并发的请求不能一起挤过去。
func (w *Window) Allow(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	e, ok := w.m[key]
	if ok && now.Sub(e.start) >= w.window {
		w.removeLocal(key, e)
		ok = false
	}
	if !ok {
		if len(w.m) >= w.maxKeys {
			w.gcLocked(now)
			if len(w.m) >= w.maxKeys {
				return false
			}
		}
		e = &entry{n: 1, start: now, index: -1}
		w.m[key] = e
		w.trackLocal(key, e)
		return true
	}
	if e.n >= w.limit {
		return false
	}
	e.n++
	return true
}

// Undo 退回 key 的一次机会：Allow 之后这次没有做成、不该算数时调用（例如后面的并发闸门满了，什么都没算，D-068）。
func (w *Window) Undo(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.m[key]; ok && e.n > 0 {
		e.n--
	}
}

// Reset 清掉 key 的计数（例如改密成功之后）。
func (w *Window) Reset(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e, ok := w.m[key]; ok {
		w.removeLocal(key, e)
	}
}

// Gate 是并发上限：TryEnter 满了就直接拒绝，不让请求堆在昂贵的计算前面；Enter 满了可以等一小会儿（有时限、有人数上限）。
type Gate struct {
	slots   chan struct{}
	waiting atomic.Int32
}

// NewGate 创建上限为 n 的并发闸门。
func NewGate(n int) *Gate { return &Gate{slots: make(chan struct{}, n)} }

// TryEnter 占一个位置；满了返回 false。占到之后必须调用 Leave。
func (g *Gate) TryEnter() bool {
	select {
	case g.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// maxWaitersPerSlot：同时在 Enter 里等的最多是位置数的这么多倍，再多的直接拒绝——等的人占着连接和内存，不能无限堆。
const maxWaitersPerSlot = 4

// Enter 占一个位置；满了最多等 wait，等到了返回 true（D-071）。等够了、ctx 取消了、同时在等的已经太多时返回 false。
// 先来的先得：让出来的位置优先给已经在等的，后来的 TryEnter 抢不走。占到之后必须调用 Leave。
func (g *Gate) Enter(ctx context.Context, wait time.Duration) bool {
	if g.TryEnter() {
		return true
	}
	if wait <= 0 {
		return false
	}
	if int(g.waiting.Add(1)) > maxWaitersPerSlot*cap(g.slots) {
		g.waiting.Add(-1)
		return false
	}
	defer g.waiting.Add(-1)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case g.slots <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

// Leave 让出位置。
func (g *Gate) Leave() { <-g.slots }

// Budget 是一组计算位置，分两类用（D-071）：
//   - 前一类（登录、解锁）可以用全部位置，满了最多等 wait；
//   - 后一类（本人改密、后台生成密码哈希）最多占一半位置（至少 1 个），满了直接拒绝。
//
// 所以至少有一半位置留给前一类。位置总数是 1 时没有一半可留，两类共用那一个。
type Budget struct {
	all  *Gate
	back *Gate
	wait time.Duration
}

// NewBudget 创建 n 个位置的预算；wait 是前一类满了之后最多等多久，0 表示不等。
func NewBudget(n int, wait time.Duration) *Budget {
	if n < 1 {
		n = 1
	}
	return &Budget{all: NewGate(n), back: NewGate(max(1, n/2)), wait: wait}
}

// EnterFront 为前一类占一个位置，满了最多等一小会儿。ok 为 true 时必须调用 leave（多调几次也只让出一次）。
func (b *Budget) EnterFront(ctx context.Context) (leave func(), ok bool) {
	if !b.all.Enter(ctx, b.wait) {
		return nil, false
	}
	return once(b.all.Leave), true
}

// TryEnterFront 为前一类占一个位置，满了不等。
func (b *Budget) TryEnterFront() (leave func(), ok bool) {
	if !b.all.TryEnter() {
		return nil, false
	}
	return once(b.all.Leave), true
}

// EnterBackground 为后一类占一个位置：后一类的那一半满了、或者全部位置满了都直接返回 false。
// ok 为 true 时必须调用 leave（多调几次也只让出一次）。
func (b *Budget) EnterBackground() (leave func(), ok bool) {
	if !b.back.TryEnter() {
		return nil, false
	}
	if !b.all.TryEnter() {
		b.back.Leave()
		return nil, false
	}
	return once(func() { b.all.Leave(); b.back.Leave() }), true
}

// InUse 返回此刻被占着的位置数：全部的，和其中后一类的。
func (b *Budget) InUse() (all, background int) { return len(b.all.slots), len(b.back.slots) }

// once 让 fn 只执行一次：让出位置的函数既会被提前调用（算完就让）、又挂在 defer 上兜底。
func once(fn func()) func() {
	var done atomic.Bool
	return func() {
		if done.CompareAndSwap(false, true) {
			fn()
		}
	}
}

// KeyedGate 是按键的不排队并发上限（D-068）：每个键同时最多 limit 个，满了直接拒绝。没有在用的键不占内存，
// 键的数量不会超过同时在处理的请求数。
type KeyedGate struct {
	mu    sync.Mutex
	limit int
	m     map[string]int
}

// NewKeyedGate 创建每个键上限为 limit 的并发闸门。
func NewKeyedGate(limit int) *KeyedGate { return &KeyedGate{limit: limit, m: map[string]int{}} }

// TryEnter 为 key 占一个位置；这个键满了返回 false。占到之后必须用同一个键调用 Leave。
func (g *KeyedGate) TryEnter(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m[key] >= g.limit {
		return false
	}
	g.m[key]++
	return true
}

// Leave 让出 key 的一个位置。
func (g *KeyedGate) Leave(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n := g.m[key]; n <= 1 {
		delete(g.m, key)
	} else {
		g.m[key] = n - 1
	}
}
