// 退出没得到服务端确认时（D-043、D-049）：记下要退出的会话号，下次启动刷新换来的正是它才补做退出、不恢复；
// 换来的是别人的会话就作废标记、照常恢复；登录成功后标记作废。

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { logoutMarkCookieName, useAuthStore } from './store'
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

function tok(accessToken: string, sessionId = SID): TokenResponse {
  return { accessToken, tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId }
}

function fakeClient(logoutResult: boolean, refreshSid = SID) {
  const client = {
    refresh: vi.fn(async () => tok('t', refreshSid)),
    get: vi.fn(async () => me),
    post: vi.fn(async () => tok('t2')),
    put: vi.fn(async () => null),
    logout: vi.fn(async () => logoutResult),
  } as unknown as Client
  setPortalContext({ portal: 'p', client })
  return client
}

/** 一个已经登录、知道自己会话号的标签页。 */
function signedIn() {
  const auth = useAuthStore()
  auth.setToken(tok('t0'))
  return auth
}

const marker = () => localStorage.getItem('ga.p.logoutPending')

describe('auth store: 没确认的退出', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  it('确认了的退出不留标记，下次启动照常恢复会话', async () => {
    const client = fakeClient(true)
    const auth = signedIn()
    await expect(auth.logout()).resolves.toBe(true)
    expect(marker()).toBeNull()
    setActivePinia(createPinia())
    const next = useAuthStore()
    await next.bootstrap()
    expect(client.refresh).toHaveBeenCalledTimes(1)
    expect(client.logout).toHaveBeenCalledTimes(1)
    expect(next.authenticated).toBe(true)
  })

  it('没确认的退出记下会话号；下次启动刷新换来的正是它就补做退出、不恢复；补做成功后标记清掉', async () => {
    const client = fakeClient(false)
    const auth = signedIn()
    await expect(auth.logout()).resolves.toBe(false)
    expect(auth.authenticated).toBe(false)
    expect(marker()).toBe(SID)

    // 恢复网络前再打开：刷新换来的是同一个会话 → 补做退出，仍失败，标记保留，会话不恢复
    setActivePinia(createPinia())
    let next = useAuthStore()
    await next.bootstrap()
    expect(client.refresh).toHaveBeenCalledTimes(1)
    expect(client.logout).toHaveBeenCalledTimes(2)
    expect(client.get).not.toHaveBeenCalled()
    expect(next.authenticated).toBe(false)
    expect(next.ready).toBe(true)
    expect(marker()).toBe(SID)

    // 恢复网络后再打开：补做成功，标记清掉，仍然是未登录（不会把旧会话恢复回来）
    ;(client.logout as ReturnType<typeof vi.fn>).mockResolvedValue(true)
    setActivePinia(createPinia())
    next = useAuthStore()
    await next.bootstrap()
    expect(client.refresh).toHaveBeenCalledTimes(2)
    expect(client.logout).toHaveBeenCalledTimes(3)
    expect(next.authenticated).toBe(false)
    expect(marker()).toBeNull()

    // 之后启动照常恢复
    setActivePinia(createPinia())
    next = useAuthStore()
    await next.bootstrap()
    expect(client.refresh).toHaveBeenCalledTimes(3)
    expect(client.logout).toHaveBeenCalledTimes(3)
    expect(next.authenticated).toBe(true)
  })

  it('标记在请求发出之前就写好：请求在途时关掉页面，下次打开也会补做退出', async () => {
    let settle: (v: boolean) => void = () => undefined
    const client = fakeClient(true)
    ;(client.logout as ReturnType<typeof vi.fn>).mockImplementation(() => new Promise<boolean>((r) => (settle = r)))
    const auth = signedIn()
    const p = auth.logout()
    expect(marker()).toBe(SID)
    settle(true)
    await expect(p).resolves.toBe(true)
    expect(marker()).toBeNull()
  })

  it('标记还在时重新登录：新会话建立，标记作废', async () => {
    fakeClient(false)
    const auth = signedIn()
    await auth.logout()
    expect(marker()).toBe(SID)
    await auth.login({ username: 'u', password: 'p' })
    expect(auth.authenticated).toBe(true)
    expect(marker()).toBeNull()
  })

  it('标记按端区分', async () => {
    fakeClient(false)
    const auth = signedIn()
    await auth.logout()
    const client = fakeClient(true)
    setPortalContext({ portal: 'other', client })
    setActivePinia(createPinia())
    const other = useAuthStore()
    await other.bootstrap()
    expect(client.logout).not.toHaveBeenCalled()
    expect(other.authenticated).toBe(true)
  })

  it('标记绑定会话（D-049）：Cookie 换来的是别人的会话时不退出别人，标记作废、照常恢复', async () => {
    // 标签页 1 是 A（会话 SID），退出没得到确认；之后（或之前）B 在同一浏览器登录了，Cookie 是 B 的会话
    const client = fakeClient(false)
    await signedIn().logout()
    expect(marker()).toBe(SID)
    ;(client.refresh as ReturnType<typeof vi.fn>).mockResolvedValue(tok('tb', OTHER))
    setActivePinia(createPinia())
    const next = useAuthStore()
    await next.bootstrap()
    expect(client.logout).toHaveBeenCalledTimes(1) // 只有 A 自己那一次
    expect(next.authenticated).toBe(true)
    expect(client.get).toHaveBeenCalledTimes(1)
    expect(marker()).toBeNull()
  })

  it('标记绑定会话（D-049）：刷新被服务端拒绝时那个会话在这个浏览器里已经补不了，标记作废；网络故障时标记留着', async () => {
    const client = fakeClient(false)
    await signedIn().logout()
    expect(marker()).toBe(SID)

    ;(client.refresh as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(-1, 'network error'))
    setActivePinia(createPinia())
    let next = useAuthStore()
    await next.bootstrap()
    expect(next.authenticated).toBe(false)
    expect(marker()).toBe(SID)

    ;(client.refresh as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(Codes.TokenInvalid, 'invalid', { status: 401 }))
    setActivePinia(createPinia())
    next = useAuthStore()
    await next.bootstrap()
    expect(next.authenticated).toBe(false)
    expect(marker()).toBeNull()
    expect(client.logout).toHaveBeenCalledTimes(1)
  })

  it('不知道会话号时不写标记；旧版本写的 "1" 标记被忽略并清掉', async () => {
    const client = fakeClient(false)
    await useAuthStore().logout()
    expect(marker()).toBeNull()

    localStorage.setItem('ga.p.logoutPending', '1')
    setActivePinia(createPinia())
    const next = useAuthStore()
    await next.bootstrap()
    expect(client.logout).toHaveBeenCalledTimes(1)
    expect(next.authenticated).toBe(true)
    expect(marker()).toBeNull()
  })
})

