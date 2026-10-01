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
