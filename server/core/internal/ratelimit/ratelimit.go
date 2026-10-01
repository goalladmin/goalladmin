// Package ratelimit 是进程内的简单限流（D-055）：按键的固定窗口计数，和一个不排队的并发上限。
// 只用在登录防护覆盖不到、又会做昂贵计算的入口（改密核对旧密码、生成验证码图片）。
package ratelimit

import (
	"net/netip"
	"sync"
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
}

type entry struct {
	n     int
	start time.Time
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
		ok = false
	}
	if !ok {
		if _, exists := w.m[key]; !exists && len(w.m) >= w.maxKeys {
			w.gcLocked(now)
			if len(w.m) >= w.maxKeys {
				return false
			}
		}
		w.m[key] = &entry{n: 1, start: now}
		return true
	}
	if e.n >= w.limit {
		return false
	}
	e.n++
	return true
}

// Reset 清掉 key 的计数（例如改密成功之后）。
func (w *Window) Reset(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.m, key)
}

func (w *Window) gcLocked(now time.Time) {
	for k, e := range w.m {
		if now.Sub(e.start) >= w.window {
			delete(w.m, k)
		}
	}
}

// Gate 是不排队的并发上限：满了就直接拒绝，不让请求堆在昂贵的计算前面。
type Gate struct{ slots chan struct{} }

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

// Leave 让出位置。
func (g *Gate) Leave() { <-g.slots }
