package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/logx"
)

// HeaderRequestID 是请求 ID 的头名，请求和响应都用它。
const HeaderRequestID = "X-Request-Id"

// KeyResponseCode 是 gin.Context 里记录本次响应错误码的键，访问日志和操作日志读它。
const KeyResponseCode = "ga.response_code"

// KeyResponseMsg 是 gin.Context 里记录本次失败响应文案的键，操作日志把它写进 error 列。
const KeyResponseMsg = "ga.response_msg"

// KeyResponseErr 是 gin.Context 里记录服务端故障（状态 ≥ 500）原始错误的键，错误日志读它（D-032）。
const KeyResponseErr = "ga.response_err"

// Envelope 是统一响应信封（规范 §9.2）。
type Envelope struct {
	Code int    `json:"code"`
	Data any    `json:"data"`
	Msg  string `json:"msg"`
	// Key、Params：业务自定的失败说明的翻译键和参数（D-026），前端有翻译时用它代替 Msg。
	Key       string         `json:"key,omitempty"`
	Params    map[string]any `json:"params,omitempty"`
	RequestID string         `json:"requestId"`
}

// PageData 是分页响应的 data 结构。
type PageData struct {
	List     any   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// OK 输出成功响应。data 为 nil 时输出 null。
func OK(c *gin.Context, data any) {
	c.Set(KeyResponseCode, CodeOK)
	c.JSON(http.StatusOK, Envelope{Code: CodeOK, Data: data, Msg: Message(CodeOK, LangOf(c)), RequestID: RequestIDFromGin(c)})
}

// OKPage 输出分页响应。list 为 nil 时输出空数组而不是 null。
func OKPage(c *gin.Context, list any, total int64, page, pageSize int) {
	if list == nil {
		list = []any{}
	}
	OK(c, PageData{List: list, Total: total, Page: page, PageSize: pageSize})
}

// Fail 输出失败响应并终止后续 handler。
// 非 *Error 的错误一律按内部错误处理：记日志、给客户端通用文案，不泄露内部细节。
func Fail(c *gin.Context, err error) {
	e := AsError(err)
	status := e.Status
	if status == 0 {
		status = StatusOf(e.Code)
	}
	lang := LangOf(c)
	msg, key, params := e.Msg, e.Key, e.Params
	if msg == "" || status >= 500 {
		msg, key, params = Message(e.Code, lang), "", nil
	}
	var data any
	switch {
	case len(e.Fields) > 0:
		data = gin.H{"fields": e.Fields}
	case e.Data != nil:
		data = e.Data
	}
	ctx := c.Request.Context()
	if status >= 500 {
		logx.From(ctx).ErrorContext(ctx, "request failed", "code", e.Code, "err", logx.ErrorText(err), "path", c.FullPath())
		c.Set(KeyResponseErr, err)
	}
	c.Set(KeyResponseCode, e.Code)
	c.Set(KeyResponseMsg, msg)
	c.AbortWithStatusJSON(status, Envelope{Code: e.Code, Data: data, Msg: msg, Key: key, Params: params, RequestID: RequestIDFromGin(c)})
}

// LangOf 按 Accept-Language 选择响应语言（D-026）：按 q 值从高到低逐个匹配支持的语言，
// 都不匹配时用英文；没有这个头时用默认语言（简体中文）。
func LangOf(c *gin.Context) string {
	return ParseAcceptLanguage(c.GetHeader("Accept-Language"))
}

// ---- 请求 ID ----

type ridKey struct{}

var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

// ValidRequestID 报告客户端提供的请求 ID 是否可沿用（规范 §9.4）。
func ValidRequestID(s string) bool { return requestIDRe.MatchString(s) }

// NewRequestID 生成 32 位十六进制请求 ID。
func NewRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// WithRequestID 把请求 ID 放进 ctx。中间件调用。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ridKey{}, id)
}

// RequestID 从 ctx 取请求 ID；没有时返回空串。
func RequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(ridKey{}).(string)
	return id
}

// RequestIDFromGin 从 gin.Context 取请求 ID。
func RequestIDFromGin(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return RequestID(c.Request.Context())
}
