package portalcmd

// D-061：CheckRoutes 抓得到装错的模块——别的端的路由、多出来的 Raw 路由。

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
)

type noUsers struct{}

func (noUsers) FindByUsername(context.Context, string) (*portal.Account, error) {
	return nil, portal.ErrAccountNotFound
}
func (noUsers) FindByID(context.Context, uint64) (*portal.Account, error) {
	return nil, portal.ErrAccountNotFound
}
func (noUsers) UpdatePasswordHash(context.Context, uint64, string, bool) error { return nil }
func (noUsers) TouchLogin(context.Context, uint64, string, time.Time) error    { return nil }

// portalModule 注册一个端和一条登录即可的路由。
type portalModule struct {
	code string
	raw  bool // 另外挂一条 Raw 路由
}

func (m portalModule) Name() string { return "m-" + m.code }
func (m portalModule) Init(d *app.Deps) error {
	return d.Portals.Register(portal.Portal{Code: m.code, Users: noUsers{}})
}
func (m portalModule) Perms() []rbac.Perm     { return nil }
func (m portalModule) Menus() []rbac.MenuNode { return nil }
func (m portalModule) Routes(r *app.Router) {
	r.Portal(m.code).GET("/x", rbac.AuthOnly(), func(c *gin.Context) {})
	if m.raw {
		r.Raw("GET", "/debug/"+m.code, "测试", func(c *gin.Context) {})
	}
}
func (m portalModule) Start(context.Context) error { return nil }
func (m portalModule) Stop(context.Context) error  { return nil }

func build(t *testing.T, p Program, portals ...string) *app.App {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	cfg.Portals = map[string]conf.Portal{}
	for _, code := range portals {
		cfg.Portals[code] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour}
	}
	a, err := p.BuildWith(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	require.NoError(t, a.Setup())
	return a
}

func TestCheckRoutes(t *testing.T) {
	ok := Program{Name: "shop", Portal: "shop", Modules: func() []app.Module { return []app.Module{portalModule{code: "shop"}} }}
	require.NoError(t, ok.CheckRoutes(build(t, ok, "shop")))

	// 把别的端（平台）的模块也装进来了
	mixed := Program{Name: "shop", Portal: "shop", Modules: func() []app.Module {
		return []app.Module{portalModule{code: "shop"}, portalModule{code: "platform"}}
	}}
	err := mixed.CheckRoutes(build(t, mixed, "shop", "platform"))
	require.ErrorContains(t, err, "/api/platform/v1/")

	// 多了一条健康检查以外的 Raw 路由
	raw := Program{Name: "shop", Portal: "shop", Modules: func() []app.Module { return []app.Module{portalModule{code: "shop", raw: true}} }}
	require.ErrorContains(t, raw.CheckRoutes(build(t, raw, "shop")), "/debug/shop")
}

func TestMain_UnknownAndMigrateUp(t *testing.T) {
	p := Program{Name: "shop", Portal: "shop"}
	var out bytes.Buffer
	require.Equal(t, 2, p.Main([]string{"bogus"}, &out))
	require.Contains(t, out.String(), "未知命令")
	out.Reset()
	// 没有 migrate up：迁移只由平台程序执行
	require.Equal(t, 1, p.Main([]string{"migrate", "up"}, &out))
	require.Contains(t, out.String(), "server migrate up")
	out.Reset()
	require.Equal(t, 0, p.Main([]string{"help"}, &out))
	require.NotContains(t, out.String(), "admin", "没有 admin 命令")
}
