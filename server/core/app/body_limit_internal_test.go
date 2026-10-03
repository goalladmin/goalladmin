package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/oplogimpl"
)

// 规范 §13.2 第 172 条：不读取请求体的操作同样受预读检查约束，失败日志保留最终结果。
func TestBodyLimit_172_RecordedRequestKeepsSessionState(t *testing.T) {
	const limit = 256
	f := newAuthFixtureCfg(t, func(cfg *conf.Config) { cfg.Server.MaxBodyBytes = limit })
	uid := f.addUser("alice", "alice-pass-123")
	access, _ := f.mustLogin("alice", "alice-pass-123")
	body := gin.H{"password": strings.Repeat("private", 50)}
	unknownLength := func(r *http.Request) {
		r.ContentLength = -1
		r.TransferEncoding = []string{"chunked"}
	}
	var entries []oplogimpl.Entry
	// 守卫仍在预读前执行，未登录请求不产生操作日志。
	unauthenticated := f.do(http.MethodPost, "/auth/lock", body, unknownLength)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.rec.Code)
	require.NoError(t, f.app.deps.DB.Where("action = ?", OpLock).Find(&entries).Error)
	require.Empty(t, entries)

	r := f.do(http.MethodPost, "/auth/lock", body, bearerOpt(access), unknownLength)
	require.Equal(t, http.StatusRequestEntityTooLarge, r.rec.Code)
	require.Equal(t, httpx.CodeBodyTooLarge, r.env.Code)
	require.Equal(t, httpx.CodeOK, f.ping(access).env.Code, "超限请求不能改变会话状态")
	require.NoError(t, f.app.deps.DB.Where("action = ?", OpLock).Find(&entries).Error)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, r.env.RequestID, e.RequestID)
	require.Equal(t, testPortal, e.Portal)
	require.Equal(t, uid, e.UserID)
	require.NotEmpty(t, e.SessionID)
	require.Equal(t, http.StatusRequestEntityTooLarge, e.HTTPStatus)
	require.Equal(t, httpx.CodeBodyTooLarge, e.Code)
	require.Equal(t, r.env.Msg, e.Error)
	require.Equal(t, "<unreadable, 256 bytes>", e.Body)
	require.NotContains(t, e.Body, "private")

	// 正常操作仍生效，并独立记录成功结果。
	ok := f.do(http.MethodPost, "/auth/lock", nil, bearerOpt(access))
	require.Equal(t, httpx.CodeOK, ok.env.Code)
	require.Equal(t, httpx.CodeSessionLocked, f.ping(access).env.Code)
	require.NoError(t, f.app.deps.DB.Where("action = ?", OpLock).Order("id").Find(&entries).Error)
	require.Len(t, entries, 2)
	require.Equal(t, http.StatusOK, entries[1].HTTPStatus)
	require.Equal(t, httpx.CodeOK, entries[1].Code)
}
