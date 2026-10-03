package ttlcache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCache_ExpiresAndDeletes(t *testing.T) {
	now := time.Unix(1000, 0)
	c := New[int](time.Minute, func() time.Time { return now })
	c.Set("a", 1)
	v, ok := c.Get("a")
	require.True(t, ok)
	require.Equal(t, 1, v)

	now = now.Add(2 * time.Minute)
	_, ok = c.Get("a")
	require.False(t, ok, "过期后取不到")

	c.Set("b", 2)
	c.Delete("b")
	_, ok = c.Get("b")
	require.False(t, ok)
}

// 先读数据库再写缓存：读的期间发生了失效（例如会话被锁定），读到的旧状态不能写回去。
func TestCache_SetIfGenDropsStaleWrites(t *testing.T) {
	c := New[string](time.Minute, nil)
	gen := c.Gen()
	require.True(t, c.SetIfGen("s", "fresh", gen))
	v, _ := c.Get("s")
	require.Equal(t, "fresh", v)

	stale := c.Gen()
	c.Delete("s") // 另一个请求让它失效
	require.False(t, c.SetIfGen("s", "stale", stale))
	_, ok := c.Get("s")
	require.False(t, ok)

	stale = c.Gen()
	c.Flush()
	require.False(t, c.SetIfGen("s", "stale", stale))

	// 失效之后重新取的代数可以写
	require.True(t, c.SetIfGen("s", "new", c.Gen()))
}

// Drop：条目在就删、代数加一（这期间读到的旧状态不写回去）；条目不在或已过期时什么都不做、代数不变——
// 反复对一个不在缓存里的键调用它，不会让别的键读到的状态一直写不回缓存（D-073）。
func TestCache_DropOnlyBumpsGenWhenPresent(t *testing.T) {
	now := time.Unix(1000, 0)
	c := New[string](time.Minute, func() time.Time { return now })
	c.Set("a", "1")
	gen := c.Gen()
	require.True(t, c.Drop("a"))
	_, ok := c.Get("a")
	require.False(t, ok)
	require.NotEqual(t, gen, c.Gen())
	require.False(t, c.SetIfGen("a", "stale", gen), "删掉之前读到的不写回去")

	gen = c.Gen()
	for range 3 {
		require.False(t, c.Drop("a"), "已经不在了")
		require.False(t, c.Drop("never"))
	}
	require.Equal(t, gen, c.Gen())
	require.True(t, c.SetIfGen("b", "fresh", gen), "别的键照常写得进去")

	// 过期的条目算不在：清掉，但代数不变
	c.Set("c", "1")
	gen = c.Gen()
	now = now.Add(2 * time.Minute)
	require.False(t, c.Drop("c"))
	require.Equal(t, gen, c.Gen())
	// 对照：Delete 不管在不在都加代数（它是在宣布一次失效，在途的读可能正要把旧状态写回来）
	c.Delete("never")
	require.NotEqual(t, gen, c.Gen())
}

// 181（D-097）：UpdateIfGen 只换值，不延长有效期；条目不在、已过期或期间发生过失效时不写。
func TestCache_181_UpdateIfGenKeepsExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	c := New[int](15*time.Second, func() time.Time { return now })
	c.Set("a", 1)

	now = now.Add(14 * time.Second)
	require.True(t, c.UpdateIfGen("a", 2, c.Gen()))
	v, ok := c.Get("a")
	require.True(t, ok)
	require.Equal(t, 2, v)

	now = now.Add(2 * time.Second) // 距第一次写入 16 秒：按原来的过期时间到期，没有因为中途改过值多活一个周期
	_, ok = c.Get("a")
	require.False(t, ok)
	require.False(t, c.UpdateIfGen("a", 3, c.Gen()), "已过期的不写")
	require.False(t, c.UpdateIfGen("missing", 3, c.Gen()), "不在的不写")
	_, ok = c.Get("missing")
	require.False(t, ok)

	c.Set("b", 1)
	gen := c.Gen()
	c.Delete("other")
	require.False(t, c.UpdateIfGen("b", 9, gen), "期间有过失效就不写")
	v, _ = c.Get("b")
	require.Equal(t, 1, v)
}
