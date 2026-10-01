package dict

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// 另一个实例正在同步时，本实例轮询等锁（不让 GET_LOCK 在服务端等，D-037），对方一放锁就能继续。
func TestSync_WaitsForLockHeldElsewhere(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- gdb.Connection(func(conn *gorm.DB) error {
			var got int
			if err := conn.Raw("SELECT GET_LOCK('ga_dict_sync', 5)").Scan(&got).Error; err != nil {
				close(held)
				return err
			}
			close(held)
			<-release
			return conn.Exec("DO RELEASE_LOCK('ga_dict_sync')").Error
		})
	}()
	<-held
	time.AfterFunc(500*time.Millisecond, func() { close(release) })

	s := NewService(Options{PortalOK: platformOnly})
	start := time.Now()
	require.NoError(t, s.Sync(ctx, []Decl{{Module: "m", Dict: goodDict()}}))
	require.GreaterOrEqual(t, time.Since(start), 400*time.Millisecond, "锁被别人拿着时要等")
	require.Less(t, time.Since(start), 5*time.Second)
	require.NoError(t, <-done)

	var n int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_dict WHERE code = ?", goodDict().Code).Scan(&n).Error)
	require.EqualValues(t, 1, n)
}

// 连接池只允许一条连接时同步也能完成：拿锁的那条连接同时跑同步的事务，不再从池子里取第二条（D-043）。
// 同步如果从池子里再取第二条连接，这里会一直等，所以给它一个上限，超时即失败而不是挂住整个测试。
func TestSync_WorksWithSingleConnectionPool(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.SetMaxOpenConns(0) })

	s := NewService(Options{PortalOK: platformOnly})
	done := make(chan error, 1)
	go func() { done <- s.Sync(ctx, []Decl{{Module: "m", Dict: goodDict()}}) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("maxOpenConns=1 时字典同步没有在 10 秒内完成：同步的 SQL 没有走拿锁的那条连接")
	}
	var n int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM ga_dict WHERE code = ?", goodDict().Code).Scan(&n).Error)
	require.EqualValues(t, 1, n)
	// 锁已释放：再拿一次能立刻拿到
	var got int
	require.NoError(t, gdb.Raw("SELECT GET_LOCK('ga_dict_sync', 0)").Scan(&got).Error)
	require.Equal(t, 1, got)
	require.NoError(t, gdb.Exec("DO RELEASE_LOCK('ga_dict_sync')").Error)
}

// 读方查库期间字典被改并清了缓存：旧值不能再写回缓存（D-043）。
func TestLoad_NoBackfillAfterWrite(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	s := NewService(Options{PortalOK: platformOnly})
	require.NoError(t, s.Sync(ctx, []Decl{{Module: "m", Dict: goodDict()}}))
	code := goodDict().Code

	fired := false
	s.testHook = func(stage string) {
		if stage != "dict.read" || fired {
			return
		}
		fired = true
		require.NoError(t, gdb.Exec("UPDATE ga_dict SET name = 'renamed' WHERE code = ?", code).Error)
		s.cache.Delete(code) // 写入方提交后清缓存（forget 的 AfterCommit 里就是这么做的）
	}
	l, err := s.load(ctx, code)
	require.NoError(t, err)
	require.NotEqual(t, "renamed", l.d.Name, "这次读到的是改名前的状态，属正常")
	s.testHook = nil
	_, cached := s.cache.Get(code)
	require.False(t, cached, "清过缓存之后读方不能把旧值填回去")
	l, err = s.load(ctx, code)
	require.NoError(t, err)
	require.Equal(t, "renamed", l.d.Name)
}
