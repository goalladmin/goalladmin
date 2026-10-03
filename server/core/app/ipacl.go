package app

import (
	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/secmark"
)

// ipDenyMiddleware 是 IP 黑名单（D-062）：对每个请求生效（登录、验证码、刷新都算），健康检查除外。
// 被拒回 403、错误码 2003，记安全事件 ip_denied（同一来源一分钟合并成一行）。
func (a *App) ipDenyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		acl := a.deps.IPACL
		if acl == nil || c.Request.URL.Path == healthPath || c.Request.URL.Path == readyPath {
			c.Next()
			return
		}
		if acl.Blocked(c.Request.Context(), c.ClientIP()) {
			secmark.Set(c, secmark.Mark{Kind: secmark.IPDenied, Detail: "deny"})
			httpx.Fail(c, httpx.ErrIPDenied)
			return
		}
		c.Next()
	}
}

// ipPortalMiddleware 是端白名单（D-062）：挂在端的路由组上，这个端的每个接口（含不需要登录的）都查。名单为空不限制。
func (a *App) ipPortalMiddleware(portalCode string) gin.HandlerFunc {
	return func(c *gin.Context) {
		acl := a.deps.IPACL
		if acl != nil && !acl.PortalAllows(c.Request.Context(), portalCode, c.ClientIP()) {
			secmark.Set(c, secmark.Mark{Kind: secmark.IPDenied, Detail: "portal"})
			httpx.Fail(c, httpx.ErrIPDenied)
			return
		}
		c.Next()
	}
}
