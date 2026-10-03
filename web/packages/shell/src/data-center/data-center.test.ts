import { describe, expect, it, vi } from 'vitest'
import { createPinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus, { ElRadioGroup } from 'element-plus'
import { createPortalI18n } from '../i18n'
import GaDataCenter from './GaDataCenter.vue'
import type { DashboardData } from './types'

vi.mock('./EChart.vue', () => ({ default: { props: ['option', 'height'], template: '<div class="test-chart" />' } }))
vi.mock('../components/GaCountUp.vue', () => ({ default: { props: ['value'], template: '<span>{{ value }}</span>' } }))
vi.mock('../components/GaSparkline.vue', () => ({ default: { template: '<span />' } }))
const data = (n: number): DashboardData => ({ days: ['2026-10-01'], users: { total: n, enabled: n, new: n }, newUsers: [n], sessions: n, logins: { success: [n], failed: [0] }, operations: [n], reasons: [], topActions: [], topUsers: [], hours: { logins: Array(24).fill(0), operations: Array(24).fill(0) } })
function deferred<T>() { let resolve!: (v: T) => void; let reject!: (e: Error) => void; const promise = new Promise<T>((r, j) => { resolve = r; reject = j }); return { promise, resolve, reject } }

describe('shared data center', () => {
  it('renders ranking names as text', async () => {
    const username = '<strong data-canary="name">literal</strong>'
    const result = { ...data(1), topUsers: [{ userId: 1, username, count: 1 }] }
    const w = mount(GaDataCenter, { props: { fetch: async () => result }, global: { plugins: [createPinia(), createPortalI18n({}, ['zh-CN']), ElementPlus], directives: { loading: {} } } })
    await flushPromises()
    expect(w.text()).toContain(username)
    expect(w.find('[data-canary="name"]').exists()).toBe(false)
    w.unmount()
  })
  it('keeps the newest range when responses arrive out of order and restores the displayed range on failure', async () => {
    const first = deferred<DashboardData>(), second = deferred<DashboardData>(), third = deferred<DashboardData>()
    const fetch = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockReturnValueOnce(third.promise)
    const w = mount(GaDataCenter, { props: { fetch }, global: { plugins: [createPinia(), createPortalI18n({}, ['zh-CN']), ElementPlus], directives: { loading: {} } } })
    expect(fetch).toHaveBeenLastCalledWith(30)
    w.findComponent(ElRadioGroup).vm.$emit('update:modelValue', 7)
    await flushPromises()
    second.resolve(data(7)); await flushPromises()
    first.resolve(data(30)); await flushPromises()
    expect(w.find('[data-test="dash-card-users"]').text()).toContain('7')
    expect(w.findComponent(ElRadioGroup).props('modelValue')).toBe(7)
    w.findComponent(ElRadioGroup).vm.$emit('update:modelValue', 90); await flushPromises()
    third.reject(new Error('offline')); await flushPromises()
    expect(w.findComponent(ElRadioGroup).props('modelValue')).toBe(7)
    expect(w.find('[data-test="dash-card-users"]').text()).toContain('7')
    expect(w.text()).toContain('这段时间没有数据')
    w.unmount()
  })
})
