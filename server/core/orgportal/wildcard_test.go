package orgportal_test

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/stretchr/testify/require"
)

func TestOrgPortal_177_Wildcard(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o := f.newOrg("通配")
		own := f.owner(o)
		r := f.ok(own, "POST", "/org/ip-deny", gin.H{"cidr": "124.55.12.*", "expiresIn": 60})
		require.Equal(t, "124.55.12.0/24", r.data()["cidr"])
		require.Equal(t, httpx.CodeIPDenied, f.raw(own, "GET", "/org/overview", "", nil, "124.55.12.255").env.Code)
		require.Zero(t, f.raw(own, "GET", "/org/overview", "", nil, "124.55.13.0").env.Code)
		before := f.snapshot(0)
		for _, cidr := range []string{"124.*.**.*", "124.*.12.*", "*.*.*.*", "203.0.113.*"} {
			require.NotZero(t, f.do(own, "POST", "/org/ip-deny", gin.H{"cidr": cidr}).env.Code)
		}
		require.Equal(t, before, f.snapshot(0))
		r = f.ok(own, "PUT", "/org/ip-allow", gin.H{"items": []gin.H{{"cidr": "203.0.113.*"}, {"cidr": "203.0.113.0/24"}}})
		items := r.data()["items"].([]any)
		require.Len(t, items, 1)
		require.Equal(t, "203.0.113.0/24", items[0].(map[string]any)["cidr"])
		require.Zero(t, f.do(own, "GET", "/org/overview", nil).env.Code)
		require.Equal(t, httpx.CodeIPDenied, f.raw(own, "GET", "/org/overview", "", nil, "203.0.114.0").env.Code)
	})
}
