// Package middleware 是框架自用的 HTTP 中间件，由 core/app 装配。业务代码不引用它。
package middleware

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

// RequestID 沿用合规的 X-Request-Id，否则生成一个；写入 ctx、ctx 的 logger 和响应头。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(httpx.HeaderRequestID)
		if !httpx.ValidRequestID(id) {
			id = httpx.NewRequestID()
		}
		ctx := httpx.WithRequestID(c.Request.Context(), id)
		ctx = logx.With(ctx, "request_id", id)
		c.Request = c.Request.WithContext(ctx)
		c.Header(httpx.HeaderRequestID, id)
		c.Next()
	}
}

// ContextLogger 把进程 logger 放进每个请求的 ctx（必须排在 RequestID 之前）。
func ContextLogger(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request = c.Request.WithContext(logx.WithLogger(c.Request.Context(), base))
		c.Next()
	}
}

// KeyPanic 是 gin.Context 里记录 panic 的键，值是 PanicInfo；错误日志读它（D-032）。
const KeyPanic = "ga.panic"

// PanicInfo 是一次 panic 的内容。
type PanicInfo struct {
	Value string // fmt.Sprint(recover())
	Type  string // recover() 值的类型，用来区分同一位置的不同 panic
	Stack []byte // runtime/debug.Stack()
}

// Recovery 把 panic 转成 500 信封并记录堆栈，不向客户端泄露任何细节。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				ctx := c.Request.Context()
				stack := debug.Stack()
				c.Set(KeyPanic, PanicInfo{Value: fmt.Sprint(r), Type: fmt.Sprintf("%T", r), Stack: stack})
				logx.From(ctx).ErrorContext(ctx, "panic recovered", "panic", r, "path", c.Request.URL.Path, "stack", string(stack))
				if c.Writer.Written() {
					c.Abort()
					return
				}
				httpx.Fail(c, httpx.ErrInternal)
			}
		}()
		c.Next()
	}
}

// Deadline 给请求 ctx 加上处理时限（D-037）：到点后 ctx 被取消，经过 ctx 的查询随之中止、连接放回池子，
// 失败的请求由 httpx.Fail 翻成 503。请求结束后才写的记录（操作日志、安全事件等）自己去掉取消信号，不受影响。
func Deadline(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d <= 0 {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// BodyLimit 在读取请求体之前套上上限；超限时读取会失败，统一返回 413 信封。
func BodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil && maxBytes > 0 {
			// 有 Content-Length 的直接拒绝，省得读
			if c.Request.ContentLength > maxBytes {
				httpx.Fail(c, httpx.ErrBodyTooLarge)
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
		// 没带 Content-Length 的分块请求在 handler 里读取时才会超限：把 MaxBytesError 翻成 413
		for _, ge := range c.Errors {
			var mbe *http.MaxBytesError
			if errors.As(ge.Err, &mbe) && !c.Writer.Written() {
				httpx.Fail(c, httpx.ErrBodyTooLarge)
				return
			}
		}
	}
}

// SecurityHeaders 加固定的安全响应头（规范 §12.1）。CSP 和 HSTS 由 nginx 负责。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		c.Next()
	}
}

// AccessLog 记录每个请求的一行访问日志。不记录请求头里的 Authorization 和 Cookie。
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		ctx := c.Request.Context()
		code, _ := c.Get(httpx.KeyResponseCode)
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"code", code,
			"latency_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
			"bytes", c.Writer.Size(),
		}
		log := logx.From(ctx)
		switch {
		case c.Writer.Status() >= 500:
			log.ErrorContext(ctx, "http", attrs...)
		case c.Writer.Status() >= 400:
			log.WarnContext(ctx, "http", attrs...)
		default:
			log.InfoContext(ctx, "http", attrs...)
		}
	}
}

// NoRoute 是未匹配路由的处理：404 信封。
func NoRoute() gin.HandlerFunc {
	return func(c *gin.Context) { httpx.Fail(c, httpx.ErrNotFound) }
}

// NoMethod 是路径存在但方法不允许的处理：405 信封。
func NoMethod() gin.HandlerFunc {
	return func(c *gin.Context) { httpx.Fail(c, httpx.ErrMethodNotAllowed) }
}