describe('auth store: localStorage 不可用时退出标记写进 Cookie（D-045）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  it('写不进 localStorage 时，没确认的退出照样在下次启动时补做、不恢复会话；补做成功后清掉', async () => {
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('quota', 'QuotaExceededError')
    })
    try {
      const client = fakeClient(false)
      await expect(signedIn().logout()).resolves.toBe(false)
      expect(document.cookie).toContain(`ga_lo_p=${SID}`)

      setActivePinia(createPinia())
      const next = useAuthStore()
      await next.bootstrap()
      expect(client.refresh).toHaveBeenCalledTimes(1)
      expect(client.logout).toHaveBeenCalledTimes(2)
      expect(next.authenticated).toBe(false)

      ;(client.logout as unknown as ReturnType<typeof vi.fn>).mockResolvedValue(true)
      setActivePinia(createPinia())
      await useAuthStore().bootstrap()
      expect(document.cookie).not.toContain(`ga_lo_p=${SID}`)
    } finally {
      setItem.mockRestore()
    }
  })
})

describe('auth store: 登录身份的代数（D-046）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
  })

  it('登录、退出时变，刷新令牌时不变', async () => {
    fakeClient(true)
    const auth = useAuthStore()
    const e0 = auth.epoch
    await auth.login({ username: 'u', password: 'p' })
    const e1 = auth.epoch
    expect(e1).not.toBe(e0)
    auth.setToken({ accessToken: 'r', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false })
    expect(auth.epoch).toBe(e1)
    await auth.logout()
    expect(auth.epoch).not.toBe(e1)
  })
})

describe('auth store: 标记写进了 localStorage、下次却读不出（D-046）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  it('Cookie 里也有一份，下次照样补做退出', async () => {
    const client = fakeClient(false)
    await expect(signedIn().logout()).resolves.toBe(false)
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('denied', 'SecurityError')
    })
    try {
      setActivePinia(createPinia())
      const next = useAuthStore()
      await next.bootstrap()
      expect(client.logout).toHaveBeenCalledTimes(2)
      expect(next.authenticated).toBe(false)
    } finally {
      getItem.mockRestore()
    }
  })
})

