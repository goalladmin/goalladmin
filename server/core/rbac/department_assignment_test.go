package rbac

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
)

func TestRoleMembers_200_RoleOwnership(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	role := createRaceRole(t, ctx, s, "member-scope")
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", 42, []uint64{role.ID}))
	// 库中残留的跨端成员关系也不能成为本端角色的持有人。
	require.NoError(t, s.st.replaceUserRoles(ctx, "other", 43, []uint64{role.ID}))
	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
		ids, err := s.st.roleMembersLocked(ctx, "platform", 0, role.ID)
		require.NoError(t, err)
		require.Equal(t, []uint64{42}, ids)
		ids, err = s.st.roleMembersLocked(ctx, "platform", 9, role.ID)
		require.NoError(t, err)
		require.Empty(t, ids, "其他主体看不到成员")
		ids, err = s.st.roleMembersLocked(ctx, "other", 0, role.ID)
		require.NoError(t, err)
		require.Empty(t, ids, "其他端看不到成员")
		return nil
	}))
}
