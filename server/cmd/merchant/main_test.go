package main

// D-061：这个程序只有 merchant 端的路由；表结构落后时拒绝启动，也不执行迁移。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

func testConfig() *conf.Config {
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Portals = map[string]conf.Portal{"merchant": {AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour}}
	return cfg
}

func TestProgram_OnlyOwnPortalRoutes(t *testing.T) {
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	a, err := program().BuildWith(testConfig(), app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	require.NoError(t, a.Start(context.Background()))
	defer func() { _ = a.Stop(context.Background()) }()
	require.NoError(t, program().CheckRoutes(a))

	// 平台端和另一个端的接口在这个程序里根本不存在
	for _, path := range []string{"/api/platform/v1/auth/me", "/api/platform/v1/system/users", "/api/agent/v1/auth/me", "/api/platform/v1/auth/login"} {
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestProgram_RefusesToStartOnOldSchemaAndNeverMigrates(t *testing.T) {
	gdb := db.OpenTestDB(t) // 空库：平台程序还没迁移
	a, err := program().BuildWith(testConfig(), app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	require.ErrorIs(t, a.Start(context.Background()), db.ErrMigrationsPending)
	require.ErrorIs(t, a.Migrate(context.Background()), app.ErrMigrationsDisabled)
	var n int64
	require.NoError(t, gdb.Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&n).Error)
	require.Zero(t, n, "这个程序不能建任何表")
}

// docs/api.md 与这个程序真实路由表的一致性（同平台程序的 TestAPIDocMatchesRoutes）：每条路由的路径都写进了文档，
// 公开路由只有文档"公开接口"一节里的那几条。
func TestAPIDocCoversRoutes(t *testing.T) {
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	a, err := program().BuildWith(testConfig(), app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	require.NoError(t, a.Setup())
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "api.md"))
	require.NoError(t, err)
	text := string(doc)
	public := text[strings.Index(text, "## 公开接口"):]
	public = public[:strings.Index(public, "\n## ")]
	n := 0
	for _, r := range a.Routes() {
		rel := strings.TrimPrefix(r.Path, app.PortalPrefix(r.Portal))
		require.True(t, strings.Contains(text, "`"+rel+"`"), "%s %s 没有写进 docs/api.md", r.Method, r.Path)
		if r.Guard == rbac.GuardPublic {
			require.Contains(t, public, "`"+rel+"`", "%s %s 是公开路由，要写进\"公开接口\"一节", r.Method, r.Path)
		}
		n++
	}
	require.Greater(t, n, 30)
}