describe('auth store: 恢复登录态在途时用户登录了（D-047）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  it('恢复失败不清掉刚完成的登录', async () => {
    const client = fakeClient(true)
    let fail: (e: unknown) => void = () => undefined
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(() => new Promise((_, rej) => (fail = rej)))
    const auth = useAuthStore()
    const boot = auth.bootstrap()
    expect(client.refresh).toHaveBeenCalledTimes(1)
    await auth.login({ username: 'u', password: 'p' })
    expect(auth.authenticated).toBe(true)
    fail(new Error('superseded'))
    await boot
    expect(auth.authenticated).toBe(true)
    expect(auth.token).toBe('t2')
  })
})

describe('auth store: 会话号与迟到的退出（D-048）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  it('登录、刷新记下会话号，清除时丢掉', () => {
    fakeClient(true)
    const auth = useAuthStore()
    auth.setToken({ accessToken: 't', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId: 's1' })
    expect(auth.sessionId).toBe('s1')
    auth.clear()
    expect(auth.sessionId).toBeNull()
  })

  it('退出在途时有人重新登录了：迟到的退出不清掉新登录', async () => {
    const client = fakeClient(true)
    let done: (v: boolean) => void = () => undefined
    ;(client.logout as ReturnType<typeof vi.fn>).mockImplementationOnce(() => new Promise<boolean>((r) => (done = r)))
    ;(client.post as ReturnType<typeof vi.fn>).mockImplementation(async () => tok('B', OTHER))
    const auth = signedIn()
    const out = auth.logout()
    // 别的请求让页面回到登录页，另一个人登录了
    auth.clear()
    await auth.login({ username: 'b', password: 'p' })
    done(true)
    await out
    expect(auth.authenticated).toBe(true)
    expect(auth.token).toBe('B')
    expect(auth.sessionId).toBe(OTHER)
  })
})

describe('auth store: 重载后按标签页记下的会话恢复（D-050）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    sessionStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  const sidB = 'b'.repeat(32)

  it('登录记下会话号，清除时删掉', async () => {
    const client = fakeClient(true)
    ;(client.post as ReturnType<typeof vi.fn>).mockImplementation(async () => ({ accessToken: 'B', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId: sidB }))
    const auth = useAuthStore()
    await auth.login({ username: 'b', password: 'p' })
    expect(sessionStorage.getItem('ga.p.sid')).toBe(sidB)
    auth.clear()
    expect(sessionStorage.getItem('ga.p.sid')).toBeNull()
  })

  it('重载后浏览器里已经是别的账号：不恢复成那个账号，登录页说明原因；登录后复位', async () => {
    const client = fakeClient(true)
    sessionStorage.setItem('ga.p.sid', sidB)
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => {
      throw new ApiError(1004, 'switched', { status: 401, key: 'auth.sessionSwitched' })
    })
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(auth.sessionId).toBeNull()
    expect(auth.authenticated).toBe(false)
    expect(auth.accountSwitched).toBe(true)
    expect(sessionStorage.getItem('ga.p.sid')).toBeNull()
    await auth.login({ username: 'b', password: 'p' })
    expect(auth.accountSwitched).toBe(false)
  })

  it('重载前记下的会话号在启动刷新时交给请求层', async () => {
    const client = fakeClient(true)
    sessionStorage.setItem('ga.p.sid', sidB)
    let seen: string | null = 'unset'
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => {
      seen = useAuthStore().sessionId
      return { accessToken: 't', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false, sessionId: sidB }
    })
    await useAuthStore().bootstrap()
    expect(seen).toBe(sidB)
  })

  it('普通的登录过期不算换了账号', async () => {
    const client = fakeClient(true)
    sessionStorage.setItem('ga.p.sid', sidB)
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => {
      throw new ApiError(1004, 'expired', { status: 401 })
    })
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(auth.accountSwitched).toBe(false)
  })
})

