package oplogimpl

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// 请求已经超时（D-037），之后才写的操作日志不能跟着丢。
func TestMiddleware_WritesAfterRequestDeadline(t *testing.T) {
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.TestContext(t, gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(db.WithDB(c.Request.Context(), gdb), 50*time.Millisecond)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	r.POST("/x", Middleware("platform", "test.slow"), func(c *gin.Context) {
		<-c.Request.Context().Done()
		httpx.Fail(c, c.Request.Context().Err())
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/x", nil))
	require.Equal(t, 503, w.Code)

	var e Entry
	require.NoError(t, gdb.Where("action = ?", "test.slow").First(&e).Error)
	require.Equal(t, 503, e.HTTPStatus)
	require.Equal(t, httpx.CodeUnavailable, e.Code)
}

// 规范 §13.2 第 113 条（D-059）：请求体的每个分支（JSON、表单、其他类型的占位符）入库前都不超过 MaxBody、
// 都是合法 UTF-8；占位符里来自 Content-Type 的类型名有长度上限。请求头再长，这条操作日志照样写进去。
func TestMiddleware_113_BodyIsBoundedOnEveryBranch(t *testing.T) {
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.TestContext(t, gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(db.WithDB(c.Request.Context(), gdb))
		c.Next()
	})
	r.POST("/x", Middleware("platform", "test.body"), func(c *gin.Context) { httpx.OK(c, nil) })

	cases := []struct{ ct, body string }{
		{"application/json", `{"a":"` + strings.Repeat("中", 5000) + `"}`},
		{"application/x-www-form-urlencoded", "a=" + strings.Repeat("b", 10_000)},
		{"application/x-" + strings.Repeat("a", 60_000), "x"},
		{"application/x-\xff\xfe", "x"},
	}
	for i, c := range cases {
		req := httptest.NewRequest("POST", "/x", strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.ct)
		r.ServeHTTP(httptest.NewRecorder(), req)
		var n int64
		require.NoError(t, gdb.Model(&Entry{}).Where("action = ?", "test.body").Count(&n).Error)
		require.EqualValues(t, i+1, n, "第 %d 条写进去了", i)
	}
	var bodies []string
	require.NoError(t, gdb.Model(&Entry{}).Where("action = ?", "test.body").Pluck("body", &bodies).Error)
	for i, b := range bodies {
		require.LessOrEqual(t, len(b), MaxBody, i)
		require.True(t, utf8.ValidString(b), i)
	}
	require.LessOrEqual(t, len(placeholder("application/x-"+strings.Repeat("a", 60_000), 1)), maxKind+32)
}
