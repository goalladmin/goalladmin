package system

import (
	"context"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/oplog"
)

// 数据中心（docs/decisions.md D-027、D-030）：只用系统里的真实数据。统计来自框架服务，模块不直接查框架的表。

type dashboardResponse struct {
	Days       []string            `json:"days"` // 连续的本地日期，最后一天是今天
	Users      UserCounts          `json:"users"`
	NewUsers   []int64             `json:"newUsers"` // 每天新建的用户数，与 days 对齐
	Sessions   int64               `json:"sessions"`
	Logins     dashboardLogins     `json:"logins"`
	Operations []int64             `json:"operations"`
	Reasons    []auth.ReasonCount  `json:"reasons"`
	TopActions []oplog.ActionCount `json:"topActions"`
	TopUsers   []oplog.UserCount   `json:"topUsers"` // 操作最多的人
	Hours      dashboardHours      `json:"hours"`    // 按本地钟点（0–23）的活跃分布
}

type dashboardHours struct {
	Logins     []int64 `json:"logins"`
	Operations []int64 `json:"operations"`
}

// dashboardTopN 是排行榜的条数。
const dashboardTopN = 8

type dashboardLogins struct {
	Success []int64 `json:"success"`
	Failed  []int64 `json:"failed"`
}

// dashboard 处理 GET /system/dashboard?days=30&tz=480。
// days 取 1–90（默认 30）；tz 是浏览器的时区偏移（分钟，东区为正），限制在 ±14 小时，用来按本地日期分组。
func (h *handlers) dashboard(c *gin.Context) {
	days := clampInt(c.Query("days"), 30, 1, 90)
	tz := clampInt(c.Query("tz"), 0, -840, 840)
	ctx := c.Request.Context()
	p := auth.MustFromCtx(ctx)

	offset := time.Duration(tz) * time.Minute
	localToday := time.Now().UTC().Add(offset).Truncate(24 * time.Hour)
	startLocal := localToday.AddDate(0, 0, -(days - 1))
	since := startLocal.Add(-offset) // 本地第一天零点对应的 UTC 时间

	resp := dashboardResponse{
		Days:       make([]string, days),
		NewUsers:   make([]int64, days),
		Logins:     dashboardLogins{Success: make([]int64, days), Failed: make([]int64, days)},
		Operations: make([]int64, days),
		Hours:      dashboardHours{Operations: make([]int64, 24)},
	}
	index := make(map[string]int, days)
	for i := range days {
		d := startLocal.AddDate(0, 0, i).Format("2006-01-02")
		resp.Days[i] = d
		index[d] = i
	}

	var err error
	if resp.Users, err = h.users.repo.Counts(ctx, since); err != nil {
		httpx.Fail(c, err)
		return
	}
	newUsers, err := h.users.repo.DailyNew(ctx, since, tz)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	for _, d := range newUsers {
		if i, ok := index[d.Day]; ok {
			resp.NewUsers[i] = d.Count
		}
	}
	if resp.Sessions, err = h.deps.Auth.CountActiveSessions(ctx, p.Portal); err != nil {
		httpx.Fail(c, err)
		return
	}
	logins, err := h.deps.Auth.LoginStats(ctx, p.Portal, since, tz)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	for _, d := range logins.Days {
		if i, ok := index[d.Day]; ok {
			resp.Logins.Success[i], resp.Logins.Failed[i] = d.Success, d.Failed
		}
	}
	resp.Reasons = logins.Reasons
	resp.Hours.Logins = logins.Hours
	ops, err := oplog.DailyCounts(ctx, p.Portal, since, tz)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	for _, d := range ops {
		if i, ok := index[d.Day]; ok {
			resp.Operations[i] = d.Count
		}
	}
	if resp.TopActions, err = oplog.TopActions(ctx, p.Portal, since, dashboardTopN); err != nil {
		httpx.Fail(c, err)
		return
	}
	if resp.TopActions == nil {
		resp.TopActions = []oplog.ActionCount{}
	}
	// 活跃用户排名带用户名：只列操作人"查看用户"范围内的人（D-039），否则数据中心会把范围外的账号名和活跃度漏出去
	if resp.TopUsers, err = h.scopedTopUsers(ctx, p, since); err != nil {
		httpx.Fail(c, err)
		return
	}
	if resp.TopUsers == nil {
		resp.TopUsers = []oplog.UserCount{}
	}
	hours, err := oplog.HourOfDayCounts(ctx, p.Portal, since, tz)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	for _, hc := range hours {
		if hc.Hour >= 0 && hc.Hour < 24 {
			resp.Hours.Operations[hc.Hour] = hc.Count
		}
	}
	httpx.OK(c, resp)
}

// scopedTopUsers 按操作人在用户数据资源上"查看用户"的范围统计活跃用户：全部范围看整个端，
// 部门范围只看范围内的用户，仅本人（包括根本没有查看用户权限的）只看自己。
// 范围内的人和他们的操作次数在同一个只读快照里读（D-052）：分两次读的话，中间被调出范围的人调走之后的操作也会算进来。
func (h *handlers) scopedTopUsers(ctx context.Context, p auth.Principal, since time.Time) ([]oplog.UserCount, error) {
	var out []oplog.UserCount
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		f, err := h.deps.RBAC.DataFilter(ctx, p, DataUser, PermUserList)
		if err != nil {
			return err
		}
		if f.All() {
			out, err = oplog.TopUsers(ctx, p.Portal, since, dashboardTopN)
			return err
		}
		out, err = oplog.TopUsersIn(ctx, p.Portal, h.users.repo.IDsInScopeQuery(ctx, f), since, dashboardTopN)
		return err
	})
	return out, err
}

// clampInt 解析整数查询参数：解析失败用默认值，超出范围夹到边界。
func clampInt(raw string, def, lo, hi int) int {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return min(max(v, lo), hi)
}
