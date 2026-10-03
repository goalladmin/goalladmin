package rbac

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
)

func TestLockedHelpers_201_RequireTransaction(t *testing.T) {
	// 无数据库和注册表：调用须在读取身份、权限或数据库之前拒绝。
	s := &Service{}
	ctx := context.Background()
	for _, p := range []auth.Principal{
		{Portal: "platform", UserID: 8, SessionID: "session"},
		{Portal: "platform", Super: true},
		{Portal: "merchant", OrgID: 9, UserID: 8, Super: true},
	} {
		t.Run(p.Portal+"/"+p.SessionID, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				run  func() error
			}{
				{"CurrentActor", func() error {
					actor, err := s.CurrentActor(ctx, p)
					require.Equal(t, auth.Principal{}, actor)
					return err
				}},
				{"AllowedLocked", func() error {
					allowed, err := s.AllowedLocked(ctx, p, "system:user:list")
					require.False(t, allowed)
					return err
				}},
				{"DataFilterLocked", func() error {
					filter, err := s.DataFilterLocked(ctx, p, "system:user", "system:user:list")
					require.Equal(t, DataFilter{}, filter)
					return err
				}},
				{"CheckEnableUser", func() error { return s.CheckEnableUser(ctx, p, p.Portal, 10) }},
				{"IsLastSuper", func() error {
					last, err := s.IsLastSuper(ctx, p.Portal, 10)
					require.False(t, last)
					return err
				}},
			} {
				t.Run(tc.name, func(t *testing.T) { require.ErrorIs(t, tc.run(), ErrNoTx) })
			}
		})
	}
}
