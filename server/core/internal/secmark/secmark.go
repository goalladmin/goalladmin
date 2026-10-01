// Package secmark 让框架各处在请求上标记"这是一个安全事件"（D-032）。
//
// 标记只放在 gin.Context 里；请求结束后由审计中间件统一记录。每个请求最多一个标记，后标的覆盖先标的。
package secmark

import "github.com/gin-gonic/gin"

const key = "ga.security_mark"

// 框架识别的安全事件类型（D-032 第 4 条）。
const (
	Forbidden          = "forbidden"            // 越权被拒；Detail 是权限码
	CrossPortal        = "cross_portal"         // 拿别的端的身份访问本端
	TokenInvalid       = "token_invalid"        // 签名或格式不对的令牌（过期不算）
	TokenMismatch      = "token_mismatch"       // 签名有效但会话不存在或和用户对不上：密钥可能泄露
	SessionRevoked     = "session_revoked"      // 已吊销、已过期的会话的令牌仍在使用
	RefreshReuse       = "refresh_reuse"        // 刷新凭证重放：凭证可能被盗，会话已吊销
	RefreshMismatch    = "refresh_mismatch"     // 刷新凭证和会话对不上：有人拿着会话号在试，会话未吊销（D-049）
	RefreshCookieDup   = "refresh_cookie_dup"   // 刷新时带了多个同名的刷新 Cookie：有别的主机往浏览器里塞了一个（D-058）
	BadOrigin          = "bad_origin"           // 登录、刷新、登出的客户端标识或来源不对
	LoginLocked        = "login_locked"         // 登录时账号或 IP 处于锁定
	LoginRateLimited   = "login_rate_limited"   // 登录请求过于频繁
	UnlockExhausted    = "unlock_exhausted"     // 锁屏解锁次数用完，会话被吊销
	PwdChangeThrottled = "pwd_change_throttled" //nolint:gosec // 事件类型名，不是凭据。改密时核对旧密码次数用完：拿着令牌在试密码（D-055）
	CLI                = "cli"                  // 命令行操作（创建管理员、清理策略）
)

// Mark 是一个安全事件的标记。身份字段为空时由审计中间件从请求的当前身份补上。
type Mark struct {
	Kind      string
	Detail    string
	UserID    uint64
	Username  string
	SessionID string
}

// Set 标记当前请求。
func Set(c *gin.Context, m Mark) { c.Set(key, m) }

// Get 取当前请求的标记。
func Get(c *gin.Context) (Mark, bool) {
	v, ok := c.Get(key)
	if !ok {
		return Mark{}, false
	}
	m, ok := v.(Mark)
	return m, ok
}
