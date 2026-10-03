package ipacl

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func notificationServices(t *testing.T) (context.Context, *gorm.DB, *Service, *Service) {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	opts := Options{DB: gdb, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Refresh: time.Hour}
	a, b := New(opts), New(opts)
	require.NoError(t, a.Load(ctx))
	require.NoError(t, b.Load(ctx))
	return ctx, gdb, a, b
}

// D-075：名单只在成功提交后通知；接收方只标记内存，下次请求再核对序号。
func TestNotify_CommitRollbackAndRemoteRefresh(t *testing.T) {
	ctx, _, a, b := notificationServices(t)
	calls, remoteCalls := 0, 0
	a.onChange = func() {
		calls++
		b.Invalidate()
	}
	b.onChange = func() { remoteCalls++ }
	before := b.snap.Load()
	rollback := errors.New("rollback")
	for _, committed := range []bool{false, true} {
		err := db.Tx(ctx, func(txctx context.Context) error {
			_, err := a.AddDeny(txctx, DenyInput{CIDR: "203.0.113.5"}, 1, "")
			require.NoError(t, err)
			require.Zero(t, calls)
			require.Same(t, before, b.snap.Load())
			if !committed {
				return rollback
			}
			return nil
		})
		if committed {
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Same(t, before, b.snap.Load(), "通知本身不查库")
			require.Zero(t, b.checked.Load())
			require.True(t, b.Blocked(ctx, "203.0.113.5"), "通知让下一请求立即核对名单")
		} else {
			require.ErrorIs(t, err, rollback)
			require.Zero(t, calls)
			require.False(t, b.Blocked(ctx, "203.0.113.5"))
		}
	}
	require.Zero(t, remoteCalls)
}

func TestInvalidate_DuringReloadDoesNotPublishOldSnapshot(t *testing.T) {
	ctx, _, a, b := notificationServices(t)
	a.onChange = b.Invalidate
	before := b.snap.Load()
	fired := false
	b.testHook = func(stage string) {
		if stage != "reload.read" || fired {
			return
		}
		fired = true
		_, err := a.AddDeny(ctx, DenyInput{CIDR: "203.0.113.5"}, 1, "")
		require.NoError(t, err)
	}
	require.NoError(t, b.Load(ctx))
	require.Same(t, before, b.snap.Load(), "失效之后不能把旧的读取结果发布")
	require.Zero(t, b.checked.Load())
	b.testHook = nil
	require.True(t, b.Blocked(ctx, "203.0.113.5"))
}

// 序号查询已经读到旧序号，此时通知到达：本次可以沿用旧快照，但下一次必须立即重新核对。
func TestInvalidate_DuringSequenceCheckIsNotLost(t *testing.T) {
	ctx, _, a, b := notificationServices(t)
	a.onChange = b.Invalidate
	b.checked.Store(0)
	fired := false
	b.testHook = func(stage string) {
		if stage != "seq.read" || fired {
			return
		}
		fired = true
		_, err := a.AddDeny(ctx, DenyInput{CIDR: "203.0.113.5"}, 1, "")
		require.NoError(t, err)
	}
	require.False(t, b.Blocked(ctx, "203.0.113.5"), "查询读到的仍是提交前的序号")
	require.Zero(t, b.checked.Load(), "序号读取不能覆盖通知留下的待重读标记")
	b.testHook = nil
	require.True(t, b.Blocked(ctx, "203.0.113.5"))
}

func TestNotify_ReloadFailureKeepsOldRulesAndStillNotifies(t *testing.T) {
	ctx, gdb, a, b := notificationServices(t)
	_, err := a.AddDeny(ctx, DenyInput{CIDR: "203.0.113.5"}, 1, "")
	require.NoError(t, err)
	calls := 0
	a.onChange = func() {
		calls++
		b.Invalidate()
	}
	a.base = gdb.Table("missing_ip_rule_table")
	_, err = a.AddDeny(ctx, DenyInput{CIDR: "203.0.113.6"}, 1, "")
	require.NoError(t, err)
	require.Equal(t, 1, calls, "写入已提交，即使本地重读失败也要通知")
	require.True(t, a.Blocked(ctx, "203.0.113.5"), "数据库读失败保留旧名单")
	require.True(t, b.Blocked(ctx, "203.0.113.6"), "远端仍然看到已提交的规则")
	a.base = gdb
	a.Invalidate()
	require.True(t, a.Blocked(ctx, "203.0.113.6"))
}
