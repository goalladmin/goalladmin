package rbac

import (
	"context"

	"github.com/gin-gonic/gin"
)

// GuardKind 是路由守卫的档位。
type GuardKind string

const (
	GuardPublic   GuardKind = "public" // 不需要登录；必须出现在 docs/api.md 的公开清单里
	GuardAuthOnly GuardKind = "auth"   // 登录即可
	GuardRequire  GuardKind = "perm"   // 需要权限码
	GuardSuper    GuardKind = "super"  // 只有本端超管（D-035）
)

// Guard 是路由注册时的必填参数：只是声明，真正的中间件由 app 按端构造。
type Guard struct {
	kind GuardKind
	perm string
}

// Kind 返回档位。
func (g Guard) Kind() GuardKind { return g.kind }

// Perm 返回所需权限码（仅 Require 有）。
func (g Guard) Perm() string { return g.perm }

// Public 声明不需要登录的路由。用得越少越好。
func Public() Guard { return Guard{kind: GuardPublic} }

// AuthOnly 声明登录即可访问的路由：个人信息、下拉选项这类对所有登录用户开放的接口。
func AuthOnly() Guard { return Guard{kind: GuardAuthOnly} }

// Require 声明需要指定权限码的路由。这是默认应使用的档位。
func Require(code string) Guard { return Guard{kind: GuardRequire, perm: code} }

// RequireSuper 声明只有本端超管能访问的路由（D-035）：没有对应的权限码，也就不能授给任何角色。
// 只用于"能接管别人账号"这类不该下放的操作，例如重置他人密码。
func RequireSuper() Guard { return Guard{kind: GuardSuper} }

// routeGuard 是本次请求的路由守卫要求，由 Require / RequireSuper 中间件放进请求的 ctx（D-048）。
// 锁内认定操作人（loadActor，WithActor 等都经过它）时按已提交的授权再核一遍，
// 通过守卫之后、拿到锁之前权限被收回的写操作就不会执行。
type routeGuard struct {
	super bool
	perm  string
}

type routeGuardKey struct{}

func setRouteGuard(c *gin.Context, g routeGuard) {
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), routeGuardKey{}, g))
}
