// Package oplog 是操作日志的公开入口（规范 §10）。
//
// 操作日志按路由显式开启：注册路由时加 oplog.Record("动作名")，不加就不记。
// 记录的是通过了守卫的请求：谁、什么时候、对哪个路径做了什么、参数（已脱敏）、结果码和耗时。
// 不记响应体；键名命中 password|passwd|secret|token|key|credential|sign|authorization 的值一律写成 ***。
//
//	g.POST("/users", rbac.Require(PermUserCreate), h.createUser, oplog.Record("user.create"))
//
// 动作名建议用 "<资源>.<动作>" 这样的稳定编码，由前端按语言翻译；它会原样写进 ga_operation_log.action。
package oplog

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/internal/oplogimpl"
)

// Entry 是 ga_operation_log 的一行。
type Entry = oplogimpl.Entry

// Filter 是查询操作日志的条件。
type Filter = oplogimpl.Filter

// Masked 是脱敏后写入的占位值。
const Masked = oplogimpl.Masked

// Record 返回路由选项：把这条路由标记为需要记录操作日志，动作名为 action。
// 中间件排在守卫之后、handler 之前，handler 结束后落库；写库失败只记错误日志，不影响响应。
func Record(action string) app.RouteOption {
	return func(r *app.Route) {
		r.OpName = action
		app.WithMiddleware(oplogimpl.Middleware(r.Portal, action))(r)
	}
}

// List 分页查询操作日志，按时间倒序。system 模块的查询接口用它；业务模块也可以直接调用。
func List(ctx context.Context, f Filter, page, pageSize int) ([]Entry, int64, error) {
	return oplogimpl.List(ctx, f, page, pageSize)
}

// DailyCount 是某一天的操作次数；ActionCount 是一种动作的次数。
type (
	DailyCount  = oplogimpl.DailyCount
	ActionCount = oplogimpl.ActionCount
)

// DailyCounts 按天统计某端从 since 起的操作次数（首页概览用，D-027）；tzOffsetMinutes 是东区为正的时区偏移。
func DailyCounts(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) ([]DailyCount, error) {
	return oplogimpl.DailyCounts(ctx, portal, since, tzOffsetMinutes)
}

// TopActions 统计某端从 since 起次数最多的 n 种动作。
func TopActions(ctx context.Context, portal string, since time.Time, n int) ([]ActionCount, error) {
	return oplogimpl.TopActions(ctx, portal, since, n)
}

// HourCount 是某个本地钟点的操作次数；UserCount 是某个操作人的操作次数。
type (
	HourCount = oplogimpl.HourCount
	UserCount = oplogimpl.UserCount
)

// HourOfDayCounts 统计某端从 since 起按本地钟点（0–23）的操作次数分布（D-030）。
func HourOfDayCounts(ctx context.Context, portal string, since time.Time, tzOffsetMinutes int) ([]HourCount, error) {
	return oplogimpl.HourOfDayCounts(ctx, portal, since, tzOffsetMinutes)
}

// TopUsers 统计某端从 since 起操作次数最多的 n 个人。结果带用户名：调用方只有在"查看用户"是全部范围时才能用它，
// 否则用 TopUsersIn 限定在范围内的用户（D-039）。
func TopUsers(ctx context.Context, portal string, since time.Time, n int) ([]UserCount, error) {
	return oplogimpl.TopUsers(ctx, portal, since, n)
}

// TopUsersIn 只在给定的用户里统计操作次数最多的 n 个人。users 是只选用户 ID 一列的子查询，过滤在数据库里做（D-055）。
func TopUsersIn(ctx context.Context, portal string, users *gorm.DB, since time.Time, n int) ([]UserCount, error) {
	return oplogimpl.TopUsersIn(ctx, portal, users, since, n)
}

// UserTopActions 统计某人从 since 起次数最多的 n 种动作。
func UserTopActions(ctx context.Context, portal string, userID uint64, since time.Time, n int) ([]ActionCount, error) {
	return oplogimpl.UserTopActions(ctx, portal, userID, since, n)
}

// IsSensitiveKey 报告一个键名是否会被脱敏。业务代码自己写审计记录时可以复用这条规则。
func IsSensitiveKey(k string) bool { return oplogimpl.IsSensitiveKey(k) }
