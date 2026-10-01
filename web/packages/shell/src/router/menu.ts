// 菜单转路由（规范 §6.6）：
//   - Component 是相对 apps/<端>/views/ 的路径，用 import.meta.glob 的映射表解析；
//   - 映射表里没有的键一律渲染 404，禁止拼接路径动态 import；
//   - 目录节点不产生路由，只用于侧边栏和面包屑。

import type { Component } from 'vue'
import type { RouteRecordRaw } from 'vue-router'

import type { MenuTree } from '../types'

/** import.meta.glob('./views/**\/*.vue') 的返回值。 */
export type ViewGlob = Record<string, () => Promise<unknown>>

/** 规范化后的组件映射：键形如 system/user/index。 */
export type ViewMap = Map<string, () => Promise<unknown>>

/** 面包屑用的祖先信息。 */
export interface MenuCrumb {
  name: string
  titleKey: string
  titles?: Record<string, string>
  icon?: string
  path: string
}

export interface MenuRouteMeta {
  [key: string | symbol]: unknown
  titleKey: string
  titles?: Record<string, string>
  icon?: string
  keepAlive: boolean
  hidden: boolean
  /** 从顶级到父级的祖先链。 */
  crumbs: MenuCrumb[]
  /** 标记为菜单路由，登录态变化时整体替换。 */
  menu: true
  /** 组件键没有命中映射表时为 true（渲染 404）。 */
  missing?: boolean
}

/**
 * 把 glob 结果的键规范化：去掉 /views/ 之前的部分和 .vue 后缀。
 * './views/system/user/index.vue' → 'system/user/index'
 * '/src/views/system/user/index.vue' → 'system/user/index'
 */
export function normalizeViews(glob: ViewGlob): ViewMap {
  const out: ViewMap = new Map()
  for (const [raw, loader] of Object.entries(glob)) {
    const idx = raw.lastIndexOf('/views/')
    const rel = idx >= 0 ? raw.slice(idx + '/views/'.length) : raw.replace(/^\.\//, '')
    out.set(rel.replace(/\.vue$/, ''), loader)
  }
  return out
}

export interface MenusToRoutesOptions {
  views: ViewMap
  /** 组件键不在映射表里时渲染的组件（404）。 */
  notFound: Component
  /** 找不到组件键时的回调（开发期打警告）。 */
  onMissing?: (node: MenuTree) => void
}

/** 深度优先展开菜单树，每个带 component 的节点变成一条扁平路由（都挂在布局根下）。 */
export function menusToRoutes(menus: MenuTree[], opts: MenusToRoutesOptions): RouteRecordRaw[] {
  const out: RouteRecordRaw[] = []
  const walk = (nodes: MenuTree[], crumbs: MenuCrumb[]) => {
    for (const n of [...nodes].sort((a, b) => a.sort - b.sort)) {
      if (n.component) {
        const loader = opts.views.get(n.component)
        if (!loader) opts.onMissing?.(n)
        const meta: MenuRouteMeta = {
          titleKey: n.titleKey,
          titles: n.titles,
          icon: n.icon,
          keepAlive: n.keepAlive,
          hidden: n.hidden,
          crumbs,
          menu: true,
          missing: !loader,
        }
        out.push({
          path: n.path,
          name: n.name,
          component: loader ?? opts.notFound,
          meta,
        })
      }
      if (n.children?.length) {
        walk(n.children, [...crumbs, { name: n.name, titleKey: n.titleKey, titles: n.titles, icon: n.icon, path: n.path }])
      }
    }
  }
  walk(menus, [])
  return out
}

/**
 * 菜单的显示名（D-025）：菜单管理里改过当前语言的显示名就用它；否则翻译 titleKey；
 * 后台分组没有 titleKey，缺当前语言时退回中文名或任一语言。结果一律按纯文本渲染。
 */
export function menuTitle(t: (key: string) => string, locale: string, titleKey: string, titles?: Record<string, string>): string {
  const own = titles?.[locale]
  if (own) return own
  if (titleKey) return t(titleKey)
  if (!titles) return ''
  return titles['zh-CN'] ?? Object.values(titles)[0] ?? ''
}

/** 侧边栏和 el-menu 用的索引：页面和目录用路径，分组没有路径时用名字。 */
export function menuIndex(node: { name: string; path: string }): string {
  return node.path || `menu:${node.name}`
}

/** 第一个可见的页面路径，作为登录后的默认落点。 */
export function firstMenuPath(menus: MenuTree[]): string | null {
  for (const n of [...menus].sort((a, b) => a.sort - b.sort)) {
    if (n.component && !n.hidden) return n.path
    if (n.children?.length) {
      const p = firstMenuPath(n.children)
      if (p) return p
    }
  }
  return null
}
