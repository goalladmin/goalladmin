// 路由（规范 §3.5）：静态路由 + 登录后由菜单生成的动态路由 + 守卫。

import { createRouter, createWebHistory } from 'vue-router'
import type { Component, App as VueApp } from 'vue'
import type { RouteRecordRaw, Router } from 'vue-router'

import { useAuthStore } from '../auth/store'
import { firstMenuPath, menusToRoutes, normalizeViews } from './menu'
import type { ViewGlob } from './menu'

export const RouteNames = {
  root: 'ga-root',
  login: 'ga-login',
  changePassword: 'ga-change-password',
  profile: 'ga-profile',
  forbidden: 'ga-403',
  notFound: 'ga-404',
} as const

export interface PortalRouterOptions {
  views: ViewGlob
  layout: Component
  pages: {
    login: Component
    changePassword: Component
    profile: Component
    forbidden: Component
    notFound: Component
  }
  /** 业务方追加的静态路由（挂在根级，不走布局）。 */
  extraRoutes?: RouteRecordRaw[]
  /** 部署的子路径。 */
  base?: string
}

declare module 'vue-router' {
  interface RouteMeta {
    /** 不需要登录。 */
    public?: boolean
    titleKey?: string
    titles?: Record<string, string>
    icon?: string
    keepAlive?: boolean
    hidden?: boolean
    menu?: boolean
    missing?: boolean
    crumbs?: { name: string; titleKey: string; titles?: Record<string, string>; icon?: string; path: string }[]
  }
}

export interface PortalRouter {
  router: Router
  /** 用当前登录用户的菜单重建动态路由。登录、启动恢复、重新拉 /auth/me 之后调用。 */
  installMenuRoutes(): void
  install(app: VueApp): void
}

export function createPortalRouter(opts: PortalRouterOptions): PortalRouter {
  const views = normalizeViews(opts.views)
  const routes: RouteRecordRaw[] = [
    { path: '/login', name: RouteNames.login, component: opts.pages.login, meta: { public: true, titleKey: 'shell.login.title' } },
    { path: '/change-password', name: RouteNames.changePassword, component: opts.pages.changePassword, meta: { titleKey: 'shell.pwd.title' } },
    {
      path: '/',
      name: RouteNames.root,
      component: opts.layout,
      children: [
        { path: 'profile', name: RouteNames.profile, component: opts.pages.profile, meta: { titleKey: 'shell.profile.title' } },
        { path: '403', name: RouteNames.forbidden, component: opts.pages.forbidden, meta: { titleKey: 'shell.error.forbidden' } },
        { path: ':pathMatch(.*)*', name: RouteNames.notFound, component: opts.pages.notFound, meta: { titleKey: 'shell.error.notFound' } },
      ],
    },
    ...(opts.extraRoutes ?? []),
  ]
  const router = createRouter({ history: createWebHistory(opts.base ?? '/'), routes })

  let installed: string[] = []
  function installMenuRoutes() {
    for (const name of installed) router.removeRoute(name)
    installed = []
    const auth = useAuthStore()
    const menuRoutes = menusToRoutes(auth.menus, {
      views,
      notFound: opts.pages.notFound,
      onMissing: (n) => console.warn(`[ga] 菜单 ${n.name} 的组件 "${n.component}" 不在 views 映射表里，将渲染 404`),
    })
    for (const r of menuRoutes) {
      router.addRoute(RouteNames.root, r)
      installed.push(String(r.name))
    }
  }

  router.beforeEach(async (to) => {
    const auth = useAuthStore()
    if (!auth.ready) {
      await auth.bootstrap()
      if (auth.authenticated) installMenuRoutes()
      // 动态路由刚加进来，重新解析一次目标地址
      return { path: to.path, query: to.query, hash: to.hash, replace: true }
    }
    if (to.meta.public) {
      if (auth.authenticated && to.name === RouteNames.login) return { path: '/' }
      return true
    }
    if (!auth.authenticated) {
      return { name: RouteNames.login, query: to.fullPath !== '/' ? { redirect: to.fullPath } : {} }
    }
    if (auth.mustChangePwd) {
      return to.name === RouteNames.changePassword ? true : { name: RouteNames.changePassword }
    }
    if (to.name === RouteNames.changePassword) return true
    // 登录后的默认落点：第一个菜单页
    if (to.name === RouteNames.root || (to.path === '/' && to.name === RouteNames.notFound)) {
      const first = firstMenuPath(auth.menus)
      return first ? { path: first, replace: true } : { name: RouteNames.profile, replace: true }
    }
    return true
  })

  return {
    router,
    installMenuRoutes,
    install(app) {
      app.use(router)
    },
  }
}
