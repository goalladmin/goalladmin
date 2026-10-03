package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

func init() { gin.SetMode(gin.TestMode) }

func do(t *testing.T, h gin.HandlerFunc, method, target string, body string, headers map[string]string) (*httptest.ResponseRecorder, httpx.Envelope) {
	t.Helper()
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(httpx.WithRequestID(c.Request.Context(), "req-0001-test"))
		c.Next()
	})
	r.Handle(method, "/x", h)
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "响应必须是统一信封: %s", w.Body.String())
	return w, env
}

func TestOK_Envelope(t *testing.T) {
	w, env := do(t, func(c *gin.Context) { httpx.OK(c, gin.H{"a": 1}) }, "GET", "/x", "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, 0, env.Code)
	require.Equal(t, "req-0001-test", env.RequestID)
	require.Equal(t, "成功", env.Msg)
	require.Equal(t, map[string]any{"a": float64(1)}, env.Data)
}

func TestOKPage_NilListBecomesEmptyArray(t *testing.T) {
	w, _ := do(t, func(c *gin.Context) { httpx.OKPage(c, nil, 0, 1, 20) }, "GET", "/x", "", nil)
	require.Contains(t, w.Body.String(), `"list":[]`)
	require.Contains(t, w.Body.String(), `"total":0`)
}

func TestFail_MapsCodesToStatusAndLanguage(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   int
	}{
		{httpx.ErrTokenInvalid, 401, httpx.CodeTokenInvalid},
		{httpx.ErrForbidden, 403, httpx.CodeForbidden},
		{httpx.ErrPwdChangeRequired, 403, httpx.CodePwdChangeRequired},
		{httpx.ErrNotFound, 404, httpx.CodeNotFound},
		{httpx.ErrBodyTooLarge, 413, httpx.CodeBodyTooLarge},
		{httpx.ErrTooManyRequests, 429, httpx.CodeTooManyRequests},
		{httpx.New(httpx.CodeLoginFailed, ""), 200, httpx.CodeLoginFailed},
		{httpx.ErrValidation, 200, httpx.CodeValidation},
		{httpx.ErrConflict, 200, httpx.CodeConflict},
		{httpx.New(12345, "biz"), 200, 12345},
		{errors.New("plain"), 500, httpx.CodeInternal},
	}
	for _, tc := range cases {
		w, env := do(t, func(c *gin.Context) { httpx.Fail(c, tc.err) }, "GET", "/x", "", nil)
		require.Equal(t, tc.status, w.Code, "err=%v", tc.err)
		require.Equal(t, tc.code, env.Code, "err=%v", tc.err)
		require.Equal(t, "req-0001-test", env.RequestID)
	}

	_, env := do(t, func(c *gin.Context) { httpx.Fail(c, httpx.ErrForbidden) }, "GET", "/x", "", map[string]string{"Accept-Language": "en-US,en;q=0.9"})
	require.Equal(t, "permission denied", env.Msg)
	_, env = do(t, func(c *gin.Context) { httpx.Fail(c, httpx.ErrForbidden) }, "GET", "/x", "", map[string]string{"Accept-Language": "zh-CN"})
	require.Equal(t, "没有权限执行此操作", env.Msg)
}

func TestFail_InternalErrorNeverLeaksDetails(t *testing.T) {
	secret := "password=hunter2 table users does not exist"
	w, env := do(t, func(c *gin.Context) { httpx.Fail(c, errors.New(secret)) }, "GET", "/x", "", nil)
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "hunter2")
	require.Equal(t, "服务器内部错误", env.Msg)

	// 即使开发者给 5xx 写了自定义 msg，也用默认文案
	w, env = do(t, func(c *gin.Context) { httpx.Fail(c, httpx.New(httpx.CodeInternal, secret)) }, "GET", "/x", "", nil)
	require.Equal(t, 500, w.Code)
	require.NotContains(t, w.Body.String(), "hunter2")
	require.Equal(t, "服务器内部错误", env.Msg)
}

