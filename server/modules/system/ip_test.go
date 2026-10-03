package system_test

// D-062（规范 §13.2 第 122–125 条）：平台端的 IP 黑名单、平台白名单、账号白名单接口。

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// doFrom 和 do 一样，但请求来自 ip。
func (f *fixture) doFrom(ip, token, method, path string, body any) resp {
	f.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, base+path, rd)
	req.RemoteAddr = ip + ":5000"
	req.Header.Set("X-GA-Client", "web")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return resp{rec: rec, env: env}
}

// 122. 黑名单：加了立即生效（三个程序都读同一份）；不能盖住操作者自己的地址；删掉立即恢复；增删记操作日志。
func TestIP_122_Blacklist(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")

	// 不能把自己封了（请求来自 203.0.113.10）
	r := f.do(root, "POST", "/system/ip-rules/deny", gin.H{"cidr": "203.0.113.0/24"})
	require.Equal(t, "ipacl.selfLockout", fieldKey(r))
	// 太宽、有效期不对、夹带字段
	require.Equal(t, "ipacl.denyTooBroad", fieldKey(f.do(root, "POST", "/system/ip-rules/deny", gin.H{"cidr": "10.0.0.0/7"})))
	require.Equal(t, "ipacl.expiresIn", fieldKey(f.do(root, "POST", "/system/ip-rules/deny", gin.H{"cidr": "198.51.100.9", "expiresIn": -1})))
	require.Equal(t, httpx.CodeBadRequest, f.do(root, "POST", "/system/ip-rules/deny", gin.H{"cidr": "198.51.100.9", "scope": "global"}).env.Code)

	r = f.do(root, "POST", "/system/ip-rules/deny", gin.H{"cidr": "198.51.100.0/24", "expiresIn": 60, "remark": "撞库"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	id := uint64(r.data()["id"].(float64))
	require.NotNil(t, r.data()["expiresAt"])

	blocked := f.doFrom("198.51.100.20", "", "POST", "/auth/login", gin.H{"username": "root", "password": "changed-pass-9"})
	require.Equal(t, 403, blocked.rec.Code)
	require.Equal(t, httpx.CodeIPDenied, blocked.env.Code)
	require.Equal(t, 403, f.doFrom("198.51.100.20", root, "GET", "/auth/me", nil).rec.Code, "已登录的令牌换到被封的地址也不行")

	list := f.do(root, "GET", "/system/ip-rules/deny", nil)
	require.Equal(t, 0, list.env.Code, list.rec.Body.String())
	require.EqualValues(t, 1, list.data()["total"])

	require.Equal(t, 0, f.do(root, "DELETE", fmt.Sprintf("/system/ip-rules/deny/%d", id), nil).env.Code)
	require.Equal(t, 0, f.doFrom("198.51.100.20", root, "GET", "/auth/me", nil).env.Code, "删掉立即恢复")
	require.Equal(t, 404, f.do(root, "DELETE", fmt.Sprintf("/system/ip-rules/deny/%d", id), nil).rec.Code)

	require.Len(t, f.oplogs(system.OpIPDeny), 5, "被拒的尝试也记操作日志")
	require.Len(t, f.oplogs(system.OpIPUnblock), 2)
}

// 123. 平台端白名单：名单不为空时必须包含操作者当前的地址；设好之后平台的每个接口只放行名单里的来源；清空恢复。
func TestIP_123_PlatformWhitelist(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")

	r := f.do(root, "PUT", "/system/ip-rules/allow", gin.H{"items": []gin.H{{"cidr": "10.0.0.0/8"}}})
	require.Equal(t, "ipacl.selfLockout", fieldKey(r))
	require.Equal(t, 0, f.doFrom("198.51.100.20", root, "GET", "/auth/me", nil).env.Code, "被拒的写入不生效")

	r = f.do(root, "PUT", "/system/ip-rules/allow", gin.H{"items": []gin.H{{"cidr": "203.0.113.0/24", "remark": "办公网"}, {"cidr": "10.1.2.3"}}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "203.0.113.10", r.data()["yourIp"])
	got := f.do(root, "GET", "/system/ip-rules/allow", nil)
	require.Len(t, got.data()["items"], 2)

	out := f.doFrom("198.51.100.20", "", "GET", "/auth/captcha", nil)
	require.Equal(t, 403, out.rec.Code, "名单外连验证码都拿不到")
	require.Equal(t, httpx.CodeIPDenied, out.env.Code)
	require.Equal(t, 403, f.doFrom("198.51.100.20", root, "GET", "/auth/me", nil).rec.Code)
	require.Equal(t, 0, f.doFrom("10.1.2.3", root, "GET", "/auth/me", nil).env.Code)

	require.Equal(t, 0, f.do(root, "PUT", "/system/ip-rules/allow", gin.H{"items": []gin.H{}}).env.Code)
	require.Equal(t, 0, f.doFrom("198.51.100.20", root, "GET", "/auth/me", nil).env.Code, "清空就不再限制")
}

// 124. 账号白名单：按"修改用户"的范围约束（范围外 404），非超管改不了超管的；改自己的必须包含当前地址；
// 名单外登录和密码错一样，已登录后换到名单外回 2003。
func TestIP_124_AccountWhitelist(t *testing.T) {
	f := newFixture(t)
	root, rootID := f.admin("root")
	ipAdmin := f.createRole(root, "ip-admin", []string{system.PermUserList, system.PermUserUpdate, system.PermUserIP})
	mgrID, mgr := f.createUser(root, "mgr", []uint64{ipAdmin})
	bobID, bob := f.createUser(root, "bob", nil)

	// 非超管不能改超管账号的白名单
	r := f.do(mgr, "PUT", fmt.Sprintf("/system/users/%d/ip-allow", rootID), gin.H{"items": []gin.H{{"cidr": "10.0.0.1"}}})
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "system.user.superProtected", fieldKey(r))
	// 改自己的必须包含当前地址
	r = f.do(mgr, "PUT", fmt.Sprintf("/system/users/%d/ip-allow", mgrID), gin.H{"items": []gin.H{{"cidr": "10.0.0.1"}}})
	require.Equal(t, "ipacl.selfLockout", fieldKey(r))
	// 给 bob 设：之后 bob 在名单外登录和密码错一样，已登录的令牌在名单外回 2003
	r = f.do(mgr, "PUT", fmt.Sprintf("/system/users/%d/ip-allow", bobID), gin.H{"items": []gin.H{{"cidr": "203.0.113.0/24"}}})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Len(t, f.do(mgr, "GET", fmt.Sprintf("/system/users/%d/ip-allow", bobID), nil).data()["items"], 1)

	denied := f.doFrom("198.51.100.20", "", "POST", "/auth/login", gin.H{"username": "bob", "password": "user-pass-456"})
	wrong := f.doFrom("198.51.100.20", "", "POST", "/auth/login", gin.H{"username": "bob", "password": "wrong-pass-456"})
	require.Equal(t, httpx.CodeLoginFailed, denied.env.Code)
	require.Equal(t, wrong.env.Msg, denied.env.Msg)
	require.Equal(t, wrong.data(), denied.data())
	r = f.doFrom("198.51.100.20", bob, "GET", "/auth/me", nil)
	require.Equal(t, 403, r.rec.Code)
	require.Equal(t, httpx.CodeIPDenied, r.env.Code)
	require.Equal(t, 0, f.do(bob, "GET", "/auth/me", nil).env.Code)

	// 范围外的目标当作不存在：只能管本人的角色看不到 bob
	selfOnly := f.scopedRole(root, "ip-self", []string{system.PermUserList, system.PermUserUpdate, system.PermUserIP}, "self")
	_, narrow := f.createUser(root, "narrow", []uint64{selfOnly})
	require.Equal(t, 404, f.do(narrow, "PUT", fmt.Sprintf("/system/users/%d/ip-allow", bobID), gin.H{"items": []gin.H{}}).rec.Code)
	require.Equal(t, 404, f.do(narrow, "GET", fmt.Sprintf("/system/users/%d/ip-allow", bobID), nil).rec.Code)
	// 没有这个权限码的人连看都不行
	plain := f.createRole(root, "plain-user-admin", []string{system.PermUserList, system.PermUserUpdate})
	_, noIP := f.createUser(root, "noip", []uint64{plain})
	require.Equal(t, 403, f.do(noIP, "GET", fmt.Sprintf("/system/users/%d/ip-allow", bobID), nil).rec.Code)

	// 超管照常管
	require.Equal(t, 0, f.do(root, "PUT", fmt.Sprintf("/system/users/%d/ip-allow", bobID), gin.H{"items": []gin.H{}}).env.Code)
	require.Equal(t, 0, f.doFrom("198.51.100.20", bob, "GET", "/auth/me", nil).env.Code)
	require.NotEmpty(t, f.oplogs(system.OpUserIP))
}

// 125. 三个 IP 权限码都是敏感权限：有授权权限的非超管也授不出去；只有查看权限的改不了。
func TestIP_125_PermissionsAreSensitive(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	granter := f.createRole(root, "granter", []string{system.PermRoleList, system.PermRoleGrant, system.PermIPList, system.PermIPDeny, system.PermIPAllow, system.PermUserIP})
	_, g := f.createUser(root, "granter", []uint64{granter})
	target := f.createRole(root, "target", nil)
	for _, perm := range []string{system.PermIPDeny, system.PermIPAllow, system.PermUserIP} {
		r := f.do(g, "PUT", fmt.Sprintf("/system/roles/%d/perms", target), gin.H{"codes": []string{perm}})
		require.NotEqual(t, 0, r.env.Code, "%s 是敏感权限，非超管授不出去", perm)
	}
	// 查看不是敏感权限
	require.Equal(t, 0, f.do(g, "PUT", fmt.Sprintf("/system/roles/%d/perms", target), gin.H{"codes": []string{system.PermIPList}}).env.Code)

	viewer := f.createRole(root, "ip-viewer", []string{system.PermIPList})
	_, v := f.createUser(root, "viewer", []uint64{viewer})
	require.Equal(t, 0, f.do(v, "GET", "/system/ip-rules/deny", nil).env.Code)
	require.Equal(t, 0, f.do(v, "GET", "/system/ip-rules/allow", nil).env.Code)
	require.Equal(t, 403, f.do(v, "POST", "/system/ip-rules/deny", gin.H{"cidr": "198.51.100.1"}).rec.Code)
	require.Equal(t, 403, f.do(v, "PUT", "/system/ip-rules/allow", gin.H{"items": []gin.H{}}).rec.Code)
}
