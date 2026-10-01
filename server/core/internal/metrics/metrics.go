// Package metrics 在进程内采集服务器状态（D-030）：每个请求按分钟、按路由模板计数，保留最近 60 分钟；
// CPU、内存、协程数、GC、数据库连接池在读取时现取。对外经 core/monitor 的类型交出去。
//
// 内存占用有上限：固定 60 个分钟桶，每个桶最多 maxRoutesPerMinute 个路由，超出的归到一个"其他"条目；
// 路由按模板（gin 的 FullPath）计，不按原始路径，所以带 ID 的路径不会让条目数增长。
package metrics

import (
	"context"
	"database/sql"
	"net/http"
	"runtime"
	"runtime/debug"
	rtmetrics "runtime/metrics"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/monitor"
)

const (
	windowMinutes      = 60
	maxRoutesPerMinute = 256
	topRoutes          = 10
	otherRoute         = "(other)"
	pingTimeout        = 2 * time.Second
	numBuckets         = 13 // 12 个上界 + 一个"更慢"
)

// bucketBound 返回第 i 个耗时分桶的上界（毫秒）；最后一个桶没有上界，返回 0。
func bucketBound(i int) float64 {
	switch i {
	case 0:
		return 1
	case 1:
		return 2
	case 2:
		return 5
	case 3:
		return 10
	case 4:
		return 20
	case 5:
		return 50
	case 6:
		return 100
	case 7:
		return 200
	case 8:
		return 500
	case 9:
		return 1000
	case 10:
		return 2000
	case 11:
		return 5000
	}
	return 0
}

func bucketOf(ms float64) int {
	for i := range numBuckets - 1 {
		if ms <= bucketBound(i) {
			return i
		}
	}
	return numBuckets - 1
}

// agg 是一组请求的汇总。
type agg struct {
	count, client, server int64
	sumMs, maxMs          float64
	hist                  [numBuckets]int64
}

func (a *agg) add(ms float64, status int) {
	a.count++
	a.sumMs += ms
	a.maxMs = max(a.maxMs, ms)
	a.hist[bucketOf(ms)]++
	switch {
	case status >= 500:
		a.server++
	case status >= 400:
		a.client++
	}
}

func (a *agg) merge(b *agg) {
	a.count += b.count
	a.client += b.client
	a.server += b.server
	a.sumMs += b.sumMs
	a.maxMs = max(a.maxMs, b.maxMs)
	for i := range a.hist {
		a.hist[i] += b.hist[i]
	}
}

func (a *agg) avg() float64 {
	if a.count == 0 {
		return 0
	}
	return a.sumMs / float64(a.count)
}

// p95 按分桶估算：取累计达到 95% 的那个桶的上界；落在最慢的桶时用最大值。
func (a *agg) p95() float64 {
	if a.count == 0 {
		return 0
	}
	need := (a.count*95 + 99) / 100
	var acc int64
	for i, n := range a.hist {
		acc += n
		if acc >= need {
			if b := bucketBound(i); b > 0 {
				return min(b, a.maxMs)
			}
			return a.maxMs
		}
	}
	return a.maxMs
}

type routeKey struct{ method, route string }

// sample 是某一时刻的资源读数。
type sample struct {
	at         time.Time
	cpu        time.Duration
	cpuOK      bool
	heapInuse  uint64
	goroutines int
}

type bucket struct {
	minute int64 // Unix 分钟数；0 表示空
	all    agg
	routes map[routeKey]*agg
	start  sample  // 这一分钟第一次用到时的读数
	cpuPct float64 // 这一分钟结束后算出的 CPU 占用
	cpuSet bool
}

// Collector 采集并汇总指标。并发安全。
type Collector struct {
	now     func() time.Time
	started time.Time
	skip    map[string]bool
	cpuTime func() (time.Duration, bool)

	mu      sync.Mutex
	buckets [windowMinutes]bucket
	cur     int64
	last    sample // 上一次读取 CPU 占用时的读数
	lastPct float64

	verMu   sync.Mutex
	version string
}

// New 创建采集器。now 为 nil 时用 time.Now；skipRoutes 里的路由模板（例如健康检查）不计入。
func New(now func() time.Time, skipRoutes ...string) *Collector {
	if now == nil {
		now = time.Now
	}
	c := &Collector{now: now, started: now(), skip: map[string]bool{}, cpuTime: processCPUTime}
	for _, r := range skipRoutes {
		c.skip[r] = true
	}
	return c
}

// Middleware 记录每个请求：方法、路由模板、状态码、耗时。放在中间件链的最前面，耗时包含整个处理过程。
func (c *Collector) Middleware() gin.HandlerFunc {
	return func(g *gin.Context) {
		start := c.now()
		g.Next()
		route := g.FullPath()
		if c.skip[route] {
			return
		}
		c.Record(g.Request.Method, route, g.Writer.Status(), c.now().Sub(start))
	}
}

