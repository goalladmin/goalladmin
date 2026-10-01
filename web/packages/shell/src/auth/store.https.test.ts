// @vitest-environment-options {"url": "https://app.test/"}

// https 下"待补退出"标记的 Cookie（D-059、D-060）：新标记写在 __Host- 前缀的名字下；旧名字的标记只作"不恢复"的依据，
// 不凭它补做退出（它可能是同主域的其他主机塞进来的）；换来的会话不在旧标记里，旧标记删掉。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useAuthStore } from './store'
import { setPortalContext } from '../context'
import type { Client } from '../request/client'
import type { MeResponse, TokenResponse } from '../types'

const me: MeResponse = {
  user: { id: 1, username: 'u', displayName: 'U', avatar: '', mustChangePwd: false, super: false },
  perms: [],
  menus: [],
}
const SID = 'a'.repeat(32)
const OTHER = 'b'.repeat(32)

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
})
