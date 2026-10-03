// Package httpx 是业务代码与 HTTP 层打交道的全部入口：
// 统一响应信封、错误类型与错误码、分页参数、参数绑定、请求 ID。
//
// 中间件实现在 core/internal/middleware，业务代码不需要也不能引用。
package httpx

import "net/http"

// 错误码分段（规范 §9.2，协议层取值见 docs/decisions.md D-004）。
//
//	0            成功
//	1000–1999    认证
//	2000–2999    授权
//	3000–3999    参数校验
//	4000–4999    框架内业务错误；4004/4005/4013/4029 是协议层错误
//	5000–5999    系统
//	10000 以上   业务模块自用
const (
	CodeOK = 0

	CodeLoginFailed     = 1001 // 账号或密码错误
	CodeCaptchaRequired = 1002 // 需要验证码
	CodeLocked          = 1003 // 已锁定
	CodeTokenInvalid    = 1004 // 令牌无效、过期、缺失
	CodeRefreshRetry    = 1005 // 刷新竞争，客户端重试一次
	CodeSessionLocked   = 1006 // 会话已锁屏，先解锁（D-027）

	CodeForbidden         = 2001 // 无权限
	CodePwdChangeRequired = 2002 // 必须先修改密码
	CodeIPDenied          = 2003 // 当前 IP 不允许访问：黑名单或白名单（D-062）

	CodeValidation = 3001 // 参数校验失败，data.fields 给出明细
	CodeBadRequest = 3002 // 请求体无法解析

	CodeConflict         = 4001 // 资源冲突（如用户名已存在）
	CodeLastSuper        = 4002 // 不能停用或降级最后一个超管
	CodeDeclaredInCode   = 4101 // 由代码声明的内容，不能在后台修改或删除（D-023）
	CodeNotFound         = 4004 // 路由或资源不存在
	CodeMethodNotAllowed = 4005 // 方法不允许
	CodeBodyTooLarge     = 4013 // 请求体过大
	CodeTooManyRequests  = 4029 // 请求过多

	CodeInternal    = 5000 // 内部错误
	CodeUnavailable = 5003 // 依赖不可用（数据库等）
)

// messages 是错误码的默认文案。业务模块自己的错误码通过 RegisterMessage 注册。
var messages = map[int]map[string]string{
	CodeOK:                {LangZH: "成功", LangEN: "ok"},
	CodeLoginFailed:       {LangZH: "账号或密码错误", LangEN: "invalid username or password"},
	CodeCaptchaRequired:   {LangZH: "请输入验证码", LangEN: "captcha required"},
	CodeLocked:            {LangZH: "登录已被暂时锁定，请稍后再试", LangEN: "login temporarily locked, try again later"},
	CodeTokenInvalid:      {LangZH: "未登录或登录已过期", LangEN: "not authenticated or session expired"},
	CodeRefreshRetry:      {LangZH: "刷新冲突，请重试", LangEN: "refresh conflict, retry once"},
	CodeSessionLocked:     {LangZH: "屏幕已锁定，请先解锁", LangEN: "screen locked; unlock first"},
	CodeForbidden:         {LangZH: "没有权限执行此操作", LangEN: "permission denied"},
	CodePwdChangeRequired: {LangZH: "请先修改密码", LangEN: "password change required"},
	CodeIPDenied:          {LangZH: "当前 IP 不允许访问", LangEN: "access from this IP address is not allowed"},
	CodeValidation:        {LangZH: "参数校验失败", LangEN: "validation failed"},
	CodeBadRequest:        {LangZH: "请求格式错误", LangEN: "malformed request"},
	CodeConflict:          {LangZH: "资源已存在或状态冲突", LangEN: "resource conflict"},
	CodeLastSuper:         {LangZH: "不能停用或降级最后一个超级管理员", LangEN: "cannot disable or demote the last super administrator"},
	CodeDeclaredInCode:    {LangZH: "该内容由代码声明，不能在后台修改或删除", LangEN: "declared in code; cannot be changed or deleted here"},
	CodeNotFound:          {LangZH: "资源不存在", LangEN: "not found"},
	CodeMethodNotAllowed:  {LangZH: "方法不允许", LangEN: "method not allowed"},
	CodeBodyTooLarge:      {LangZH: "请求体过大", LangEN: "request body too large"},
	CodeTooManyRequests:   {LangZH: "请求过于频繁，请稍后再试", LangEN: "too many requests"},
	CodeInternal:          {LangZH: "服务器内部错误", LangEN: "internal server error"},
	CodeUnavailable:       {LangZH: "服务暂时不可用", LangEN: "service unavailable"},
}

// RegisterMessage 注册（或覆盖）一个错误码的中英文文案。业务模块在 Init 里调用，用 10000 以上的码。
// 其他语言用 RegisterMessages；没有登记的语言按 FallbackLangs 回退。
func RegisterMessage(code int, zh, en string) {
	RegisterMessages(code, map[string]string{LangZH: zh, LangEN: en})
}

// RegisterMessages 注册（或覆盖）一个错误码在若干语言下的文案，键是 Langs() 里的语言代码。
func RegisterMessages(code int, byLang map[string]string) {
	m := map[string]string{}
	for k, v := range messages[code] {
		m[k] = v
	}
	for k, v := range byLang {
		m[k] = v
	}
	messages[code] = m
}

// Message 返回错误码在指定语言下的文案：先找该语言，再按 FallbackLangs 回退，都没有时用 HTTP 状态说明。
func Message(code int, lang string) string {
	m := messages[code]
	extra := langMessages(lang)
	for _, l := range append([]string{lang}, FallbackLangs(lang)...) {
		if s := m[l]; s != "" {
			return s
		}
		if l == lang {
			if s := extra[code]; s != "" {
				return s
			}
		}
	}
	return http.StatusText(StatusOf(code))
}

// StatusOf 返回错误码默认对应的 HTTP 状态（规范 §9.2）。
func StatusOf(code int) int {
	switch {
	case code == CodeOK:
		return http.StatusOK
	case code == CodeTokenInvalid:
		return http.StatusUnauthorized
	case code == CodeRefreshRetry:
		return http.StatusConflict
	case code == CodeSessionLocked:
		return http.StatusLocked
	case code >= 1000 && code < 2000:
		return http.StatusOK // 登录失败等业务性失败
	case code == CodeForbidden, code == CodePwdChangeRequired, code == CodeIPDenied:
		return http.StatusForbidden
	case code >= 2000 && code < 3000:
		return http.StatusForbidden
	case code == CodeBadRequest:
		return http.StatusBadRequest
	case code >= 3000 && code < 4000:
		return http.StatusOK // 参数校验失败按业务性失败处理
	case code == CodeNotFound:
		return http.StatusNotFound
	case code == CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case code == CodeBodyTooLarge:
		return http.StatusRequestEntityTooLarge
	case code == CodeTooManyRequests:
		return http.StatusTooManyRequests
	case code >= 4000 && code < 5000:
		return http.StatusOK
	case code == CodeUnavailable:
		return http.StatusServiceUnavailable
	case code >= 5000 && code < 6000:
		return http.StatusInternalServerError
	default:
		return http.StatusOK // 业务模块的错误码
	}
}
