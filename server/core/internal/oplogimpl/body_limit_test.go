package oplogimpl

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/middleware"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

type observedBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *observedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *observedBody) Close() error { b.closed = true; return nil }

// 规范 §13.2 第 172 条：预读在大小边界处决定是否进入后续路由链。
func TestMiddleware_172_BodyLimitBeforeNext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const limit = 64
	for _, ct := range []string{"application/json", "application/problem+json", "application/x-www-form-urlencoded", "application/octet-stream", ""} {
		for _, size := range []int{0, limit - 1, limit, limit + 1, limit * 8} {
			t.Run(fmt.Sprintf("%s/%d", ct, size), func(t *testing.T) {
				raw := strings.Repeat("a", size)
				body := &observedBody{Reader: strings.NewReader(raw)}
				r := gin.New()
				r.Use(middleware.ContextLogger(logx.New("error", "text", io.Discard)), middleware.RequestID(), middleware.BodyLimit(limit))
				var nextRan, handlerRan bool
				r.POST("/x", Middleware("platform", "test.body-limit"), func(c *gin.Context) {
					nextRan = true
					c.Next()
				}, func(c *gin.Context) {
					handlerRan = true
					got, err := io.ReadAll(c.Request.Body)
					require.NoError(t, err)
					require.Equal(t, raw, string(got), "预读后完整回放")
					httpx.OK(c, nil)
				})
				req := httptest.NewRequest(http.MethodPost, "/x", body)
				req.ContentLength = -1
				req.TransferEncoding = []string{"chunked"}
				req.Header.Set("Content-Type", ct)
				req.Header.Set("Accept-Language", "en")
				req.Header.Set(httpx.HeaderRequestID, "body-limit-172")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				var env httpx.Envelope
				dec := json.NewDecoder(w.Body)
				require.NoError(t, dec.Decode(&env))
				require.ErrorIs(t, dec.Decode(new(any)), io.EOF, "只输出一个响应信封")
				require.Equal(t, "body-limit-172", env.RequestID)
				require.True(t, body.closed)
				require.LessOrEqual(t, body.read, limit+1, "预读不能继续消耗超限内容")
				if size > limit {
					require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
					require.Equal(t, httpx.CodeBodyTooLarge, env.Code)
					require.Equal(t, httpx.Message(httpx.CodeBodyTooLarge, httpx.LangEN), env.Msg)
					require.False(t, nextRan)
					require.False(t, handlerRan)
				} else {
					require.Equal(t, http.StatusOK, w.Code)
					require.Equal(t, httpx.CodeOK, env.Code)
					require.True(t, nextRan)
					require.True(t, handlerRan)
				}
			})
		}
	}
}

func TestMiddleware_172_ReadErrorClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name string
		err  error
		code int
	}{
		{"wrapped limit", fmt.Errorf("body reader: %w", &http.MaxBytesError{Limit: 64}), httpx.CodeBodyTooLarge},
		{"other error", io.ErrUnexpectedEOF, httpx.CodeBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(middleware.ContextLogger(logx.New("error", "text", io.Discard)), middleware.BodyLimit(64))
			ran := false
			r.POST("/x", Middleware("platform", "test.body-error"), func(c *gin.Context) {
				ran = true
				data, err := io.ReadAll(c.Request.Body)
				require.Empty(t, data, "不能把部分内容作为完整请求交给处理函数")
				require.ErrorIs(t, err, tc.err)
				httpx.Fail(c, httpx.ErrBadRequest.WithCause(err))
			})
			body := &observedBody{Reader: io.MultiReader(strings.NewReader("private-fragment"), errReader{err: tc.err})}
			req := httptest.NewRequest(http.MethodPost, "/x", body)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			var env httpx.Envelope
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
			require.Equal(t, tc.code, env.Code)
			require.Equal(t, httpx.StatusOf(tc.code), w.Code)
			require.Equal(t, tc.code != httpx.CodeBodyTooLarge, ran)
			require.NotContains(t, w.Body.String(), "private-fragment")
			require.True(t, body.closed)
		})
	}
}
