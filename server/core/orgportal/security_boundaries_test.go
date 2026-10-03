package orgportal_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOrgPortal_178_RequestBoundaries(t *testing.T) {
	forKinds(t, func(t *testing.T, f *fixture) {
		o := f.newOrg("边界")
		own := f.owner(o)
		login := f.ok("", "POST", "/auth/login", gin.H{"org": o.code, "username": "admin", "password": newPass})
		before := f.snapshot(0)
		// 浏览器自动携带的 Cookie 不替代业务接口要求的 Bearer 身份。
		for _, endpoint := range []struct{ method, path, body string }{
			{"GET", "/org/dashboard", ""},
			{"GET", "/org/ip-allow", ""},
			{"GET", "/org/ip-deny", ""},
			{"POST", "/org/ip-deny", `{"cidr":"198.51.100.9"}`},
			{"PUT", "/org/ip-allow", `{"items":[]}`},
			{"DELETE", "/org/ip-deny/1", ""},
		} {
			req := httptest.NewRequest(endpoint.method, f.base+endpoint.path, strings.NewReader(endpoint.body))
			req.RemoteAddr = clientIP + ":5000"
			req.Header.Set("Origin", "https://other.example")
			req.Header.Set("Content-Type", "application/json")
			for _, cookie := range login.rec.Result().Cookies() {
				req.AddCookie(cookie)
			}
			rec := httptest.NewRecorder()
			f.app.Handler().ServeHTTP(rec, req)
			require.Equal(t, 401, rec.Code, endpoint.path)
			require.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
		}
		require.Equal(t, before, f.snapshot(0))
		for _, body := range []string{
			`{"cidr":"198.51.100.9","expiresIn":-1}`,
			`{"cidr":"198.51.100.9","expiresIn":525601}`,
			`{"cidr":"198.51.100.9","expiresIn":"60"}`,
			`{"cidr":"198.51.100.9","scope":"global"}`,
			`{"cidr":"198.51.100.9","portal":"platform"}`,
			`{"cidr":"198.51.100.9"}]`,
		} {
			require.NotZero(t, f.raw(own, "POST", "/org/ip-deny", "application/json", []byte(body), clientIP).env.Code)
		}
		for _, id := range []string{"0", "-1", "not-an-id", "18446744073709551616"} {
			require.NotZero(t, f.do(own, "DELETE", "/org/ip-deny/"+id, nil).env.Code)
		}
		require.Equal(t, before, f.snapshot(0))
		// 惰性标记文本用于验证数据往返与渲染契约，不包含可执行内容。
		const remark = `<strong data-canary="remark">literal</strong> 100%_`
		f.ok(own, "POST", "/org/ip-deny", gin.H{"cidr": "198.51.100.9", "remark": remark})
		rows := f.ok(own, "GET", "/org/ip-deny", nil).list()
		require.Len(t, rows, 1)
		require.Equal(t, remark, rows[0]["remark"])
		// 搜索中的 SQL 通配字符仅按普通文字查找。
		require.Empty(t, f.ok(own, "GET", "/org/ip-deny?keyword=%25", nil).list())
		dashboard := f.ok(own, "GET", "/org/dashboard?days=not-a-number&tz=not-a-number", nil)
		require.Len(t, dashboard.data()["days"], 30)
		encoded, err := json.Marshal(rows)
		require.NoError(t, err)
		require.Contains(t, string(encoded), `\u003cstrong`)
	})
}
