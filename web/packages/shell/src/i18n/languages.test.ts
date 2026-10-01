// D-026：语言清单、浏览器语言匹配、回退链、按文件名装载文案。

import { describe, expect, it } from 'vitest'

import { createPortalI18n, detectLocale, localesFromGlob } from './index'
import { AllLocaleCodes, SupportedLocales, fallbackChain, matchLocale } from './languages'

describe('语言清单', () => {
  it('11 种语言，只有繁体中文没有旗帜而用字标', () => {
    expect(AllLocaleCodes).toEqual(['en-US', 'zh-CN', 'zh-TW', 'ja-JP', 'ko-KR', 'ms-MY', 'ta-IN', 'bn-BD', 'ru-RU', 'fr-FR', 'de-DE'])
    for (const l of SupportedLocales) {
      if (l.code === 'zh-TW') {
        expect(l.flag).toBeNull()
        expect(l.badge).toBe('繁')
      } else {
        expect(l.flag, l.code).toBeTruthy()
      }
    }
  })
})

describe('matchLocale（与后端 httpx.MatchLang 同一套规则）', () => {
  const cases: [string, string | null][] = [
    ['zh-CN', 'zh-CN'],
    ['zh', 'zh-CN'],
    ['zh-SG', 'zh-CN'],
    ['zh-TW', 'zh-TW'],
    ['zh-HK', 'zh-TW'],
    ['zh-Hant-MO', 'zh-TW'],
    ['en-GB', 'en-US'],
    ['ta-SG', 'ta-IN'],
    ['ms-SG', 'ms-MY'],
    ['bn-IN', 'bn-BD'],
    ['de-AT', 'de-DE'],
    ['fr_CA', 'fr-FR'],
    ['ur-PK', null],
    ['', null],
  ]
  for (const [tag, want] of cases) {
    it(`${tag || '(空)'} → ${want}`, () => expect(matchLocale(tag)).toBe(want))
  }
  it('只在启用的语言里匹配', () => {
    expect(matchLocale('ja-JP', ['zh-CN', 'en-US'])).toBeNull()
    expect(matchLocale('zh-HK', ['zh-CN', 'en-US'])).toBeNull()
  })
})

describe('回退链', () => {
  it('繁体先简体再英文，其他语言先英文', () => {
    expect(fallbackChain('zh-TW')).toEqual(['zh-CN', 'en-US'])
    expect(fallbackChain('ja-JP')).toEqual(['en-US', 'zh-CN'])
    expect(fallbackChain('en-US')).toEqual(['zh-CN'])
  })
  it('缺的键按回退链显示，不会显示成键名', () => {
    const i18n = createPortalI18n(
      { 'zh-CN': { demo: { a: '甲', b: '乙' } }, 'en-US': { demo: { a: 'A' } }, 'zh-TW': {}, 'ja-JP': {} },
      ['zh-CN', 'en-US', 'zh-TW', 'ja-JP'],
    )
    const t = (locale: string, k: string) => {
      i18n.global.locale.value = locale as never
      return i18n.global.t(k)
    }
    expect(t('ja-JP', 'demo.a')).toBe('A') // 日语缺 → 英文
    expect(t('ja-JP', 'demo.b')).toBe('乙') // 英文也缺 → 简体
    expect(t('zh-TW', 'demo.a')).toBe('甲') // 繁体缺 → 简体，不是英文
  })
})

describe('detectLocale', () => {
  it('上次选的语言优先；不在启用列表里时按浏览器语言；都没有用默认语言', () => {
    localStorage.setItem('ga.locale', 'ko-KR')
    expect(detectLocale()).toBe('ko-KR')
    expect(detectLocale(['zh-CN', 'en-US'])).not.toBe('ko-KR')
    localStorage.removeItem('ga.locale')
  })
})

describe('localesFromGlob', () => {
  it('按文件名认语言，不认识的文件忽略', () => {
    const out = localesFromGlob({
      './locales/zh-CN.ts': { default: { a: '甲' } },
      './locales/ja-JP.ts': { default: { a: 'あ' } },
      './locales/index.ts': { default: { a: 'x' } },
      './locales/ur-PK.ts': { default: { a: 'x' } },
    })
    expect(Object.keys(out).sort()).toEqual(['ja-JP', 'zh-CN'])
    expect(out['ja-JP']).toEqual({ a: 'あ' })
  })
})
