package app

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/redisx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

type captchaPortalsModule struct{ authTestModule }

func (m *captchaPortalsModule) Init(d *Deps) error {
	for _, code := range []string{testPortal, "other"} {
		if err := d.Portals.Register(portal.Portal{Code: code, Users: m.users, Login: portal.LoginPolicy{CaptchaAlways: true}}); err != nil {
			return err
		}
	}
	return nil
}

func captchaChallenge(f *authFixture) (string, string) {
	f.t.Helper()
	r := f.do(http.MethodGet, "/auth/captcha", nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	require.Len(f.t, r.data(), 2, "只返回 ID 和图片，不返回答案")
	id := r.data()["captchaId"].(string)
	require.Contains(f.t, r.data()["image"], "data:image/png;base64,")
	answer := f.app.captcha.Peek(id)
	require.Len(f.t, answer, 5)
	return id, answer
}

func captchaLogin(f *authFixture, id, answer string) resp {
	return f.do(http.MethodPost, "/auth/login", gin.H{
		"username": "alice", "password": "alice-pass-123", "captchaId": id, "captchaCode": answer,
	})
}

// 166：真实 HTTP 登录必须接受另一实例生成的验证码，任何一次核对后不得重放。
func TestCaptcha_166_CrossInstanceLogin(t *testing.T) {
	a, b := newSharedCountsPair(t, portal.LoginPolicy{CaptchaAlways: true, IPRatePerMinute: 120, AccountRatePerMinute: 120})
	a.addUser("alice", "alice-pass-123")
	for _, pair := range [][2]*authFixture{{a, b}, {b, a}} {
		id, answer := captchaChallenge(pair[0])
		require.Equal(t, byte('r'), id[0])
		require.Equal(t, 0, captchaLogin(pair[1], id, answer).env.Code)
		require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(pair[0], id, answer).env.Code)
	}
	for _, wrong := range []string{"wrong", ""} {
		id, answer := captchaChallenge(a)
		require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(b, id, wrong).env.Code)
		require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(a, id, answer).env.Code)
	}
	id, answer := captchaChallenge(a)
	a.clock.Advance(5 * time.Minute)
	require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(b, id, answer).env.Code)
}

// 166：故障期间保持本地验证码服务；共享验证码失败不跳过检查，恢复不搬运本地答案。
func TestCaptcha_166_LoginFallbackAndRecovery(t *testing.T) {
	gdb, clock, addr, prefix := newInvalidationDB(t)
	proxy := redisx.NewTestProxy(t, addr)
	users := newMemUsers()
	fixture := func() *authFixture {
		module := &sharedCountsModule{invalidationTestModule: invalidationTestModule{authTestModule{users: users}},
			policy: portal.LoginPolicy{CaptchaAlways: true, IPRatePerMinute: 120, AccountRatePerMinute: 120}}
		a := newInvalidationApp(t, gdb, clock, proxy.Addr(), prefix, testPortal, testSecret, module)
		return &authFixture{t: t, app: a, users: users, clock: clock}
	}
	a, b := fixture(), fixture()
	a.addUser("alice", "alice-pass-123")
	shared, sharedAnswer := captchaChallenge(a)
	proxy.Cut()
	require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(b, shared, sharedAnswer).env.Code)
	local, localAnswer := captchaChallenge(a)
	require.Equal(t, byte('l'), local[0])
	require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(b, local, localAnswer).env.Code)
	require.Equal(t, 0, captchaLogin(a, local, localAnswer).env.Code)
	local, localAnswer = captchaChallenge(a)
	proxy.Restore()
	require.Eventually(t, func() bool { return a.app.redis.Available() && b.app.redis.Available() }, 8*time.Second, 10*time.Millisecond)
	require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(b, local, localAnswer).env.Code)
	require.Equal(t, 0, captchaLogin(a, local, localAnswer).env.Code)
	fresh, freshAnswer := captchaChallenge(a)
	require.Equal(t, byte('r'), fresh[0])
	require.Equal(t, 0, captchaLogin(b, fresh, freshAnswer).env.Code)
}

func TestCaptcha_166_HTTPPortalIsolation(t *testing.T) {
	for _, mode := range []string{"local", "redis"} {
		t.Run(mode, func(t *testing.T) {
			gdb, clock, addr, prefix := newInvalidationDB(t)
			cfg := conf.Default()
			cfg.Server.AllowedOrigins = []string{testOrigin}
			for _, code := range []string{testPortal, "other"} {
				cfg.Portals[code] = conf.Portal{AccessTTL: 15 * time.Minute, RefreshTTL: 168 * time.Hour, JWTSecret: testSecret}
			}
			if mode == "redis" {
				cfg.Redis.Addr, cfg.Redis.KeyPrefix = addr, prefix
			}
			a, err := New(cfg, WithDB(gdb), WithClock(clock.Now), WithLogger(logx.New("error", "text", io.Discard)), WithPasswordHashParams(64, 1), WithoutMigrations())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
			users := newMemUsers()
			a.Register(&captchaPortalsModule{authTestModule{users: users}})
			require.NoError(t, a.Setup())
			f := &authFixture{t: t, app: a, users: users, clock: clock}
			f.addUser("alice", "alice-pass-123")
			other := func(path string) reqOpt {
				return func(r *http.Request) { r.URL.Path = "/api/other/v1" + path }
			}
			body := func(id, answer string) gin.H {
				return gin.H{"username": "alice", "password": "alice-pass-123", "captchaId": id, "captchaCode": answer}
			}
			id, answer := captchaChallenge(f)
			require.Equal(t, httpx.CodeCaptchaRequired, f.do("POST", "/auth/login", body(id, answer), other("/auth/login")).env.Code)
			require.Equal(t, 0, captchaLogin(f, id, answer).env.Code, "跨端请求不消费原端验证码")
			r := f.do("GET", "/auth/captcha", nil, other("/auth/captcha"))
			require.Equal(t, 0, r.env.Code)
			id = r.data()["captchaId"].(string)
			answer = a.captcha.Peek(id)
			require.Len(t, answer, 5)
			require.Equal(t, httpx.CodeCaptchaRequired, captchaLogin(f, id, answer).env.Code)
			require.Equal(t, 0, f.do("POST", "/auth/login", body(id, answer), other("/auth/login")).env.Code)
		})
	}
}
