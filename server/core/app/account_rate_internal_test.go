package app

import (
	"fmt"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 规范 §13.2 第 187 条（D-103）：账号的登录请求次数是所有来源合计的，超限不拒绝、改为要求验证码。
// 一个来源对着某个账号不停地发请求，不能让另一个来源用正确的密码登录失败。
func TestAuth_187_OneSourceCannotBlockAnotherSourcesLogin(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	login := func(password, ip string, extra gin.H) resp {
		body := gin.H{"username": "alice", "password": password}
		for k, v := range extra {
			body[k] = v
		}
		return f.do("POST", "/auth/login", body, fromIP(ip))
	}

	// 一个来源不带验证码对着 alice 发 18 次（来源自己的每分钟 20 次以内）：前 3 次是密码错误，之后它自己要验证码
	for i := 0; i < 18; i++ {
		r := login("wrong-9", "198.51.100.66", nil)
		require.NotEqual(t, 429, r.rec.Code, i)
		if i < 3 {
			require.Equal(t, httpx.CodeLoginFailed, r.env.Code, i)
		} else {
			require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code, i)
		}
	}
	// 另一个来源用正确的密码：直接登录成功——不是 429，也没有因为别人的请求多出一道验证码
	ok := login("correct-horse-9", "203.0.113.5", nil)
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())
	require.Zero(t, f.countSecurity("login_rate_limited"))

	// 换一个窗口。十个来源各真的核对了一次密码，把账号这一分钟的次数用完
	f.clock.Advance(61 * time.Second)
	for i := 0; i < 10; i++ {
		r := login("wrong-9", fmt.Sprintf("192.0.2.%d", i+1), nil)
		require.Equal(t, httpx.CodeLoginFailed, r.env.Code, i)
	}
	// 再来的来源不被拒绝，而是要带验证码
	r := login("correct-horse-9", "203.0.113.6", nil)
	require.NotEqual(t, 429, r.rec.Code)
	require.Equal(t, httpx.CodeCaptchaRequired, r.env.Code, r.rec.Body.String())
	require.Equal(t, true, r.data()["captchaRequired"])
	// 带上验证码，正确的密码登录成功
	cap := f.do("GET", "/auth/captcha", nil, fromIP("203.0.113.6"))
	cid, _ := cap.data()["captchaId"].(string)
	require.NotEmpty(t, cid)
	ok = login("correct-horse-9", "203.0.113.6", gin.H{"captchaId": cid, "captchaCode": f.app.captcha.Peek(cid)})
	require.Equal(t, 0, ok.env.Code, ok.rec.Body.String())
	// 验证码错的仍然不放行
	cap = f.do("GET", "/auth/captcha", nil, fromIP("203.0.113.7"))
	cid, _ = cap.data()["captchaId"].(string)
	bad := login("correct-horse-9", "203.0.113.7", gin.H{"captchaId": cid, "captchaCode": "00000x"})
	require.Equal(t, httpx.CodeCaptchaRequired, bad.env.Code)
	require.Zero(t, f.countSecurity("login_rate_limited"), "整个过程没有一次按账号的次数拒绝")

	// 来源自己的次数用完仍然是 429
	for i := 0; i < 20; i++ {
		login("wrong-9", "198.51.100.90", nil)
	}
	require.Equal(t, 429, login("wrong-9", "198.51.100.90", nil).rec.Code)
}
