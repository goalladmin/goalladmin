package system

import (
	"context"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
)

// 监控中心（docs/decisions.md D-030）：安全情况看最近 24 小时的登录、会话和操作；服务器状态来自框架的进程内指标。
// 两个接口分开，前端按不同频率刷新。只读，不接受任何会改变范围的参数（端取自调用者身份）。

// securityRecentN 是"实时登录"列表的条数。
const securityRecentN = 15

// securityCache 按端缓存安全统计（D-031）：缓存时长取配置 monitor.securityCache，
// 同时打开页面的人再多，每个端每个周期也只查一次数据库。结果和查看的人无关，所以可以共用。
// 缓存过期的那一刻同时到达的请求只让一个去查库，其余等它的结果（按端各一把锁），不会一起冲到数据库上（D-043）。
type securityCache struct {
	mu      sync.Mutex
	entries map[string]*securityEntry
}

type securityEntry struct {
	fill sync.Mutex // 查库时持有：同一端并发的未命中排队，后来的直接用前一个填好的结果
	at   time.Time
	resp monitorSecurityResponse
	gen  uint64 // 每次查库（不论成败）加一；排队的请求据此知道前面那次查库失败了，一起失败而不是挨个再查
	err  error  // 最近一次查库的错误
}

// entry 返回某端的缓存项（没有就建一个空的）。
func (s *securityCache) entry(portal string) *securityEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]*securityEntry{}
	}
	e, ok := s.entries[portal]
	if !ok {
		e = &securityEntry{}
		s.entries[portal] = e
	}
	return e
}

// fresh 报告缓存项是否还新鲜并返回内容。at 和 resp 的读写都在 s.mu 下。
func (s *securityCache) fresh(e *securityEntry, now time.Time, ttl time.Duration) (monitorSecurityResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ttl <= 0 || e.at.IsZero() || now.Sub(e.at) >= ttl {
		return monitorSecurityResponse{}, false
	}
	return e.resp, true
}

// load 返回某端的安全统计：缓存新鲜就直接用，否则由拿到 fill 锁的那个请求查库并写回，其余请求等它；
// 它查库失败时，排在它后面的请求一起拿到这个错误（数据库出故障时不会一个接一个地各等一次超时）。
func (s *securityCache) load(portal string, now func() time.Time, ttl time.Duration, compute func() (monitorSecurityResponse, error)) (monitorSecurityResponse, error) {
	e := s.entry(portal)
	s.mu.Lock()
	seen := e.gen
	s.mu.Unlock()
	if resp, ok := s.fresh(e, now(), ttl); ok {
		return resp, nil
	}
	e.fill.Lock()
	defer e.fill.Unlock()
	// 排队期间前一个请求可能已经填好了，或者已经失败了
	if resp, ok := s.fresh(e, now(), ttl); ok {
		return resp, nil
	}
	s.mu.Lock()
	failedAhead := e.gen != seen && e.err != nil
	err := e.err
	s.mu.Unlock()
	if failedAhead {
		return monitorSecurityResponse{}, err
	}
	resp, err := compute()
	s.mu.Lock()
	e.gen++
	e.err = err
	if err == nil {
		e.at, e.resp = now(), resp
	}
	s.mu.Unlock()
	if err != nil {
		return monitorSecurityResponse{}, err
	}
	return resp, nil
}

type monitorSecurityResponse struct {
	Hours          []time.Time     `json:"hours"` // 24 个 UTC 整点，最后一个是当前小时
	Success        []int64         `json:"success"`
	Failed         []int64         `json:"failed"`
	Sessions       int64           `json:"sessions"`
	LockedSessions int64           `json:"lockedSessions"`
	Operations     int64           `json:"operations"` // 最近 24 小时的操作次数
	FailedIPs      []auth.IPCount  `json:"failedIps"`
	Recent         []securityLogin `json:"recent"`
}

// securityLogin 是登录日志的精简视图：不含 User-Agent 和请求 ID。
type securityLogin struct {
	Username  string    `json:"username"`
	Success   bool      `json:"success"`
	Reason    string    `json:"reason"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"createdAt"`
}

// monitorSecurity 处理 GET /system/monitor/security。
func (h *handlers) monitorSecurity(c *gin.Context) {
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)
	resp, err := h.security.load(p.Portal, time.Now, h.deps.Conf.Monitor.SecurityCache, func() (monitorSecurityResponse, error) {
		return h.securityStats(ctx, p.Portal, time.Now())
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, resp)
}

// securityStats 查一次某端最近 24 小时的安全统计。
func (h *handlers) securityStats(ctx context.Context, portal string, now time.Time) (monitorSecurityResponse, error) {
	hour := now.UTC().Truncate(time.Hour)
	since := hour.Add(-23 * time.Hour)

	resp := monitorSecurityResponse{Hours: make([]time.Time, 24), Success: make([]int64, 24), Failed: make([]int64, 24)}
	for i := range 24 {
		resp.Hours[i] = since.Add(time.Duration(i) * time.Hour)
	}
	st, err := h.deps.Auth.SecurityStats(ctx, portal, since)
	if err != nil {
		return resp, err
	}
	for _, hl := range st.Hours {
		if i := int(hl.Hour.Sub(since) / time.Hour); i >= 0 && i < 24 {
			resp.Success[i], resp.Failed[i] = hl.Success, hl.Failed
		}
	}
	resp.FailedIPs, resp.LockedSessions = st.FailedIPs, st.LockedSessions
	if resp.Sessions, err = h.deps.Auth.CountActiveSessions(ctx, portal); err != nil {
		return resp, err
	}
	if _, resp.Operations, err = oplog.List(ctx, oplog.Filter{Portal: portal, From: since}, 1, 1); err != nil {
		return resp, err
	}
	logs, _, err := h.deps.Auth.ListLoginLogs(ctx, auth.LoginLogFilter{Portal: portal}, 1, securityRecentN)
	if err != nil {
		return resp, err
	}
	resp.Recent = make([]securityLogin, 0, len(logs))
	for _, l := range logs {
		resp.Recent = append(resp.Recent, securityLogin{Username: l.Username, Success: l.Success, Reason: l.Reason, IP: l.IP, CreatedAt: l.CreatedAt})
	}
	return resp, nil
}

// monitorServer 处理 GET /system/monitor/server。配置关掉服务器状态时只回 {enabled: false}（D-031）。
func (h *handlers) monitorServer(c *gin.Context) {
	s, err := h.deps.Monitor.Server(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if !s.Enabled {
		httpx.OK(c, gin.H{"enabled": false})
		return
	}
	httpx.OK(c, s)
}
