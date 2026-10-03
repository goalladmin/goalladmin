package system_test

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// hashProbe 包在 auth.Service 外面：数生成密码哈希的次数；full 时像并发上限占满那样回 429。
type hashProbe struct {
	auth.Service
	calls atomic.Int64
	full  atomic.Bool
}

func (p *hashProbe) HashPassword(portalCode, plain string) (string, error) {
	if p.full.Load() {
		return "", httpx.ErrTooManyRequests
	}
	p.calls.Add(1)
	return p.Service.HashPassword(portalCode, plain)
}

// 规范 §13.2 第 150 条（D-068）：平台建用户、重置用户密码要算密码哈希，和登录核对密码占同一个并发上限。
// 便宜的检查先做——字段不合法、目标不存在的请求不去算；位置满了回 429、什么都不写（用户没建、密码没换、会话没吊销）。
func TestUser_150_ChecksBeforeHashing(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	uid, userTok := f.createUser(admin, "alice", nil)
	deps := f.app.Deps()
	probe := &hashProbe{Service: deps.Auth}
	deps.Auth = probe // 模块每次都从 Deps 取

	users := func() (n int64) {
		require.NoError(t, f.gdb.Table("ga_user").Count(&n).Error)
		return n
	}
	hashOf := func(id uint64) (h string) {
		require.NoError(t, f.gdb.Raw("SELECT password_hash FROM ga_user WHERE id = ?", id).Scan(&h).Error)
		return h
	}
	reset := fmt.Sprintf("/system/users/%d/reset-password", uid)

	n := users()
	for name, c := range map[string]struct {
		path   string
		body   any
		status int
		code   int
	}{
		"登录名不合法":   {"/system/users", gin.H{"username": "1bad", "password": "user-pass-123"}, 200, httpx.CodeValidation},
		"密码不合策略":   {"/system/users", gin.H{"username": "newbie", "password": "short"}, 200, httpx.CodeValidation},
		"显示名带控制字符": {"/system/users", gin.H{"username": "newbie", "password": "user-pass-123", "displayName": "a\x07b"}, 200, httpx.CodeValidation},
		"重置不存在的用户": {"/system/users/987654/reset-password", nil, 404, httpx.CodeNotFound},
	} {
		r := f.do(admin, "POST", c.path, c.body)
		require.Equal(t, c.status, r.rec.Code, "%s: %s", name, r.rec.Body.String())
		require.Equal(t, c.code, r.env.Code, "%s: %s", name, r.rec.Body.String())
		require.Zero(t, probe.calls.Load(), "%s：不该去算哈希", name)
	}
	require.Equal(t, n, users())

	probe.full.Store(true)
	r := f.do(admin, "POST", "/system/users", gin.H{"username": "newbie", "password": "user-pass-123"})
	require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
	require.Equal(t, httpx.CodeTooManyRequests, r.env.Code)
	require.Equal(t, n, users(), "位置满了用户不建")
	old := hashOf(uid)
	r = f.do(admin, "POST", reset, nil)
	require.Equal(t, 429, r.rec.Code, r.rec.Body.String())
	require.Equal(t, old, hashOf(uid), "位置满了密码不换")
	require.Equal(t, 0, f.do(userTok, "GET", "/auth/me", nil).env.Code, "会话也没有被吊销")
	probe.full.Store(false)

	r = f.do(admin, "POST", "/system/users", gin.H{"username": "newbie", "password": "user-pass-123"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 1, probe.calls.Load())
	r = f.do(admin, "POST", reset, nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.EqualValues(t, 2, probe.calls.Load())
	require.NotEqual(t, old, hashOf(uid))
	require.Equal(t, 401, f.do(userTok, "GET", "/auth/me", nil).rec.Code, "重置之后原来的会话失效")
}