// Record 记一次请求。方法不是标准方法时记为 OTHER，避免任意方法名撑大条目数。
func (c *Collector) Record(method, route string, status int, d time.Duration) {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions:
	default:
		method = "OTHER"
	}
	ms := float64(d.Microseconds()) / 1000
	c.mu.Lock()
	defer c.mu.Unlock()
	b := c.bucketLocked(c.now())
	b.all.add(ms, status)
	k := routeKey{method, route}
	a, ok := b.routes[k]
	if !ok {
		if len(b.routes) >= maxRoutesPerMinute {
			k = routeKey{"", otherRoute}
			if a, ok = b.routes[k]; !ok {
				a = &agg{}
				b.routes[k] = a
			}
		} else {
			a = &agg{}
			b.routes[k] = a
		}
	}
	a.add(ms, status)
}

// bucketLocked 返回当前分钟的桶；进入新的一分钟时取一次资源读数，并算出上一个桶的 CPU 占用。
func (c *Collector) bucketLocked(now time.Time) *bucket {
	m := now.Unix() / 60
	if m < c.cur { // 墙上时间回拨：记到当前桶里
		m = c.cur
	}
	b := &c.buckets[m%windowMinutes]
	if b.minute == m {
		return b
	}
	s := c.sample(now)
	if c.cur != 0 {
		prev := &c.buckets[c.cur%windowMinutes]
		if prev.minute == c.cur {
			if pct, ok := cpuPercent(prev.start, s); ok {
				prev.cpuPct, prev.cpuSet = pct, true
			}
		}
	}
	*b = bucket{minute: m, routes: map[routeKey]*agg{}, start: s}
	c.cur = m
	return b
}

func (c *Collector) sample(now time.Time) sample {
	cpu, ok := c.cpuTime()
	return sample{at: now, cpu: cpu, cpuOK: ok, heapInuse: heapInuse(), goroutines: runtime.NumGoroutine()}
}

// cpuPercent 算两次读数之间进程占全部核心的百分比；间隔太短或取不到 CPU 时间时 ok 为 false。
func cpuPercent(a, b sample) (float64, bool) {
	wall := b.at.Sub(a.at)
	if !a.cpuOK || !b.cpuOK || wall < time.Second {
		return 0, false
	}
	pct := float64(b.cpu-a.cpu) / float64(wall) / float64(runtime.NumCPU()) * 100
	return min(max(pct, 0), 100), true
}

// heapInuse 用 runtime/metrics 读堆占用（不停顿整个程序）。
func heapInuse() uint64 {
	s := []rtmetrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/heap/unused:bytes"}}
	rtmetrics.Read(s)
	var n uint64
	for _, v := range s {
		if v.Value.Kind() == rtmetrics.KindUint64 {
			n += v.Value.Uint64()
		}
	}
	return n
}

// Server 汇总当前状态。sqlDB 为 nil 时数据库一栏为空。
func (c *Collector) Server(ctx context.Context, sqlDB *sql.DB) monitor.Server {
	now := c.now()
	out := monitor.Server{Enabled: true, Now: now.UTC(), StartedAt: c.started.UTC(), Uptime: int64(now.Sub(c.started).Seconds())}
	out.Runtime = monitor.Runtime{
		GoVersion: shortGoVersion(runtime.Version()), OS: runtime.GOOS, Arch: runtime.GOARCH, Version: mainVersion(),
		NumCPU: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Goroutines: runtime.NumGoroutine(),
	}

	c.mu.Lock()
	cur := c.bucketLocked(now) // 让当前分钟一定有桶，窗口按它对齐
	s := c.sample(now)
	// 和上一次读取之间至少隔 1 秒才重新计算；第一次读取时用这一分钟开始时的读数
	if pct, ok := cpuPercent(c.last, s); ok {
		c.lastPct, c.last = pct, s
	} else if !c.last.cpuOK {
		if pct, ok := cpuPercent(cur.start, s); ok {
			c.lastPct = pct
		}
		c.last = s
	}
	out.CPU = monitor.CPU{Supported: s.cpuOK, Percent: c.lastPct}
	out.Requests = c.requestsLocked(cur.minute, s)
	c.mu.Unlock()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out.Memory = monitor.Memory{
		Sys: ms.Sys, HeapAlloc: ms.HeapAlloc, HeapInuse: ms.HeapInuse, StackInuse: ms.StackInuse,
		HeapObjects: ms.HeapObjects, NumGC: ms.NumGC, PauseTotal: float64(ms.PauseTotalNs) / 1e6,
	}
	if ms.LastGC > 0 {
		t := time.Unix(0, int64(ms.LastGC)).UTC() //nolint:gosec // LastGC 是纳秒时间戳，不会超过 int64
		out.Memory.LastGC = &t
	}
	if sqlDB != nil {
		out.DB = c.dbStats(ctx, sqlDB)
	}
	return out
}

