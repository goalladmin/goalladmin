import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, h, KeepAlive, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus, { ElMessageBox } from 'element-plus'
import { createPortalI18n } from '../../i18n'
import { useAuthStore } from '../../auth/store'
import { setPortalContext } from '../../context'
import type { Client } from '../../request/client'
import GaOrgAccounts from './GaOrgAccounts.vue'

// D-098：重置密码后"只显示这一次"的密码，关掉对话框就清空，不留在页面状态和隐藏的节点里。
const PASSWORD = 'One-Time-Pass-9'

function setup(portal: string) {
  const staff = { id: 7, orgId: 1, username: 'staff', displayName: 'Staff', email: '', phone: '', avatar: '', owner: false, mustChangePwd: false, lastLoginAt: null, lastLoginIp: '', status: 1, sort: 0, remark: '', createdAt: new Date().toISOString(), roles: [] }
  const get = vi.fn(async (path: string) => path === '/org/roles' ? [] : ({ list: [staff], total: 1, page: 1, pageSize: 20 }))
  const post = vi.fn(async () => ({ initialPassword: PASSWORD, account: staff }))
  setPortalContext({ portal, scoped: true, client: { get, post, put: vi.fn(), delete: vi.fn() } as unknown as Client })
  const pinia = createPinia(); setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = { id: 1, username: 'owner', displayName: '', avatar: '', super: true, mustChangePwd: false }
  return { post, staff, auth, global: { plugins: [pinia, createPortalI18n({}, ['zh-CN']), ElementPlus] } }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
type PageState = { openCreate(): void; submit(): Promise<void>; resetPassword(v: { id: number; username: string }): Promise<void>; form: { username: string; password: string }; dialog: { visible: boolean }; reveal: { visible: boolean; password: string } }
function cached(portal: string) {
  const s = setup(portal)
  const active = ref(true)
  const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => active.value ? h(GaOrgAccounts) : h('div') }) })
  const w = mount(host, { global: s.global, attachTo: document.body })
  return { ...s, w, active, vm: w.findComponent(GaOrgAccounts).vm as unknown as PageState }
}

beforeEach(() => { document.body.innerHTML = '' })
describe.each(['agent', 'merchant'])('%s accounts page', (portal) => {
  it('clears the one-time password when the dialog is closed', async () => {
    const s = setup(portal)
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as never)
    const w = mount(GaOrgAccounts, { global: s.global, attachTo: document.body }); await flushPromises()
    await w.get('[data-test="org-account-reset"]').trigger('click'); await flushPromises()
    expect(s.post).toHaveBeenCalledWith('/org/accounts/7/reset-password')
    expect(document.querySelector('[data-test="org-initial-password"]')?.textContent).toBe(PASSWORD)

    ;(document.querySelector('[data-test="org-password-close"]') as HTMLElement).click(); await flushPromises()
    expect(document.body.textContent).not.toContain(PASSWORD)
    expect(document.body.innerHTML).not.toContain(PASSWORD)

    // 再重置一次照常显示新的
    await w.get('[data-test="org-account-reset"]').trigger('click'); await flushPromises()
    expect(document.querySelector('[data-test="org-initial-password"]')?.textContent).toBe(PASSWORD)
    w.unmount()
  })
  it('normal creation reveals the result and closing clears manual passwords', async () => {
    const s = cached(portal)
    await flushPromises()
    s.vm.openCreate()
    await flushPromises()
    s.vm.form.username = 'fixture_staff'
    s.vm.form.password = 'Manual-Fixture-9'
    s.vm.dialog.visible = false
    expect(s.vm.form.password).toBe('')
    s.vm.openCreate()
    await flushPromises()
    s.vm.form.username = 'fixture_staff'
    await s.vm.submit()
    expect(s.post).toHaveBeenCalledWith('/org/accounts', expect.objectContaining({ username: 'fixture_staff' }))
    expect(s.vm.reveal).toMatchObject({ visible: true, password: PASSWORD })
    s.w.unmount()
  })
  it('closing a creation form rejects its late result', async () => {
    const s = cached(portal)
    await flushPromises()
    const result = deferred<{ initialPassword: string; account: typeof s.staff }>()
    s.post.mockReturnValue(result.promise)
    s.vm.openCreate()
    await flushPromises()
    s.vm.form.username = 'fixture_staff'
    const pending = s.vm.submit()
    await flushPromises()
    expect(s.post).toHaveBeenCalledOnce()
    s.vm.dialog.visible = false
    result.resolve({ initialPassword: PASSWORD, account: s.staff })
    await pending
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    s.w.unmount()
  })
  it('a cached page rejects late creation responses after it has been restored', async () => {
    const s = cached(portal)
    await flushPromises()
    const result = deferred<{ initialPassword: string; account: typeof s.staff }>()
    s.post.mockReturnValue(result.promise)
    s.vm.openCreate()
    await flushPromises()
    s.vm.form.username = 'fixture_staff'
    const pending = s.vm.submit()
    await flushPromises()
    expect(s.post).toHaveBeenCalledOnce()
    s.active.value = false
    await flushPromises()
    s.active.value = true
    await flushPromises()
    result.resolve({ initialPassword: PASSWORD, account: s.staff })
    await pending
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    expect(document.body.textContent).not.toContain(PASSWORD)
    s.w.unmount()
  })
  it('leaving during reset confirmation prevents submission', async () => {
    const confirm = deferred<Awaited<ReturnType<typeof ElMessageBox.confirm>>>()
    vi.spyOn(ElMessageBox, 'confirm').mockReturnValue(confirm.promise)
    const s = cached(portal)
    await flushPromises()
    const pending = s.vm.resetPassword(s.staff)
    s.active.value = false
    await flushPromises()
    // Element Plus 将返回类型写成了交集；确认的运行时返回值仍是字符串。
    confirm.resolve('confirm' as never)
    await pending
    expect(s.post).not.toHaveBeenCalled()
    s.w.unmount()
  })
  it('late reset responses do not restore a secret after deactivation', async () => {
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as never)
    const s = cached(portal)
    await flushPromises()
    const result = deferred<{ initialPassword: string; account: typeof s.staff }>()
    s.post.mockReturnValue(result.promise)
    const pending = s.vm.resetPassword(s.staff)
    await flushPromises()
    expect(s.post).toHaveBeenCalledWith('/org/accounts/7/reset-password')
    s.active.value = false
    await flushPromises()
    s.active.value = true
    await flushPromises()
    result.resolve({ initialPassword: PASSWORD, account: s.staff })
    await pending
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    expect(document.body.textContent).not.toContain(PASSWORD)
    s.w.unmount()
  })
  it('deactivation clears an already visible secret', async () => {
    vi.spyOn(ElMessageBox, 'confirm').mockResolvedValue('confirm' as never)
    const s = cached(portal)
    await flushPromises()
    await s.vm.resetPassword(s.staff)
    expect(s.vm.reveal.password).toBe(PASSWORD)
    s.active.value = false
    await flushPromises()
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    expect(document.body.textContent).not.toContain(PASSWORD)
    s.w.unmount()
  })
})
