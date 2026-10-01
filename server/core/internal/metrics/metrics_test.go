package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestCollector(start time.Time, skip ...string) (*Collector, *clock) {
	clk := &clock{t: start}
	c := New(clk.now, skip...)
	return c, clk
}

func TestRecord_MinutesAndWindow(t *testing.T) {
	start := time.Date(2026, 9, 28, 10, 0, 30, 0, time.UTC)
	c, clk := newTestCollector(start)

	c.Record(http.MethodGet, "/a", 200, 10*time.Millisecond)
	c.Record(http.MethodGet, "/a", 404, 30*time.Millisecond)
	c.Record(http.MethodPost, "/b", 500, 2*time.Second)
	clk.t = start.Add(2 * time.Minute) // 中间空一分钟
	c.Record(http.MethodGet, "/a", 200, 5*time.Millisecond)

	s := c.Server(context.Background(), nil)
	m := s.Requests.Minutes
	require.Len(t, m, windowMinutes)
	last := m[len(m)-1]
	require.Equal(t, time.Date(2026, 9, 28, 10, 2, 0, 0, time.UTC), last.At)
	require.EqualValues(t, 1, last.Count)
	require.EqualValues(t, 0, m[len(m)-2].Count, "没有请求的分钟是 0")
	first := m[len(m)-3]
	require.EqualValues(t, 3, first.Count)
	require.EqualValues(t, 1, first.ClientErrors)
	require.EqualValues(t, 1, first.ServerErrors)
	require.InDelta(t, (10.0+30+2000)/3, first.AvgMs, 0.01)
	require.InDelta(t, 2000, first.MaxMs, 0.01)
	require.InDelta(t, 2000, first.P95Ms, 0.01, "3 个里第 95% 落在最慢的那个")
	require.NotNil(t, first.Goroutines)
	require.NotNil(t, first.HeapInuse)

	// 路由按请求数排序，按模板汇总
	require.Equal(t, "/a", s.Requests.Routes[0].Route)
	require.EqualValues(t, 3, s.Requests.Routes[0].Count)
	require.Equal(t, "/b", s.Requests.Routes[1].Route)
	require.EqualValues(t, 1, s.Requests.Routes[1].ServerErrors)

	// 60 分钟之后，旧的分钟滑出窗口
	clk.t = start.Add(61 * time.Minute)
	s = c.Server(context.Background(), nil)
	var total int64
	for _, mi := range s.Requests.Minutes {
		total += mi.Count
	}
	require.EqualValues(t, 1, total, "只剩 10:02 那一个")
	clk.t = start.Add(63 * time.Minute)
	s = c.Server(context.Background(), nil)
	for _, mi := range s.Requests.Minutes {
		require.Zero(t, mi.Count)
	}
	require.Empty(t, s.Requests.Routes)
}

func TestRecord_BoundedRoutesAndMethods(t *testing.T) {
	c, _ := newTestCollector(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC))
	for i := range maxRoutesPerMinute + 50 {
		c.Record(http.MethodGet, "/r"+strconv.Itoa(i), 200, time.Millisecond)
	}
	c.Record("PROPFIND", "/x", 405, time.Millisecond)
	c.mu.Lock()
	b := &c.buckets[c.cur%windowMinutes]
	n := len(b.routes)
	other := b.routes[routeKey{"", otherRoute}]
	c.mu.Unlock()
	require.Equal(t, maxRoutesPerMinute+1, n, "超出上限的归到一个条目")
	require.NotNil(t, other)
	require.EqualValues(t, 51, other.count)
	s := c.Server(context.Background(), nil)
	require.Len(t, s.Requests.Routes, topRoutes)
}

func TestP95(t *testing.T) {
	var a agg
	for range 95 {
		a.add(3, 200) // 落在 ≤5ms 的桶
	}
	for range 5 {
		a.add(800, 200) // 落在 ≤1000ms 的桶
	}
	require.InDelta(t, 5, a.p95(), 0.001)
	a.add(800, 200)
	require.InDelta(t, 800, a.p95(), 0.001, "上界不超过实际最大值")
	var empty agg
	require.Zero(t, empty.p95())
	require.Zero(t, empty.avg())
}

func TestCPUPercent(t *testing.T) {
	at := time.Unix(1000, 0)
	a := sample{at: at, cpu: 0, cpuOK: true}
	b := sample{at: at.Add(2 * time.Second), cpu: 500 * time.Millisecond, cpuOK: true}
	pct, ok := cpuPercent(a, b)
	require.True(t, ok)
	require.InDelta(t, 25/float64(runtime.NumCPU()), pct, 0.001)
	_, ok = cpuPercent(a, sample{at: at.Add(500 * time.Millisecond), cpuOK: true})
	require.False(t, ok, "间隔太短")
	_, ok = cpuPercent(sample{at: at}, b)
	require.False(t, ok, "取不到 CPU 时间")
}

func TestMiddleware_SkipsHealthChecks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := newTestCollector(time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC), "/healthz")
	e := gin.New()
	e.Use(c.Middleware())
	e.GET("/healthz", func(g *gin.Context) { g.Status(http.StatusOK) })
	e.GET("/users/:id", func(g *gin.Context) { g.Status(http.StatusOK) })
	for _, p := range []string{"/healthz", "/users/1", "/users/2", "/nope"} {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	s := c.Server(context.Background(), nil)
	require.EqualValues(t, 3, s.Requests.Minutes[len(s.Requests.Minutes)-1].Count)
	require.Equal(t, "/users/:id", s.Requests.Routes[0].Route, "带 ID 的路径按模板汇总")
	require.EqualValues(t, 2, s.Requests.Routes[0].Count)
	require.Equal(t, "", s.Requests.Routes[1].Route, "没匹配到的路由记为空")
}

func TestServer_RuntimeAndMemory(t *testing.T) {
	c, _ := newTestCollector(time.Now())
	s := c.Server(context.Background(), nil)
	require.NotEmpty(t, s.Runtime.GoVersion)
	require.Positive(t, s.Runtime.NumCPU)
	require.Positive(t, s.Runtime.Goroutines)
	require.Positive(t, s.Memory.Sys)
	require.False(t, s.DB.OK, "没有数据库时为空")
}

// D-031：第三方组件的版本号只到大版本，不带补丁号和发行版后缀。
func TestShortVersions(t *testing.T) {
	for in, want := range map[string]string{
		"go1.26.8":                "go1.26",
		"go1.27":                  "go1.27",
		"go1.27rc1":               "go1.27",
		"devel go1.27-abcdef1234": "go1.27",
		"weird":                   "",
	} {
		require.Equal(t, want, shortGoVersion(in), in)
	}
	for in, want := range map[string]string{
		"8.0.46-0ubuntu0.24.04.4":   "8.0",
		"8.4.3":                     "8.4",
		"10.11.6-MariaDB-0+deb12u1": "10.11 MariaDB",
		"5.7.44-log":                "5.7",
		"":                          "",
		"v":                         "",
		"8":                         "",
		"8.":                        "",
	} {
		require.Equal(t, want, shortDBVersion(in), in)
	}
	c, _ := newTestCollector(time.Now())
	s := c.Server(context.Background(), nil)
	require.True(t, s.Enabled)
	require.Regexp(t, `^go1\.\d+$`, s.Runtime.GoVersion)
}
