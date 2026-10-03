package db_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
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

// D-061：不执行迁移的程序启动时只读地核对：不建版本表、不执行 DDL；落后就报 ErrMigrationsPending，库比程序新不算错。
func TestMigrateCheck_ReadOnlyAndDetectsPending(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	fsys := fstest.MapFS{
		"m/00001_a.sql": {Data: []byte("CREATE TABLE t_check_a (id int);")},
		"m/00002_b.sql": {Data: []byte("CREATE TABLE t_check_b (id int);")},
	}
	tableExists := func(name string) bool {
		var n int64
		require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", name).Scan(&n).Error)
		return n > 0
	}

	// 从没迁移过：报落后，而且不顺手建版本表
	err := db.MigrateCheck(ctx, gdb, fsys, "m", "t_check_version")
	require.ErrorIs(t, err, db.ErrMigrationsPending)
	require.False(t, tableExists("t_check_version"), "核对不能建版本表")

	// 只应用了第一个：报落后，并列出没应用的文件
	_, err = db.MigrateUp(ctx, gdb, fstest.MapFS{"m/00001_a.sql": fsys["m/00001_a.sql"]}, "m", "t_check_version")
	require.NoError(t, err)
	err = db.MigrateCheck(ctx, gdb, fsys, "m", "t_check_version")
	require.ErrorIs(t, err, db.ErrMigrationsPending)
	require.ErrorContains(t, err, "00002_b.sql")
	require.False(t, tableExists("t_check_b"), "核对不能执行迁移")

	// 全部应用：通过
	_, err = db.MigrateUp(ctx, gdb, fsys, "m", "t_check_version")
	require.NoError(t, err)
	require.NoError(t, db.MigrateCheck(ctx, gdb, fsys, "m", "t_check_version"))

	// 库比程序新（平台先升级、多了程序不认识的迁移）：不算落后
	require.NoError(t, db.MigrateCheck(ctx, gdb, fstest.MapFS{"m/00001_a.sql": fsys["m/00001_a.sql"]}, "m", "t_check_version"))
}

// 迁移 00013（D-061）可以重跑：再执行一遍不报错，列和唯一键都只有一份。
func TestMigrate_00013_OrgColumnsRerunnable(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	raw, err := fs.ReadFile(migrations.Core(), migrations.CoreDir+"/00013_org.sql")
	require.NoError(t, err)
	for _, stmt := range db.SplitStatements(string(raw)) {
		require.NoError(t, gdb.Exec(stmt).Error)
	}
	for _, table := range []string{"ga_session", "ga_login_log", "ga_operation_log", "ga_security_event", "ga_role"} {
		var n int64
		require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'org_id'", table).Scan(&n).Error)
		require.EqualValues(t, 1, n, table)
	}
	var keys []string
	require.NoError(t, gdb.Raw("SELECT DISTINCT index_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'ga_role' AND non_unique = 0 ORDER BY index_name").Scan(&keys).Error)
	require.Equal(t, []string{"PRIMARY", "uk_role_portal_org_code"}, keys, "角色的唯一键改成 (portal, org_id, code)")
	// 同一个端里，不同主体可以有同编码的角色；同一主体里不行
	ins := "INSERT INTO ga_role (portal, org_id, code, name, is_super, status, sort, remark, created_at, updated_at, created_by, updated_by) VALUES ('merchant', ?, 'cashier', 'c', 0, 1, 0, '', UTC_TIMESTAMP(3), UTC_TIMESTAMP(3), 0, 0)"
	require.NoError(t, gdb.Exec(ins, 1).Error)
	require.NoError(t, gdb.Exec(ins, 2).Error)
	require.Error(t, gdb.Exec(ins, 1).Error)
}

