// 规范 §13.3：菜单转路由、组件键白名单。

import { describe, expect, it, vi } from 'vitest'

import { firstMenuPath, menuIndex, menuTitle, menusToRoutes, normalizeViews } from './menu'
import type { MenuTree } from '../types'

const NotFound = { name: 'NotFound' }

const views = normalizeViews({
  './views/system/user/index.vue': async () => 'user',
  './views/system/role/index.vue': async () => 'role',
  '/src/views/order/list/index.vue': async () => 'order',
})

const menus: MenuTree[] = [
  {
    name: 'system', path: '/system', titleKey: 'menu.system', icon: 'Setting', keepAlive: false, hidden: false, sort: 900,
    children: [
      { name: 'system-role', path: '/system/roles', component: 'system/role/index', titleKey: 'menu.system.role', keepAlive: true, hidden: false, sort: 20 },
      { name: 'system-user', path: '/system/users', component: 'system/user/index', titleKey: 'menu.system.user', keepAlive: true, hidden: false, sort: 10 },
      { name: 'system-user-detail', path: '/system/users/:id', component: 'system/user/detail', titleKey: 'menu.system.userDetail', keepAlive: false, hidden: true, sort: 11 },
    ],
  },
  {
    name: 'order', path: '/order', titleKey: 'menu.order', keepAlive: false, hidden: false, sort: 100,
    children: [{ name: 'order-list', path: '/order/list', component: 'order/list/index', titleKey: 'menu.order.list', keepAlive: true, hidden: false, sort: 10 }],
  },
  { name: 'empty-dir', path: '/empty', titleKey: 'menu.empty', keepAlive: false, hidden: false, sort: 1 },
]

describe('normalizeViews', () => {
  it('把 glob 的键规范化成相对 views/ 的路径', () => {
    expect([...views.keys()].sort()).toEqual(['order/list/index', 'system/role/index', 'system/user/index'])
  })
})

describe('menusToRoutes', () => {
  it('展开成扁平路由，按 sort 排序，带祖先链和 meta', () => {
    const routes = menusToRoutes(menus, { views, notFound: NotFound })
    expect(routes.map((r) => r.name)).toEqual(['order-list', 'system-user', 'system-user-detail', 'system-role'])
    const user = routes.find((r) => r.name === 'system-user')!
    expect(user.path).toBe('/system/users')
    expect(user.meta).toMatchObject({ titleKey: 'menu.system.user', keepAlive: true, hidden: false, menu: true, missing: false })
    expect(user.meta?.crumbs).toEqual([{ name: 'system', titleKey: 'menu.system', icon: 'Setting', path: '/system' }])
    expect(user.component).toBe(views.get('system/user/index'))
    // 目录节点不产生路由
    expect(routes.find((r) => r.name === 'system')).toBeUndefined()
    expect(routes.find((r) => r.name === 'empty-dir')).toBeUndefined()
  })

  it('组件键不在映射表里时渲染 404，且回调告警；绝不拼接路径动态 import', () => {
    const onMissing = vi.fn()
    const routes = menusToRoutes(menus, { views, notFound: NotFound, onMissing })
    const detail = routes.find((r) => r.name === 'system-user-detail')!
    expect(detail.component).toBe(NotFound)
    expect(detail.meta?.missing).toBe(true)
    expect(onMissing).toHaveBeenCalledTimes(1)
    expect(onMissing.mock.calls[0]?.[0]?.component).toBe('system/user/detail')
  })

  it('路径穿越、绝对路径这类键一律不命中', () => {
    const evil: MenuTree[] = [
      { name: 'e1', path: '/e1', component: '../../main', titleKey: 'x', keepAlive: false, hidden: false, sort: 1 },
      { name: 'e2', path: '/e2', component: '/etc/passwd', titleKey: 'x', keepAlive: false, hidden: false, sort: 2 },
      { name: 'e3', path: '/e3', component: 'system/user/index.vue', titleKey: 'x', keepAlive: false, hidden: false, sort: 3 },
    ]
    const routes = menusToRoutes(evil, { views, notFound: NotFound })
    expect(routes.every((r) => r.component === NotFound)).toBe(true)
  })
})

describe('firstMenuPath', () => {
  it('返回按 sort 排序后第一个可见页面', () => {
    expect(firstMenuPath(menus)).toBe('/order/list')
    expect(firstMenuPath([])).toBeNull()
  })
})

describe('菜单管理里的调整（D-025）', () => {
  const grouped: MenuTree[] = [
    {
      name: '@g-0123456789ab', path: '', titleKey: '', titles: { 'zh-CN': '常用' }, keepAlive: false, hidden: false, sort: 1,
      children: [
        { name: 'system-user', path: '/system/users', component: 'system/user/index', titleKey: 'menu.system.user', titles: { 'zh-CN': '账号' }, keepAlive: true, hidden: false, sort: 10 },
      ],
    },
  ]

  it('分组不产生路由；显示名随路由和面包屑下发，路径不变', () => {
    const routes = menusToRoutes(grouped, { views, notFound: NotFound })
    expect(routes.map((r) => r.name)).toEqual(['system-user'])
    const user = routes[0]!
    expect(user.path).toBe('/system/users')
    expect(user.meta?.titles).toEqual({ 'zh-CN': '账号' })
    expect(user.meta?.crumbs).toEqual([{ name: '@g-0123456789ab', titleKey: '', titles: { 'zh-CN': '常用' }, path: '' }])
  })

  it('显示名：当前语言改过就用它，否则翻译 titleKey；分组缺当前语言时退回中文', () => {
    const t = (k: string) => `T(${k})`
    expect(menuTitle(t, 'zh-CN', 'menu.system.user', { 'zh-CN': '账号' })).toBe('账号')
    expect(menuTitle(t, 'en-US', 'menu.system.user', { 'zh-CN': '账号' })).toBe('T(menu.system.user)')
    expect(menuTitle(t, 'en-US', 'menu.system.user')).toBe('T(menu.system.user)')
    expect(menuTitle(t, 'en-US', '', { 'zh-CN': '常用' })).toBe('常用')
    expect(menuTitle(t, 'en-US', '', { 'en-US': 'Fav', 'zh-CN': '常用' })).toBe('Fav')
    expect(menuTitle(t, 'en-US', '')).toBe('')
  })

  it('没有路径的分组用名字做菜单索引，不会和页面路径撞', () => {
    expect(menuIndex({ name: '@g-0123456789ab', path: '' })).toBe('menu:@g-0123456789ab')
    expect(menuIndex({ name: 'system', path: '/system' })).toBe('/system')
  })
})
