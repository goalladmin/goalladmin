package org

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// Dashboard 是主体的数据中心；所有数量只属于指定端的一个主体（D-090）。
type Dashboard struct {
	Days       []string          `json:"days"`
	Users      DashboardUsers    `json:"users"`
	NewUsers   []int64           `json:"newUsers"`
	Sessions   int64             `json:"sessions"`
	Logins     DashboardLogins   `json:"logins"`
	Operations []int64           `json:"operations"`
	Reasons    []DashboardReason `json:"reasons"`
	TopActions []DashboardAction `json:"topActions"`
	TopUsers   []DashboardUser   `json:"topUsers"`
	Hours      DashboardHours    `json:"hours"`
}
type DashboardUsers struct {
	Total   int64 `json:"total"`
	Enabled int64 `json:"enabled"`
	New     int64 `json:"new"`
}
type DashboardLogins struct {
	Success []int64 `json:"success"`
	Failed  []int64 `json:"failed"`
}
type DashboardHours struct {
	Logins     []int64 `json:"logins"`
	Operations []int64 `json:"operations"`
}
type DashboardReason struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}
type DashboardAction struct {
	Action string `json:"action"`
	Count  int64  `json:"count"`
}
type DashboardUser struct {
	UserID   uint64 `json:"userId"`
	Username string `json:"username"`
	Count    int64  `json:"count"`
}

// DataCenter 在调用方的只读快照内查询。rankUserID 为 0 表示本主体全部账号，否则排名只限此账号。
// 主体 ID 与 rankUserID 由已授权的调用方从身份取得，不接受请求参数决定范围。
func (s *Service) DataCenter(ctx context.Context, k Kind, orgID uint64, days, tz int, rankUserID uint64) (Dashboard, error) {
	var out Dashboard
	if err := k.check(); err != nil {
		return out, err
	}
	if orgID == 0 {
		return out, httpx.ErrNotFound
	}
	days, tz = min(max(days, 1), 90), min(max(tz, -840), 840)
	now := s.now().UTC()
	offset := time.Duration(tz) * time.Minute
	today := now.Add(offset).Truncate(24 * time.Hour)
	first := today.AddDate(0, 0, 1-days)
	since, until := first.Add(-offset), today.AddDate(0, 0, 1).Add(-offset)
	out = Dashboard{Days: make([]string, days), NewUsers: make([]int64, days), Operations: make([]int64, days),
		Logins: DashboardLogins{make([]int64, days), make([]int64, days)}, Hours: DashboardHours{make([]int64, 24), make([]int64, 24)},
		Reasons: []DashboardReason{}, TopActions: []DashboardAction{}, TopUsers: []DashboardUser{}}
	indexes := map[string]int{}
	for i := range days {
		d := first.AddDate(0, 0, i).Format("2006-01-02")
		out.Days[i] = d
		indexes[d] = i
	}
	users := func() *gorm.DB { return db.From(ctx).Table(k.userTable).Where("org_id = ?", orgID) }
	logs := func(table string) *gorm.DB {
		return db.From(ctx).Table(table).Where("portal = ? AND org_id = ? AND created_at >= ? AND created_at < ?", k.portal, orgID, since, until)
	}
	if err := users().Select("COUNT(*) AS total, COALESCE(SUM(status = 1), 0) AS enabled, COALESCE(SUM(created_at >= ? AND created_at < ?), 0) AS new", since, until).Scan(&out.Users).Error; err != nil {
		return out, err
	}
	if err := db.From(ctx).Table("ga_session").Where("portal = ? AND org_id = ? AND revoked_at IS NULL AND expires_at > ? AND user_id IN (?)", k.portal, orgID, now, users().Select("id")).Count(&out.Sessions).Error; err != nil {
		return out, err
	}
	type dayCount struct {
		Day     string
		Count   int64
		Success int64
		Failed  int64
	}
	var created, logins, ops []dayCount
	dateExpr := "DATE_FORMAT(DATE_ADD(created_at, INTERVAL ? MINUTE), '%Y-%m-%d') AS day"
	if err := users().Where("created_at >= ? AND created_at < ?", since, until).Select(dateExpr+", COUNT(*) AS count", tz).Group("day").Scan(&created).Error; err != nil {
		return out, err
	}
	if err := logs("ga_login_log").Select(dateExpr+", SUM(success = 1) AS success, SUM(success = 0) AS failed", tz).Group("day").Scan(&logins).Error; err != nil {
		return out, err
	}
	if err := logs("ga_operation_log").Select(dateExpr+", COUNT(*) AS count", tz).Group("day").Scan(&ops).Error; err != nil {
		return out, err
	}
	for _, d := range created {
		if i, ok := indexes[d.Day]; ok {
			out.NewUsers[i] = d.Count
		}
	}
	for _, d := range logins {
		if i, ok := indexes[d.Day]; ok {
			out.Logins.Success[i] = d.Success
			out.Logins.Failed[i] = d.Failed
		}
	}
	for _, d := range ops {
		if i, ok := indexes[d.Day]; ok {
			out.Operations[i] = d.Count
		}
	}
	if err := logs("ga_login_log").Where("success = 0").Select("reason, COUNT(*) AS count").Group("reason").Order("count DESC, reason").Scan(&out.Reasons).Error; err != nil {
		return out, err
	}
	if err := logs("ga_operation_log").Select("action, COUNT(*) AS count").Group("action").Order("count DESC, action").Limit(8).Scan(&out.TopActions).Error; err != nil {
		return out, err
	}
	ranked := users().Select("id")
	if rankUserID != 0 {
		ranked = ranked.Where("id = ?", rankUserID)
	}
	if err := logs("ga_operation_log").Where("user_id IN (?)", ranked).Select("user_id, MAX(username) AS username, COUNT(*) AS count").Group("user_id").Order("count DESC, user_id").Limit(8).Scan(&out.TopUsers).Error; err != nil {
		return out, err
	}
	type hourCount struct {
		Hour  int
		Count int64
	}
	for _, item := range []struct {
		table  string
		login  bool
		target []int64
	}{{"ga_login_log", true, out.Hours.Logins}, {"ga_operation_log", false, out.Hours.Operations}} {
		q := logs(item.table)
		if item.login {
			q = q.Where("success = 1")
		}
		var rows []hourCount
		if err := q.Select("HOUR(DATE_ADD(created_at, INTERVAL ? MINUTE)) AS hour, COUNT(*) AS count", tz).Group("hour").Scan(&rows).Error; err != nil {
			return out, err
		}
		for _, r := range rows {
			if r.Hour >= 0 && r.Hour < 24 {
				item.target[r.Hour] = r.Count
			}
		}
	}
	return out, nil
}
