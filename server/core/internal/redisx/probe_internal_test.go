package redisx

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 扩展探测在初次连接与恢复时都执行；实际业务键通过 Client.Key 传给调用方。
func TestProbe_165_AdditionalCheckRunsAtStartupAndRecovery(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.20\r\n")
	f.SetReply("EVAL", ":1")
	var calls atomic.Int64
	c, err := openFake(t, f, Options{Check: func(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
		calls.Add(1)
		return rdb.Eval(ctx, "return 1", []string{key("guard", "{counts}", "data")}).Err()
	}})
	require.NoError(t, err)
	require.True(t, c.Available())
	require.EqualValues(t, 1, calls.Load())
	require.Contains(t, strings.Join(f.Lines(), "\n"), "t:guard:{counts}:data")
	f.SetReply("EVAL", "-NOPERM eval unavailable")
	err = c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error { return rdb.Eval(ctx, "return 1", nil).Err() })
	require.ErrorIs(t, err, ErrUnavailable)
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, time.Second, 5*time.Millisecond)
	require.False(t, c.Available(), "业务探测仍失败时不能只凭 PING 恢复")
	f.SetReply("EVAL", ":1")
	require.Eventually(t, c.Available, time.Second, 5*time.Millisecond)
}

func TestProbe_165_AdditionalPermissionErrorIsRejectedAndRedacted(t *testing.T) {
	secret := "probe-secret-165"
	f := NewTestFakeServer(t, "redis_version:6.0.20\r\n")
	f.SetReply("EVAL", "-ERR Error running script (call to f_test): @user_script:1: NOPERM command denied "+secret)
	var logs bytes.Buffer
	c, err := openFake(t, f, Options{Password: secret, Log: slog.New(slog.NewTextHandler(&logs, nil)),
		Check: func(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
			return rdb.Eval(ctx, "return 1", []string{key("guard", "{counts}", "data")}).Err()
		}})
	var rejected *RejectedError
	require.ErrorAs(t, err, &rejected)
	require.Nil(t, c)
	require.NotContains(t, err.Error(), secret)
	require.NotContains(t, logs.String(), secret)
	// 回调自己的普通错误不是服务器权限拒绝。
	mine := errors.New("business NOPERM label")
	require.False(t, isRejection(mine))
	f.SetReply("EVAL", "-ERR ordinary business NOPERM label")
	raw := redis.NewClient(&redis.Options{Addr: f.Addr(), Protocol: 2, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	err = raw.Eval(context.Background(), "return 1", nil).Err()
	require.Error(t, err)
	require.False(t, isRejection(err), "普通服务器业务错误里的 NOPERM 字样不改变状态")
}

// Redis 6 的 Lua 内权限错误不是 NOPERM 前缀；用真实受限用户确认分类与启动结果。
func TestProbe_165_RealScriptACLFailure(t *testing.T) {
	addr, prefix := TestAddr(t), TestPrefix(t)
	admin := redis.NewClient(&redis.Options{Addr: addr, Protocol: 2, DisableIdentity: true})
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	username := "guard-probe-" + strings.ReplaceAll(prefix, ":", "")
	ctx := context.Background()
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">acl-test", "~*", "+@all", "-hset").Err())
	t.Cleanup(func() { require.NoError(t, admin.Do(context.Background(), "ACL", "DELUSER", username).Err()) })
	c, err := Open(Options{Addr: addr, Username: username, Password: "acl-test", KeyPrefix: prefix, Log: quiet(),
		Check: func(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
			return rdb.Eval(ctx, "return redis.call('HSET', KEYS[1], 'probe', '1')", []string{key("guard", "{counts}", "data")}).Err()
		}})
	if c != nil {
		t.Cleanup(func() { require.NoError(t, c.Close()) })
	}
	t.Logf("Redis 脚本内权限错误：%v", err)
	var rejected *RejectedError
	require.ErrorAs(t, err, &rejected)
}

// 假服务器的探测形状支持超过 bufio 默认缓冲区的多行 bulk；业务脚本仍明确拒绝。
func TestProbe_165_FakeAcceptsLargeMultilineProbe(t *testing.T) {
	f := NewTestFakeServer(t, "redis_version:6.0.20\r\n")
	script := strings.Repeat("-- 多行脚本参数\n", 1000) + "return 1"
	c, err := openFake(t, f, Options{Check: func(ctx context.Context, rdb redis.Cmdable, key func(...string) string) error {
		windowKeys := []string{key("{counts}", "windows"), key("{counts}", "window-expiry"), key("{counts}", "window-capacity")}
		if err := rdb.Eval(ctx, script, windowKeys).Err(); err != nil {
			return err
		}
		guardKeys := []string{key("guard", "{counts}", "data"), key("guard", "{counts}", "expiry"), key("guard", "{counts}", "capacity")}
		if err := rdb.Eval(ctx, script, guardKeys, "probe", "_probe").Err(); err != nil {
			return err
		}
		if err := rdb.HDel(ctx, windowKeys[0], "probe").Err(); err != nil {
			return err
		}
		return rdb.ZRem(ctx, windowKeys[1], "probe").Err()
	}})
	require.NoError(t, err)
	require.True(t, c.Available())
	require.Contains(t, strings.Join(f.Lines(), "\n"), script)
	err = c.Do(context.Background(), func(ctx context.Context, rdb redis.Cmdable) error {
		return rdb.Eval(ctx, script, []string{c.Key("{counts}", "windows"), c.Key("{counts}", "window-expiry"), c.Key("{counts}", "window-capacity")}, "user").Err()
	})
	require.ErrorContains(t, err, "unknown command")
	require.True(t, c.Available())
}
