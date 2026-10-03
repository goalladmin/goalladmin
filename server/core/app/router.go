package app

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// Guard 是路由注册时的必填参数，由 core/rbac 的 Public / AuthOnly / Require / RequireSuper 提供；
// 注册路由时只接受 rbac.Guard，别的实现会让启动失败。真正的中间件由 GuardResolver 按端构造，Guard 本身只是声明。
type Guard interface {
	Kind() rbac.GuardKind
	Perm() string
}

// GuardResolver 为某端的某个守卫构造中间件链。由认证、授权模块安装。
type GuardResolver func(portal string, g Guard) ([]gin.HandlerFunc, error)

// Route 是一条端内路由的登记信息，供测试、docs/api.md 一致性检查和操作日志使用。
type Route struct {
	Portal string
	Method string
	Path   string // 完整路径，含 /api/<portal>/v1 前缀
	Guard  rbac.GuardKind
	Perm   string
	OpName string // 操作日志的动作名；空表示不记
	// LiveAuth 表示这条路由的认证按库核对会话和账号状态、不用状态缓存（D-073）。公开路由不认证，恒为 false
	LiveAuth bool
	extra    []gin.HandlerFunc
}

// RouteOption 修饰一条路由：附加中间件、设置操作日志名。
type RouteOption func(*Route)

// WithMiddleware 给单条路由附加中间件，排在守卫之后、handler 之前。
func WithMiddleware(mw gin.HandlerFunc) RouteOption {
	return func(r *Route) { r.extra = append(r.extra, mw) }
}

// WithOpName 标记这条路由的操作日志动作名。core/oplog.Record 用它。
func WithOpName(name string) RouteOption {
	return func(r *Route) { r.OpName = name }
}

// RawRoute 是不属于任何端的路由（健康检查、对外网关的自定义验签接口等）。
type RawRoute struct {
	Method  string
	Path    string
	Purpose string
}

// Router 是模块注册路由的入口。
type Router struct {
	app *App
}

// Raw 把路由直接挂在根上，不经过任何端的认证和授权。purpose 必填，启动时打印清单。
func (r *Router) Raw(method, path, purpose string, handlers ...gin.HandlerFunc) {
	if strings.TrimSpace(purpose) == "" {
		panic(fmt.Sprintf("app: Raw 路由 %s %s 必须写明用途", method, path))
	}
	if len(handlers) == 0 {
		panic(fmt.Sprintf("app: Raw 路由 %s %s 没有 handler", method, path))
	}
	r.app.engine.Handle(method, path, handlers...)
	r.app.rawRoutes = append(r.app.rawRoutes, RawRoute{Method: method, Path: path, Purpose: purpose})
}

// Portal 返回某个端的路由器；端必须已注册，否则 panic（这是装配期错误，越早暴露越好）。
func (r *Router) Portal(code string) *PortalRouter {
	g, ok := r.app.portalGroups[code]
	if !ok {
		panic(fmt.Sprintf("app: 端 %q 未注册，不能在它下面注册路由", code))
	}
	return &PortalRouter{app: r.app, portal: code, group: g, prefix: PortalPrefix(code)}
}

// PortalPrefix 返回端的路由前缀：/api/<code>/v1。
func PortalPrefix(code string) string { return "/api/" + code + "/v1" }

// PortalRouter 在某个端下注册路由，每条路由都必须带守卫。
type PortalRouter struct {
	app    *App
	portal string
	group  *gin.RouterGroup
	prefix string
	live   bool // 这个分组下的路由按库核对（LiveAuth）
}

// Group 建子分组。mws 是分组级中间件，排在守卫之前。子分组继承 LiveAuth。
func (p *PortalRouter) Group(rel string, mws ...gin.HandlerFunc) *PortalRouter {
	return &PortalRouter{app: p.app, portal: p.portal, group: p.group.Group(rel, mws...), prefix: joinPath(p.prefix, rel), live: p.live}
}

// LiveAuth 返回同一个分组的另一个注册入口：用它注册的路由（包括它的子分组），认证时按库核对会话和账号状态，
// 不用状态缓存（D-073）。代价是每个请求多几条主键查询，换来的是别的程序在库里吊销会话、停用账号或主体、
// 更换主账号之后，这些路由在下一个请求就按新状态处理。只管认证这一步：角色和权限码仍按授权快照判断，
// IP 名单仍按它自己的快照。用在只有少数人看的敏感内容上（主体端的后台）；量大的业务接口不要标。
func (p *PortalRouter) LiveAuth() *PortalRouter {
	cp := *p
	cp.live = true
	return &cp
}

