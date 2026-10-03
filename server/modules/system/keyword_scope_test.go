package system_test

import (
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/modules/system"
)

// 真实装配的用户列表：关键字的每个分支都同时受权限范围、状态和部门筛选约束。
func TestUserListKeywordKeepsDataScopeAndStatus(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	_, tok := w.manager("scopekeyword-manager", w.sales, "dept", system.PermUserList)
	disabled, _ := f.userIn(w.root, "disabled-keyword", w.sales, nil)
	require.Equal(t, 0, f.do(w.root, "POST", fmt.Sprintf("/system/users/%d/status", disabled), gin.H{"status": 0}).env.Code)
	require.NoError(t, f.gdb.Model(&system.User{}).Where("id IN ?", []uint64{w.alice, w.eve, disabled}).Update("display_name", "scopekeyword").Error)
	require.NoError(t, f.gdb.Model(&system.User{}).Where("id IN ?", []uint64{w.olga, w.nod}).Updates(map[string]any{
		"display_name": "scopekeyword", "email": "scopekeyword-foreignonly@example.test",
	}).Error)
	for _, tc := range []struct {
		name  string
		query string
		want  []string
	}{
		{"enabled username and display", "keyword=scopekeyword&status=1", []string{"alice", "scopekeyword-manager"}},
		{"disabled display", "keyword=scopekeyword&status=0", []string{"disabled-keyword"}},
		{"foreign email", "keyword=foreignonly&status=1", nil},
		{"foreign department", fmt.Sprintf("keyword=scopekeyword&status=1&deptId=%d", w.ops), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.do(tok, "GET", "/system/users?pageSize=100&"+tc.query, nil)
			require.Equal(t, 0, r.env.Code, r.rec.Body.String())
			require.ElementsMatch(t, tc.want, names(r, "username"))
			require.EqualValues(t, len(tc.want), r.data()["total"])
		})
	}
}
