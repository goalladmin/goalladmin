package rbac

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
)

// D-075：本地变更只在外层事务成功提交之后通知；回滚保留原来的缓存和授权。
func TestNotify_PolicyAndMenuAfterCommit(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	root := auth.Principal{Portal: "platform", UserID: 1, Super: true}
	makeSuper(t, ctx, s, root.UserID)
	r := createRaceRole(t, ctx, s, "ops")
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	policyCalls := 0
	s.onPolicyChange = func() {
		require.Zero(t, s.publishing.Load(), "本地发布完成后无需等待远端通知")
		policyCalls++
	}
	rollback := errors.New("rollback")
	for _, committed := range []bool{false, true} {
		err := db.Tx(ctx, func(txctx context.Context) error {
			require.NoError(t, s.AssignUserRoles(txctx, root, "platform", 42, []uint64{r.ID}))
			require.Zero(t, policyCalls, "提交之前不能通知")
			if !committed {
				return rollback
			}
			return nil
		})
		if committed {
			require.NoError(t, err)
			require.Equal(t, 1, policyCalls)
		} else {
			require.ErrorIs(t, err, rollback)
			require.Zero(t, policyCalls)
		}
		ok, err := s.Allowed(ctx, "platform", 42, "m:thing:list")
		require.NoError(t, err)
		require.Equal(t, committed, ok)
	}
	r2, err := s.CreateRole(ctx, root, "platform", RoleInput{Code: "new", Name: "new", Status: 1})
	require.NoError(t, err)
	require.Equal(t, 1, policyCalls, "空角色没有授权变更（D-110）")
	_, err = s.UpdateRole(ctx, root, "platform", r2.ID, RoleInput{Code: "new", Name: "renamed", KeepStatus: true})
	require.NoError(t, err)
	require.Equal(t, 1, policyCalls, "纯显示字段不触发授权重载")
	require.NoError(t, s.GrantRolePerms(ctx, root, "platform", r2.ID, []string{"m:thing:list"}))
	require.Equal(t, 2, policyCalls, "首次授权仍通知变更")

	var menus []string
	s.onMenuChange = func(portal string) { menus = append(menus, portal) }
	for _, committed := range []bool{false, true} {
		s.menuCache.Set("platform", []menuCustom{{Name: "old"}})
		err := db.Tx(ctx, func(txctx context.Context) error {
			err := s.menuTx(txctx, root, func(context.Context, []*layoutNode, map[string]*layoutNode) error { return nil })
			require.NoError(t, err)
			require.Empty(t, menus)
			_, cached := s.menuCache.Get("platform")
			require.True(t, cached, "提交之前本地缓存也保留")
			if !committed {
				return rollback
			}
			return nil
		})
		_, cached := s.menuCache.Get("platform")
		if committed {
			require.NoError(t, err)
			require.Equal(t, []string{"platform"}, menus)
			require.False(t, cached)
		} else {
			require.ErrorIs(t, err, rollback)
			require.Empty(t, menus)
			require.True(t, cached)
		}
	}
}

// 通知打断了正在读取的快照：旧读取不能清掉失效标记，下一次判定重新读取已提交的授权。
func TestInvalidatePolicy_DuringReloadDoesNotPublishOldState(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	r := createRaceRole(t, ctx, s, "ops")
	require.NoError(t, s.st.replaceRolePerms(ctx, "platform", r.ID, []string{"m:thing:list"}))
	require.NoError(t, s.st.replaceUserRoles(ctx, "platform", 42, []uint64{r.ID}))
	require.NoError(t, s.Reload())
	before := s.snap.Load()
	calls := 0
	s.onPolicyChange = func() { calls++ }
	fired := false
	s.testHook = func(stage string) {
		if stage != "reload.read" || fired {
			return
		}
		fired = true
		require.NoError(t, s.st.replaceUserRoles(ctx, "platform", 42, nil))
		s.InvalidatePolicy()
	}
	require.ErrorIs(t, s.Reload(), errPolicyStale)
	require.Same(t, before, s.snap.Load())
	require.True(t, s.stale.Load())
	require.Zero(t, s.retryAt.Load())
	s.testHook = nil
	ok, err := s.Allowed(ctx, "platform", 42, "m:thing:list")
	require.NoError(t, err)
	require.False(t, ok)
	require.Zero(t, calls, "接收通知与惰性重读不能再次发布")
}

func TestInvalidateMenu_DuringFillAndFlushDoNotNotify(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	now := time.Now().UTC()
	require.NoError(t, db.From(ctx).Create(&menuCustom{Portal: "platform", Name: "old", Kind: kindCode, CreatedAt: now, UpdatedAt: now}).Error)
	calls := 0
	s.onMenuChange = func(string) { calls++ }
	s.testHook = func(stage string) {
		if stage == "menu.read" {
			s.InvalidateMenu("platform")
		}
	}
	rows, err := s.menuCustomCached(ctx, "platform")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, cached := s.menuCache.Get("platform")
	require.False(t, cached)
	s.testHook = nil
	s.menuCache.Set("platform", rows)
	s.menuCache.Set("merchant", rows)
	s.InvalidateAllMenus()
	_, cached = s.menuCache.Get("platform")
	require.False(t, cached)
	_, cached = s.menuCache.Get("merchant")
	require.False(t, cached)
	require.Zero(t, calls)
}

// 本地重载失败不能吞掉已提交的变更通知。
func TestNotify_PolicyReloadFailureStillNotifies(t *testing.T) {
	s, ctx, _ := newRaceService(t)
	calls := 0
	s.onPolicyChange = func() { calls++ }
	s.base = context.Background()
	require.NoError(t, db.Tx(ctx, func(txctx context.Context) error {
		s.holdPublish(txctx)
		require.Zero(t, calls)
		return nil
	}))
	require.Equal(t, 1, calls)
	require.True(t, s.stale.Load())
}
