package orgportal_test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/org"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// 165：两个套件共用主体密码操作次数。走真实请求，便宜检查、本地预算退款和主体隔离都保持原行为。
func TestOrgPortal_165_SharedPasswordOperations(t *testing.T) {
	for _, kind := range []org.Kind{org.Agent(), org.Merchant()} {
		t.Run(kind.Portal(), func(t *testing.T) {
			addr, prefix := redisx.TestAddr(t), redisx.TestPrefix(t)
			gdb := db.OpenTestDB(t)
			_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
			require.NoError(t, err)
			// 冻结两实例的时钟，确保配额不会在测试途中自然过期。
			now := time.Now().UTC().Truncate(time.Second)
			newApp := func() *fixture {
				cfg := conf.Default()
				cfg.Log.Level = "error"
				cfg.Redis.Addr, cfg.Redis.KeyPrefix = addr, prefix
				cfg.Portals = map[string]conf.Portal{kind.Portal(): {
					AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: "org-portal-test-secret-0123456789abcdef0123",
					Login: conf.PortalLogin{IPRatePerMinute: 600, AccountRatePerMinute: 120},
				}}
				a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithClock(func() time.Time { return now }), app.WithPasswordHashParams(64, 1), app.WithoutMigrations())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
				a.Register(&kitModule{kind: kind})
				require.NoError(t, a.Setup())
				return &fixture{t: t, kind: kind, app: a, gdb: gdb, ctx: a.Context(context.Background()), base: app.PortalPrefix(kind.Portal()), user: "ga_" + kind.Portal() + "_user"}
			}
			a, b := newApp(), newApp()
			first, other := a.newOrg("甲"), a.newOrg("乙")
			ownerA, ownerOther := a.owner(first), a.owner(other)
			staff, _ := a.staff(ownerA, first, "staff") // 主体甲已占一次
			reset := fmt.Sprintf("/org/accounts/%d/reset-password", staff)
			pa := &hashProbe{Service: a.app.Deps().Auth}
			pb := &hashProbe{Service: b.app.Deps().Auth}
			a.app.Deps().Auth, b.app.Deps().Auth = pa, pb
			// 预算没准入时退回共享配额，另一个实例随后仍能用到全部剩余次数。
			pa.full.Store(true)
			for i := 0; i < 3; i++ {
				require.Equal(t, httpx.CodeTooManyRequests, a.do(ownerA, "POST", reset, nil).env.Code)
			}
			pa.full.Store(false)
			// 明确会被便宜检查拒绝的请求也不占共享次数。
			for i := 0; i < 3; i++ {
				require.Equal(t, httpx.CodeConflict, b.do(ownerA, "POST", "/org/accounts", gin.H{"username": "staff", "password": staffPass}).env.Code)
			}
			for i := 0; i < 29; i++ {
				f := a
				if i%2 == 0 {
					f = b
				}
				f.ok(ownerA, "POST", reset, nil)
			}
			require.EqualValues(t, 29, pa.calls.Load()+pb.calls.Load(), "两实例合计计算，预算拒绝和便宜检查未计入")
			for _, f := range []*fixture{a, b} {
				require.Equal(t, httpx.CodeTooManyRequests, f.do(ownerA, "POST", reset, nil).env.Code)
				require.Equal(t, httpx.CodeTooManyRequests, f.do(ownerA, "POST", "/org/accounts", gin.H{"username": "another", "password": staffPass}).env.Code)
			}
			require.EqualValues(t, 29, pa.calls.Load()+pb.calls.Load(), "共享配额耗尽后不计算密码")
			b.ok(ownerOther, "POST", "/org/accounts", gin.H{"username": "fresh", "password": staffPass})
			require.EqualValues(t, 30, pa.calls.Load()+pb.calls.Load(), "另一个主体的配额独立")
		})
	}
}
