package captcha

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/stretchr/testify/require"
)

type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestShared_199_CallerDeadlineNeverFallsBack(t *testing.T) {
	fake := redisx.NewTestFakeServer(t, "redis_version:6.0.20\r\n")
	client, err := redisx.Open(redisx.Options{Addr: fake.Addr(), KeyPrefix: "deadline-fixture:", Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	c := NewShared(nil, client)
	ctx := delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	id, err := c.store(ctx, "platform", "fixture", "12345")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, id)
	require.Zero(t, c.size())
	require.True(t, client.Available())
}
