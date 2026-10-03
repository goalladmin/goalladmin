import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, KeepAlive, reactive, ref } from 'vue'
import type { Component } from 'vue'
import ElementPlus, { ElDialog } from 'element-plus'
import type * as ElementPlusExports from 'element-plus'
import OrgListPage from './OrgListPage.vue'
import OwnerPasswordDialog from './OwnerPasswordDialog.vue'
import UserPage from '../../views/system/user/index.vue'

const fixture = vi.hoisted(() => ({ create: vi.fn(), reset: vi.fn(), confirm: vi.fn(), auth: { epoch: 0, locked: false, user: { super: true } } }))
vi.mock('element-plus', async (original) => ({ ...await original<typeof ElementPlusExports>(), ElMessageBox: { confirm: fixture.confirm } }))
vi.mock('@ga/shell', () => ({
  useAuthStore: () => fixture.auth, useI18n: () => ({ t: (key: string) => key, te: () => false }), formatTime: String,
  describeApiError: String, isApiError: () => false, GaAvatar: { template: '<span />' },
  useTable: () => ({ query: {}, list: [], loading: false, total: 0, page: 1, pageSize: 20, reload: vi.fn(), search: vi.fn(), reset: vi.fn() }),
}))
vi.mock('../../api/partner', () => ({ orgApi: () => ({ create: fixture.create }), orgPerms: () => ({}), merchantApi: { agentOptions: async () => [] } }))
vi.mock('../../api/system', () => ({ userApi: { create: fixture.create, resetPassword: fixture.reset }, roleApi: { list: async () => [] }, deptApi: { options: async () => [] }, postApi: { options: async () => [] }, buildTree: () => [] }))

const SECRET = 'Temporary-Fixture-9'
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
function setup(component: Component, props: Record<string, unknown>) {
  const active = ref(true)
  const host = defineComponent({ setup: () => () => h(KeepAlive, null, { default: () => active.value ? h(component, props) : h('div') }) })
  const w = mount(host, { attachTo: document.body, global: { plugins: [ElementPlus], directives: { perm: () => {} }, stubs: { OrgDetailDrawer: true, IpAllowEditor: true } } })
  return { w, active, page: w.findComponent(component) }
}
type PageState = { openCreate(): void; submit(): Promise<void>; resetPassword(v: { id: number; username: string }): Promise<void>; form: { username?: string; ownerUsername?: string; name?: string; password?: string }; dialog: { visible: boolean }; reveal: { visible: boolean; password: string } }
beforeEach(() => {
  vi.clearAllMocks()
  document.body.innerHTML = ''
  fixture.auth = reactive({ epoch: 0, locked: false, user: { super: true } })
  fixture.confirm.mockResolvedValue(undefined)
  fixture.reset.mockResolvedValue({ initialPassword: SECRET })
  fixture.create.mockResolvedValue({ user: { username: 'staff' }, agent: { code: 'ORG1', ownerUsername: 'owner' }, merchant: { code: 'ORG1', ownerUsername: 'owner' }, initialPassword: SECRET })
})

