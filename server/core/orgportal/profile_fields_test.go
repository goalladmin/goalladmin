package orgportal_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 规范 §13.2 第 183 条（D-099）：个人中心只返回本人的资料，管理者写在这个账号上的备注、排序和状态不给本人看。
func TestOrgPortal_183_ProfileOmitsManagerFields(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		e := f.newEnv("甲")
		id, st := f.staff(e.own, e.o, "clerk")
		f.ok(e.own, "PUT", fmt.Sprintf("/org/accounts/%d", id), gin.H{"displayName": "Clerk", "sort": 7, "remark": "manager-only-note"})

		r := f.ok(st, "GET", "/org/profile", nil)
		data := r.data()
		require.Equal(t, "clerk", data["username"])
		require.Equal(t, "Clerk", data["displayName"])
		require.Equal(t, e.o.code, data["orgCode"])
		for _, key := range []string{"id", "orgId", "email", "phone", "avatar", "owner", "mustChangePwd", "lastLoginAt", "lastLoginIp", "createdAt", "roles", "orgName", "sessions"} {
			require.Contains(t, data, key)
		}
		for _, key := range []string{"remark", "sort", "status"} {
			require.NotContains(t, data, key)
		}
		require.NotContains(t, r.rec.Body.String(), "manager-only-note")

		// 有查看账号权限的人在账号列表里照常看得到
		row := f.ok(e.own, "GET", fmt.Sprintf("/org/accounts/%d", id), nil).data()
		require.Equal(t, "manager-only-note", row["remark"])
		require.EqualValues(t, 7, row["sort"])
	})
}

// 181（D-097）、183（D-099）在主体端的接线：下线接口只认规范写法的会话号；日志查询的时间参数超出范围不出 500。
func TestOrgPortal_181_183_SessionIDAndTimeFilters(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		e := f.newEnv("甲")
		upper := strings.ToUpper(e.staffSID)
		require.NotEqual(t, e.staffSID, upper)
		require.Equal(t, 404, f.do(e.own, "POST", "/org/sessions/"+upper+"/revoke", nil).rec.Code)
		require.Equal(t, e.staffSID, f.activeSID(e.staffID), "大写写法不能让会话下线")
		f.ok(e.own, "POST", "/org/sessions/"+e.staffSID+"/revoke", nil)

		early := url.QueryEscape("0000-01-01T00:00:00+23:59")
		late := url.QueryEscape("9999-12-31T23:59:59-23:59")
		for _, path := range []string{"/org/login-logs", "/org/operation-logs"} {
			r := f.do(e.own, "GET", path+"?from="+early+"&to="+late, nil)
			require.Equal(t, 200, r.rec.Code, "%s: %s", path, r.rec.Body.String())
			require.Equal(t, 0, r.env.Code, "%s: %s", path, r.rec.Body.String())
		}
	})
}
