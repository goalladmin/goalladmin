package orgportal_test

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/orgportal"
)

// 规范 §13.2 第 181 条（D-097）：主体的 IP 白名单、黑名单只有主账号能看。改名单的请求体里就是名单的内容，
// 有 oplog:list 的员工在操作日志里看得到这两种操作的记录，看不到请求体；主账号照常看得到。
func TestOrgPortal_181_IPListBodiesAreOwnerOnlyInOperationLogs(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		e := f.newEnv("甲")
		_, st := f.staff(e.own, e.o, "auditor", f.role(e.own, "auditor", f.perm(orgportal.PermOplogList)))

		f.ok(e.own, "PUT", "/org/ip-allow", gin.H{"items": []gin.H{{"cidr": "203.0.113.0/24", "remark": "finance-office"}}})
		f.ok(e.own, "POST", "/org/ip-deny", gin.H{"cidr": "198.51.100.0/24", "expiresIn": 0, "remark": "blocked-range"})

		bodies := func(tok, query string) map[string][]string {
			out := map[string][]string{}
			for _, row := range f.ok(tok, "GET", "/org/operation-logs?pageSize=200"+query, nil).list() {
				action, _ := row["action"].(string)
				body, _ := row["body"].(string)
				out[action] = append(out[action], body)
			}
			return out
		}

		// 主账号：看得到自己提交的名单
		own := bodies(e.own, "")
		require.Len(t, own[orgportal.OpIPAllow], 1)
		require.Contains(t, own[orgportal.OpIPAllow][0], "finance-office")
		require.Contains(t, own[orgportal.OpIPAllow][0], "203.0.113.0/24")
		require.Len(t, own[orgportal.OpIPDeny], 1)
		require.Contains(t, own[orgportal.OpIPDeny][0], "blocked-range")

		// 员工：记录还在，请求体只有 ***；不带筛选、按动作筛选都一样
		for _, query := range []string{"", "&action=" + orgportal.OpIPAllow, "&action=" + orgportal.OpIPDeny} {
			got := bodies(st, query)
			for _, action := range []string{orgportal.OpIPAllow, orgportal.OpIPDeny} {
				for _, body := range got[action] {
					require.Equal(t, "***", body, "%s %s", query, action)
				}
			}
			for action, list := range got {
				for _, body := range list {
					require.NotContains(t, body, "finance-office", action)
					require.NotContains(t, body, "blocked-range", action)
					require.NotContains(t, body, "203.0.113.0/24", action)
					require.NotContains(t, body, "198.51.100.0/24", action)
				}
			}
		}
		all := bodies(st, "")
		require.Len(t, all[orgportal.OpIPAllow], 1, "记录本身照常可见")
		require.Len(t, all[orgportal.OpIPDeny], 1)
		// 别的操作的请求体员工照常看得到（建子账号的登录名）
		require.NotEmpty(t, all[orgportal.OpAccountCreate])
		found := false
		for _, body := range all[orgportal.OpAccountCreate] {
			if body != "***" && body != "" {
				found = true
			}
		}
		require.True(t, found, "%v", all[orgportal.OpAccountCreate])
	})
}