// Handle 注册一条路由。守卫是必填参数，漏写编译不过。
func (p *PortalRouter) Handle(method, rel string, g Guard, h gin.HandlerFunc, opts ...RouteOption) {
	if g == nil {
		panic(fmt.Sprintf("app: 路由 %s %s 没有守卫", method, joinPath(p.prefix, rel)))
	}
	// 守卫只能来自 core/rbac 的 Public / AuthOnly / Require / RequireSuper：自己实现 Guard 接口、
	// 让 Kind() 返回 public，就能开出不在 docs/api.md 公开清单里的接口
	if _, ok := g.(rbac.Guard); !ok {
		panic(fmt.Sprintf("app: 路由 %s %s 的守卫不是 core/rbac 提供的（%T）", method, joinPath(p.prefix, rel), g))
	}
	if h == nil {
		panic(fmt.Sprintf("app: 路由 %s %s 没有 handler", method, joinPath(p.prefix, rel)))
	}
	if g.Kind() == rbac.GuardRequire && g.Perm() == "" {
		panic(fmt.Sprintf("app: 路由 %s %s 的 Require 守卫没有权限码", method, joinPath(p.prefix, rel)))
	}
	rt := Route{Portal: p.portal, Method: method, Path: joinPath(p.prefix, rel), Guard: g.Kind(), Perm: g.Perm(),
		LiveAuth: p.live && g.Kind() != rbac.GuardPublic}
	for _, o := range opts {
		o(&rt)
	}
	if p.app.guardResolver == nil {
		panic("app: 没有安装 GuardResolver，端内路由无法注册（认证模块未装配？）")
	}
	chain, err := p.app.guardResolver(p.portal, g)
	if err != nil {
		panic(fmt.Sprintf("app: 路由 %s %s 的守卫无法解析: %v", method, rt.Path, err))
	}
	handlers := make([]gin.HandlerFunc, 0, len(chain)+len(rt.extra)+2)
	if rt.LiveAuth {
		// 标记排在守卫链（认证）前面，跟着这条路由自己的处理链走；守卫链已经解析出来了，这个端的认证器一定在
		handlers = append(handlers, p.app.authenticators[p.portal].LiveAuth())
	}
	handlers = append(handlers, chain...)
	handlers = append(handlers, rt.extra...)
	handlers = append(handlers, h)
	p.group.Handle(method, rel, handlers...)
	p.app.routes = append(p.app.routes, rt)
}

// GET 注册 GET 路由。
func (p *PortalRouter) GET(rel string, g Guard, h gin.HandlerFunc, opts ...RouteOption) {
	p.Handle(http.MethodGet, rel, g, h, opts...)
}

// POST 注册 POST 路由。
func (p *PortalRouter) POST(rel string, g Guard, h gin.HandlerFunc, opts ...RouteOption) {
	p.Handle(http.MethodPost, rel, g, h, opts...)
}

// PUT 注册 PUT 路由。
func (p *PortalRouter) PUT(rel string, g Guard, h gin.HandlerFunc, opts ...RouteOption) {
	p.Handle(http.MethodPut, rel, g, h, opts...)
}

// PATCH 注册 PATCH 路由。
func (p *PortalRouter) PATCH(rel string, g Guard, h gin.HandlerFunc, opts ...RouteOption) {
	p.Handle(http.MethodPatch, rel, g, h, opts...)
}

// DELETE 注册 DELETE 路由。
func (p *PortalRouter) DELETE(rel string, g Guard, h gin.HandlerFunc, opts ...RouteOption) {
	p.Handle(http.MethodDelete, rel, g, h, opts...)
}

// Routes 返回全部端内路由，按端、路径、方法排序。
func (a *App) Routes() []Route {
	out := make([]Route, len(a.routes))
	copy(out, a.routes)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Portal != out[j].Portal {
			return out[i].Portal < out[j].Portal
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// RawRoutes 返回全部 Raw 路由。
func (a *App) RawRoutes() []RawRoute {
	out := make([]RawRoute, len(a.rawRoutes))
	copy(out, a.rawRoutes)
	return out
}

func joinPath(prefix, rel string) string {
	if rel == "" || rel == "/" {
		return prefix
	}
	return strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(rel, "/")
}
