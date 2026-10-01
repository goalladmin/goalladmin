// 国际化：壳自带 11 种语言的文案（D-026），各端把自己的（菜单标题、权限名、页面文案）合并进来。

import { createI18n } from 'vue-i18n'

import bnBD from './bn-BD'
import deDE from './de-DE'
import enUS from './en-US'
import frFR from './fr-FR'
import jaJP from './ja-JP'
import koKR from './ko-KR'
import msMY from './ms-MY'
import ruRU from './ru-RU'
import taIN from './ta-IN'
import zhCN from './zh-CN'
import zhTW from './zh-TW'
import { AllLocaleCodes, DefaultLocale, fallbackChain, isLocaleCode, matchLocale } from './languages'
import type { LocaleCode } from './languages'

export { SupportedLocales, DefaultLocale, AllLocaleCodes, fallbackChain, matchLocale, isLocaleCode, languageInfo } from './languages'
export type { LocaleCode, LanguageInfo } from './languages'

/** 壳自带的文案，按语言。 */
const shellMessages: Record<LocaleCode, Record<string, unknown>> = {
  'en-US': enUS,
  'zh-CN': zhCN,
  'zh-TW': zhTW,
  'ja-JP': jaJP,
  'ko-KR': koKR,
  'ms-MY': msMY,
  'ta-IN': taIN,
  'bn-BD': bnBD,
  'ru-RU': ruRU,
  'fr-FR': frFR,
  'de-DE': deDE,
}

/** 各端传入的文案：{ 'zh-CN': {...}, 'en-US': {...}, ... }，键与壳的合并（深合并，端的优先）。 */
export type LocaleMessages = Partial<Record<LocaleCode, Record<string, unknown>>>

/**
 * 从 import.meta.glob('./locales/*.ts', { eager: true }) 的结果得到按语言的文案：文件名就是语言代码（zh-CN.ts、ja-JP.ts），
 * 不认识的文件名忽略。这样加一种语言只需要加一个文件。
 */
export function localesFromGlob(glob: Record<string, unknown>): LocaleMessages {
  const out: LocaleMessages = {}
  for (const [path, mod] of Object.entries(glob)) {
    const code = path.split('/').pop()?.replace(/\.ts$/, '')
    if (isLocaleCode(code)) out[code] = (mod as { default?: Record<string, unknown> }).default ?? {}
  }
  return out
}

const STORAGE_KEY = 'ga.locale'

/** 选语言：先看用户上次选的，再按浏览器语言匹配启用的语言，都不行用默认语言（不在启用列表里时用第一个启用的）。 */
export function detectLocale(enabled: readonly LocaleCode[] = AllLocaleCodes): LocaleCode {
  try {
    const saved = localStorage.getItem(STORAGE_KEY)
    if (isLocaleCode(saved) && enabled.includes(saved)) return saved
  } catch {
    // 隐私模式等情况下 localStorage 不可用
  }
  const tags = typeof navigator !== 'undefined' ? [...(navigator.languages ?? []), navigator.language] : []
  for (const tag of tags) {
    if (!tag) continue
    const m = matchLocale(tag, enabled)
    if (m) return m
  }
  return enabled.includes(DefaultLocale) ? DefaultLocale : (enabled[0] ?? DefaultLocale)
}

export function persistLocale(code: LocaleCode): void {
  try {
    localStorage.setItem(STORAGE_KEY, code)
  } catch {
    // 忽略
  }
}

function deepMerge(base: Record<string, unknown>, extra: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...base }
  for (const [k, v] of Object.entries(extra)) {
    const cur = out[k]
    if (isPlainObject(cur) && isPlainObject(v)) {
      out[k] = deepMerge(cur, v)
    } else {
      out[k] = v
    }
  }
  return out
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

/**
 * 文案键的解析：每一层先把剩余路径当作一个整体键查（允许 'system.user' 这样带点的键），
 * 查不到再按第一段下钻。这样 menu.system（目录）和 menu.system.user（页面）可以同时存在：
 *   menu: { system: '系统管理', 'system.user': '用户管理' }
 * 返回 null 表示没有（vue-i18n 的 te() 依赖这个约定）。
 */
export function resolveMessage(obj: unknown, path: string): unknown {
  if (!isPlainObject(obj)) return null
  if (path in obj) return obj[path] ?? null
  const dot = path.indexOf('.')
  if (dot < 0) return null
  return resolveMessage(obj[path.slice(0, dot)], path.slice(dot + 1))
}

// vue-i18n 的消息类型是递归字典；文案在运行期合并，这里按字典断言。
type Messages = Record<string, Record<string, string>>

/**
 * 创建端的 i18n。app 可以是一份文案，也可以是按顺序叠加的多份（后面的覆盖前面的）。
 * enabled 是端启用的语言；缺文案时按 fallbackChain 回退，所以简体中文和英文总会装进来。
 */
export function createPortalI18n(app: LocaleMessages | LocaleMessages[] = {}, enabled: readonly LocaleCode[] = AllLocaleCodes) {
  const layers = Array.isArray(app) ? app : [app]
  const codes = new Set<LocaleCode>([...enabled, 'zh-CN', 'en-US'])
  const messages: Record<string, Record<string, unknown>> = {}
  const fallback: Record<string, LocaleCode[]> = {}
  for (const code of codes) {
    let m = shellMessages[code]
    for (const l of layers) m = deepMerge(m, l[code] ?? {})
    messages[code] = m
    fallback[code] = fallbackChain(code)
  }
  return createI18n({
    legacy: false,
    locale: detectLocale(enabled),
    fallbackLocale: { ...fallback, default: ['en-US', 'zh-CN'] },
    messages: messages as unknown as Messages,
    messageResolver: resolveMessage as (obj: unknown, path: string) => never,
    missingWarn: false,
    fallbackWarn: false,
  })
}
