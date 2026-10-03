package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadiness_173_CacheAndIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var failing atomic.Bool
		failure := errors.New("database unavailable")
		ping := func(context.Context) error {
			calls.Add(1)
			if failing.Load() {
				return failure
			}
			return nil
		}
		r := &readinessCheck{now: time.Now, ping: ping}
		other := &readinessCheck{now: time.Now, ping: ping}
		defer r.stop()
		defer other.stop()
		ctx := context.Background()
		for range 100 {
			require.NoError(t, r.check(ctx))
		}
		require.EqualValues(t, 1, calls.Load())
		require.NoError(t, other.check(ctx))
		require.EqualValues(t, 2, calls.Load(), "不同实例不共用结果")
		failing.Store(true)
		time.Sleep(readinessTTL - time.Nanosecond)
		require.NoError(t, r.check(ctx))
		require.EqualValues(t, 2, calls.Load())
		time.Sleep(time.Nanosecond)
		require.ErrorIs(t, r.check(ctx), failure)
		require.EqualValues(t, 3, calls.Load(), "边界到期后才重新检查")
		failing.Store(false)
		for range 100 {
			require.ErrorIs(t, r.check(ctx), failure)
		}
		require.EqualValues(t, 3, calls.Load(), "失败结果也需要缓存")
		time.Sleep(readinessTTL)
		require.NoError(t, r.check(ctx))
		require.EqualValues(t, 4, calls.Load())
	})
}

func TestReadiness_173_SharedCheckSurvivesCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		r := &readinessCheck{now: time.Now, ping: func(ctx context.Context) error {
			calls.Add(1)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}}
		defer r.stop()
		first, cancelFirst := context.WithCancel(context.Background())
		defer cancelFirst()
		firstResult := make(chan error, 1)
		go func() { firstResult <- r.check(first) }()
		synctest.Wait()
		results := make(chan error, 32)
		for range cap(results) {
			go func() { results <- r.check(context.Background()) }()
		}
		follower, cancelFollower := context.WithCancel(context.Background())
		defer cancelFollower()
		followerResult := make(chan error, 1)
		go func() { followerResult <- r.check(follower) }()
		synctest.Wait()
		require.EqualValues(t, 1, calls.Load())
		cancelFirst()
		cancelFollower()
		require.ErrorIs(t, <-firstResult, context.Canceled)
		require.ErrorIs(t, <-followerResult, context.Canceled)
		synctest.Wait()
		require.Empty(t, results, "取消单个请求不能结束共享检查")
		time.Sleep(time.Second)
		close(release)
		for range cap(results) {
			require.NoError(t, <-results)
		}
		time.Sleep(readinessTTL - time.Nanosecond)
		require.NoError(t, r.check(context.Background()))
		require.EqualValues(t, 1, calls.Load(), "缓存期限从检查完成起算")
	})
}

func TestReadiness_173_TimeoutAndRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var healthy atomic.Bool
		r := &readinessCheck{now: time.Now, ping: func(ctx context.Context) error {
			calls.Add(1)
			if healthy.Load() {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		}}
		defer r.stop()
		started := time.Now()
		require.ErrorIs(t, r.check(context.Background()), context.DeadlineExceeded)
		require.Equal(t, readinessTimeout, time.Since(started))
		healthy.Store(true)
		require.ErrorIs(t, r.check(context.Background()), context.DeadlineExceeded)
		require.EqualValues(t, 1, calls.Load())
		time.Sleep(readinessTTL)
		require.NoError(t, r.check(context.Background()))
		require.EqualValues(t, 2, calls.Load())
	})
}

func TestReadiness_173_StopAndCancelledRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		var finished atomic.Bool
		r := &readinessCheck{now: time.Now, ping: func(ctx context.Context) error {
			calls.Add(1)
			<-ctx.Done()
			finished.Store(true)
			return ctx.Err()
		}}
		defer r.stop()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.ErrorIs(t, r.check(ctx), context.Canceled)
		require.Zero(t, calls.Load(), "已取消的请求不启动检查")
		result := make(chan error, 1)
		go func() { result <- r.check(context.Background()) }()
		synctest.Wait()
		require.EqualValues(t, 1, calls.Load())
		r.stop()
		require.True(t, finished.Load(), "停止要等在途检查结束")
		require.ErrorIs(t, <-result, context.Canceled)
		r.stop()
		time.Sleep(readinessTTL)
		require.ErrorIs(t, r.check(context.Background()), context.Canceled)
		require.EqualValues(t, 1, calls.Load(), "停止后不再访问数据库")
	})
}
