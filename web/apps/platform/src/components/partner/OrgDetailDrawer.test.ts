import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import type * as ElementPlusExports from 'element-plus'
import OrgDetailDrawer from './OrgDetailDrawer.vue'
import OwnerPasswordDialog from './OwnerPasswordDialog.vue'

const mock = vi.hoisted(() => ({ confirm: vi.fn(), reset: vi.fn(), get: vi.fn(), clearAllow: vi.fn(), clearDeny: vi.fn(), owner: vi.fn(), revoke: vi.fn() }))
vi.mock('@ga/shell', () => ({ formatTime: String, hasPerm: () => true, useAuthStore: () => ({ epoch: 0, locked: false }), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('element-plus', async (original) => ({ ...await original<typeof ElementPlusExports>(), ElMessageBox: { confirm: mock.confirm } }))
vi.mock('../../api/partner', () => ({ orgApi: () => ({ get: mock.get, resetOwnerPassword: mock.reset, clearIPAllow: mock.clearAllow, clearIPDeny: mock.clearDeny, changeOwner: mock.owner, revokeSession: mock.revoke, ipAllow: async () => ({ items: [] }), ipDeny: async () => ({ list: [], total: 0 }), accounts: async () => ({ list: [], total: 0 }), sessions: async () => ({ list: [], total: 0 }) }), orgPerms: () => ({ owner: 'owner' }) }))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}
async function setup() {
  const w = mount(OrgDetailDrawer, { props: { modelValue: true, orgId: 1, kind: 'merchant' }, global: { plugins: [ElementPlus], directives: { perm: () => {} } } })
  await flushPromises()
  return w
}
describe('password result ownership', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mock.confirm.mockResolvedValue(undefined)
    mock.get.mockImplementation(async (id: number) => ({ id, name: `Org ${id}`, code: `ORG${id}`, ownerUsername: `owner${id}`, status: 1 }))
    mock.reset.mockResolvedValue({ initialPassword: 'temporary-fixture-value' })
  })
  it('结果绑定目标身份，关闭即清空密码', async () => {
    const w = await setup()
    await w.find('[data-test="org-reset-owner"]').trigger('click')
    await flushPromises()
    const dialog = w.findComponent(OwnerPasswordDialog)
    expect(mock.reset).toHaveBeenCalledWith(1)
    expect(dialog.props()).toMatchObject({ code: 'ORG1', username: 'owner1', password: 'temporary-fixture-value', modelValue: true })
    dialog.vm.$emit('update:modelValue', false)
    await flushPromises()
    expect(dialog.props('password')).toBe('')
    w.unmount()
  })
  it('确认期间切换主体不发重置请求', async () => {
    const confirm = deferred<void>()
    mock.confirm.mockReturnValue(confirm.promise)
    const w = await setup()
    await w.find('[data-test="org-reset-owner"]').trigger('click')
    await w.setProps({ orgId: 2 })
    confirm.resolve()
    await flushPromises()
    expect(mock.reset).not.toHaveBeenCalled()
    expect(w.findComponent(OwnerPasswordDialog).props('password')).toBe('')
    w.unmount()
  })
  it('关闭后迟到的结果不恢复，再打开显示新主体', async () => {
    const result = deferred<{ initialPassword: string }>()
    mock.reset.mockReturnValue(result.promise)
    const w = await setup()
    await w.find('[data-test="org-reset-owner"]').trigger('click')
    await flushPromises()
    await w.setProps({ modelValue: false })
    result.resolve({ initialPassword: 'late-fixture-value' })
    await flushPromises()
    expect(w.findComponent(OwnerPasswordDialog).props()).toMatchObject({ modelValue: false, password: '' })
    await w.setProps({ modelValue: true, orgId: 2 })
    await flushPromises()
    expect(w.findComponent(OwnerPasswordDialog).props('password')).toBe('')
    w.unmount()
  })
})

describe.each([
  { method: 'clearIP', spy: mock.clearAllow, arg: undefined, args: [1] },
  { method: 'clearDeny', spy: mock.clearDeny, arg: undefined, args: [1] },
  { method: 'makeOwner', spy: mock.owner, arg: { id: 7, username: 'staff' }, args: [1, 7] },
  { method: 'revoke', spy: mock.revoke, arg: { sid: 'session-fixture', username: 'staff' }, args: [1, 'session-fixture'] },
])('$method confirmation ownership', ({ method, spy, arg, args }) => {
  beforeEach(() => {
    vi.clearAllMocks()
    mock.confirm.mockResolvedValue(undefined)
    mock.get.mockImplementation(async (id: number) => ({ id, name: `Org ${id}`, code: `ORG${id}`, ownerUsername: `owner${id}`, status: 1 }))
    spy.mockResolvedValue(undefined)
  })
  const invoke = (w: Awaited<ReturnType<typeof setup>>) => (w.vm as unknown as Record<string, (v: unknown) => Promise<void>>)[method]!(arg)
  it('normal confirmation submits the captured organisation and target', async () => {
    const w = await setup()
    await invoke(w)
    expect(spy).toHaveBeenCalledWith(...args)
    w.unmount()
  })
  it('switching organisations while confirmation is pending submits nothing', async () => {
    const confirm = deferred<void>()
    mock.confirm.mockReturnValue(confirm.promise)
    const w = await setup()
    const pending = invoke(w)
    await w.setProps({ orgId: 2 })
    confirm.resolve()
    await pending
    expect(spy).not.toHaveBeenCalled()
    w.unmount()
  })
  it('closing while confirmation is pending submits nothing', async () => {
    const confirm = deferred<void>()
    mock.confirm.mockReturnValue(confirm.promise)
    const w = await setup()
    const pending = invoke(w)
    await w.setProps({ modelValue: false })
    confirm.resolve()
    await pending
    expect(spy).not.toHaveBeenCalled()
    w.unmount()
  })
})
