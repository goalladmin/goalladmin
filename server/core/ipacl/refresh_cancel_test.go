package ipacl_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/ipacl"
)

func TestRefresh_194_CancelledTrigger(t *testing.T) {
	ctx, gdb, clock := setup(t)
	reader := newSvc(t, ctx, gdb, clock)
	writer := newSvc(t, ctx, gdb, clock)
	_, err := writer.AddDeny(ctx, ipacl.DenyInput{CIDR: "203.0.113.5"}, 1, "192.0.2.10")
	require.NoError(t, err)
	clock.Advance(6 * time.Second)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.True(t, reader.Blocked(cancelled, "203.0.113.5"), "取消触发请求仍完成共享名单刷新")
	clock.Advance(time.Second)
	require.True(t, reader.Blocked(ctx, "203.0.113.5"))
}