describe('auth store: 标签页会话号与多条待补退出（D-051，规范 §13.2 第 87 条）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    sessionStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  it('启动时断网、服务端临时出错：这一页回到未登录，但标签页绑定的会话号留着，下次重载照样带上', async () => {
    const client = fakeClient(true)
    sessionStorage.setItem('ga.p.sid', SID)
    for (const err of [new ApiError(-1, 'network error'), new ApiError(5003, 'unavailable', { status: 503 })]) {
      ;(client.refresh as ReturnType<typeof vi.fn>).mockRejectedValueOnce(err)
      setActivePinia(createPinia())
      const auth = useAuthStore()
      await auth.bootstrap()
      expect(auth.authenticated).toBe(false)
      expect(auth.accountSwitched).toBe(false)
      expect(sessionStorage.getItem('ga.p.sid')).toBe(SID)
    }
    // 恢复之后重载：刷新仍然声明要续的是这个会话（Cookie 已经是别人的就会被拒，不会悄悄变成别人）
    let seen: string | null = 'unset'
    ;(client.refresh as ReturnType<typeof vi.fn>).mockImplementationOnce(async () => {
      seen = useAuthStore().sessionId
      return tok('t', SID)
    })
    setActivePinia(createPinia())
    await useAuthStore().bootstrap()
    expect(seen).toBe(SID)

    // 服务端明确说登录失效（401）才删
    ;(client.refresh as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(Codes.TokenInvalid, 'invalid', { status: 401 }))
    setActivePinia(createPinia())
    await useAuthStore().bootstrap()
    expect(sessionStorage.getItem('ga.p.sid')).toBeNull()
  })

  it('两个标签页各有一次没确认的退出：旧标签页迟到的退出成功只删自己那一条', async () => {
    const client = fakeClient(false)
    let settleA: (v: boolean) => void = () => undefined
    ;(client.logout as ReturnType<typeof vi.fn>)
      .mockImplementationOnce(() => new Promise<boolean>((r) => (settleA = r))) // 标签页 A：迟迟没回
      .mockImplementationOnce(async () => false) // 标签页 B：没确认
    setActivePinia(createPinia())
    const a = useAuthStore()
    a.setToken(tok('ta', SID))
    const outA = a.logout()

    setActivePinia(createPinia())
    const b = useAuthStore()
    b.setToken(tok('tb', OTHER))
    await expect(b.logout()).resolves.toBe(false)
    expect(marker()).toBe(`${SID}.${OTHER}`)

    settleA(true)
    await expect(outA).resolves.toBe(true)
    expect(marker()).toBe(OTHER) // B 那一条还在

    // 下次启动：Cookie 是 B 的会话，补做 B 的退出，不恢复
    ;(client.refresh as ReturnType<typeof vi.fn>).mockResolvedValueOnce(tok('t', OTHER))
    ;(client.logout as ReturnType<typeof vi.fn>).mockResolvedValueOnce(true)
    setActivePinia(createPinia())
    const next = useAuthStore()
    await next.bootstrap()
    expect(client.logout).toHaveBeenCalledTimes(3)
    expect(next.authenticated).toBe(false)
    expect(marker()).toBeNull()
  })

  it('刷新被拒是因为 Cookie 属于别的会话时，待补退出的记录不动（那个会话可能正是其中一条）', async () => {
    const client = fakeClient(false)
    await signedIn().logout()
    expect(marker()).toBe(SID)
    sessionStorage.setItem('ga.p.sid', OTHER)
    ;(client.refresh as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(1004, 'switched', { status: 401, key: 'auth.sessionSwitched' }))
    setActivePinia(createPinia())
    const auth = useAuthStore()
    await auth.bootstrap()
    expect(auth.accountSwitched).toBe(true)
    expect(marker()).toBe(SID)
  })

  it('登录带客户端标识；只作废登录之前就在的待补退出记录', async () => {
    const client = fakeClient(false)
    let finish: (t: TokenResponse) => void = () => undefined
    ;(client.post as ReturnType<typeof vi.fn>).mockImplementationOnce(() => new Promise<TokenResponse>((r) => (finish = r)))
    await signedIn().logout()
    expect(marker()).toBe(SID)
    setActivePinia(createPinia())
    const auth = useAuthStore()
    const p = auth.login({ username: 'u', password: 'p' })
    // 登录在途时，另一个标签页记下了一条
    setActivePinia(createPinia())
    const other = useAuthStore()
    other.setToken(tok('to', OTHER))
    await other.logout()
    expect(marker()).toBe(`${SID}.${OTHER}`)
    finish(tok('t2', 'c'.repeat(32)))
    await p
    expect(marker()).toBe(OTHER)
    const config = (client.post as ReturnType<typeof vi.fn>).mock.calls[0]?.[2] as { headers?: Record<string, string> } | undefined
    expect(config?.headers?.['X-GA-Client']).toBe('web')
  })
})

