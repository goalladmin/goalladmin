package authimpl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/stretchr/testify/require"
)

func TestAdmission_193_BeforeWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &Authenticator{p: portal.Portal{Code: "platform"}, refreshRate: ratelimit.New(1, time.Minute, 10, nil)}
	r := gin.New()
	r.GET("/captcha", a.Captcha)
	r.POST("/refresh", a.Refresh)
	for _, headers := range []map[string]string{{}, {HeaderClient: "web", "Sec-Fetch-Site": "cross-site"}} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/captcha", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(w, req)
		require.Equal(t, 403, w.Code, "拒绝发生在未初始化的计数与验证码访问之前")
	}
	require.True(t, a.refreshRate.Allow("203.0.113.10"))
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/refresh", nil)
	req.RemoteAddr = "203.0.113.10:5000"
	req.Header.Set(HeaderClient, "web")
	req.AddCookie(&http.Cookie{Name: a.CookieName(), Value: strings.Repeat("a", 32) + "." + strings.Repeat("b", 64)})
	r.ServeHTTP(w, req)
	require.Equal(t, 429, w.Code, "窗口拒绝在访问未初始化的会话库之前")
	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, httpx.CodeTooManyRequests, env.Code)
	require.Empty(t, w.Result().Cookies())
}
