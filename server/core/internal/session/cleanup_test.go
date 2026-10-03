package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func cleanupFixture(t *testing.T) (*Manager, context.Context, time.Time) {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	return NewManager(Config{Now: func() time.Time { return now }, RefreshTTL: 7 * 24 * time.Hour}), ctx, now
}

func historySession(id int, portal string, expiry time.Time, revoked *time.Time) Session {
	return Session{SID: fmt.Sprintf("%032x", id), Portal: portal, UserID: 1,
		RefreshHash: strings.Repeat("a", 64), ExpiresAt: expiry, RevokedAt: revoked,
		CreatedAt: expiry.Add(-100 * 24 * time.Hour), RotatedAt: expiry.Add(-time.Hour), LastSeenAt: expiry.Add(-time.Hour)}
}

// 规范 §13.2 第 170 条：保留边界、跨端、仍有效的旧会话及日志。
func TestCleanup_RetentionAndPortal(t *testing.T) {
	m, ctx, now := cleanupFixture(t)
	keep := 30 * 24 * time.Hour
	cut := now.Add(-keep)
	old, recent, future := cut.Add(-time.Millisecond), cut.Add(time.Millisecond), now.Add(time.Hour)
	rows := []Session{
		historySession(1, "platform", old, nil),
		historySession(2, "platform", cut, nil),
		historySession(3, "platform", recent, nil),
		historySession(4, "platform", future, nil),
		historySession(5, "platform", future, &old),
		historySession(6, "platform", future, &cut),
		historySession(7, "platform", future, &recent),
		historySession(8, "platform", old, &old),
		historySession(9, "agent", old, &old),
		historySession(10, "merchant", old, nil),
		historySession(11, "platform", future, nil),
	}
	rows[10].LockedAt = &now
	require.NoError(t, db.From(ctx).Create(&rows).Error)
	log := LoginLog{Portal: "platform", SessionID: rows[0].SID, Username: "owner", CreatedAt: old}
	require.NoError(t, db.From(ctx).Create(&log).Error)
	for _, args := range []struct {
		portal string
		keep   time.Duration
	}{{"", keep}, {"platform", 0}, {"platform", -keep}} {
		n, err := m.Cleanup(ctx, args.portal, args.keep)
		require.Error(t, err)
		require.Zero(t, n)
	}
	n, err := m.Cleanup(ctx, "platform", keep)
	require.NoError(t, err)
	require.EqualValues(t, 3, n)
	var ids []uint64
	require.NoError(t, db.From(ctx).Model(&Session{}).Order("id").Pluck("id", &ids).Error)
	require.Equal(t, []uint64{2, 3, 4, 6, 7, 9, 10, 11}, ids)
	var logs int64
	require.NoError(t, db.From(ctx).Model(&LoginLog{}).Count(&logs).Error)
	require.EqualValues(t, 1, logs)
	n, err = m.Cleanup(ctx, "platform", keep)
	require.NoError(t, err)
	require.Zero(t, n)
}

