// 端上下文：一个页面只装配一个端，createPortalApp 建好后放在这里，store 和组件按需取用。
// 测试通过 setPortalContext 注入假的上下文。

import type { Router } from 'vue-router'

import type { Client } from './request/client'
import { SupportedLocales } from './i18n/languages'
import type { LanguageInfo, LocaleCode } from './i18n/languages'

export interface PortalContext {
  /** 端编码，例如 platform。 */
  portal: string
  /** 该端的请求客户端，baseURL 为 /api/<portal>/v1。 */
  client: Client
  router?: Router
  /** 用当前用户的菜单重建动态路由（登录、改密之后调用）。 */
  installMenuRoutes?: () => void
  /** 端启用的界面语言（D-026）；不设时为全部支持的语言。 */
  languages?: readonly LocaleCode[]
  /** 读上传头像的接口路径（相对端的接口前缀，D-040），默认 /system/avatars。 */
  avatarPath?: string
}

let current: PortalContext | null = null

export function setPortalContext(ctx: PortalContext | null): void {
  current = ctx
}

export function usePortal(): PortalContext {
  if (!current) {
    throw new Error('@ga/shell: 端尚未装配（createPortalApp 还没调用）')
  }
  return current
}

/** 端启用的界面语言（按选择器的顺序）。端还没装配时返回全部支持的语言。 */
export function useLanguages(): LanguageInfo[] {
  const enabled = current?.languages
  return SupportedLocales.filter((l) => !enabled || enabled.includes(l.code))
}

/** 端的接口前缀。 */
export function apiPrefix(portal: string): string {
  return `/api/${portal}/v1`
}

/** 读上传头像的接口路径（D-040）。端还没装配时用默认值。 */
export function avatarPath(): string {
  return current?.avatarPath ?? '/system/avatars'
}
