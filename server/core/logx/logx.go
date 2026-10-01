// Package logx 是标准库 log/slog 的薄封装。
//
// 业务代码只用 From(ctx) 拿 logger；request_id、用户等字段由中间件放进 ctx，业务不用自己带。
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// New 按配置创建 logger。level 取 debug/info/warn/error，format 取 text/json。
// w 为 nil 时写到 stderr。
func New(level, format string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{Level: ParseLevel(level), ReplaceAttr: auditLevelName}
	var h slog.Handler
	if strings.EqualFold(format, "json") {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

// ParseLevel 把字符串转成 slog.Level，未知值按 info。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithLogger 把 logger 放进 ctx。
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// With 在 ctx 现有 logger 上追加字段，返回新 ctx。中间件用它加 request_id、user_id。
func With(ctx context.Context, args ...any) context.Context {
	return WithLogger(ctx, From(ctx).With(args...))
}

// From 返回 ctx 里的 logger；没有时返回 slog.Default()，永不返回 nil。
func From(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
			return l
		}
	}
	return slog.Default()
}

// LevelAudit 是审计记录（登录、操作、安全事件）的级别（D-032）：高于 error，不论配置的日志级别是什么都会输出，
// 显示为 AUDIT。外部日志系统按它收集，数据库被删改时外部还有一份。
const LevelAudit = slog.Level(12)

// Audit 写一行审计记录。
func Audit(ctx context.Context, msg string, args ...any) {
	From(ctx).Log(ctx, LevelAudit, msg, args...)
}

func auditLevelName(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.LevelKey {
		if l, ok := a.Value.Any().(slog.Level); ok && l == LevelAudit {
			a.Value = slog.StringValue("AUDIT")
		}
	}
	return a
}
