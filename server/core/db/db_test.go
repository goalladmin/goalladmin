package db_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func TestFrom_PanicsWithoutHandle(t *testing.T) {
	require.PanicsWithError(t, db.ErrNoDB.Error(), func() { db.From(context.Background()) })
}

func TestTx_ErrorsWithoutHandle(t *testing.T) {
	err := db.Tx(context.Background(), func(ctx context.Context) error { return nil })
	require.ErrorIs(t, err, db.ErrNoDB)
}

func TestSplitStatements(t *testing.T) {
	sql := `
-- 注释
CREATE TABLE t (
  id int
);
INSERT INTO t VALUES (1); -- 行尾注释不处理，但语句照常
INSERT INTO t (name) VALUES ('a;b');
INSERT INTO t (name) VALUES ('it''s;
still one');
`
	got := db.SplitStatements(sql)
	require.Len(t, got, 4)
	require.Contains(t, got[0], "CREATE TABLE t")
	require.Equal(t, "INSERT INTO t (name) VALUES ('a;b')", got[2])
	require.Contains(t, got[3], "still one")
}

func TestLoadMigrations_ValidatesNamesAndDuplicates(t *testing.T) {
	fsys := fstest.MapFS{
		"m/00002_b.sql": {Data: []byte("SELECT 2;")},
		"m/00001_a.sql": {Data: []byte("SELECT 1;")},
		"m/README.md":   {Data: []byte("ignored")},
	}
	ms, err := db.LoadMigrations(fsys, "m")
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, []int64{ms[0].Version, ms[1].Version})

	bad := fstest.MapFS{"m/init.sql": {Data: []byte("SELECT 1;")}}
	_, err = db.LoadMigrations(bad, "m")
	require.Error(t, err)

	dup := fstest.MapFS{
		"m/00001_a.sql": {Data: []byte("SELECT 1;")},
		"m/1_b.sql":     {Data: []byte("SELECT 1;")},
	}
	_, err = db.LoadMigrations(dup, "m")
	require.ErrorContains(t, err, "重复")
}

func TestMigrateUp_RejectsBadTableName(t *testing.T) {
	_, err := db.MigrateUp(context.Background(), nil, fstest.MapFS{}, "m", "Bad-Name")
	require.ErrorContains(t, err, "版本表名")
}

// ---- 以下需要真实 MySQL（GA_TEST_DSN）----

func TestTx_NestedSharesTransactionAndRollsBack(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	require.NoError(t, db.From(ctx).Exec("CREATE TABLE t_tx (id int PRIMARY KEY)").Error)

	boom := errors.New("boom")
	err := db.Tx(ctx, func(ctx context.Context) error {
		require.True(t, db.InTx(ctx))
		require.NoError(t, db.From(ctx).Exec("INSERT INTO t_tx VALUES (1)").Error)
		// 嵌套：必须加入同一事务，能看到未提交的行
		return db.Tx(ctx, func(inner context.Context) error {
			var n int64
			require.NoError(t, db.From(inner).Raw("SELECT COUNT(*) FROM t_tx").Scan(&n).Error)
			require.EqualValues(t, 1, n)
			return boom
		})
	})
	require.ErrorIs(t, err, boom)

	var n int64
	require.NoError(t, db.From(ctx).Raw("SELECT COUNT(*) FROM t_tx").Scan(&n).Error)
	require.EqualValues(t, 0, n, "外层事务必须整体回滚")
	require.False(t, db.InTx(ctx))
}

func TestTx_CommitsOnSuccess(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	require.NoError(t, db.From(ctx).Exec("CREATE TABLE t_ok (id int PRIMARY KEY)").Error)
	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
		return db.From(ctx).Exec("INSERT INTO t_ok VALUES (1)").Error
	}))
	var n int64
	require.NoError(t, db.From(ctx).Raw("SELECT COUNT(*) FROM t_ok").Scan(&n).Error)
	require.EqualValues(t, 1, n)
}

