package orgportal_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOrgRole_192_OmittedStatusKeepsDisabled(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o := f.newOrg("status-org")
		own := f.owner(o)
		r := f.ok(own, "POST", "/org/roles", gin.H{"code": "disabled", "name": "Disabled", "status": 0})
		id := uint64(r.data()["id"].(float64))
		path := fmt.Sprintf("/org/roles/%d", id)
		r = f.ok(own, "PUT", path, gin.H{"name": "Renamed"})
		require.Equal(t, float64(0), r.data()["status"])
		r = f.ok(own, "PUT", path, gin.H{"name": "Renamed", "status": 1})
		require.Equal(t, float64(1), r.data()["status"])
	})
}
