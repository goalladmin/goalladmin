package app

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

// 规范 §13.2 第 163 条（D-074）：没配 redis.addr 时程序不连 Redis；配了就在启动时连上，停止时关闭；
// 服务器版本低于 6.0 拒绝启动；启动时连不上不拒绝启动，按不可用处理。
func TestRedis_163_OptionalConnection(t *testing.T) {
	gdb := db.OpenTestDB(t)
	newApp := func(tweak func(cfg *conf.Config)) (*App, error) {
		cfg := conf.Default()
		cfg.Log.Level = "error"
		cfg.Portals[testPortal] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: testSecret}
		tweak(cfg)
		return New(cfg, WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)), WithoutMigrations())
	}

	// 默认：不用 Redis
	a, err := newApp(func(*conf.Config) {})
	require.NoError(t, err)
	require.Nil(t, a.redis)
	require.NoError(t, a.Stop(context.Background()))

	// 配了：连上，Stop 时关闭
	addr := redisx.TestAddr(t)
	prefix := redisx.TestPrefix(t)
	a, err = newApp(func(cfg *conf.Config) { cfg.Redis.Addr, cfg.Redis.KeyPrefix = addr, prefix })
	require.NoError(t, err)
	require.NotNil(t, a.redis)
	require.True(t, a.redis.Available())
	require.NotEmpty(t, a.redis.Version())
	require.Equal(t, prefix+"x", a.redis.Key("x"), "键前缀来自配置")
	require.NoError(t, a.Stop(context.Background()))
	require.False(t, a.redis.Available(), "Stop 关闭 Redis 连接")
	require.NoError(t, a.Stop(context.Background()), "重复 Stop 不出错")

	// 版本太低：拒绝启动，错误里说明版本和最低要求
	old := redisx.NewTestFakeServer(t, "redis_version:5.0.14\r\n")
	a, err = newApp(func(cfg *conf.Config) { cfg.Redis.Addr = old.Addr() })
	require.Error(t, err)
	require.Nil(t, a)
	var tooOld *redisx.TooOldError
	require.ErrorAs(t, err, &tooOld)
	require.Contains(t, err.Error(), "5.0.14")
	require.Contains(t, err.Error(), "6.0")

	// 启动时连不上：照常启动，Redis 按不可用处理
	gone := redisx.NewTestProxy(t, addr)
	gone.Cut()
	start := time.Now()
	degraded, err := newApp(func(cfg *conf.Config) { cfg.Redis.Addr, cfg.Redis.ConnectWait = gone.Addr(), 0 })
	require.NoError(t, err)
	t.Cleanup(func() { _ = degraded.Stop(context.Background()) })
	require.Less(t, time.Since(start), 2*time.Second)
	require.NotNil(t, degraded.redis)
	require.False(t, degraded.redis.Available())

	// 启动等待、用户名、密码、库号、TLS 都从配置传到连接上
	started := func(tweak func(cfg *conf.Config)) *App {
		app, err := newApp(tweak)
		require.NoError(t, err)
		t.Cleanup(func() { _ = app.Stop(context.Background()) })
		return app
	}
	gone2 := redisx.NewTestProxy(t, addr)
	gone2.Cut()
	start = time.Now()
	started(func(cfg *conf.Config) { cfg.Redis.Addr, cfg.Redis.ConnectWait = gone2.Addr(), 1200*time.Millisecond })
	require.GreaterOrEqual(t, time.Since(start), 700*time.Millisecond, "按 redis.connectWait 等")
	fake := redisx.NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	b := started(func(cfg *conf.Config) {
		cfg.Redis.Addr, cfg.Redis.Username, cfg.Redis.Password, cfg.Redis.DB = fake.Addr(), "app", "from-config", 7
	})
	require.True(t, b.redis.Available())
	require.Contains(t, fake.Lines(), "AUTH app from-config")
	require.Contains(t, fake.Lines(), "SELECT 7")
	tlsOn := started(func(cfg *conf.Config) { cfg.Redis.Addr, cfg.Redis.TLS, cfg.Redis.ConnectWait = addr, true, 0 })
	require.False(t, tlsOn.redis.Available(), "开了 TLS 就不用明文连")

	// 服务器拒绝这份配置（密码不对）：拒绝启动，错误里不带密码
	denied := redisx.NewTestFakeServer(t, "redis_version:7.2.4\r\n")
	denied.SetReply("PING", "-WRONGPASS invalid username-password pair or user is disabled.")
	_, err = newApp(func(cfg *conf.Config) { cfg.Redis.Addr, cfg.Redis.Password = denied.Addr(), "wrong-pass-123" })
	var rejected *redisx.RejectedError
	require.ErrorAs(t, err, &rejected)
	require.NotContains(t, err.Error(), "wrong-pass-123")

	// 配置写错（不是"没配"）：拒绝启动，不悄悄当成没配
	_, err = newApp(func(cfg *conf.Config) { cfg.Redis.Addr = "127.0.0.1" })
	require.ErrorIs(t, err, conf.ErrInvalidConfig)
}
