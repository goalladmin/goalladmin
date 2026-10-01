package app

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// 这里直接操作内部字段注册一个端，验证路由器本身的行为，不经过认证模块。
func newAppWithPortal(t *testing.T) (*App, *[]string) {
	t.Helper()
	cfg := conf.Default()
	cfg.Log.Level = "error"
	a, err := New(cfg, WithDB(nil), WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.portalGroups["demo"] = a.engine.Group(PortalPrefix("demo"))
	var seen []string
	a.SetGuardResolver(func(portal string, g Guard) ([]gin.HandlerFunc, error) {
		return []gin.HandlerFunc{func(c *gin.Context) {
			seen = append(seen, portal+"/"+string(g.Kind())+"/"+g.Perm())
			c.Next()
		}}, nil
	})
	return a, &seen
}

func TestPortalRouter_RegistersWithGuardAndPrefix(t *testing.T) {
	a, seen := newAppWithPortal(t)
	p := a.router.Portal("demo")
	p.GET("/things", rbac.Require("demo:thing:list"), func(c *gin.Context) { httpx.OK(c, "ok") })
	p.Group("/sub").POST("/items", rbac.Public(), func(c *gin.Context) { httpx.OK(c, "ok") },
		WithOpName("create-item"))

	w := httptest.NewRecorder()
	a.engine.ServeHTTP(w, httptest.NewRequest("GET", "/api/demo/v1/things", nil))
	require.Equal(t, 200, w.Code)
	require.Equal(t, []string{"demo/perm/demo:thing:list"}, *seen)

	routes := a.Routes()
	require.Len(t, routes, 2)
	require.Equal(t, "/api/demo/v1/sub/items", routes[0].Path)
	require.Equal(t, rbac.GuardPublic, routes[0].Guard)
	require.Equal(t, "create-item", routes[0].OpName)
	require.Equal(t, "/api/demo/v1/things", routes[1].Path)
	require.Equal(t, "demo:thing:list", routes[1].Perm)
}

func TestPortalRouter_PanicsOnMissingGuardOrPerm(t *testing.T) {
	a, _ := newAppWithPortal(t)
	p := a.router.Portal("demo")
	h := func(c *gin.Context) {}
	require.Panics(t, func() { p.GET("/a", nil, h) })
	require.Panics(t, func() { p.GET("/b", rbac.Require(""), h) })
	require.Panics(t, func() { p.GET("/c", rbac.Public(), nil) })
}

// fakeGuard 自己实现 Guard 接口、冒充公开守卫：这样就能开出不在公开清单里的接口，注册时必须拒绝。
type fakeGuard struct{}

func (fakeGuard) Kind() rbac.GuardKind { return rbac.GuardPublic }
func (fakeGuard) Perm() string         { return "" }

func TestPortalRouter_RejectsGuardsNotFromRBAC(t *testing.T) {
	a, _ := newAppWithPortal(t)
	p := a.router.Portal("demo")
	h := func(c *gin.Context) { httpx.OK(c, "ok") }
	require.PanicsWithValue(t, "app: 路由 GET /api/demo/v1/x 的守卫不是 core/rbac 提供的（app.fakeGuard）", func() { p.GET("/x", fakeGuard{}, h) })
	require.Empty(t, a.Routes())
}

func TestPortalRouter_PanicsWithoutResolver(t *testing.T) {
	a, _ := newAppWithPortal(t)
	a.guardResolver = nil
	require.Panics(t, func() {
		a.router.Portal("demo").GET("/a", rbac.Public(), func(c *gin.Context) {})
	})
}
