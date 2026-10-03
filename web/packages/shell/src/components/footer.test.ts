// 页脚的版权说明（D-119）：11 种语言都带版权人的注册名称；登录后的布局里它在内容区之后，内容区最大化时也在。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import GaFooter from './GaFooter.vue'
import GaLayout from '../layout/GaLayout.vue'
import { useLayoutStore } from '../layout/store'
import { setPortalContext } from '../context'
import { AllLocaleCodes, createPortalI18n } from '../i18n'
import type { LocaleCode } from '../i18n'
import type { Client } from '../request/client'

const owner = 'STARDATA INTERNATIONAL PTE. LTD.'

function i18nFor(code: LocaleCode) {
  const i18n = createPortalI18n({}, [code])
  i18n.global.locale.value = code
  return i18n
}

describe('GaFooter', () => {
  it('简体中文和英文按约定的写法显示', () => {
    const text = (code: LocaleCode) => mount(GaFooter, { global: { plugins: [i18nFor(code)] } }).text()
    expect(text('zh-CN')).toBe(`本框架版权归属 ${owner}（新加坡）`)
    expect(text('en-US')).toBe(`© 2026 ${owner} · Singapore`)
  })

  for (const code of AllLocaleCodes) {
    it(`${code}：有本语言的文案，带版权人的注册名称，没有剩下的占位符`, () => {
      const i18n = i18nFor(code)
      // 文案在本语言的文件里，不是靠回退链显示出来的
      const own = (i18n.global.getLocaleMessage(code) as { shell?: { footer?: { copyright?: string } } }).shell?.footer?.copyright
      expect(own).toContain('{owner}')
      const w = mount(GaFooter, { global: { plugins: [i18n] } })
      expect(w.text()).toBe(own!.replace('{owner}', owner))
      expect(w.text()).not.toMatch(/[{}]/)
      expect(w.attributes('data-test')).toBe('footer-copyright')
    })
  }
})

describe('布局里的页脚', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    setPortalContext({ portal: 'platform', client: {} as unknown as Client })
    vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }))
  })
  afterEach(() => vi.unstubAllGlobals())

  it('在内容区之后，不在滚动的内容区里面；内容区最大化时保留', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: { template: '<div />' } }] })
    await router.push('/')
    await router.isReady()
    const w = mount(GaLayout, {
      global: {
        plugins: [router, i18nFor('zh-CN')],
        stubs: { GaSidebar: true, GaTopbar: true, GaTabs: true, RouterView: true, ElDrawer: true, ElIcon: true },
      },
    })
    const footer = w.find('.ga-layout__body > main.ga-layout__main + footer.ga-layout__footer')
    expect(footer.exists()).toBe(true)
    expect(footer.text()).toBe(`本框架版权归属 ${owner}（新加坡）`)
    expect(w.find('main.ga-layout__main footer').exists()).toBe(false)
    expect(w.findAll('[data-test="footer-copyright"]')).toHaveLength(1)

    useLayoutStore().maximized = true
    await nextTick()
    expect(w.find('ga-topbar-stub').exists()).toBe(false)
    expect(w.find('.ga-layout__body > main.ga-layout__main + footer.ga-layout__footer').exists()).toBe(true)
    w.unmount()
  })
})