func TestFail_CustomMsgAndFields(t *testing.T) {
	err := httpx.New(httpx.CodeConflict, "用户名已存在").WithFields(httpx.FieldError{Field: "username", Message: "taken"})
	w, env := do(t, func(c *gin.Context) { httpx.Fail(c, err) }, "GET", "/x", "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, "用户名已存在", env.Msg)
	require.Contains(t, w.Body.String(), `"fields":[{"field":"username","message":"taken"}]`)
}

func TestError_IsAndUnwrap(t *testing.T) {
	cause := errors.New("db down")
	err := httpx.ErrNotFound.WithCause(cause)
	require.True(t, errors.Is(err, httpx.ErrNotFound))
	require.True(t, errors.Is(err, cause))
	require.False(t, errors.Is(err, httpx.ErrForbidden))

	wrapped := httpx.Wrap(httpx.CodeUnavailable, cause)
	require.Equal(t, httpx.CodeUnavailable, httpx.AsError(wrapped).Code)
	require.Equal(t, httpx.CodeInternal, httpx.AsError(cause).Code)
}

func TestBind_ValidationFieldsUseJSONNames(t *testing.T) {
	type dto struct {
		Username string `json:"username" binding:"required,min=3"`
		Email    string `json:"email" binding:"omitempty,email"`
	}
	h := func(c *gin.Context) {
		var d dto
		if err := httpx.BindJSON(c, &d); err != nil {
			httpx.Fail(c, err)
			return
		}
		httpx.OK(c, d)
	}
	w, env := do(t, h, "POST", "/x", `{"username":"ab","email":"nope"}`, map[string]string{"Content-Type": "application/json"})
	require.Equal(t, 200, w.Code)
	require.Equal(t, httpx.CodeValidation, env.Code)
	body := w.Body.String()
	require.Contains(t, body, `"field":"username"`)
	require.Contains(t, body, `"field":"email"`)
	require.NotContains(t, body, `"Username"`)

	w, env = do(t, h, "POST", "/x", `{not json`, map[string]string{"Content-Type": "application/json"})
	require.Equal(t, 400, w.Code)
	require.Equal(t, httpx.CodeBadRequest, env.Code)

	w, env = do(t, h, "POST", "/x", `{"username":"alice"}`, map[string]string{"Content-Type": "application/json"})
	require.Equal(t, 200, w.Code)
	require.Equal(t, 0, env.Code)
}

func TestBindPage_ClampsAndWhitelistsSort(t *testing.T) {
	var got httpx.PageQuery
	h := func(c *gin.Context) { got = httpx.BindPage(c); httpx.OK(c, nil) }

	do(t, h, "GET", "/x?page=0&pageSize=9999&sortBy=created_at;DROP&sortOrder=DESC", "", nil)
	require.Equal(t, 1, got.Page)
	require.Equal(t, httpx.MaxPageSize, got.PageSize)
	require.Equal(t, "desc", got.SortOrder)
	allowed := map[string]string{"createdAt": "created_at", "name": "display_name"}
	require.Equal(t, "id DESC", got.Order(allowed, "id DESC"), "不在白名单的 sortBy 必须回落")

	do(t, h, "GET", "/x?page=3&sortBy=createdAt&sortOrder=desc", "", nil)
	require.Equal(t, 3, got.Page)
	require.Equal(t, httpx.DefaultPageSize, got.PageSize)
	require.Equal(t, 40, got.Offset())
	require.Equal(t, "created_at DESC", got.Order(allowed, "id DESC"))

	do(t, h, "GET", "/x?sortBy=name&sortOrder=sideways", "", nil)
	require.Equal(t, "display_name ASC", got.Order(allowed, "id DESC"))
}

func TestRequestID(t *testing.T) {
	require.True(t, httpx.ValidRequestID("abcdefgh-1234"))
	require.False(t, httpx.ValidRequestID("short"))
	require.False(t, httpx.ValidRequestID("has space here"))
	require.False(t, httpx.ValidRequestID(strings.Repeat("a", 65)))
	require.Len(t, httpx.NewRequestID(), 32)
	require.Equal(t, "", httpx.RequestID(context.Background()))
	require.Equal(t, "r-12345678", httpx.RequestID(httpx.WithRequestID(context.Background(), "r-12345678")))
}

func TestStatusOf_Segments(t *testing.T) {
	require.Equal(t, http.StatusOK, httpx.StatusOf(httpx.CodeOK))
	require.Equal(t, http.StatusUnauthorized, httpx.StatusOf(httpx.CodeTokenInvalid))
	require.Equal(t, http.StatusConflict, httpx.StatusOf(httpx.CodeRefreshRetry))
	require.Equal(t, http.StatusOK, httpx.StatusOf(httpx.CodeLoginFailed))
	require.Equal(t, http.StatusForbidden, httpx.StatusOf(2999))
	require.Equal(t, http.StatusInternalServerError, httpx.StatusOf(5999))
	require.Equal(t, http.StatusOK, httpx.StatusOf(10001))
}

func TestRegisterMessage(t *testing.T) {
	httpx.RegisterMessage(10001, "余额不足", "insufficient balance")
	require.Equal(t, "余额不足", httpx.Message(10001, httpx.LangZH))
	require.Equal(t, "insufficient balance", httpx.Message(10001, httpx.LangEN))
	require.Equal(t, "OK", httpx.Message(10002, httpx.LangZH), "未登记的码回落到 HTTP 状态文本")
}

func TestBindJSONStrict_RejectsUnknownFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type body struct {
		Name string `json:"name" binding:"required,max=4"`
		Sub  []struct {
			V int `json:"v"`
		} `json:"sub"`
	}
	run := func(raw string) error {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(raw))
		c.Request.Header.Set("Content-Type", "application/json")
		var b body
		return httpx.BindJSONStrict(c, &b)
	}
	require.NoError(t, run(`{"name":"ok","sub":[{"v":1}]}`))
	require.ErrorIs(t, run(`{"name":"ok","path":"/evil"}`), httpx.ErrBadRequest)
	require.ErrorIs(t, run(`{"name":"ok","sub":[{"v":1,"x":2}]}`), httpx.ErrBadRequest, "嵌套结构体里的未知字段同样拒绝")
	require.ErrorIs(t, run(`{"name":"ok"}{"name":"again"}`), httpx.ErrBadRequest)
	require.ErrorIs(t, run(`{"name":"toolong"}`), httpx.ErrValidation)
	require.ErrorIs(t, run(`not json`), httpx.ErrBadRequest)
	for _, suffix := range []string{"]", "}", " true", " null", " trailing"} {
		require.ErrorIs(t, run(`{"name":"ok"}`+suffix), httpx.ErrBadRequest)
	}
	require.NoError(t, run("{\"name\":\"ok\"} \r\n\t"))
}

