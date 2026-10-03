// @vitest-environment-options {"url": "https://app.test/"}

// https 下"待补退出"标记的 Cookie（D-059、D-060）：新标记写在 __Host- 前缀的名字下；旧名字的标记只作"不恢复"的依据，
// 不凭它补做退出（它可能是同主域的其他主机塞进来的）；换来的会话不在旧标记里，旧标记删掉。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useAuthStore } from './store'
import { setPortalContext } from '../context'
import type { Client } from '../request/client'
import { ApiError, Codes } from '../types'
import type { MeResponse, TokenResponse } from '../types'

const me: MeResponse = {
  user: { id: 1, username: 'u', displayName: 'U', avatar: '', mustChangePwd: false, super: false },
  perms: [],
  menus: [],
}
const SID = 'a'.repeat(32)
const OTHER = 'b'.repeat(32)
const LATE = 'c'.repeat(32)

function fakeClient(refreshSid: string, logoutResult = false) {
  const tok = (sessionId: string): TokenResponse => ({ accessToken: 't', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId })
  const client = {
    refresh: vi.fn(async () => tok(refreshSid)),
    get: vi.fn(async () => me),
    post: vi.fn(async () => tok(SID)),
    put: vi.fn(async () => null),
    logout: vi.fn(async () => logoutResult),
  } as unknown as Client
  setPortalContext({ portal: 'p', client })
  return client
}

const clearCookies = () => {
  document.cookie = 'ga_lo_p=; Path=/; Max-Age=0; Secure'
  document.cookie = '__Host-ga_lo_p=; Path=/; Max-Age=0; Secure'
}

describe('auth store over https: 待补退出标记（D-059、D-060）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    sessionStorage.clear()
    clearCookies()
  })

  it('新标记写在 __Host- 前缀的名字下', async () => {
    expect(location.protocol).toBe('https:')
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('quota', 'QuotaExceededError')
    })
    try {
      fakeClient(SID, false)
      const auth = useAuthStore()
      auth.setToken({ accessToken: 't0', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId: SID })
      await auth.logout()
      expect(document.cookie).toContain(`__Host-ga_lo_p=${SID}`)
      expect(document.cookie).not.toMatch(/(^|; )ga_lo_p=/)
    } finally {
      setItem.mockRestore()
    }
  })

  it('换来的会话在旧名字的标记里：不恢复，也不发退出请求；旧标记留着', async () => {
    document.cookie = `ga_lo_p=${SID}; Path=/; Secure`
    const client = fakeClient(SID)
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(client.logout).not.toHaveBeenCalled()
    expect(client.get).not.toHaveBeenCalled()
    expect(auth.authenticated).toBe(false)
    expect(document.cookie).toContain(`ga_lo_p=${SID}`)
  })

  it('换来的会话不在旧标记里：照常恢复，旧标记删掉', async () => {
    document.cookie = `ga_lo_p=${OTHER}; Path=/; Secure`
    const client = fakeClient(SID)
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(client.logout).not.toHaveBeenCalled()
    expect(auth.authenticated).toBe(true)
    expect(document.cookie).not.toContain(OTHER)
  })

  // D-069：标记是启动时读的，刷新回来才删，中间隔着一次网络请求。这期间别的标签页写进来的会话号不能跟着一起删。
  /** 刷新在途时，"别的标签页"往旧名字的标记里写 value，然后刷新按 outcome 结束。 */
  function refreshWritesMeanwhile(client: Client, value: string, outcome: () => TokenResponse) {
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => {
      document.cookie = `ga_lo_p=${value}; Path=/; Secure`
      return outcome()
    })
  }
  const tok = (sessionId: string): TokenResponse => ({ accessToken: 't', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId })

  it('刷新在途时写进旧标记的会话号不被删掉：换来的是别的会话，照常恢复，新写的留着', async () => {
    document.cookie = `ga_lo_p=${OTHER}; Path=/; Secure`
    const client = fakeClient(SID)
    refreshWritesMeanwhile(client, `${OTHER}.${LATE}`, () => tok(SID))
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(auth.authenticated).toBe(true)
    expect(document.cookie).toContain(LATE)
  })

  it('刷新在途时写进旧标记的正是换来的会话：不恢复、不发退出请求，标记留着', async () => {
    const client = fakeClient(SID)
    refreshWritesMeanwhile(client, SID, () => tok(SID))
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(client.logout).not.toHaveBeenCalled()
    expect(client.get).not.toHaveBeenCalled()
    expect(auth.authenticated).toBe(false)
    expect(document.cookie).toContain(`ga_lo_p=${SID}`)
  })

  it('启动时读到的旧标记在刷新在途时被别的标签页删了：换来的还是那个会话，照样不恢复', async () => {
    document.cookie = `ga_lo_p=${SID}; Path=/; Secure`
    const client = fakeClient(SID)
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => {
      document.cookie = 'ga_lo_p=; Path=/; Max-Age=0; Secure'
      return tok(SID)
    })
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(client.logout).not.toHaveBeenCalled()
    expect(auth.authenticated).toBe(false)
  })

  it('刷新被服务端拒绝：旧标记删掉；在途时写进来的会话号留着', async () => {
    const rejected = () => {
      throw new ApiError(Codes.TokenInvalid, 'invalid', { status: 401 })
    }
    document.cookie = `ga_lo_p=${OTHER}; Path=/; Secure`
    let client = fakeClient(SID)
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => rejected())
    await useAuthStore().bootstrap()
    expect(document.cookie).not.toContain(OTHER)

    setActivePinia(createPinia())
    document.cookie = `ga_lo_p=${OTHER}; Path=/; Secure`
    client = fakeClient(SID)
    refreshWritesMeanwhile(client, `${OTHER}.${LATE}`, rejected)
    await useAuthStore().bootstrap()
    expect(document.cookie).toContain(LATE)
  })

  it('没有新写进来的：下一次启动照常把旧标记删掉', async () => {
    document.cookie = `ga_lo_p=${OTHER}.${LATE}; Path=/; Secure`
    fakeClient(SID)
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(auth.authenticated).toBe(true)
    expect(document.cookie).not.toMatch(/(^|; )ga_lo_p=/)
  })
})
