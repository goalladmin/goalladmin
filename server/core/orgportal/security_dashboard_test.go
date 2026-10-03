package orgportal_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/orgportal"
	"github.com/stretchr/testify/require"
)

func TestOrgPortal_176_SecurityMenusAndDeny(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o, other := f.newOrg("甲"), f.newOrg("乙")
		own, otherTok := f.owner(o), f.owner(other)
		_, staff := f.staff(own, o, "manager", f.role(own, "all", f.allPerms()...))
		ownerMenus := f.ok(own, "GET", "/auth/me", nil)
		raw, _ := json.Marshal(ownerMenus.data()["menus"])
		require.Contains(t, string(raw), "org-security")
		require.Contains(t, string(raw), "org-ip-allow")
		require.Contains(t, string(raw), "org-ip-deny")
		nodes := ownerMenus.data()["menus"].([]any)
		require.Equal(t, "org-dashboard", nodes[0].(map[string]any)["name"])
		staffMenus := f.ok(staff, "GET", "/auth/me", nil)
		raw, _ = json.Marshal(staffMenus.data()["menus"])
		require.NotContains(t, string(raw), "org-security")
		for _, path := range []string{"/org/ip-allow", "/org/ip-deny"} {
			require.Equal(t, 403, f.do(staff, "GET", path, nil).rec.Code)
		}
		require.Equal(t, 403, f.do(staff, "POST", "/org/ip-deny", gin.H{"cidr": "198.51.100.8"}).rec.Code)
		require.Equal(t, 403, f.do(staff, "DELETE", "/org/ip-deny/1", nil).rec.Code)
		require.NotZero(t, f.do(own, "POST", "/org/ip-deny", gin.H{"cidr": clientIP}).env.Code)
		require.NotZero(t, f.do(own, "POST", "/org/ip-deny", gin.H{"cidr": "198.51.100.8", "orgId": other.id}).env.Code)
		// 在规则创建前登录的会话，刷新和普通请求也要经过主体黑名单。
		login := f.ok("", "POST", "/auth/login", gin.H{"org": o.code, "username": "admin", "password": newPass})
		token := login.data()["accessToken"].(string)
		r := f.ok(own, "POST", "/org/ip-deny", gin.H{"cidr": "198.51.100.8", "expiresIn": 60, "remark": "test"})
		id := uint64(r.data()["id"].(float64))
		require.Len(t, f.ok(own, "GET", "/org/ip-deny", nil).list(), 1)
		require.Empty(t, f.ok(otherTok, "GET", "/org/ip-deny", nil).list())
		require.Equal(t, 404, f.do(otherTok, "DELETE", fmt.Sprintf("/org/ip-deny/%d", id), nil).rec.Code)
		require.Equal(t, httpx.CodeIPDenied, f.raw(token, "GET", "/org/overview", "", nil, "198.51.100.8").env.Code)
		require.Zero(t, f.raw(otherTok, "GET", "/org/overview", "", nil, "198.51.100.8").env.Code)
		body, _ := json.Marshal(gin.H{"org": o.code, "username": "admin", "password": newPass})
		require.NotZero(t, f.raw("", "POST", "/auth/login", "application/json", body, "198.51.100.8").env.Code)
		req := httptest.NewRequest("POST", f.base+"/auth/refresh", nil)
		req.RemoteAddr = "198.51.100.8:5000"
		req.Header.Set("X-GA-Client", "web")
		require.NotEmpty(t, login.rec.Result().Cookies())
		for _, cookie := range login.rec.Result().Cookies() {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		f.app.Handler().ServeHTTP(rec, req)
		var envelope httpx.Envelope
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
		require.Equal(t, httpx.CodeIPDenied, envelope.Code)
		f.ok(own, "DELETE", fmt.Sprintf("/org/ip-deny/%d", id), nil)
		require.Zero(t, f.raw(token, "GET", "/org/overview", "", nil, "198.51.100.8").env.Code)
	})
}

