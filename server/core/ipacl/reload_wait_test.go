package ipacl

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 202：等待另一个重载时仍按本次时限退出，不访问数据库或占住后续重载。
func TestReload_202_WaitIncludesDeadline(t *testing.T) {
	s := &Service{}
	s.reloadMu.Lock()
	var once sync.Once
	release := func() { once.Do(s.reloadMu.Unlock) }
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.reload(ctx) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		release()
		<-done
		t.Fatal("重载没有在持锁操作结束之前按本次时限退出")
	}
	release()
	require.NoError(t, s.lockReload(context.Background()), "时限结束没有泄漏互斥锁")
	s.reloadMu.Unlock()
}

func TestReload_202_CanceledBeforeDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Service{} // 未提供数据库；取消后不允许访问它。
	require.ErrorIs(t, s.reload(ctx), context.Canceled)
	require.True(t, s.reloadMu.TryLock(), "取消的请求没有占住互斥锁")
	s.reloadMu.Unlock()
}

func TestReload_202_CancelWaiting(t *testing.T) {
	s := &Service{}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.reload(ctx) }()
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("取消未结束互斥等待")
	}
}
