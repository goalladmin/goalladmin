package redisx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// 模拟连接计时已到，但调用方 context 定时器尚未被调度的状态。
type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestDo_199_CallerDeadlinePropagation(t *testing.T) {
	c := &Client{}
	c.up.Store(true)
	ctx := delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	original := errors.New("fixture read deadline")
	err := c.Do(ctx, func(context.Context, redis.Cmdable) error { return original })
	require.ErrorIs(t, err, original)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, ctx.Err(), "调用方定时器尚未更新")
	require.True(t, c.Available())
}
