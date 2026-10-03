package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

func TestInput_190_RawArrayBounds(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	id, _ := f.createUser(root, "array-user", nil)
	for _, body := range []gin.H{
		{"username": "over-posts", "postIds": make([]uint64, 21)},
		{"username": "over-roles", "roleIds": make([]uint64, 21)},
	} {
		r := f.do(root, "POST", "/system/users", body)
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	}
	r := f.do(root, "PUT", fmt.Sprintf("/system/users/%d", id), gin.H{"displayName": "Array user", "postIds": make([]uint64, 21)})
	require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
	var count int64
	require.NoError(t, f.gdb.Table("ga_user").Where("username IN ?", []string{"over-posts", "over-roles"}).Count(&count).Error)
	require.Zero(t, count)
}