func TestTx_AfterCommitRunsOnlyAfterOutermostCommit(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)

	// 不在事务里：立即执行
	ran := 0
	db.AfterCommit(ctx, func() { ran++ })
	require.Equal(t, 1, ran)

	// 在事务里：事务体内不执行，嵌套层注册的也要等最外层提交；回滚则一律不执行
	var order []string
	boom := errors.New("boom")
	err := db.Tx(ctx, func(ctx context.Context) error {
		db.AfterCommit(ctx, func() { order = append(order, "outer") })
		_ = db.Tx(ctx, func(ctx context.Context) error {
			db.AfterCommit(ctx, func() { order = append(order, "inner") })
			return nil
		})
		require.Empty(t, order, "提交前不能执行")
		return boom
	})
	require.ErrorIs(t, err, boom)
	require.Empty(t, order, "回滚后不能执行")

	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
		db.AfterCommit(ctx, func() { order = append(order, "outer") })
		return db.Tx(ctx, func(ctx context.Context) error {
			db.AfterCommit(ctx, func() { order = append(order, "inner") })
			return nil
		})
	}))
	require.Equal(t, []string{"outer", "inner"}, order)
}

func TestMigrateUp_CoreMigrationsCreateAllTables(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)

	applied, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	require.NotEmpty(t, applied)

	var tables []string
	require.NoError(t, gdb.Raw("SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name").Scan(&tables).Error)
	for _, want := range []string{"ga_user", "ga_role", "ga_user_role", "ga_casbin_rule", "ga_session", "ga_login_log", "ga_operation_log", migrations.CoreTable} {
		require.Contains(t, tables, want)
	}

	// super 角色已种下，且不存在任何账号
	var roles, users int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_role WHERE portal='platform' AND code='super' AND is_super=1").Scan(&roles).Error)
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_user").Scan(&users).Error)
	require.EqualValues(t, 1, roles)
	require.EqualValues(t, 0, users)

	// 幂等：第二次不再应用任何东西
	again, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	require.Empty(t, again)

	st, err := db.MigrateStatus(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	require.Len(t, st, len(applied))
	for _, s := range st {
		require.NotNil(t, s.AppliedAt, "版本 %d 应已应用", s.Version)
	}
}

func TestMigrateUp_StopsAtFailingFileAndRetries(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	fsys := fstest.MapFS{
		"m/00001_ok.sql":  {Data: []byte("CREATE TABLE m1 (id int);")},
		"m/00002_bad.sql": {Data: []byte("CREATE TABLE m2 (id int);\nINSERT INTO nope VALUES (1);")},
	}
	applied, err := db.MigrateUp(ctx, gdb, fsys, "m", "t_schema_version")
	require.Error(t, err)
	require.ErrorContains(t, err, "00002_bad.sql")
	require.Len(t, applied, 1)

	st, err := db.MigrateStatus(ctx, gdb, fsys, "m", "t_schema_version")
	require.NoError(t, err)
	require.NotNil(t, st[0].AppliedAt)
	require.Nil(t, st[1].AppliedAt, "失败的文件不记版本")

	// 模块迁移与框架迁移互不影响：各自的版本表
	_, err = db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
}

// AfterEnd：提交、回滚、fn 里 panic 都会调用，并说明是否提交（D-055）；不在事务里时立即调用。
func TestAfterEnd_RunsOnCommitRollbackAndPanic(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.WithDB(context.Background(), gdb)
	var got []bool
	record := func(committed bool) { got = append(got, committed) }

	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error { db.AfterEnd(ctx, record); return nil }))
	require.Error(t, db.Tx(ctx, func(ctx context.Context) error { db.AfterEnd(ctx, record); return errors.New("x") }))
	require.Panics(t, func() {
		_ = db.Tx(ctx, func(ctx context.Context) error { db.AfterEnd(ctx, record); panic("boom") })
	})
	db.AfterEnd(ctx, record)
	require.Equal(t, []bool{true, false, false, true}, got)
}
