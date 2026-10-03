// Package ttlcache 是一个带过期时间的进程内缓存，供认证链路缓存会话和账号状态。
// 配了 Redis 时仍使用本地内存，由失效通知和 TTL 共同保持新鲜（D-075）。
package ttlcache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	val     V
	expires time.Time
}

// Cache 是泛型 TTL 缓存。
type Cache[V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]entry[V]
	hits  int
	// gen 在每次 Delete / Flush 时加一。先读数据库再写缓存的调用方在读之前取 Gen()，
	// 写入时用 SetIfGen：期间有过失效（例如会话刚被锁定或吊销）就不写，免得把旧状态写回去。
	gen uint64
}

// New 创建缓存。now 为 nil 时用 time.Now。
func New[V any](ttl time.Duration, now func() time.Time) *Cache[V] {
	if now == nil {
		now = time.Now
	}
	return &Cache[V]{ttl: ttl, now: now, items: map[string]entry[V]{}}
}

// Get 返回缓存值；过期或不存在时 ok 为 false。
func (c *Cache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || c.now().After(e.expires) {
		if ok {
			delete(c.items, key)
		}
		var zero V
		return zero, false
	}
	return e.val, true
}

// Set 写入缓存。
func (c *Cache[V]) Set(key string, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, val)
}

func (c *Cache[V]) setLocked(key string, val V) {
	c.items[key] = entry[V]{val: val, expires: c.now().Add(c.ttl)}
	c.hits++
	if c.hits%1024 == 0 {
		c.sweepLocked()
	}
}

// Gen 返回当前的失效代数，配合 SetIfGen 使用。
func (c *Cache[V]) Gen() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// SetIfGen 只有在取 gen 之后没有发生过 Delete / Flush 时才写入，返回是否写入。
func (c *Cache[V]) SetIfGen(key string, val V, gen uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return false
	}
	c.setLocked(key, val)
	return true
}

// UpdateIfGen 换掉一项的值、不动它的过期时间：这一项还在、没过期，并且取 gen 之后没有发生过 Delete / Flush 时才写，
// 返回是否写入。用在"顺手改一下缓存里的值"的场合（D-097）：用 SetIfGen 的话过期时间会重新算，
// 一条本该到期的旧状态就多活一个周期。
func (c *Cache[V]) UpdateIfGen(key string, val V, gen uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen {
		return false
	}
	e, ok := c.items[key]
	if !ok || c.now().After(e.expires) {
		return false
	}
	c.items[key] = entry[V]{val: val, expires: e.expires}
	return true
}

// Delete 删除一项。
func (c *Cache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
	c.gen++
}

// Drop 删除一项，返回它原来在不在。和 Delete 的区别：这一项本来就不在（或已过期）时什么都不做，代数不变。
// 用在"读到的状态和缓存里的不一样，把缓存里那条去掉"这种场合——调用方自己刚读过库，不是在宣布一次失效；
// 条目不在时也加代数的话，反复触发它的请求会让别的请求读到的状态一直写不回缓存。
func (c *Cache[V]) Drop(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return false
	}
	delete(c.items, key)
	if c.now().After(e.expires) {
		return false
	}
	c.gen++
	return true
}

// Flush 清空。
func (c *Cache[V]) Flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = map[string]entry[V]{}
	c.gen++
}

func (c *Cache[V]) sweepLocked() {
	now := c.now()
	for k, e := range c.items {
		if now.After(e.expires) {
			delete(c.items, k)
		}
	}
}
