package httpx

import (
	"context"
	"errors"
	"fmt"
)

// Error 是带错误码的错误。业务代码返回它，handler 交给 Fail 输出。
type Error struct {
	Code   int
	Status int            // 0 表示按 StatusOf(Code)
	Msg    string         // 空表示按 Message(Code, lang)
	Fields []FieldError   // 校验失败的字段明细
	Key    string         // Msg 的翻译键（D-026），前端有翻译时用它；空表示 Msg 就是最终文案或用错误码的默认文案
	Params map[string]any // Key 的参数
	Data   any            // 随失败响应返回的附加数据（如 captchaRequired）
	cause  error
}

// FieldError 是一个字段的校验错误。
//
// Key 和 Params 是给前端翻译用的（D-026）：前端有 err.<Key> 的翻译时代入 Params 显示，没有时显示 Message。
// Message 用英文写，给日志、接口调用方和没有翻译时兜底。
type FieldError struct {
	Field   string         `json:"field"`
	Message string         `json:"message"`
	Key     string         `json:"key,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
}

// NewField 构造一个带翻译键的字段错误。params 按 名字, 值, 名字, 值… 成对传入。
func NewField(field, key, message string, params ...any) FieldError {
	return FieldError{Field: field, Message: message, Key: key, Params: pairs(params)}
}

// NewKey 用错误码和翻译键创建错误：message 是英文兜底文案，params 按 名字, 值… 成对传入（D-026）。
func NewKey(code int, key, message string, params ...any) *Error {
	return &Error{Code: code, Msg: message, Key: key, Params: pairs(params)}
}

func pairs(kv []any) map[string]any {
	if len(kv) == 0 {
		return nil
	}
	out := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			out[k] = kv[i+1]
		}
	}
	return out
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("httpx: code=%d msg=%q: %v", e.Code, e.Msg, e.cause)
	}
	return fmt.Sprintf("httpx: code=%d msg=%q", e.Code, e.Msg)
}

// Unwrap 支持 errors.Is / errors.As 穿透到底层原因。
func (e *Error) Unwrap() error { return e.cause }

// Is 让 errors.Is(err, httpx.ErrNotFound) 这类比较按错误码匹配。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// New 用错误码创建错误。msg 为空时输出该码的默认文案。
func New(code int, msg string) *Error {
	return &Error{Code: code, Msg: msg}
}

// Newf 是 New 的格式化版本。
func Newf(code int, format string, a ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// Wrap 给错误加上错误码；原始错误保留在 cause 里，只进日志不进响应。
func Wrap(code int, err error) *Error {
	return &Error{Code: code, cause: err}
}

// WithStatus 覆盖 HTTP 状态码。
func (e *Error) WithStatus(status int) *Error {
	c := *e
	c.Status = status
	return &c
}

// WithFields 附加字段级校验错误。
func (e *Error) WithFields(fields ...FieldError) *Error {
	c := *e
	c.Fields = append(append([]FieldError{}, e.Fields...), fields...)
	return &c
}

// WithData 附加随失败响应返回的数据。
func (e *Error) WithData(data any) *Error {
	c := *e
	c.Data = data
	return &c
}

// WithCause 附加底层原因。
func (e *Error) WithCause(err error) *Error {
	c := *e
	c.cause = err
	return &c
}

// 常用错误的哨兵值。用 errors.Is(err, httpx.ErrNotFound) 判断，用 httpx.ErrNotFound.WithCause(...) 派生。
var (
	ErrTokenInvalid      = New(CodeTokenInvalid, "")
	ErrForbidden         = New(CodeForbidden, "")
	ErrPwdChangeRequired = New(CodePwdChangeRequired, "")
	ErrSessionLocked     = New(CodeSessionLocked, "")
	ErrValidation        = New(CodeValidation, "")
	ErrBadRequest        = New(CodeBadRequest, "")
	ErrConflict          = New(CodeConflict, "")
	ErrDeclaredInCode    = New(CodeDeclaredInCode, "")
	ErrNotFound          = New(CodeNotFound, "")
	ErrMethodNotAllowed  = New(CodeMethodNotAllowed, "")
	ErrBodyTooLarge      = New(CodeBodyTooLarge, "")
	ErrTooManyRequests   = New(CodeTooManyRequests, "")
	ErrInternal          = New(CodeInternal, "")
	ErrUnavailable       = New(CodeUnavailable, "")
)

// AsError 把任意 error 归一成 *Error：本来就是的原样返回，否则包成 CodeInternal。
// 超时（context.DeadlineExceeded）例外，归成 CodeUnavailable（D-037）。
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		// 业务代码把超时包成了内部错误：仍按"暂时不可用"回（D-037）
		if e.Code == CodeInternal && errors.Is(err, context.DeadlineExceeded) {
			if e.cause != nil {
				return Wrap(CodeUnavailable, e.cause)
			}
			return Wrap(CodeUnavailable, err)
		}
		return e
	}
	// 请求处理超时（server.handlerTimeout 到点、数据库不回话）是依赖不可用，不是程序错误（D-037）
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(CodeUnavailable, err)
	}
	return Wrap(CodeInternal, err)
}
