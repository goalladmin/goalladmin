// 主体端按"编号 + 账号 + 密码"登录（D-061、D-067）：scoped 的端登录页多一个编号输入框（必填），编号去空白、转大写后提交，
// 登录成功后记在这个浏览器里、下次自动填上；平台端没有这个输入框，也不提交编号。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import ElementPlus from 'element-plus'

import GaLogin from './GaLogin.vue'
import { setPortalContext } from '../context'
import { createPortalI18n, scopedMessages } from '../i18n'
import type { Client } from '../request/client'
import { RouteNames } from '../router'
import type { MeResponse } from '../types'

const me: MeResponse = {
  user: { id: 7, username: 'staff', displayName: '员工', avatar: '', mustChangePwd: false, super: false },
  perms: [],
  menus: [],
  org: { code: 'M12345678', name: '商户甲' },
}

function setup(scoped: boolean) {
  const post = vi.fn(async () => ({ accessToken: 't', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId: 'a'.repeat(32) }))
  const client = { post, get: vi.fn(async () => me) } as unknown as Client
  setPortalContext({ portal: 'merchant', client, scoped })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/login', name: RouteNames.login, component: GaLogin },
      { path: '/change-password', name: RouteNames.changePassword, component: { template: '<div />' } },
      { path: '/', component: { template: '<div id="home" />' } },
    ],
  })
  const i18n = createPortalI18n(scoped ? [scopedMessages()] : {}, ['zh-CN'])
  return { post, router, i18n }
}

async function mountLogin(scoped: boolean) {
  const s = setup(scoped)
  await s.router.push('/login')
  await s.router.isReady()
  const w = mount(GaLogin, { global: { plugins: [s.router, s.i18n, ElementPlus] }, attachTo: document.body })
  return { ...s, w }
}

describe('GaLogin', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.body.innerHTML = ''
  })

  it('主体端：编号去空白转大写后提交，登录成功后记住', async () => {
    const { w, post, router } = await mountLogin(true)
    expect(w.find('[data-test="login-org"]').exists()).toBe(true)

    await w.find('[data-test="login-username"]').setValue('staff')
    await w.find('[data-test="login-password"]').setValue('secret-pass-1')
    await w.find('[data-test="login-org"]').setValue('  m12345678 ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(post).toHaveBeenCalledTimes(1)
    const body = (post.mock.calls[0] as unknown[])[1] as Record<string, unknown>
    expect(body.org).toBe('M12345678')
    expect(body.username).toBe('staff')
    expect(localStorage.getItem('ga.merchant.orgCode')).toBe('M12345678')
    expect(router.currentRoute.value.path).toBe('/')
    w.unmount()

    // 下次打开登录页自动填上
    setActivePinia(createPinia())
    const again = await mountLogin(true)
    expect((again.w.find('[data-test="login-org"]').element as HTMLInputElement).value).toBe('M12345678')
    again.w.unmount()
  })

  it('平台端：没有编号输入框，也不提交编号', async () => {
    const { w, post } = await mountLogin(false)
    expect(w.find('[data-test="login-org"]').exists()).toBe(false)
    await w.find('[data-test="login-username"]').setValue('admin')
    await w.find('[data-test="login-password"]').setValue('secret-pass-1')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(post).toHaveBeenCalledTimes(1)
    const body = (post.mock.calls[0] as unknown[])[1] as Record<string, unknown>
    expect(body.org).toBeUndefined()
    expect(localStorage.getItem('ga.merchant.orgCode')).toBeNull()
    w.unmount()
  })

  it('底部在原有的一句话下面显示版权说明（D-119）', async () => {
    const { w } = await mountLogin(false)
    const foot = w.find('.ga-auth__foot')
    expect(foot.text()).toContain('GoAllAdmin · 一个能安全上线的最小后台')
    const copyright = foot.find('[data-test="footer-copyright"]')
    expect(copyright.text()).toBe('本框架版权归属 STARDATA INTERNATIONAL PTE. LTD.（新加坡）')
    expect(foot.text().endsWith(copyright.text())).toBe(true)
    w.unmount()
  })

  it('读不了本地存储时照样能登录，只是记不住', async () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('denied')
    })
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('denied')
    })
    try {
      const { w, post } = await mountLogin(true)
      await w.find('[data-test="login-org"]').setValue('M1')
      await w.find('[data-test="login-username"]').setValue('staff')
      await w.find('[data-test="login-password"]').setValue('secret-pass-1')
      await w.find('form').trigger('submit')
      await flushPromises()
      expect(post).toHaveBeenCalledTimes(1)
      w.unmount()
    } finally {
      get.mockRestore()
      set.mockRestore()
    }
  })
})

describe('主体端的文案层', () => {
  it('scopedMessages 把"超级管理员"的说法换成主账号，平台端不受影响', () => {
    const scoped = createPortalI18n([scopedMessages()], ['zh-CN']).global
    const platform = createPortalI18n({}, ['zh-CN']).global
    expect(scoped.t('err.rbac.perm.sensitive')).toContain('主账号')
    expect(platform.t('err.rbac.perm.sensitive')).toContain('超级管理员')
    // 别的键照旧
    expect(scoped.t('err.rbac.perm.notOwned')).toBe(platform.t('err.rbac.perm.notOwned'))
  })
})
