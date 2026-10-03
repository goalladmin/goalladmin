package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func TestSessionCleanup_ScheduleAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			runSessionCleanup(ctx, func(context.Context) bool {
				return calls.Add(1) == 1 // 首轮有积压，次轮空闲。
			})
		}()
		synctest.Wait()
		time.Sleep(time.Minute - time.Nanosecond)
		require.Zero(t, calls.Load())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.EqualValues(t, 1, calls.Load())
		time.Sleep(time.Minute)
		synctest.Wait()
		require.EqualValues(t, 2, calls.Load())
		time.Sleep(time.Hour - time.Nanosecond)
		require.EqualValues(t, 2, calls.Load())
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.EqualValues(t, 3, calls.Load())
		cancel()
		<-done
		time.Sleep(2 * time.Hour)
		require.EqualValues(t, 3, calls.Load())
	})
}

func TestSessionCleanup_CancelInFlight(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		entered, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			runSessionCleanup(ctx, func(ctx context.Context) bool {
				close(entered)
				<-ctx.Done()
				return true
			})
		}()
		<-entered
		cancel()
		<-done
	})
}

type cleanupStartFailure struct{ *authTestModule }

func (*cleanupStartFailure) Start(context.Context) error { return errors.New("start failed") }

func TestSessionCleanup_AppLifecycle(t *testing.T) {
	f := newAuthFixture(t)
	a := f.app
	t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
	require.Nil(t, a.sessionCleanupDone, "Setup 不启动清理")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, a.Start(ctx))
	done := a.sessionCleanupDone
	require.NotNil(t, done)
	a.startSessionCleanup(ctx)
	require.Equal(t, done, a.sessionCleanupDone, "不重复启动同一后台任务")
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("应用上下文取消后清理未退出")
	}
	require.NoError(t, a.Stop(context.Background()))
	require.Nil(t, a.sessionCleanupDone)
	require.NoError(t, a.Stop(context.Background()))

	// 第二次启动由 Stop 主动取消；Stop 返回时任务必须已退出。
	require.NoError(t, a.Start(context.Background()))
	done = a.sessionCleanupDone
	require.NoError(t, a.Stop(context.Background()))
	select {
	case <-done:
	default:
		t.Fatal("Stop 返回前必须等清理任务退出")
	}

	b := newAuthFixture(t).app
	b.modules = []Module{&cleanupStartFailure{&authTestModule{users: newMemUsers()}}}
	t.Cleanup(func() { require.NoError(t, b.Stop(context.Background())) })
	require.ErrorContains(t, b.Start(context.Background()), "start failed")
	require.Nil(t, b.sessionCleanupDone, "模块启动失败不能启动清理")

	c, err := New(conf.Default(), WithDB(nil), WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	require.NoError(t, c.Start(context.Background()))
	require.Nil(t, c.sessionCleanupDone)
	require.NoError(t, c.Stop(context.Background()))
}

func TestSessionCleanup_170_RegisteredPortalsFailureAndTimeout(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for _, code := range []string{"platform", "agent", "merchant"} {
		cfg.Portals[code] = conf.Portal{JWTSecret: testSecret, AccessTTL: time.Minute, RefreshTTL: time.Hour}
	}
	a, err := New(cfg, WithDB(gdb), WithClock(func() time.Time { return now }), WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
	for _, code := range []string{"platform", "agent", "merchant"} {
		require.NoError(t, a.deps.Portals.Register(portal.Portal{Code: code, Users: newMemUsers()}))
	}
	require.NoError(t, a.Setup())
	m := session.NewManager(session.Config{Now: func() time.Time { return now }, RefreshTTL: time.Hour})
	for _, code := range []string{"platform", "agent", "merchant", "other"} {
		_, _, err := m.Create(ctx, code, 0, 1, "", "")
		require.NoError(t, err)
	}
	require.NoError(t, gdb.Model(&session.Session{}).Where("1=1").Update("expires_at", now.Add(-31*24*time.Hour)).Error)
	active, token, err := m.Create(ctx, "platform", 0, 1, "", "")
	require.NoError(t, err)
	var failedContext context.Context
	require.NoError(t, gdb.Callback().Raw().Before("gorm:raw").Register("test:cleanup-timeout", func(tx *gorm.DB) {
		if strings.HasPrefix(tx.Statement.SQL.String(), "DELETE FROM ga_session") && tx.Statement.Vars[0] == "agent" {
			failedContext = tx.Statement.Context
			<-failedContext.Done() // 实际等待应用为每端设置的 5 秒上限。
			_ = tx.AddError(failedContext.Err())
		}
	}))
	t.Cleanup(func() { _ = gdb.Callback().Raw().Remove("test:cleanup-timeout") })
	require.True(t, a.cleanupSessions(ctx), "失败或有删除时应稍后继续")
	require.ErrorIs(t, failedContext.Err(), context.DeadlineExceeded)
	var remaining []string
	require.NoError(t, gdb.Model(&session.Session{}).Where("sid <> ?", active.SID).Order("portal").Pluck("portal", &remaining).Error)
	require.Equal(t, []string{"agent", "other"}, remaining, "一端失败不能阻止其他端；未注册端不清理")
	require.NoError(t, gdb.Callback().Raw().Remove("test:cleanup-timeout"))
	require.True(t, a.cleanupSessions(ctx), "故障恢复后继续清理")
	require.False(t, a.cleanupSessions(ctx), "已排空则延长检查间隔")
	got, _, err := m.Rotate(ctx, "platform", token, "", "")
	require.NoError(t, err, "有效会话仍可刷新")
	require.Equal(t, active.SID, got.SID)
}
