package rbac

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPolicy_196_ConcurrentInvalidationWait(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	r := createRaceRole(t, ctx, s, "wait-role")
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", 42, []uint64{r.ID}))
	require.NoError(t, s.Reload())
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, nil))
	s.InvalidatePolicy()
	read, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	s.testHook = func(stage string) {
		if stage == "reload.read" && calls.Add(1) == 1 {
			close(read)
			<-release
		}
	}
	type result struct {
		allowed bool
		err     error
	}
	done := make(chan result, 2)
	check := func() { ok, err := s.Allowed(ctx, "platform", 42, "m:thing:list"); done <- result{ok, err} }
	go check()
	select {
	case <-read:
	case <-time.After(time.Second):
		t.Fatal("未开始重载")
	}
	go check()
	select {
	case <-done:
		t.Fatal("等待期间不得返回旧权限或立即 503")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	for range 2 {
		select {
		case got := <-done:
			require.NoError(t, got.err)
			require.False(t, got.allowed)
		case <-time.After(time.Second):
			t.Fatal("重载完成后应放行判定")
		}
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestPolicy_196_CancelledBuild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newEnforcerContext(ctx, []policyRule{{PType: "p", V0: "role:1", V1: "platform", V2: "m:thing:list"}})
	require.ErrorIs(t, err, context.Canceled)
}