// D-026：Accept-Language 匹配到 11 种语言之一；缺文案时按回退链。
func TestParseAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"":                           httpx.LangZH,
		"en-US,en;q=0.9":             httpx.LangEN,
		"en-GB":                      httpx.LangEN,
		"zh-CN":                      httpx.LangZH,
		"zh":                         httpx.LangZH,
		"zh-SG":                      httpx.LangZH,
		"zh-TW":                      httpx.LangZHTW,
		"zh-HK":                      httpx.LangZHTW,
		"zh-Hant-MO":                 httpx.LangZHTW,
		"ja":                         httpx.LangJA,
		"ko-KR":                      httpx.LangKO,
		"ms-SG":                      httpx.LangMS,
		"ta-SG":                      httpx.LangTA,
		"ta":                         httpx.LangTA,
		"bn-IN":                      httpx.LangBN,
		"ru":                         httpx.LangRU,
		"fr-CA":                      httpx.LangFR,
		"de-AT,de;q=0.9":             httpx.LangDE,
		"ur-PK":                      httpx.LangEN, // 不支持的语言用英文
		"ur-PK,fr;q=0.5":             httpx.LangFR, // 按顺序找第一个支持的
		"de;q=0.2,ja;q=0.8":          httpx.LangJA, // 按 q 值排序
		"ja;q=0,de":                  httpx.LangDE, // q=0 表示不要
		"  ZH-hant  ":                httpx.LangZHTW,
		"garbage;;;,,":               httpx.LangEN,
		"en_US":                      httpx.LangEN,
		"x-klingon, ko;q=0.1":        httpx.LangKO,
		"zh-Hans-CN;q=0.9, en;q=0.8": httpx.LangZH,
	}
	for h, want := range cases {
		require.Equal(t, want, httpx.ParseAcceptLanguage(h), "Accept-Language: %q", h)
	}
	require.Len(t, httpx.Langs(), 11)
	for _, l := range httpx.Langs() {
		require.Equal(t, l, httpx.MatchLang(l))
	}
}