// 迁移 00016（D-070）：把三张账号表的密码哈希列从 100 个字符加宽到 255，已有的哈希原样留着；可以重跑。
func TestMigrate_00016_PasswordHashWidthRerunnable(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	tables := []string{"ga_user", "ga_agent_user", "ga_merchant_user"}
	width := func(table string) (n int, nullable string) {
		var col struct {
			Len      int    `gorm:"column:len"`
			Nullable string `gorm:"column:nullable"`
		}
		require.NoError(t, gdb.Raw("SELECT CHARACTER_MAXIMUM_LENGTH AS len, IS_NULLABLE AS nullable FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'password_hash'", table).Scan(&col).Error)
		return col.Len, col.Nullable
	}
	// 回到升级前的样子：100 个字符的列，里面有一份旧的 bcrypt 哈希
	for _, table := range tables {
		require.NoError(t, gdb.Exec("ALTER TABLE `"+table+"` MODIFY `password_hash` varchar(100) NOT NULL COMMENT 'bcrypt 哈希'").Error)
		n, _ := width(table)
		require.Equal(t, 100, n, table)
	}
	old := "$2a$12$Nva5w2RIjIpehq7kK63Y4.w6GcOP7tku6vis2v3MOeUwSWBYO6NKq"
	require.NoError(t, gdb.Exec("INSERT INTO ga_user (username, password_hash, created_at, updated_at) VALUES ('legacy', ?, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))", old).Error)
	long := "$argon2id$v=19$m=65536,t=8,p=4$" + strings.Repeat("A", 86) + "$" + strings.Repeat("B", 86) // 参数、盐、输出都取到上限
	require.Error(t, gdb.Exec("UPDATE ga_user SET password_hash = ? WHERE username = 'legacy'", long).Error, "升级前的列放不下")

	raw, err := fs.ReadFile(migrations.Core(), migrations.CoreDir+"/00016_password_hash_width.sql")
	require.NoError(t, err)
	for range 2 { // 跑两遍：第二遍是重跑
		for _, stmt := range db.SplitStatements(string(raw)) {
			require.NoError(t, gdb.Exec(stmt).Error)
		}
		for _, table := range tables {
			n, nullable := width(table)
			require.Equal(t, 255, n, table)
			require.Equal(t, "NO", nullable, table)
		}
		var got string
		require.NoError(t, gdb.Raw("SELECT password_hash FROM ga_user WHERE username = 'legacy'").Scan(&got).Error)
		require.Equal(t, old, got, "已有的哈希原样留着")
	}
	require.NoError(t, gdb.Exec("UPDATE ga_user SET password_hash = ? WHERE username = 'legacy'", long).Error, "参数取到上限的哈希也放得下")
}

// TxReadCommitted（D-063）：新开的事务是 READ COMMITTED——同一事务里的第二次普通读能看到别的连接在第一次读之后提交的行
// （可重复读看不到）；已在事务里时加入外层事务、沿用外层的隔离级别；提交后的回调照常执行。
func TestTxReadCommitted_IsolationAndNesting(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	require.NoError(t, db.From(ctx).Exec("CREATE TABLE t_rc (id int PRIMARY KEY)").Error)
	next := 0
	// seesLaterCommit 在 ctx 的事务里先读一次，再从另一条连接插入并提交一行，然后再读：看得到返回 true
	seesLaterCommit := func(ctx context.Context) bool {
		var before, after int64
		require.NoError(t, db.From(ctx).Raw("SELECT COUNT(*) FROM t_rc").Scan(&before).Error)
		next++
		require.NoError(t, gdb.Exec("INSERT INTO t_rc VALUES (?)", next).Error) // 不在 ctx 的事务里：另一条连接、自动提交
		require.NoError(t, db.From(ctx).Raw("SELECT COUNT(*) FROM t_rc").Scan(&after).Error)
		return after > before
	}
	ran := false
	require.NoError(t, db.TxReadCommitted(ctx, func(ctx context.Context) error {
		require.True(t, db.InTx(ctx))
		require.True(t, seesLaterCommit(ctx), "READ COMMITTED：每条语句都看最新提交的数据")
		db.AfterCommit(ctx, func() { ran = true })
		return nil
	}))
	require.True(t, ran)
	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
		require.False(t, seesLaterCommit(ctx), "对照：Tx 是可重复读")
		return db.TxReadCommitted(ctx, func(inner context.Context) error {
			require.False(t, seesLaterCommit(inner), "嵌套时加入外层事务，沿用外层的可重复读")
			return nil
		})
	}))
}
