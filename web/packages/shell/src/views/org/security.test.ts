import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { createPortalI18n } from '../../i18n'
import { useAuthStore } from '../../auth/store'
import { setPortalContext } from '../../context'
import type { Client } from '../../request/client'
import GaOrgOverview from './GaOrgOverview.vue'
import GaOrgIPAllow from './GaOrgIPAllow.vue'
import GaOrgIPDeny from './GaOrgIPDeny.vue'

function setup(portal: string, remark?: string) {
  const get = vi.fn(async (path: string) => {
    if (path === '/org/overview') return { owner: true, counts: {}, org: { code: 'M1', name: '主体', status: 1, createdAt: new Date().toISOString() } }
    if (path === '/org/ip-allow') return { items: [], yourIp: '203.0.113.10' }
    return { list: remark ? [{ id: 1, cidr: '198.51.100.9/32', expiresAt: null, remark }] : [], total: remark ? 1 : 0, page: 1, pageSize: 20 }
  })
  const put = vi.fn(async (_path: string, body: unknown) => ({ items: (body as { items: unknown[] }).items, yourIp: '203.0.113.10' }))
  const post = vi.fn(async () => ({}))
  setPortalContext({ portal, scoped: true, client: { get, put, post, delete: vi.fn(async () => null) } as unknown as Client })
  const pinia = createPinia(); setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = { id: 1, username: 'owner', displayName: '', avatar: '', super: true, mustChangePwd: false }
  const global = { plugins: [pinia, createPortalI18n({}, ['zh-CN']), ElementPlus] }
  return { get, put, post, global }
}

beforeEach(() => { document.body.innerHTML = '' })
describe.each(['agent', 'merchant'])('%s security pages', (portal) => {
  it('moves whitelist loading out of overview and saves through its dedicated page', async () => {
    const s = setup(portal)
    const overview = mount(GaOrgOverview, { global: s.global }); await flushPromises()
    expect(s.get).toHaveBeenCalledTimes(1)
    expect(s.get).toHaveBeenCalledWith('/org/overview')
    expect(overview.find('[data-test="org-ip-allow"]').exists()).toBe(false)
    overview.unmount()
    const allow = mount(GaOrgIPAllow, { global: s.global }); await flushPromises()
    await allow.get('[data-test="org-ip-add-mine"]').trigger('click'); await flushPromises()
    await allow.get('[data-test="org-ip-save"]').trigger('click'); await flushPromises()
    expect(s.put).toHaveBeenCalledWith('/org/ip-allow', { items: [{ cidr: '203.0.113.10', remark: '我当前的 IP' }] })
    await allow.get('input[data-test="org-ip-cidr-0"], [data-test="org-ip-cidr-0"] input').setValue('203.0.113.*')
    await allow.get('[data-test="org-ip-save"]').trigger('click'); await flushPromises()
    expect(s.put).toHaveBeenLastCalledWith('/org/ip-allow', { items: [{ cidr: '203.0.113.*', remark: '我当前的 IP' }] })
    expect(allow.text()).toContain('116.88.8.*')
    allow.unmount()
  })
  it('renders stored remarks as text', async () => {
    const remark = '<strong data-canary="remark">literal</strong>'
    const s = setup(portal, remark)
    const w = mount(GaOrgIPDeny, { global: s.global }); await flushPromises()
    expect(w.text()).toContain(remark)
    expect(w.find('[data-canary="remark"]').exists()).toBe(false)
    w.unmount()
  })
  it.each(['198.51.100.8', '116.88.8.*'])('creates a scoped block rule for %s without supplying organization identity', async (cidr) => {
    const s = setup(portal)
    const w = mount(GaOrgIPDeny, { global: s.global, attachTo: document.body }); await flushPromises()
    await w.get('[data-test="org-deny-add"]').trigger('click'); await flushPromises()
    const input = document.querySelector('input[data-test="org-deny-cidr"], [data-test="org-deny-cidr"] input') as HTMLInputElement
    input.value = cidr; input.dispatchEvent(new Event('input', { bubbles: true })); await flushPromises()
    ;(document.querySelector('[data-test="org-deny-save"]') as HTMLButtonElement).click(); await flushPromises()
    expect(s.post).toHaveBeenCalledWith('/org/ip-deny', { cidr, expiresIn: 1440, remark: '' }, { silent: true })
    expect(s.get).toHaveBeenLastCalledWith('/org/ip-deny', { params: { keyword: '', includeExpired: 0, page: 1, pageSize: 20 } })
    w.unmount()
  })
})
