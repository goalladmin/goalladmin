// @ga/shell 的公开 API。业务页面只能从 '@ga/shell' 引用（package.json 的 exports 限制了深层路径）。

export { createPortalApp } from './app'
export type { PortalAppOptions } from './app'


export { usePortal, useLanguages, apiPrefix } from './context'
export type { PortalContext } from './context'
import { usePortal } from './context'

export { useAuthStore } from './auth/store'
export type { LoginInput } from './auth/store'

export { hasPerm, vPerm, GaPerm } from './perm'
export type { PermMode, PermValue } from './perm'

export { RouteNames } from './router'
export { resolveIcon, iconNames } from './layout/icons'
export { menusToRoutes, normalizeViews, firstMenuPath, menuTitle, menuIndex } from './router/menu'
export type { ViewGlob, ViewMap, MenuRouteMeta } from './router/menu'

export { useLayoutStore } from './layout/store'

export { default as GaLogo } from './components/GaLogo.vue'
export { default as GaCountUp } from './components/GaCountUp.vue'
export { default as GaSparkline } from './components/GaSparkline.vue'
export { default as GaLiquid } from './components/GaLiquid.vue'
export { default as GaAvatar } from './components/GaAvatar.vue'
export { AVATAR_PRESETS, presetAvatarSrc, parseAvatar, avatarInitial } from './avatar'
export type { AvatarPreset, AvatarRef } from './avatar'
export { prefersReducedMotion, easeOutCubic, countFrame, sparkGeometry } from './motion'
export { formatBytes, formatRelative, splitDuration } from './format'

export { createClient, HeaderClient } from './request/client'
export type { Client, ClientOptions, TokenStore } from './request/client'
export { RefreshGate } from './request/refresh'

export { useTable } from './composables/useTable'

export { useDict, invalidateDicts, createDictLoader, findDictItem, enabledDictItems, dictTagProps } from './dict'
export type { UseDict, DictData, DictItem, DictLoader } from './dict'
export { default as GaDictTag } from './dict/GaDictTag.vue'
export type { UseTable, UseTableOptions } from './composables/useTable'

export { SupportedLocales, AllLocaleCodes, DefaultLocale, detectLocale, persistLocale, matchLocale, fallbackChain, languageInfo, localesFromGlob } from './i18n'
export type { LocaleCode, LocaleMessages, LanguageInfo } from './i18n'
export { describeApiError, formatApiError } from './request/errors'
export { default as GaFlag } from './components/GaFlag.vue'
export { default as GaLanguageSwitch } from './components/GaLanguageSwitch.vue'
export { default as GaI18nInputs } from './components/GaI18nInputs.vue'

export { ApiError, Codes, isApiError } from './types'
export type { Envelope, PageData, PageQuery, FieldError, TokenResponse, MeUser, MenuTree, MeResponse, PwdPolicy } from './types'

// 业务页面常用的第三方入口，统一从这里拿，端应用不必直接依赖 vue-i18n。
export { useI18n } from 'vue-i18n'

/** 当前端的请求客户端。 */
export function useRequest() {
  return usePortal().client
}

/** 把 ISO 时间格式化成本地时间；空值返回空串。 */
export function formatTime(v: string | null | undefined): string {
  if (!v) return ''
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}
