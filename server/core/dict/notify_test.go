package dict

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func notificationContext(t *testing.T) context.Context {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	return ctx
}

// D-075：外层提交之后清本地缓存并通知；远端失效不再次通知。
func TestNotify_AfterCommitAndRemoteInvalidation(t *testing.T) {
	ctx := notificationContext(t)
	var notified []string
	s := NewService(Options{PortalOK: platformOnly, OnChange: func(code string) { notified = append(notified, code) }})
	rollback := errors.New("rollback")
	for _, committed := range []bool{false, true} {
		s.cache.Set("level", &loaded{d: dictRow{Name: "old"}})
		err := db.Tx(ctx, func(txctx context.Context) error {
			_, err := s.CreateDict(txctx, DictInput{Code: "level", Portal: "platform", Name: "level"})
			require.NoError(t, err)
			require.Empty(t, notified)
			_, cached := s.cache.Get("level")
			require.True(t, cached)
			if !committed {
				return rollback
			}
			return nil
		})
		_, cached := s.cache.Get("level")
		if committed {
			require.NoError(t, err)
			require.Equal(t, []string{"level"}, notified)
			require.False(t, cached)
		} else {
			require.ErrorIs(t, err, rollback)
			require.Empty(t, notified)
			require.True(t, cached)
		}
	}
	s.cache.Set("level", &loaded{})
	s.cache.Set("other", &loaded{})
	s.Invalidate("level")
	_, cached := s.cache.Get("level")
	require.False(t, cached)
	_, cached = s.cache.Get("other")
	require.True(t, cached, "单编码通知只清对应字典")
	s.Invalidate("")
	_, cached = s.cache.Get("other")
	require.False(t, cached)
	require.Equal(t, []string{"level"}, notified)
}

func TestInvalidate_DuringFillDoesNotBackfillOrNotify(t *testing.T) {
	ctx := notificationContext(t)
	calls := 0
	s := NewService(Options{PortalOK: platformOnly, OnChange: func(string) { calls++ }})
	_, err := s.CreateDict(ctx, DictInput{Code: "level", Portal: "platform", Name: "old"})
	require.NoError(t, err)
	calls = 0
	s.testHook = func(stage string) {
		if stage != "dict.read" {
			return
		}
		require.NoError(t, db.From(ctx).Exec("UPDATE ga_dict SET name = 'new' WHERE code = 'level'").Error)
		s.Invalidate("level")
	}
	l, err := s.load(ctx, "level")
	require.NoError(t, err)
	require.Equal(t, "old", l.d.Name)
	_, cached := s.cache.Get("level")
	require.False(t, cached)
	s.testHook = nil
	l, err = s.load(ctx, "level")
	require.NoError(t, err)
	require.Equal(t, "new", l.d.Name)
	require.Zero(t, calls)
}

// 每本代码字典单独提交：后面一本失败时，前面已经提交的那本仍要通知远端。
func TestSync_PartialCommitAndOrphansNotify(t *testing.T) {
	ctx := notificationContext(t)
	a, z := goodDict(), goodDict()
	a.Code, z.Code = "demo.a", "demo.z"
	var notified []string
	s := NewService(Options{PortalOK: platformOnly, OnChange: func(code string) { notified = append(notified, code) }})
	require.NoError(t, s.Sync(ctx, []Decl{{Module: "demo", Dict: a}, {Module: "demo", Dict: z}}))
	require.Equal(t, []string{a.Code, z.Code}, notified)
	_, err := s.load(ctx, a.Code)
	require.NoError(t, err)
	notified = nil
	a.Name = "changed"
	z.Name = strings.Repeat("x", 1000) // 让第二本在真实数据库写入失败，第一本已经提交。
	require.Error(t, s.Sync(ctx, []Decl{{Module: "demo", Dict: a}, {Module: "demo", Dict: z}}))
	require.Equal(t, []string{a.Code}, notified)
	_, cached := s.cache.Get(a.Code)
	require.False(t, cached)
	l, err := s.load(ctx, a.Code)
	require.NoError(t, err)
	require.Equal(t, "changed", l.d.Name)
	notified = nil
	require.NoError(t, s.Sync(ctx, nil))
	require.ElementsMatch(t, []string{a.Code, z.Code}, notified, "已不再声明的字典转为后台来源也通知")
}

func TestCacheTTL_DefaultIsFifteenSeconds(t *testing.T) {
	now := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	s := NewService(Options{Now: func() time.Time { return now }})
	s.cache.Set("level", &loaded{})
	now = now.Add(14 * time.Second)
	_, cached := s.cache.Get("level")
	require.True(t, cached)
	now = now.Add(time.Second + time.Nanosecond)
	_, cached = s.cache.Get("level")
	require.False(t, cached)
}