func TestMessage_EveryLanguageHasEveryCodeOrFallsBack(t *testing.T) {
	codes := []int{
		httpx.CodeOK, httpx.CodeLoginFailed, httpx.CodeCaptchaRequired, httpx.CodeLocked, httpx.CodeTokenInvalid,
		httpx.CodeRefreshRetry, httpx.CodeSessionLocked, httpx.CodeForbidden, httpx.CodePwdChangeRequired, httpx.CodeValidation, httpx.CodeBadRequest,
		httpx.CodeConflict, httpx.CodeLastSuper, httpx.CodeDeclaredInCode, httpx.CodeNotFound, httpx.CodeMethodNotAllowed,
		httpx.CodeBodyTooLarge, httpx.CodeTooManyRequests, httpx.CodeInternal, httpx.CodeUnavailable,
	}
	for _, l := range httpx.Langs() {
		for _, c := range codes {
			require.NotEmpty(t, httpx.Message(c, l), "lang=%s code=%d", l, c)
		}
	}
	require.Equal(t, "没有权限执行此操作", httpx.Message(httpx.CodeForbidden, httpx.LangZH))
	require.Equal(t, "permission denied", httpx.Message(httpx.CodeForbidden, "xx-XX"), "未知语言回落英文")
	// 业务模块只登记了中英文时，其他语言回落英文
	httpx.RegisterMessage(19999, "测试", "test")
	require.Equal(t, "test", httpx.Message(19999, httpx.LangJA))
	require.Equal(t, "测试", httpx.Message(19999, httpx.LangZHTW), "繁体先回落简体")
	httpx.RegisterMessages(19999, map[string]string{httpx.LangJA: "テスト"})
	require.Equal(t, "テスト", httpx.Message(19999, httpx.LangJA))
	require.Equal(t, "test", httpx.Message(19999, httpx.LangEN), "追加语言不影响已有的")
}

func TestKeyedErrorsReachTheEnvelope(t *testing.T) {
	_, env := do(t, func(c *gin.Context) {
		httpx.Fail(c, httpx.ErrValidation.WithFields(httpx.NewField("items", "rbac.menu.maxDepth", "at most 4 levels", "limit", 4)))
	}, "GET", "/x", "", map[string]string{"Accept-Language": "ja-JP"})
	fields := env.Data.(map[string]any)["fields"].([]any)
	f := fields[0].(map[string]any)
	require.Equal(t, "rbac.menu.maxDepth", f["key"])
	require.Equal(t, map[string]any{"limit": float64(4)}, f["params"])
	require.Equal(t, "at most 4 levels", f["message"])

	_, env = do(t, func(c *gin.Context) {
		httpx.Fail(c, httpx.NewKey(httpx.CodeConflict, "dict.hasChildren", "delete its child items first"))
	}, "GET", "/x", "", nil)
	require.Equal(t, "dict.hasChildren", env.Key)
	require.Equal(t, "delete its child items first", env.Msg)

	// 5xx 一律用错误码的默认文案，不带键
	_, env = do(t, func(c *gin.Context) {
		httpx.Fail(c, httpx.NewKey(httpx.CodeInternal, "x.y", "secret detail"))
	}, "GET", "/x", "", nil)
	require.Empty(t, env.Key)
	require.NotContains(t, env.Msg, "secret")
}