func TestCleanup_BatchesConcurrentAndRollback(t *testing.T) {
	m, ctx, now := cleanupFixture(t)
	old := now.Add(-31 * 24 * time.Hour)
	rows := make([]Session, 0, 2402)
	for i := 1; i <= 1201; i++ {
		rows = append(rows, historySession(i, "agent", old, nil), historySession(i+1201, "agent", now.Add(time.Hour), &old))
	}
	require.NoError(t, db.From(ctx).CreateInBatches(&rows, 200).Error)
	rollback := errors.New("rollback")
	require.ErrorIs(t, db.Tx(ctx, func(tx context.Context) error {
		n, err := m.Cleanup(tx, "agent", 30*24*time.Hour)
		require.NoError(t, err)
		require.EqualValues(t, 1000, n)
		return rollback
	}), rollback)
	var count int64
	require.NoError(t, db.From(ctx).Model(&Session{}).Count(&count).Error)
	require.EqualValues(t, 2402, count)
	n, err := m.Cleanup(ctx, "agent", 30*24*time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 1000, n)
	type result struct {
		n   int64
		err error
	}
	out := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			n, err := NewManager(Config{Now: func() time.Time { return now }}).Cleanup(ctx, "agent", 30*24*time.Hour)
			out <- result{n, err}
		})
	}
	wg.Wait()
	close(out)
	total := n
	for r := range out {
		require.NoError(t, r.err)
		require.LessOrEqual(t, r.n, int64(1000))
		total += r.n
	}
	require.EqualValues(t, 2402, total)
	require.NoError(t, db.From(ctx).Model(&Session{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestCleanup_PartialFailureCancellationAndRecovery(t *testing.T) {
	m, ctx, now := cleanupFixture(t)
	old := now.Add(-31 * 24 * time.Hour)
	rows := []Session{historySession(1, "platform", old, nil), historySession(2, "platform", now.Add(time.Hour), &old)}
	gdb := db.From(ctx)
	require.NoError(t, gdb.Create(&rows).Error)
	failure := errors.New("temporary cleanup failure")
	require.NoError(t, gdb.Callback().Raw().Before("gorm:raw").Register("test:cleanup-failure", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "ORDER BY revoked_at") {
			_ = tx.AddError(failure)
		}
	}))
	t.Cleanup(func() { _ = gdb.Callback().Raw().Remove("test:cleanup-failure") })
	n, err := m.Cleanup(ctx, "platform", 30*24*time.Hour)
	require.ErrorIs(t, err, failure)
	require.EqualValues(t, 1, n)
	require.NoError(t, gdb.Callback().Raw().Remove("test:cleanup-failure"))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	n, err = m.Cleanup(canceled, "platform", 30*24*time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, n)
	n, err = m.Cleanup(ctx, "platform", 30*24*time.Hour)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

func TestCleanup_LockTimeoutAndCurrentExpiry(t *testing.T) {
	m, ctx, now := cleanupFixture(t)
	row := historySession(1, "platform", now.Add(-31*24*time.Hour), nil)
	gdb := db.From(ctx)
	require.NoError(t, gdb.Create(&row).Error)
	tx := gdb.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { tx.Rollback() })
	var id uint64
	require.NoError(t, tx.Raw("SELECT id FROM ga_session WHERE id = ? FOR UPDATE", row.ID).Scan(&id).Error)
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	n, err := m.Cleanup(limited, "platform", 30*24*time.Hour)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, n)
	// 阻塞的删除取消后，把记录改为有效；后续清理必须重新判断过期时间。
	require.NoError(t, tx.Model(&Session{}).Where("id = ?", row.ID).Update("expires_at", now.Add(time.Hour)).Error)
	require.NoError(t, tx.Commit().Error)
	n, err = m.Cleanup(ctx, "platform", 30*24*time.Hour)
	require.NoError(t, err)
	require.Zero(t, n)
	got, err := m.Get(ctx, row.SID)
	require.NoError(t, err)
	require.True(t, got.Active(now))
}

func TestCleanup_IndexMigrationCanResumeAndRepeat(t *testing.T) {
	_, ctx, now := cleanupFixture(t)
	gdb := db.From(ctx)
	row := historySession(1, "platform", now.Add(-31*24*time.Hour), nil)
	require.NoError(t, gdb.Create(&row).Error)
	// 模拟只完成一个索引、尚未记迁移版本的状态。
	require.NoError(t, gdb.Exec("ALTER TABLE ga_session DROP INDEX idx_session_portal_revoked").Error)
	for range 2 {
		require.NoError(t, gdb.Exec("DELETE FROM ga_schema_version WHERE version = 18").Error)
		applied, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
		require.NoError(t, err)
		require.Len(t, applied, 1)
		require.EqualValues(t, 18, applied[0].Version)
	}
	for name, expected := range map[string][]string{
		"idx_session_portal_expires": {"portal", "expires_at"},
		"idx_session_portal_revoked": {"portal", "revoked_at"},
	} {
		var columns []string
		require.NoError(t, gdb.Raw("SELECT COLUMN_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'ga_session' AND INDEX_NAME = ? ORDER BY SEQ_IN_INDEX", name).Scan(&columns).Error)
		require.Equal(t, expected, columns)
	}
	var count int64
	require.NoError(t, gdb.Model(&Session{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
