// 支持的界面语言（docs/decisions.md D-026）。清单与后端 server/core/httpx/lang.go 一致。
// 加一种语言：这里加一行、壳和各端各加一份文案文件、GaRoot 里接上 Element Plus 的语言包、后端 lang.go 加一行。

import flagBD from 'flag-icons/flags/4x3/bd.svg'
import flagCN from 'flag-icons/flags/4x3/cn.svg'
import flagDE from 'flag-icons/flags/4x3/de.svg'
import flagFR from 'flag-icons/flags/4x3/fr.svg'
import flagIN from 'flag-icons/flags/4x3/in.svg'
import flagJP from 'flag-icons/flags/4x3/jp.svg'
import flagKR from 'flag-icons/flags/4x3/kr.svg'
import flagMY from 'flag-icons/flags/4x3/my.svg'
import flagRU from 'flag-icons/flags/4x3/ru.svg'
import flagUS from 'flag-icons/flags/4x3/us.svg'

export type LocaleCode = 'en-US' | 'zh-CN' | 'zh-TW' | 'ja-JP' | 'ko-KR' | 'ms-MY' | 'ta-IN' | 'bn-BD' | 'ru-RU' | 'fr-FR' | 'de-DE'

export interface LanguageInfo {
  code: LocaleCode
  /** 该语言自己的名字，选择器里显示它（不随界面语言变化）。 */
  label: string
  /** 旗帜图片地址；没有旗帜时为 null，用 badge 代替。 */
  flag: string | null
  /** 没有旗帜时显示的字标。 */
  badge?: string
}

/** 全部支持的语言，选择器按这个顺序列出。 */
export const SupportedLocales: readonly LanguageInfo[] = [
  { code: 'en-US', label: 'English', flag: flagUS },
  { code: 'zh-CN', label: '简体中文', flag: flagCN },
  // 繁体中文不配旗帜（D-026）
  { code: 'zh-TW', label: '繁體中文', flag: null, badge: '繁' },
  { code: 'ja-JP', label: '日本語', flag: flagJP },
  { code: 'ko-KR', label: '한국어', flag: flagKR },
  { code: 'ms-MY', label: 'Bahasa Melayu', flag: flagMY },
  { code: 'ta-IN', label: 'தமிழ்', flag: flagIN },
  { code: 'bn-BD', label: 'বাংলা', flag: flagBD },
  { code: 'ru-RU', label: 'Русский', flag: flagRU },
  { code: 'fr-FR', label: 'Français', flag: flagFR },
  { code: 'de-DE', label: 'Deutsch', flag: flagDE },
]

/** 默认语言：没有任何线索时用它，字典等数据的主字段也是这种语言。 */
export const DefaultLocale: LocaleCode = 'zh-CN'

export const AllLocaleCodes: readonly LocaleCode[] = SupportedLocales.map((l) => l.code)

export function isLocaleCode(v: unknown): v is LocaleCode {
  return typeof v === 'string' && (AllLocaleCodes as readonly string[]).includes(v)
}

export function languageInfo(code: LocaleCode): LanguageInfo {
  return SupportedLocales.find((l) => l.code === code) ?? SupportedLocales[0]!
}

/** 缺文案时依次尝试的语言（与后端 httpx.FallbackLangs 一致）：繁体先简体再英文，其他语言先英文再简体。 */
export function fallbackChain(code: LocaleCode): LocaleCode[] {
  switch (code) {
    case 'zh-TW':
      return ['zh-CN', 'en-US']
    case 'en-US':
      return ['zh-CN']
    case 'zh-CN':
      return ['en-US']
    default:
      return ['en-US', 'zh-CN']
  }
}

/**
 * 把浏览器的语言标签（zh-Hant-HK、ta-SG、de 这类）匹配到启用的语言，规则与后端 httpx.MatchLang 一致：
 * 先完全匹配；zh 带 Hant/TW/HK/MO 归繁体、其余归简体；其他按语言部分匹配。匹配不上返回 null。
 */
export function matchLocale(tag: string, enabled: readonly LocaleCode[] = AllLocaleCodes): LocaleCode | null {
  const t = tag.trim().replace(/_/g, '-').toLowerCase()
  if (!t) return null
  const exact = enabled.find((c) => c.toLowerCase() === t)
  if (exact) return exact
  const parts = t.split('-')
  let want: string | undefined
  if (parts[0] === 'zh') {
    want = parts.slice(1).some((p) => p === 'hant' || p === 'tw' || p === 'hk' || p === 'mo') ? 'zh-TW' : 'zh-CN'
    return enabled.find((c) => c === want) ?? null
  }
  return enabled.find((c) => c.toLowerCase().startsWith(`${parts[0]}-`)) ?? null
}