describe('auth store: 认证动作在等待之后核对登录身份（D-053，规范 §13.2 第 93 条）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    localStorage.clear()
    sessionStorage.clear()
    document.cookie = 'ga_lo_p=; Path=/; Max-Age=0'
  })

  /** 让某个接口挂起，返回"放行"函数。 */
  function hold<T>(fn: ReturnType<typeof vi.fn>, value: T): () => void {
    let release: () => void = () => undefined
    fn.mockImplementationOnce(() => new Promise<T>((r) => (release = () => r(value))))
    return () => release()
  }

  it('/auth/me 的结果回来之前退出了：不写回上一个人的资料、权限和菜单', async () => {
    const client = fakeClient(true)
    const auth = signedIn()
    const go = hold(client.get as ReturnType<typeof vi.fn>, me)
    const p = auth.fetchMe().catch((e: unknown) => e)
    auth.clear()
    go()
    const err = await p
    expect((err as ApiError).code).toBe(Codes.Superseded)
    expect(auth.user).toBeNull()
    expect(auth.authenticated).toBe(false)
  })

  it('登录结果回来之前本页被清了：不写令牌、不拉 /auth/me', async () => {
    const client = fakeClient(true)
    const auth = useAuthStore()
    const go = hold(client.post as ReturnType<typeof vi.fn>, tok('late', OTHER))
    const p = auth.login({ username: 'u', password: 'p' }).catch((e: unknown) => e)
    auth.clear()
    go()
    expect(((await p) as ApiError).code).toBe(Codes.Superseded)
    expect(auth.token).toBeNull()
    expect(client.get).not.toHaveBeenCalled()
  })

  it('锁屏、解锁、改密的请求回来之前退出了：不锁新身份的界面，不替现在的人拉 /auth/me', async () => {
    const client = fakeClient(true)
    let auth = signedIn()
    let go = hold(client.post as ReturnType<typeof vi.fn>, null)
    let p = auth.lock().catch((e: unknown) => e)
    auth.clear()
    go()
    expect(((await p) as ApiError).code).toBe(Codes.Superseded)
    expect(auth.locked).toBe(false)

    for (const act of ['unlock', 'changePassword'] as const) {
      setActivePinia(createPinia())
      auth = signedIn()
      const target = act === 'unlock' ? client.post : (client as unknown as { put: ReturnType<typeof vi.fn> }).put
      go = hold(target as ReturnType<typeof vi.fn>, null)
      p = (act === 'unlock' ? auth.unlock('pw') : auth.changePassword('a', 'b')).catch((e: unknown) => e)
      auth.clear()
      go()
      expect(((await p) as ApiError).code).toBe(Codes.Superseded)
    }
    expect(client.get).not.toHaveBeenCalled()
  })

  it('启动恢复的刷新回来之前用户已经登录了：不替新登录的人补做退出、拉 /auth/me', async () => {
    const client = fakeClient(true)
    const go = hold(client.refresh as ReturnType<typeof vi.fn>, tok('t', SID))
    const auth = useAuthStore()
    const p = auth.bootstrap()
    auth.setToken(tok('new', OTHER))
    auth.epoch++ // 登录成功时代数加一
    go()
    await p
    expect(client.get).not.toHaveBeenCalled()
    expect(client.logout).not.toHaveBeenCalled()
    expect(auth.token).toBe('new')
  })
})

// D-059：https 下待补退出标记的 Cookie 名带 __Host- 前缀，同一主域下的其他主机写不进同名 Cookie。
describe('logoutMarkCookieName (D-059)', () => {
  it('https 用 __Host- 前缀，http（本机开发）用原名', () => {
    expect(logoutMarkCookieName('p', true)).toBe('__Host-ga_lo_p')
    expect(logoutMarkCookieName('p', false)).toBe('ga_lo_p')
  })
})

describe('auth store: 主体端的主体（D-067）', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('取自 /auth/me 的 org，退出（clear）时清掉；平台端为 null', async () => {
    const client = fakeClient(true)
    ;(client.get as ReturnType<typeof vi.fn>).mockResolvedValueOnce({ ...me, org: { code: 'A12345678', name: '华东代理' } })
    const auth = useAuthStore()
    await auth.fetchMe()
    expect(auth.org).toEqual({ code: 'A12345678', name: '华东代理' })
    auth.clear()
    expect(auth.org).toBeNull()
    await auth.fetchMe()
    expect(auth.org).toBeNull()
  })
})
