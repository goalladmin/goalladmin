package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// TestDSNEnv 是测试用连接串的环境变量名（Makefile 里有默认值）。
const TestDSNEnv = "GA_TEST_DSN"

// OpenTestDB 为一个测试创建独立的临时数据库并返回句柄；测试结束时自动删库。
// 未设置 GA_TEST_DSN 时跳过测试。连接串里的用户需要 CREATE/DROP DATABASE 权限。
//
// 放在非 _test 文件里是为了让其他包的测试也能用（core/app、modules/*）。
func OpenTestDB(t testing.TB) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(TestDSNEnv)
	if dsn == "" {
		t.Skipf("未设置 %s，跳过需要 MySQL 的测试", TestDSNEnv)
	}
	admin, err := OpenDSN(dsn, nil)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "ga_test_" + hex.EncodeToString(b[:])
	if err := admin.Exec(fmt.Sprintf("CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci", name)).Error; err != nil {
		t.Fatalf("创建临时库失败: %v", err)
	}
	_ = Close(admin)

	gdb, err := OpenDSN(replaceDBName(dsn, name), nil)
	if err != nil {
		t.Fatalf("连接临时库失败: %v", err)
	}
	t.Cleanup(func() {
		_ = gdb.Exec(fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name)).Error
		_ = Close(gdb)
	})
	return gdb
}

// TestContext 返回带数据库句柄的 ctx，供测试直接调用 From / Tx。
func TestContext(t testing.TB, gdb *gorm.DB) context.Context {
	t.Helper()
	return WithDB(context.Background(), gdb)
}

// replaceDBName 把 DSN "user:pw@tcp(host)/dbname?params" 里的库名换掉。
func replaceDBName(dsn, name string) string {
	slash := strings.LastIndex(dsn, "/")
	if slash < 0 {
		return dsn
	}
	rest := dsn[slash+1:]
	params := ""
	if q := strings.Index(rest, "?"); q >= 0 {
		params = rest[q:]
	}
	return dsn[:slash+1] + name + params
}