func (c *Collector) requestsLocked(cur int64, now sample) monitor.Requests {
	out := monitor.Requests{Minutes: make([]monitor.Minute, 0, windowMinutes)}
	routes := map[routeKey]*agg{}
	for m := cur - windowMinutes + 1; m <= cur; m++ {
		mi := monitor.Minute{At: time.Unix(m*60, 0).UTC()}
		b := &c.buckets[((m%windowMinutes)+windowMinutes)%windowMinutes]
		if b.minute == m {
			mi.Count, mi.ClientErrors, mi.ServerErrors = b.all.count, b.all.client, b.all.server
			mi.AvgMs, mi.P95Ms, mi.MaxMs = b.all.avg(), b.all.p95(), b.all.maxMs
			heap, gr := b.start.heapInuse, b.start.goroutines
			mi.HeapInuse, mi.Goroutines = &heap, &gr
			if b.cpuSet {
				pct := b.cpuPct
				mi.CPU = &pct
			} else if m == cur {
				if pct, ok := cpuPercent(b.start, now); ok {
					mi.CPU = &pct
				}
			}
			for k, a := range b.routes {
				t, ok := routes[k]
				if !ok {
					t = &agg{}
					routes[k] = t
				}
				t.merge(a)
			}
		}
		out.Minutes = append(out.Minutes, mi)
	}
	list := make([]monitor.Route, 0, len(routes))
	for k, a := range routes {
		list = append(list, monitor.Route{Method: k.method, Route: k.route, Count: a.count, ServerErrors: a.server, AvgMs: a.avg(), P95Ms: a.p95()})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count != list[j].Count {
			return list[i].Count > list[j].Count
		}
		if list[i].Route != list[j].Route {
			return list[i].Route < list[j].Route
		}
		return list[i].Method < list[j].Method
	})
	if len(list) > topRoutes {
		list = list[:topRoutes]
	}
	out.Routes = list
	return out
}

func (c *Collector) dbStats(ctx context.Context, sqlDB *sql.DB) monitor.DB {
	st := sqlDB.Stats()
	out := monitor.DB{
		MaxOpen: st.MaxOpenConnections, Open: st.OpenConnections, InUse: st.InUse, Idle: st.Idle,
		WaitCount: st.WaitCount, WaitMs: float64(st.WaitDuration.Microseconds()) / 1000,
	}
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	start := time.Now()
	if err := sqlDB.PingContext(pctx); err != nil {
		return out
	}
	out.OK = true
	out.LatencyMs = float64(time.Since(start).Microseconds()) / 1000
	out.Version = c.dbVersion(pctx, sqlDB)
	return out
}

// dbVersion 查一次数据库版本并缓存；查不到时下次再试。
func (c *Collector) dbVersion(ctx context.Context, sqlDB *sql.DB) string {
	c.verMu.Lock()
	defer c.verMu.Unlock()
	if c.version != "" {
		return c.version
	}
	var v string
	if err := sqlDB.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v); err == nil {
		c.version = shortDBVersion(v)
	}
	return c.version
}

// 版本号只到大版本（D-031）：补丁号和发行版后缀（例如 0ubuntu0.24.04.4）会透露服务器的系统和补丁情况，
// 运维判断该不该升级看大版本就够了，精确版本去服务器上查。

// majorMinor 从 s 里第一个数字开始读出"主.次"版本；读不到时 ok 为 false。
func majorMinor(s string) (string, bool) {
	i := strings.IndexFunc(s, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return "", false
	}
	digits := func(j int) int {
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		return j
	}
	j := digits(i)
	if j >= len(s) || s[j] != '.' {
		return "", false
	}
	k := digits(j + 1)
	if k == j+1 {
		return "", false
	}
	return s[i:k], true
}

// shortGoVersion：go1.26.8 → go1.26；go1.27rc1、devel go1.27-abcdef → go1.27；认不出时为空。
func shortGoVersion(v string) string {
	i := strings.Index(v, "go1")
	if i < 0 {
		return ""
	}
	mm, ok := majorMinor(v[i+2:])
	if !ok {
		return ""
	}
	return "go" + mm
}

// shortDBVersion：8.0.46-0ubuntu0.24.04.4 → 8.0；10.11.6-MariaDB-0+deb12u1 → 10.11 MariaDB；认不出时为空。
func shortDBVersion(v string) string {
	mm, ok := majorMinor(v)
	if !ok {
		return ""
	}
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return mm + " MariaDB"
	}
	return mm
}

func mainVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		return bi.Main.Version
	}
	return ""
}