describe.each([
  { name: 'platform user', component: UserPage, props: {} },
  { name: 'agent creation', component: OrgListPage, props: { kind: 'agent' } },
  { name: 'merchant creation', component: OrgListPage, props: { kind: 'merchant' } },
])('$name temporary password lifetime', ({ component, props }) => {
  async function open() {
    const s = setup(component, props)
    await flushPromises()
    const vm = s.page.vm as unknown as PageState
    vm.openCreate()
    await flushPromises()
    Object.assign(vm.form, { username: 'staff', ownerUsername: 'owner', name: 'Fixture Org' })
    return { ...s, vm }
  }
  it('normal creation shows its secret and closing clears it', async () => {
    const s = await open()
    await s.vm.submit()
    await flushPromises()
    expect(fixture.create).toHaveBeenCalledOnce()
    expect(s.vm.reveal).toMatchObject({ visible: true, password: SECRET })
    if (component === OrgListPage) s.page.findComponent(OwnerPasswordDialog).vm.$emit('update:modelValue', false)
    else s.vm.reveal.visible = false
    await flushPromises()
    expect(s.vm.reveal.password).toBe('')
    expect(document.body.textContent).not.toContain(SECRET)
    s.w.unmount()
  })
  it('deactivation clears a secret already displayed by the page', async () => {
    const s = await open()
    await s.vm.submit()
    expect(s.vm.reveal.password).toBe(SECRET)
    s.active.value = false
    await flushPromises()
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    expect(document.body.textContent).not.toContain(SECRET)
    s.w.unmount()
  })
  it('unmounting clears a displayed secret from retained component state', async () => {
    const s = await open()
    await s.vm.submit()
    expect(s.vm.reveal.password).toBe(SECRET)
    s.w.unmount()
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
  })
  it('closing the creation form rejects a late secret', async () => {
    const result = deferred<unknown>()
    fixture.create.mockReturnValue(result.promise)
    const s = await open()
    const pending = s.vm.submit()
    await flushPromises()
    expect(fixture.create).toHaveBeenCalledOnce()
    s.page.findAllComponents(ElDialog)[0]!.vm.$emit('update:modelValue', false)
    await flushPromises()
    result.resolve({ user: { username: 'staff' }, agent: { code: 'ORG1' }, initialPassword: SECRET })
    await pending
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    expect(document.body.textContent).not.toContain(SECRET)
    s.w.unmount()
  })
  it('KeepAlive deactivation rejects a late secret even after the page is restored', async () => {
    const result = deferred<unknown>()
    fixture.create.mockReturnValue(result.promise)
    const s = await open()
    const pending = s.vm.submit()
    await flushPromises()
    s.active.value = false
    await flushPromises()
    s.active.value = true
    await flushPromises()
    result.resolve({ user: { username: 'staff' }, agent: { code: 'ORG1' }, initialPassword: SECRET })
    await pending
    expect(s.vm.reveal).toMatchObject({ visible: false, password: '' })
    expect(document.body.textContent).not.toContain(SECRET)
    s.w.unmount()
  })
})

describe('platform manual and reset passwords', () => {
  it('normal reset shows the requested account secret and closing clears it', async () => {
    const s = setup(UserPage, {})
    const vm = s.page.vm as unknown as PageState
    await vm.resetPassword({ id: 7, username: 'staff' })
    expect(fixture.reset).toHaveBeenCalledWith(7)
    expect(vm.reveal).toMatchObject({ visible: true, password: SECRET })
    vm.reveal.visible = false
    expect(vm.reveal.password).toBe('')
    s.w.unmount()
  })
  it('closing clears the manually entered password immediately', async () => {
    const s = setup(UserPage, {})
    const vm = s.page.vm as unknown as PageState
    vm.openCreate()
    await flushPromises()
    vm.form.password = SECRET
    s.page.findAllComponents(ElDialog)[0]!.vm.$emit('update:modelValue', false)
    expect(vm.form.password).toBe('')
    s.w.unmount()
  })
  it('leaving during reset confirmation submits nothing', async () => {
    const confirm = deferred<void>()
    fixture.confirm.mockReturnValue(confirm.promise)
    const s = setup(UserPage, {})
    const vm = s.page.vm as unknown as PageState
    const pending = vm.resetPassword({ id: 7, username: 'staff' })
    s.active.value = false
    await flushPromises()
    confirm.resolve()
    await pending
    expect(fixture.reset).not.toHaveBeenCalled()
    s.w.unmount()
  })
  it('late reset responses cannot reveal secrets on a restored page', async () => {
    const result = deferred<{ initialPassword: string }>()
    fixture.reset.mockReturnValue(result.promise)
    const s = setup(UserPage, {})
    const vm = s.page.vm as unknown as PageState
    const pending = vm.resetPassword({ id: 7, username: 'staff' })
    await flushPromises()
    expect(fixture.reset).toHaveBeenCalledWith(7)
    s.active.value = false
    await flushPromises()
    s.active.value = true
    await flushPromises()
    result.resolve({ initialPassword: SECRET })
    await pending
    expect(vm.reveal).toMatchObject({ visible: false, password: '' })
    s.w.unmount()
  })
})
