import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import ElementPlus from 'element-plus'
import GaRegister from './GaRegister.vue'
import { setPortalContext } from '../context'
import { createPortalI18n } from '../i18n'
import type { Client } from '../request/client'

async function setup(enabled: boolean, hash = '') {
  const post = vi.fn(async () => ({ reference: 'reference-for-contact' }))
  const get = vi.fn(async (path: string) => path === '/onboarding/config' ? { enabled } : { captchaId: 'challenge', image: 'data:image/png;base64,YQ==' })
  setPortalContext({ portal: 'merchant', scoped: true, client: { get, post } as unknown as Client })
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/register', component: GaRegister }, { path: '/login', component: { template: '<div />' } }] })
  await router.push('/register' + hash)
  await router.isReady()
  const w = mount(GaRegister, { global: { plugins: [router, createPortalI18n({}, ['zh-CN']), ElementPlus] } })
  await flushPromises()
  return { w, router, post, get }
}
describe('registration', () => {
  beforeEach(() => { setActivePinia(createPinia()); localStorage.clear() })
  it('关闭时不显示表单、不生成验证码', async () => {
    const { w, get, post } = await setup(false)
    expect(w.find('[data-test="register-closed"]').exists()).toBe(true)
    expect(w.find('form').exists()).toBe(false)
    expect(get).toHaveBeenCalledTimes(1)
    expect(post).not.toHaveBeenCalled()
    w.unmount()
  })
  it('邀请只存内存并作为凭证提交，不提交主体 ID', async () => {
    const token = 'a'.repeat(64)
    const { w, router, post } = await setup(true, '#invite=' + token)
    expect(router.currentRoute.value.hash).toBe('')
    for (const [key, value] of Object.entries({ name: 'Shop', contact: 'Owner', phone: '12345678', username: 'owner', captcha: '12345' })) {
      await w.find(`[data-test="register-${key}"]`).setValue(value)
    }
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(post).toHaveBeenCalledWith('/onboarding/applications', { name: 'Shop', contactName: 'Owner', contactPhone: '12345678', ownerUsername: 'owner', invitationToken: token, captchaId: 'challenge', captchaCode: '12345' }, { skipAuth: true, headers: { 'X-GA-Client': 'web' } })
    expect(localStorage.length).toBe(0)
    expect(w.find('[data-test="register-reference"]').text()).toContain('reference-for-contact')
    expect(w.find('form').exists()).toBe(false)
    w.unmount()
  })
  it('非法邀请不退化成直属平台申请', async () => {
    const { w, get, post } = await setup(true, '#invite=bad')
    expect(w.find('form').exists()).toBe(false)
    expect(get).toHaveBeenCalledTimes(1)
    expect(post).not.toHaveBeenCalled()
    w.unmount()
  })
})
