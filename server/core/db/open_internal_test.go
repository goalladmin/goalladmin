package db

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestWaitReady_NoWaitFailsOnFirstAttempt(t *testing.T) {
	calls := 0
	err := waitReady(context.Background(), func(context.Context) error { calls++; return errors.New("refused") }, 0, time.Second, time.Millisecond, quietLog())
	require.Error(t, err)
	require.Equal(t, 1, calls, "connectWait 为 0 时只试一次")
	require.Contains(t, err.Error(), "refused")
	require.Contains(t, err.Error(), "connectWait", "错误信息要提示可以打开启动等待")
}

func TestWaitReady_RetriesUntilReady(t *testing.T) {
	calls := 0
	ping := func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("refused")
		}
		return nil
	}
	require.NoError(t, waitReady(context.Background(), ping, time.Minute, time.Second, time.Millisecond, quietLog()))
	require.Equal(t, 3, calls)
}

func TestWaitReady_GivesUpAfterWait(t *testing.T) {
	calls := 0
	start := time.Now()
	err := waitReady(context.Background(), func(context.Context) error { calls++; return errors.New("refused") }, 100*time.Millisecond, time.Second, 20*time.Millisecond, quietLog())
	require.Error(t, err)
	require.Contains(t, err.Error(), "refused")
	require.Greater(t, calls, 1)
	require.Less(t, time.Since(start), time.Second, "等满就放弃，不多等")
}

func TestWaitReady_EachAttemptHasItsOwnTimeout(t *testing.T) {
	// 一次 Ping 卡住（数据库假死）也不能拖住整个等待：每次尝试有自己的时限
	calls := 0
	ping := func(ctx context.Context) error {
		calls++
		if calls == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	start := time.Now()
	require.NoError(t, waitReady(context.Background(), ping, time.Minute, 50*time.Millisecond, time.Millisecond, quietLog()))
	require.Less(t, time.Since(start), time.Second)
}

func TestWaitReady_StopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := waitReady(ctx, func(context.Context) error { calls++; cancel(); return errors.New("refused") }, 10*time.Minute, time.Second, time.Minute, quietLog())
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
}

// 规范 §13.2 第 95 条：出错和慢查询的 SQL 日志只有占位符模板，不带绑定参数的值（D-054）——参数里可能是密码哈希、
// 刷新凭证的哈希、联系方式。
func TestSQLLog_NoBoundParams(t *testing.T) {
	base := OpenTestDB(t)
	sqlDB, err := base.DB()
	require.NoError(t, err)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	gdb, err := openGorm(sqlDB, os.Getenv(TestDSNEnv), log, time.Nanosecond) // 每条都算慢查询
	require.NoError(t, err)

	const secret = "$2a$12$secret-password-hash-value"
	var n int
	require.NoError(t, gdb.Raw("SELECT LENGTH(?)", secret).Scan(&n).Error)
	require.Error(t, gdb.Exec("UPDATE no_such_table SET password_hash = ? WHERE id = ?", secret, 7).Error)

	out := buf.String()
	require.Contains(t, out, "slow sql")
	require.Contains(t, out, "sql error")
	require.Contains(t, out, "password_hash = ?", "日志里留着占位符模板")
	require.NotContains(t, out, secret, "绑定参数的值不能进日志")
	require.Contains(t, out, "parameters were expanded", "Scan 走 gorm 自己的记录器、参数已被展开：不记 SQL 文本")
	require.Contains(t, out, "open_internal_test.go", "记下调用位置，排查时找得到是哪条语句")
	require.NotContains(t, out, sqlTemplateMark)
}