func TestFail_DeadlineIsUnavailableNotInternal(t *testing.T) {
	// 请求超时（D-037）：不管是原样返回、被 fmt 包了一层，还是被业务代码包成内部错误，都回 503
	for _, err := range []error{
		context.DeadlineExceeded,
		fmt.Errorf("query users: %w", context.DeadlineExceeded),
		httpx.ErrInternal.WithCause(fmt.Errorf("query: %w", context.DeadlineExceeded)),
	} {
		w, env := do(t, func(c *gin.Context) { httpx.Fail(c, err) }, "GET", "/x", "", nil)
		require.Equal(t, 503, w.Code, "err=%v", err)
		require.Equal(t, httpx.CodeUnavailable, env.Code, "err=%v", err)
		require.NotContains(t, w.Body.String(), "deadline")
	}
	// 业务错误即使带着超时原因，也保持原样
	w, env := do(t, func(c *gin.Context) { httpx.Fail(c, httpx.ErrConflict.WithCause(context.DeadlineExceeded)) }, "GET", "/x", "", nil)
	require.Equal(t, 200, w.Code)
	require.Equal(t, httpx.CodeConflict, env.Code)
	// 客户端断开（Canceled）不是超时，仍是 500
	w, _ = do(t, func(c *gin.Context) { httpx.Fail(c, context.Canceled) }, "GET", "/x", "", nil)
	require.Equal(t, 500, w.Code)
}

// 规范 §13.2 第 99 条：页码有上限（D-055），极大的页码不会变成极深的 OFFSET，也不会溢出。
func TestPage_99_PageNumberCapped(t *testing.T) {
	q := httpx.PageQuery{Page: 1 << 40, PageSize: 200}.Normalize()
	require.Equal(t, httpx.MaxPage, q.Page)
	require.Equal(t, (httpx.MaxPage-1)*200, q.Offset())
	require.Equal(t, 3, httpx.PageQuery{Page: 3, PageSize: 20}.Normalize().Page)
}

// 179（D-095）：JSON 绑定只收声明成 JSON 的请求体。类型不对的不解析、回 3002，结构体保持零值；
// application/json（可带参数）和 +json 结尾的类型照常绑定。
func TestBindJSON_179_RequiresJSONContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type body struct {
		Name string `json:"name" binding:"required"`
	}
	binders := map[string]func(*gin.Context, any) error{"BindJSON": httpx.BindJSON, "BindJSONStrict": httpx.BindJSONStrict}
	run := func(bind func(*gin.Context, any) error, contentType string) (body, error) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"ok"}`))
		if contentType != "" {
			c.Request.Header.Set("Content-Type", contentType)
		}
		var b body
		err := bind(c, &b)
		return b, err
	}
	for name, bind := range binders {
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x",
			"application/octet-stream", "application/jsonx", "json", "application/json; charset", "text/json+xml"} {
			b, err := run(bind, ct)
			require.ErrorIs(t, err, httpx.ErrBadRequest, "%s %q", name, ct)
			require.Equal(t, httpx.CodeBadRequest, httpx.AsError(err).Code, "%s %q", name, ct)
			require.Empty(t, b.Name, "%s %q：类型不对的请求体不解析", name, ct)
		}
		for _, ct := range []string{"application/json", "application/json; charset=utf-8", "APPLICATION/JSON", "application/merge-patch+json"} {
			b, err := run(bind, ct)
			require.NoError(t, err, "%s %q", name, ct)
			require.Equal(t, "ok", b.Name, "%s %q", name, ct)
		}
	}
}

// 183（D-099）：ParseTime 只收 RFC 3339、换算成 UTC 后年份在 1–9999 之间的时间。
func TestParseTime_183(t *testing.T) {
	for _, in := range []string{"2026-10-03T08:00:00Z", "2026-10-03T08:00:00+08:00", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59Z", "2026-10-03T08:00:00.123456789Z"} {
		got, valid := httpx.ParseTime(in)
		require.True(t, valid, in)
		want, err := time.Parse(time.RFC3339, in)
		require.NoError(t, err)
		require.True(t, want.Equal(got), in)
	}
	for _, bad := range []string{"", "2026-10-03", "2026-10-03 08:00:00", "not a time",
		"0000-01-01T00:00:00Z", "0000-12-31T23:59:59Z", "0001-01-01T00:00:00+00:01", "9999-12-31T23:59:59-00:01", "9999-12-31T23:59:59-23:59"} {
		got, valid := httpx.ParseTime(bad)
		require.False(t, valid, bad)
		require.True(t, got.IsZero(), bad)
	}
}