func TestOrgPortal_176_DashboardIsolationAndRank(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o, other := f.newOrg("甲"), f.newOrg("乙")
		own := f.owner(o)
		role := f.role(own, "dashboard", f.perm(orgportal.PermDashboardView))
		staffID, staff := f.staff(own, o, "reader", role)
		_, none := f.staff(own, o, "none")
		require.Equal(t, 403, f.do(none, "GET", "/org/dashboard", nil).rec.Code)
		for _, table := range []string{"ga_login_log", "ga_operation_log"} {
			require.NoError(t, f.gdb.Exec("DELETE FROM "+table).Error)
		}
		today := time.Now().UTC().Truncate(24 * time.Hour)
		at := today.Add(-time.Hour) // UTC 昨日 23:00，UTC+8 今日 07:00
		for _, row := range []struct {
			portal   string
			oid, uid uint64
			name     string
			n        int
		}{{f.kind.Portal(), o.id, o.ownerID, "admin", 2}, {f.kind.Portal(), o.id, staffID, "reader", 1}, {f.kind.Portal(), other.id, other.ownerID, "outside", 5}, {"platform", o.id, o.ownerID, "platform", 7}} {
			for i := 0; i < row.n; i++ {
				require.NoError(t, f.gdb.Table("ga_operation_log").Create(map[string]any{"portal": row.portal, "org_id": row.oid, "user_id": row.uid, "username": row.name, "action": "fixture." + row.name, "created_at": at}).Error)
				require.NoError(t, f.gdb.Table("ga_login_log").Create(map[string]any{"portal": row.portal, "org_id": row.oid, "user_id": row.uid, "username": row.name, "success": i%2 == 0, "reason": "fixture", "created_at": at}).Error)
			}
		}
		for _, tok := range []string{own, staff} {
			r := f.ok(tok, "GET", "/org/dashboard?days=7&tz=480", nil)
			d := r.data()
			require.EqualValues(t, 3, d["users"].(map[string]any)["total"])
			require.Len(t, d["days"], 7)
			ops := d["operations"].([]any)
			idx := 0
			for i, day := range d["days"].([]any) {
				if day == at.Add(8*time.Hour).Format("2006-01-02") {
					idx = i
				}
			}
			require.EqualValues(t, 3, ops[idx])
			logins := d["logins"].(map[string]any)
			require.EqualValues(t, 2, logins["success"].([]any)[idx])
			require.EqualValues(t, 1, logins["failed"].([]any)[idx])
			hours := d["hours"].(map[string]any)
			require.EqualValues(t, 3, hours["operations"].([]any)[7])
			raw, _ := json.Marshal(d)
			require.NotContains(t, string(raw), "outside")
			require.NotContains(t, string(raw), "fixture.platform")
			ranks := d["topUsers"].([]any)
			if tok == own {
				require.Len(t, ranks, 2)
			} else {
				require.Len(t, ranks, 1)
				require.EqualValues(t, staffID, ranks[0].(map[string]any)["userId"])
			}
		}
		// 账号查看权限单独撤回时，仍可读数据中心，但热缓存不能保留其他账号的排名。
		listRole := f.role(own, "accounts", f.perm(orgportal.PermAccountList))
		f.ok(own, "PUT", fmt.Sprintf("/org/accounts/%d/roles", staffID), gin.H{"roleIds": []uint64{role, listRole}})
		require.Len(t, f.ok(staff, "GET", "/org/dashboard?days=7&tz=480", nil).data()["topUsers"], 2)
		require.NoError(t, f.gdb.Table("ga_user_role").Where("portal = ? AND user_id = ? AND role_id = ?", f.kind.Portal(), staffID, listRole).Delete(map[string]any{}).Error)
		ranks := f.ok(staff, "GET", "/org/dashboard?days=7&tz=480", nil).data()["topUsers"].([]any)
		require.Len(t, ranks, 1)
		require.EqualValues(t, staffID, ranks[0].(map[string]any)["userId"])
		// 热缓存中有数据中心权限，数据库撤回后仍按本次快照拒绝。
		require.NoError(t, f.gdb.Table("ga_user_role").Where("portal = ? AND user_id = ?", f.kind.Portal(), staffID).Delete(map[string]any{}).Error)
		require.Equal(t, 403, f.do(staff, "GET", "/org/dashboard", nil).rec.Code)
		require.NotZero(t, f.do(own, "GET", "/org/dashboard?orgId="+fmt.Sprint(other.id), nil).env.Code)
		r := f.ok(own, "GET", "/org/dashboard?days=999&tz=-9999", nil)
		require.Len(t, r.data()["days"], 90)
		// 单日区间长度固定。
		otherTok := f.owner(other)
		r = f.ok(otherTok, "GET", "/org/dashboard?days=1", nil)
		require.Len(t, r.data()["days"], 1)
	})
}
