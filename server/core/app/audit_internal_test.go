package app

// D-032 安全事件：认证环节的攻击迹象都留下记录；正常现象（令牌过期、没带令牌）不记；
// 每条同时写一行 AUDIT 级别的记录到日志输出，日志级别调到 error 也照样输出。

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/audit"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

// syncBuffer 是可以并发写的缓冲区（请求在 httptest 里是同步的，这里只是保险）。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (f *authFixture) securityEvents() map[string]audit.SecurityEvent {
	f.t.Helper()
	ctx := f.app.Context(context.Background())
	f.app.audit.Flush(ctx)
	rows, _, err := f.app.Deps().Audit.ListSecurityEvents(ctx, audit.SecurityFilter{}, 1, 100)
	require.NoError(f.t, err)
	out := map[string]audit.SecurityEvent{}
	for _, r := range rows {
		out[r.Kind] = r
	}
	return out
}

// 规范 §13.2 第 45 条。
func TestAudit_45_AuthAnomaliesAreRecorded(t *testing.T) {
	logs := &syncBuffer{}
	f := newAuthFixture(t, WithLogger(logx.New("error", "json", logs)))
	uid := f.addUser("alice", "correct-horse-9")
	access, c0 := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 200, f.ping(access).rec.Code)
	require.Empty(t, f.securityEvents(), "正常登录、正常访问不产生安全事件")

	// 没带令牌、令牌过期：正常现象，不记
	require.Equal(t, 401, f.do("GET", "/ping", nil).rec.Code)
	f.clock.Advance(16 * time.Minute)
	require.Equal(t, 401, f.ping(access).rec.Code)
	require.Empty(t, f.securityEvents())

	// 签名不对的令牌：token_invalid
	now := f.clock.Now()
	claims := jwt.MapClaims{"iss": "goalladmin", "aud": testPortal, "sub": "1", "sid": strings.Repeat("a", 32), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	require.Equal(t, 401, f.ping(forgeToken(t, jwt.SigningMethodHS256, []byte("another-secret-0123456789abcdef0123456789"), claims)).rec.Code)
	// 过期、同时受众不对：不是单纯的过期，照样记 token_invalid（合并进上面那一行）
	expiredWrongAud := jwt.MapClaims{"iss": "goalladmin", "aud": "other", "sub": "1", "sid": strings.Repeat("a", 32), "iat": now.Add(-time.Hour).Unix(), "exp": now.Add(-time.Minute).Unix()}
	require.Equal(t, 401, f.ping(forgeToken(t, jwt.SigningMethodHS256, []byte("another-secret-0123456789abcdef0123456789"), expiredWrongAud)).rec.Code)
	// 用真密钥签、但会话不存在：token_mismatch（密钥可能泄露）
	require.Equal(t, 401, f.ping(forgeToken(t, jwt.SigningMethodHS256, []byte(testSecret), claims)).rec.Code)
	// 用真密钥签、但会话字段为空：同样是 token_mismatch
	noSid := jwt.MapClaims{"iss": "goalladmin", "aud": testPortal, "sub": "1", "sid": "", "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	require.Equal(t, 401, f.ping(forgeToken(t, jwt.SigningMethodHS256, []byte(testSecret), noSid)).rec.Code)

	// 刷新凭证重放：refresh_reuse，带会话和用户
	r1 := f.refresh(c0)
	require.Equal(t, 200, r1.rec.Code, r1.rec.Body.String())
	access1, _ := r1.data()["accessToken"].(string)
	var sid string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT sid FROM ga_session WHERE user_id = ?", uid).Scan(&sid).Error)
	f.clock.Advance(11 * time.Second)
	require.Equal(t, 401, f.refresh(c0).rec.Code)
	// 会话被吊销后，手里还没过期的访问令牌再用：session_revoked
	require.Equal(t, 401, f.ping(access1).rec.Code)

	// 刷新请求的来源不对：bad_origin
	_, c2 := f.mustLogin("alice", "correct-horse-9")
	require.Equal(t, 403, f.do("POST", "/auth/refresh", nil, cookieOpt(c2), func(r *http.Request) {
		r.Header.Set("X-GA-Client", "web")
		r.Header.Set("Origin", "http://evil.test")
	}).rec.Code)

	ev := f.securityEvents()
	require.Len(t, ev, 5, "%v", ev)
	require.Equal(t, audit.LevelWarning, ev["token_invalid"].Level)
	require.EqualValues(t, 2, ev["token_invalid"].Count)
	require.Equal(t, audit.LevelCritical, ev["token_mismatch"].Level)
	rows, _, err := f.app.Deps().Audit.ListSecurityEvents(f.app.Context(context.Background()), audit.SecurityFilter{Kind: "token_mismatch"}, 1, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	details := []string{rows[0].Detail, rows[1].Detail}
	require.ElementsMatch(t, []string{"unknown session", "bad claims"}, details)

	reuse := ev["refresh_reuse"]
	require.Equal(t, audit.LevelCritical, reuse.Level)
	require.Equal(t, sid, reuse.SessionID)
	require.Equal(t, uid, reuse.UserID)
	require.Equal(t, "203.0.113.10", reuse.IP)
	require.Equal(t, "/api/"+testPortal+"/v1/auth/refresh", reuse.Path)
	require.NotEmpty(t, reuse.RequestID)
	require.Equal(t, testPortal, reuse.Portal)

	revoked := ev["session_revoked"]
	require.Equal(t, audit.LevelInfo, revoked.Level)
	require.Equal(t, sid, revoked.SessionID)
	require.Equal(t, uid, revoked.UserID)

	require.Equal(t, "refresh", ev["bad_origin"].Detail)

	// 同时写进了日志输出：级别是 AUDIT，日志级别设成 error 也照样有
	out := logs.String()
	require.Contains(t, out, `"level":"AUDIT","msg":"audit.security"`)
	require.Contains(t, out, `"kind":"refresh_reuse"`)
	require.Contains(t, out, `"level":"AUDIT","msg":"audit.login"`)
	require.NotContains(t, out, "correct-horse-9")
}

// 同一来源刷接口：写库次数有上限，次数一个不少（规范 §13.2 第 46 条的一部分）。
func TestAudit_46_FloodIsMerged(t *testing.T) {
	f := newAuthFixture(t)
	now := f.clock.Now()
	claims := jwt.MapClaims{"iss": "goalladmin", "aud": testPortal, "sub": "1", "sid": strings.Repeat("b", 32), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	bad := forgeToken(t, jwt.SigningMethodHS256, []byte("another-secret-0123456789abcdef0123456789"), claims)
	for range 300 {
		require.Equal(t, 401, f.ping(bad).rec.Code)
	}
	ev := f.securityEvents()
	require.Len(t, ev, 1)
	require.EqualValues(t, 300, ev["token_invalid"].Count)

	// 下一分钟再来：新的一行
	f.clock.Advance(time.Minute)
	f.ping(bad)
	rows, total, err := f.app.Deps().Audit.ListSecurityEvents(f.app.Context(context.Background()), audit.SecurityFilter{Kind: "token_invalid"}, 1, 10)
	f.app.audit.Flush(f.app.Context(context.Background()))
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	require.EqualValues(t, 1, rows[0].Count)
}

// 会话到了最长寿命自然过期，手里的访问令牌比它晚到期：这是正常现象，不记安全事件。
func TestAudit_45b_SessionExpiryIsNotAnEvent(t *testing.T) {
	f := newAuthFixture(t)
	uid := f.addUser("alice", "correct-horse-9")
	access, _ := f.mustLogin("alice", "correct-horse-9")
	require.NoError(t, f.app.Deps().DB.Exec("UPDATE ga_session SET expires_at = ? WHERE user_id = ?", f.clock.Now().Add(-time.Second), uid).Error)
	f.app.authenticators[testPortal].ForgetSession(f.sidOf(uid))
	require.Equal(t, 401, f.ping(access).rec.Code)
	require.Empty(t, f.securityEvents())
}

func (f *authFixture) sidOf(uid uint64) string {
	var sid string
	require.NoError(f.t, f.app.Deps().DB.Raw("SELECT sid FROM ga_session WHERE user_id = ? ORDER BY id DESC LIMIT 1", uid).Scan(&sid).Error)
	return sid
}

// 规范 §13.2 第 104 条（D-058）：请求头里的非法 UTF-8、按字节截断切开的汉字，都不能让审计记录写不进去——
// 登录日志、操作日志、安全事件照常落库，登录照常能成功。
func TestAudit_104_InvalidUTF8IsStillAudited(t *testing.T) {
	f := newAuthFixture(t)
	f.addUser("alice", "correct-horse-9")
	badUA := func(r *http.Request) { r.Header.Set("User-Agent", "Mozilla/5.0 \xff\xfe") }
	count := func(q string, args ...any) int64 {
		var n int64
		require.NoError(t, f.app.Deps().DB.Raw(q, args...).Scan(&n).Error)
		return n
	}

	// 登录日志：坏 User-Agent；用户名 22 个"中"（66 字节，按 64 字节截断会切开最后一个字）
	require.Equal(t, httpx.CodeLoginFailed, f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "wrong-9"}, badUA).env.Code)
	require.Equal(t, httpx.CodeLoginFailed, f.login(strings.Repeat("中", 22), "wrong-9").env.Code)
	require.EqualValues(t, 2, count("SELECT COUNT(*) FROM ga_login_log"))
	var ua string
	require.NoError(t, f.app.Deps().DB.Raw("SELECT user_agent FROM ga_login_log ORDER BY id LIMIT 1").Scan(&ua).Error)
	require.True(t, utf8.ValidString(ua))
	require.Contains(t, ua, "Mozilla/5.0 ")

	// 密码正确、User-Agent 坏：登录照常成功，会话、登录日志都在
	r := f.do("POST", "/auth/login", gin.H{"username": "alice", "password": "correct-horse-9"}, badUA)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	access, _ := r.data()["accessToken"].(string)
	require.EqualValues(t, 1, count("SELECT COUNT(*) FROM ga_login_log WHERE success = 1"))

	// 操作日志：锁屏会记一条
	require.Equal(t, 0, f.do("POST", "/auth/lock", nil, bearerOpt(access), badUA).env.Code)
	require.EqualValues(t, 1, count("SELECT COUNT(*) FROM ga_operation_log WHERE action = ?", OpLock))

	// 安全事件：签名不对的令牌
	now := f.clock.Now()
	claims := jwt.MapClaims{"iss": "goalladmin", "aud": testPortal, "sub": "1", "sid": strings.Repeat("a", 32), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix()}
	require.Equal(t, 401, f.do("GET", "/ping", nil, bearerOpt(forgeToken(t, jwt.SigningMethodHS256, []byte("another-secret-0123456789abcdef0123456789"), claims)), badUA).rec.Code)
	require.Contains(t, f.securityEvents(), "token_invalid")
}
