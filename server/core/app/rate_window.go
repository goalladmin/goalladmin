package app

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/goalladmin/goalladmin/server/core/internal/captcha"
	"github.com/goalladmin/goalladmin/server/core/internal/loginguard"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
)

// RateWindow 是有容量上限的操作计数窗口（D-076）。占用成功后不该计数时调用 refund，退款只影响本次窗口且只生效一次。
type RateWindow interface {
	Reserve(ctx context.Context, key string) (allowed bool, refund func())
}

type rateWindow struct{ window *ratelimit.Window }

func (w rateWindow) Reserve(ctx context.Context, key string) (bool, func()) {
	ticket, ok := w.window.Reserve(ctx, key)
	if !ok {
		return false, nil
	}
	return true, ticket.Undo
}

func (a *App) setupRateWindows() {
	a.deps.NewRateWindow = func(namespace string, limit int, span time.Duration, maxKeys int) RateWindow {
		return rateWindow{ratelimit.NewShared(limit, span, maxKeys, a.now, a.redis, namespace)}
	}
}

// 脚本探测使用实际共享状态键的独立字段，启动和恢复时都检查权限。
func checkRedisState(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
	if err := ratelimit.Probe(ctx, rdb, key); err != nil {
		return err
	}
	if err := loginguard.Probe(ctx, rdb, key); err != nil {
		return err
	}
	return captcha.Probe(ctx, rdb, key)
}

// RateIPKey 将客户端 IP 归一化成限流键，IPv6 按 /64 归组。
func RateIPKey(ip string) string { return ratelimit.IPKey(ip) }
