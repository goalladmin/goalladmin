package db_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// testDatabase 把 GA_TEST_DSN（user:pw@tcp(host:port)/name?params）拆成配置；没设置时跳过。
func testDatabase(t *testing.T) conf.Database {
	t.Helper()
	dsn := os.Getenv(db.TestDSNEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过需要 MySQL 的测试", db.TestDSNEnv)
	}
	cred, rest, _ := strings.Cut(dsn, "@tcp(")
	user, pw, _ := strings.Cut(cred, ":")
	addr, rest, _ := strings.Cut(rest, ")/")
	name, params, _ := strings.Cut(rest, "?")
	host, portStr, _ := strings.Cut(addr, ":")
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	d := conf.Default().Database
	d.Host, d.Port, d.User, d.Password, d.Name, d.Params = host, port, user, pw, name, params
	return d
}

func TestOpen_UnreachableFailsFastWithoutWait(t *testing.T) {
	d := conf.Default().Database
	d.Port = 1 // 本机没人监听，立即被拒
	start := time.Now()
	_, err := db.Open(d, quiet())
	require.Error(t, err)
	require.Contains(t, err.Error(), "connectWait")
	require.Less(t, time.Since(start), 3*time.Second)
}

func TestOpen_WaitsThenGivesUp(t *testing.T) {
	d := conf.Default().Database
	d.Port = 1
	d.ConnectWait = 3 * time.Second
	start := time.Now()
	_, err := db.Open(d, quiet())
	require.Error(t, err)
	require.Contains(t, err.Error(), "仍不可用")
	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 3*time.Second, "打开启动等待后一直试到等满")
	require.Less(t, elapsed, 5*time.Second, "等满就放弃")
}

func TestOpen_ReadTimeoutBoundsAHungQuery(t *testing.T) {
	d := testDatabase(t)
	d.ReadTimeout = time.Second
	gdb, err := db.Open(d, quiet())
	require.NoError(t, err)
	defer func() { _ = db.Close(gdb) }()

	// 没有 ctx 时限的查询（后台任务那种）遇到数据库不回话：最多等 readTimeout
	start := time.Now()
	err = gdb.WithContext(context.Background()).Exec("SELECT SLEEP(5)").Error
	require.Error(t, err)
	require.Less(t, time.Since(start), 4*time.Second)

	// 坏掉的连接被丢弃，连接池照常可用
	var n int
	require.NoError(t, gdb.Raw("SELECT 1").Scan(&n).Error)
	require.Equal(t, 1, n)
}

func TestOpenMigrator_NotBoundByReadTimeout(t *testing.T) {
	d := testDatabase(t)
	d.ReadTimeout = time.Second
	gdb, err := db.OpenMigrator(d, quiet())
	require.NoError(t, err)
	defer func() { _ = db.Close(gdb) }()
	// 迁移里的长语句不能被读超时掐断
	var n int
	require.NoError(t, gdb.Raw("SELECT SLEEP(2)").Scan(&n).Error)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	require.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)
}
