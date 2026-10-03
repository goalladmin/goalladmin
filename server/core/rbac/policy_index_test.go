package rbac

import (
	"context"
	"fmt"
	"testing"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/stretchr/testify/require"
)

func TestPolicy_196_RoleIndexAndBounds(t *testing.T) {
	rows := []policyRule{{PType: "p", V0: "role:1", V1: "platform", V2: "m:thing:list"}, {PType: "p", V0: "role:1", V1: "platform", V2: "m:other:*"}}
	for i := 2; i < 12; i++ {
		rows = append(rows, policyRule{PType: "p", V0: fmt.Sprintf("role:%d", i), V1: "merchant", V2: "m:thing:*"})
	}
	e, err := newEnforcer(rows)
	require.NoError(t, err)
	p, err := e.byRole[policyKey{"role:1", "platform"}].GetPolicy()
	require.NoError(t, err)
	require.Len(t, p, 2, "其他角色的规则不进入目标扫描集合")
	require.True(t, e.allow(1, "platform", "m:thing:list"))
	require.False(t, e.allow(1, "platform", "m:thing:update"))
	require.True(t, e.allow(1, "platform", "m:other:update"))
	require.False(t, e.allow(1, "merchant", "m:other:update"))
	s := &Service{}
	require.ErrorIs(t, s.AssignUserRoles(context.Background(), auth.Principal{}, "platform", 1, make([]uint64, MaxRolesPerUser+1)), httpx.ErrValidation)
	require.ErrorIs(t, s.GrantRole(context.Background(), auth.Principal{}, "platform", 1, make([]string, MaxGrantCodes+1), nil), httpx.ErrValidation)
}
