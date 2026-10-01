// 路由守卫：启动恢复（先刷新再拉 /auth/me）、未登录跳登录页、强制改密、登录后默认落点、动态路由。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent } from 'vue'

import { RouteNames, createPortalRouter } from './index'
import { setPortalContext } from '../context'
import type { Client } from '../request/client'
import type { MeResponse } from '../types'

const Stub = (name: string) => defineComponent({ name, render: () => null })
const pages = { login: Stub('Login'), changePassword: Stub('Pwd'), profile: Stub('Profile'), forbidden: Stub('F'), notFound: Stub('NF') }

function fakeClient(opts: { loggedIn: boolean; me?: Partial<MeResponse> }) {
  const me: MeResponse = {
    user: { id: 1, username: 'u', displayName: 'U', avatar: '', mustChangePwd: false, super: false },
    perms: ['system:user:list'],
    menus: [
      {
        name: 'system', path: '/system', titleKey: 'menu.system', keepAlive: false, hidden: false, sort: 900,
        children: [{ name: 'system-user', path: '/system/users', component: 'system/user/index', titleKey: 'menu.system.user', keepAlive: true, hidden: false, sort: 10 }],
      },
    ],
    ...opts.me,
  }
  const client = {
    refresh: vi.fn(async () => {
      if (!opts.loggedIn) throw new Error('401')
      return { accessToken: 't', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: me.user.mustChangePwd }
    }),
    get: vi.fn(async (url: string) => {
      if (url === '/auth/me') return me
      throw new Error('unexpected ' + url)
    }),
    logout: vi.fn(async () => undefined),
  } as unknown as Client
  return client
}

function build(client: Client) {
  const pr = createPortalRouter({
    views: { './views/system/user/index.vue': async () => ({ default: Stub('UserPage') }) },
    layout: Stub('Layout'),
    pages,
  })
  setPortalContext({ portal: 'p', client, router: pr.router, installMenuRoutes: pr.installMenuRoutes })
  return pr
}

describe('router guard', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('未登录：刷新失败后跳登录页并带 redirect', async () => {
    const client = fakeClient({ loggedIn: false })
    const { router } = build(client)
    await router.push('/system/users')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe(RouteNames.login)
    expect(router.currentRoute.value.query.redirect).toBe('/system/users')
    expect(client.refresh).toHaveBeenCalledTimes(1)
    expect(client.get).not.toHaveBeenCalled()
  })

  it('已登录：启动时刷新 + /auth/me，动态路由可用，访问 / 落到第一个菜单', async () => {
    const client = fakeClient({ loggedIn: true })
    const { router } = build(client)
    await router.push('/')
    await router.isReady()
    expect(client.refresh).toHaveBeenCalledTimes(1)
    expect(client.get).toHaveBeenCalledWith('/auth/me')
    expect(router.currentRoute.value.path).toBe('/system/users')
    expect(router.currentRoute.value.name).toBe('system-user')
    expect(router.currentRoute.value.meta.titleKey).toBe('menu.system.user')
    // 已登录再去登录页 → 回首页
    await router.push('/login')
    expect(router.currentRoute.value.path).toBe('/system/users')
    // 未知路径 → 404（布局内）
    await router.push('/nope')
    expect(router.currentRoute.value.name).toBe(RouteNames.notFound)
  })

  it('必须改密：任何页面都被带到改密页', async () => {
    const client = fakeClient({ loggedIn: true, me: { user: { id: 1, username: 'u', displayName: 'U', avatar: '', mustChangePwd: true, super: false } } })
    const { router } = build(client)
    await router.push('/system/users')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe(RouteNames.changePassword)
    await router.push('/profile')
    expect(router.currentRoute.value.name).toBe(RouteNames.changePassword)
  })

  it('没有任何菜单的用户落到个人信息页', async () => {
    const client = fakeClient({ loggedIn: true, me: { menus: [], perms: [] } })
    const { router } = build(client)
    await router.push('/')
    await router.isReady()
    expect(router.currentRoute.value.name).toBe(RouteNames.profile)
  })
})
