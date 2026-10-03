package rbac

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/stretchr/testify/require"
)

func TestWriteAdmission_197_NestedAndBeforeTransaction(t *testing.T) {
	s := &Service{selfWrites: ratelimit.NewKeyedGate(2), allWrites: ratelimit.NewGate(32)}
	ctx := context.Background()
	key := "self:platform/8"
	require.NoError(t, s.withWriteAdmission(ctx, key, s.selfWrites, func(ctx context.Context) error {
		return s.withWriteAdmission(ctx, key, s.selfWrites, func(context.Context) error { return nil })
	}))
	require.True(t, s.selfWrites.TryEnter(key))
	require.True(t, s.selfWrites.TryEnter(key))
	require.ErrorIs(t, s.WithSelf(ctx, auth.Principal{Portal: "platform", UserID: 8}, func(context.Context) error { t.Fatal("拒绝后不执行回调"); return nil }), httpx.ErrTooManyRequests)
	s.selfWrites.Leave(key)
	s.selfWrites.Leave(key)
	require.NoError(t, s.withWriteAdmission(ctx, key, s.selfWrites, func(context.Context) error { return nil }))
}

func TestWriteAdmission_197_ManagementAndGlobal(t *testing.T) {
	s := &Service{selfWrites: ratelimit.NewKeyedGate(2), managerWrites: ratelimit.NewKeyedGate(2), allWrites: ratelimit.NewGate(32)}
	ctx := context.Background()
	key := "manager:platform/0"
	require.True(t, s.managerWrites.TryEnter(key))
	require.True(t, s.managerWrites.TryEnter(key))
	require.ErrorIs(t, s.withLock(ctx, "platform", 0, func(context.Context) error { t.Fatal("满位不得开事务"); return nil }), httpx.ErrTooManyRequests)
	s.managerWrites.Leave(key)
	s.managerWrites.Leave(key)
	for range 32 {
		require.True(t, s.allWrites.TryEnter())
	}
	require.ErrorIs(t, s.withLock(ctx, "platform", 0, func(context.Context) error { t.Fatal("满位不得开事务"); return nil }), httpx.ErrTooManyRequests)
	for range 32 {
		s.allWrites.Leave()
	}
	require.NoError(t, s.withWriteAdmission(ctx, key, s.managerWrites, func(context.Context) error { return nil }))
}

func TestWriteAdmission_197_RoleWindowIdentity(t *testing.T) {
	now := time.Now()
	s := &Service{roleWrites: ratelimit.New(1, time.Minute, 10, func() time.Time { return now })}
	ctx := context.Background()
	require.NoError(t, s.reserveRoleWrite(ctx, auth.Principal{Portal: "platform", UserID: 8}))
	require.ErrorIs(t, s.reserveRoleWrite(ctx, auth.Principal{Portal: "platform", UserID: 9}), httpx.ErrTooManyRequests)
	require.NoError(t, s.reserveRoleWrite(ctx, auth.Principal{Portal: "merchant", UserID: 8}))
	now = now.Add(time.Minute)
	require.NoError(t, s.reserveRoleWrite(ctx, auth.Principal{Portal: "platform", UserID: 8}))
}

func TestRole_196_TotalLimit(t *testing.T) {
	s, ctx, gdb := newRaceService(t)
	var count int64
	require.NoError(t, gdb.Model(&roleRow{}).Where("portal = ? AND org_id = 0", "platform").Count(&count).Error)
	var rows []roleRow
	for i := int(count); i < MaxRolesPerOrg; i++ {
		rows = append(rows, roleRow{Portal: "platform", Code: "limit-" + fmt.Sprint(i), Name: "limit", Status: 1})
	}
	require.NoError(t, gdb.Create(&rows).Error)
	_, err := s.CreateRole(ctx, auth.Principal{Portal: "platform", Super: true}, "platform", RoleInput{Code: "over-limit", Name: "limit", Status: 1})
	require.ErrorIs(t, err, httpx.ErrValidation)
}
