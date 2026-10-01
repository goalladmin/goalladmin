// 规范 §13.3：单飞刷新与 409 重试。用自定义 axios 适配器模拟后端，不发真实请求。

import { describe, expect, it, vi } from 'vitest'
import type { AxiosAdapter, AxiosResponse, InternalAxiosRequestConfig } from 'axios'

import { createClient, RefreshRetryDelays } from './client'
import type { TokenResponse } from '../types'
import { ApiError, Codes } from '../types'

type Handler = (cfg: InternalAxiosRequestConfig) => { status: number; body: unknown }

function env(code: number, data: unknown = null, msg = code === 0 ? 'ok' : 'failed') {
  return { code, data, msg, requestId: 'r1' }
}

function tokenOf(cfg: InternalAxiosRequestConfig): string | undefined {
  const h = cfg.headers.get('Authorization')
  return typeof h === 'string' ? h.replace('Bearer ', '') : undefined
}

/** 造一个假后端：按 url 分发；记录全部请求。 */
function fakeBackend(routes: Record<string, Handler>) {
  const calls: { url: string; token?: string; client?: string }[] = []
  const adapter: AxiosAdapter = async (cfg) => {
    const c = cfg as InternalAxiosRequestConfig
    const url = c.url ?? ''
    calls.push({ url, token: tokenOf(c), client: c.headers.get('X-GA-Client') as string | undefined })
    const h = routes[url]
    if (!h) throw new Error('no route ' + url)
    const r = h(c)
    return { status: r.status, statusText: '', data: r.body, headers: {}, config: c } as AxiosResponse
  }
  return { adapter, calls }
}

function memToken() {
  let cur: string | null = 'old'
  const store = {
    get: () => cur,
    set: (t: TokenResponse) => {
      cur = t.accessToken
    },
    clear: () => {
      cur = null
    },
  }
  return store
}

const fresh: TokenResponse = { accessToken: 'new', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false }

describe('createClient', () => {
  it('带 Bearer、拆信封、失败抛 ApiError 并统一提示（silent 时不提示）', async () => {
    const be = fakeBackend({
      '/x': () => ({ status: 200, body: env(0, { a: 1 }) }),
      '/bad': () => ({ status: 200, body: env(Codes.Validation, { fields: [{ field: 'f', message: 'm' }] }, '校验失败') }),
    })
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, notify })
    await expect(c.get<{ a: number }>('/x')).resolves.toEqual({ a: 1 })
    expect(be.calls[0]?.token).toBe('old')

    const err = await c.get('/bad').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe(Codes.Validation)
    expect((err as ApiError).fields).toEqual([{ field: 'f', message: 'm' }])
    expect(notify).toHaveBeenCalledTimes(1)

    await c.get('/bad', { silent: true }).catch(() => undefined)
    expect(notify).toHaveBeenCalledTimes(1)
  })

  it('并发 401 只刷新一次，全部用新令牌重放', async () => {
    let refreshes = 0
    const be = fakeBackend({
      '/auth/refresh': () => {
        refreshes++
        return { status: 200, body: env(0, fresh) }
      },
      '/data': (cfg) =>
        tokenOf(cfg) === 'new' ? { status: 200, body: env(0, 'ok:' + tokenOf(cfg)) } : { status: 401, body: env(Codes.TokenInvalid) },
    })
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter })
    const results = await Promise.all([c.get('/data'), c.get('/data'), c.get('/data'), c.get('/data'), c.get('/data')])
    expect(results).toEqual(['ok:new', 'ok:new', 'ok:new', 'ok:new', 'ok:new'])
    expect(refreshes).toBe(1)
    expect(c.refreshCount).toBe(1)
    const refreshCalls = be.calls.filter((x) => x.url === '/auth/refresh')
    expect(refreshCalls).toHaveLength(1)
    expect(refreshCalls[0]?.client).toBe('web')
    expect(refreshCalls[0]?.token).toBeUndefined()
    // 5 次原请求 + 1 次刷新 + 5 次重放
    expect(be.calls).toHaveLength(11)
    expect(be.calls.filter((x) => x.url === '/data' && x.token === 'new')).toHaveLength(5)
  })

  it('刷新遇到 409 REFRESH_RETRY 时隔一会儿再试', async () => {
    let n = 0
    const be = fakeBackend({
      '/auth/refresh': () => (++n === 1 ? { status: 409, body: env(Codes.RefreshRetry) } : { status: 200, body: env(0, fresh) }),
    })
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, refreshRetryDelays: [1, 1] })
    await expect(c.refresh()).resolves.toEqual(fresh)
    expect(n).toBe(2)
    expect(c.refreshCount).toBe(1)
  })

  it('刷新连续 409：按间隔重试，别的标签页的新 Cookie 到了就成功（D-049）', async () => {
    // 模拟：第一个标签页的成功响应 30ms 后才到达浏览器（Cookie 才换成新的），期间的刷新都拿到 409
    const start = Date.now()
    const be = fakeBackend({
      '/auth/refresh': () => (Date.now() - start < 30 ? { status: 409, body: env(Codes.RefreshRetry) } : { status: 200, body: env(0, fresh) }),
    })
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, refreshRetryDelays: [5, 10, 20, 40] })
    await expect(c.refresh()).resolves.toEqual(fresh)
    expect(be.calls.length).toBeGreaterThan(2)
    expect(be.calls.length).toBeLessThanOrEqual(5)
  })

  it('刷新的重试间隔用完仍是 409 才放弃；默认间隔加起来在服务端 10 秒宽限期之内', async () => {
    const be = fakeBackend({ '/auth/refresh': () => ({ status: 409, body: env(Codes.RefreshRetry) }) })
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, refreshRetryDelays: [1, 1, 1] })
    const err = await c.refresh().catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(Codes.RefreshRetry)
    expect(be.calls).toHaveLength(4)
    expect(RefreshRetryDelays.reduce((a, b) => a + b, 0)).toBeLessThan(10_000)
    expect(RefreshRetryDelays.length).toBeGreaterThanOrEqual(3)
  })

  it('普通请求触发的刷新用完重试仍是 409：这次请求失败，但登录状态不清、不算登录失效（D-049）', async () => {
    const be = fakeBackend({
      '/auth/refresh': () => ({ status: 409, body: env(Codes.RefreshRetry) }),
      '/data': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
    })
    const token = memToken()
    const onAuthLost = vi.fn()
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token, adapter: be.adapter, onAuthLost, notify, refreshRetryDelays: [1] })
    const err = await c.get('/data').catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(Codes.RefreshRetry)
    expect(onAuthLost).not.toHaveBeenCalled()
    expect(token.get()).toBe('old')
    expect(notify).toHaveBeenCalledTimes(1)
    expect(be.calls.filter((x) => x.url === '/data')).toHaveLength(1)
    // 登出遇到这种情况也不算"服务端确认"
    const withLogout = fakeBackend({
      '/auth/refresh': () => ({ status: 409, body: env(Codes.RefreshRetry) }),
      '/auth/logout': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
    })
    const c2 = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: withLogout.adapter, refreshRetryDelays: [1] })
    await expect(c2.logout()).resolves.toBe(false)
  })

  it('同源标签页之间的刷新用 Web Locks 串起来；拿不到锁时直接刷新（D-049）', async () => {
    const be = fakeBackend({ '/auth/refresh': () => ({ status: 200, body: env(0, fresh) }) })
    const held: string[] = []
    const request = vi.fn(async (name: string, _o: { signal?: AbortSignal }, cb: () => Promise<unknown>) => {
      held.push(name)
      return cb()
    })
    const nav = globalThis.navigator as unknown as { locks?: unknown }
    const orig = nav.locks
    Object.defineProperty(globalThis.navigator, 'locks', { value: { request }, configurable: true })
    try {
      const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter })
      await expect(c.refresh()).resolves.toEqual(fresh)
      expect(held).toEqual(['ga.refresh:/api/p/v1'])
      expect(be.calls).toHaveLength(1)
      // 等锁超时（signal 触发 abort）：不等了，直接刷新
      request.mockImplementationOnce(
        (_n, o) => new Promise((_, rej) => o.signal?.addEventListener('abort', () => rej(new DOMException('aborted', 'AbortError')))),
      )
      vi.useFakeTimers()
      try {
        const p = c.refresh()
        await vi.advanceTimersByTimeAsync(6000)
        await expect(p).resolves.toEqual(fresh)
      } finally {
        vi.useRealTimers()
      }
      expect(be.calls).toHaveLength(2)
    } finally {
      Object.defineProperty(globalThis.navigator, 'locks', { value: orig, configurable: true })
    }
  })

  it('刷新失败：清令牌、通知登录失效、原请求失败', async () => {
    const be = fakeBackend({
      '/auth/refresh': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
      '/data': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
    })
    const token = memToken()
    const onAuthLost = vi.fn()
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token, adapter: be.adapter, onAuthLost, notify })
    const err = await c.get('/data').catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(Codes.TokenInvalid)
    expect(onAuthLost).toHaveBeenCalledTimes(1)
    expect(token.get()).toBeNull()
    expect(be.calls.map((x) => x.url)).toEqual(['/data', '/auth/refresh'])
  })

  it('登录失败（200 + 1001）不会触发刷新；认证接口本身的 401 也不刷新', async () => {
    const be = fakeBackend({
      '/auth/login': () => ({ status: 200, body: env(Codes.LoginFailed, { captchaRequired: true }, '账号或密码错误') }),
      '/auth/refresh': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
    })
    const onAuthLost = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, onAuthLost })
    const err = await c.post('/auth/login', { username: 'a', password: 'b' }, { silent: true }).catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(Codes.LoginFailed)
    expect(((err as ApiError).data as { captchaRequired: boolean }).captchaRequired).toBe(true)
    await c.refresh().catch(() => undefined)
    expect(onAuthLost).not.toHaveBeenCalled()
    expect(be.calls.map((x) => x.url)).toEqual(['/auth/login', '/auth/refresh'])
  })

  it('2002 必须改密时回调', async () => {
    const be = fakeBackend({ '/data': () => ({ status: 403, body: env(Codes.PwdChangeRequired) }) })
    const onPwdChangeRequired = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, onPwdChangeRequired })
    await c.get('/data').catch(() => undefined)
    expect(onPwdChangeRequired).toHaveBeenCalledTimes(1)
  })

  it('1006 锁屏时回调，静默请求也不例外，且不走普通提示', async () => {
    const be = fakeBackend({ '/data': () => ({ status: 423, body: env(Codes.SessionLocked) }) })
    const onSessionLocked = vi.fn()
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter, onSessionLocked, notify })
    const err = await c.put('/data', {}, { silent: true }).catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(Codes.SessionLocked)
    expect(onSessionLocked).toHaveBeenCalledTimes(1)
    expect(notify).not.toHaveBeenCalled()
  })

  it('网络错误变成 code -1 的 ApiError', async () => {
    const adapter: AxiosAdapter = async () => {
      throw new Error('Network Error')
    }
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter })
    const err = await c.get('/x').catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(-1)
  })
})

describe('Accept-Language（D-026）', () => {
  it('每个请求带上当前界面语言；调用方自己指定的不覆盖', async () => {
    const seen: (string | undefined)[] = []
    const adapter: AxiosAdapter = async (cfg) => {
      const c = cfg as InternalAxiosRequestConfig
      seen.push(c.headers.get('Accept-Language') as string | undefined)
      return { status: 200, statusText: '', data: env(0, 1), headers: {}, config: c } as AxiosResponse
    }
    let lang = 'ja-JP'
    const client = createClient({ baseURL: '/api', token: memToken(), adapter, locale: () => lang })
    await client.get('/a')
    lang = 'de-DE'
    await client.get('/b')
    await client.get('/c', { headers: { 'Accept-Language': 'fr-FR' } })
    expect(seen).toEqual(['ja-JP', 'de-DE', 'fr-FR'])
  })
  it('登出：服务端确认才算数；网络故障返回 false，服务端说会话已经结束（auth.sessionEnded）也算确认', async () => {
    const ok = fakeBackend({ '/auth/logout': () => ({ status: 200, body: env(0) }) })
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: ok.adapter }).logout()).toBe(true)
    expect(ok.calls[0]?.client).toBe('web')

    // 访问令牌过期 → 401 → 刷新被拒且服务端说会话已经结束：会话确实没了，算确认
    const gone = fakeBackend({
      '/auth/logout': () => ({ status: 401, body: env(1004) }),
      '/auth/refresh': () => ({ status: 401, body: { ...env(1004), key: 'auth.sessionEnded' } }),
    })
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: gone.adapter }).logout()).toBe(true)

    // 刷新成功后重放登出成功：算确认
    const stale = fakeBackend({
      '/auth/logout': (c) => (tokenOf(c) === 'new' ? { status: 200, body: env(0) } : { status: 401, body: env(1004) }),
      '/auth/refresh': () => ({ status: 200, body: env(0, fresh) }),
    })
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: stale.adapter }).logout()).toBe(true)

    // 401 之后刷新因为服务端故障或网络失败：会话多半还在，不算确认
    const refreshDown = fakeBackend({ '/auth/logout': () => ({ status: 401, body: env(1004) }), '/auth/refresh': () => ({ status: 500, body: env(5000) }) })
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: refreshDown.adapter }).logout()).toBe(false)
    const refreshNet = fakeBackend({ '/auth/logout': () => ({ status: 401, body: env(1004) }) }) // 刷新没有路由 → 网络错误
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: refreshNet.adapter }).logout()).toBe(false)

    const down = fakeBackend({ '/auth/logout': () => ({ status: 500, body: env(5000) }) })
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: down.adapter }).logout()).toBe(false)

    const netErr: AxiosAdapter = async () => {
      throw new Error('Network Error')
    }
    const notify = vi.fn()
    expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: netErr, notify }).logout()).toBe(false)
    expect(notify).not.toHaveBeenCalled()
  })
})

describe('换了登录身份的旧请求（D-046）', () => {
  it('发出后登录身份变了：收到 401 不刷新、不重放，也不提示', async () => {
    let epoch = 1
    let cur: string | null = 'A'
    const store = {
      get: () => cur,
      set: (t: TokenResponse) => {
        cur = t.accessToken
      },
      clear: () => {
        cur = null
      },
      epoch: () => epoch,
    }
    let release: () => void = () => undefined
    const slow = new Promise<void>((r) => (release = r))
    const calls: { url: string; token?: string }[] = []
    const adapter: AxiosAdapter = async (cfg) => {
      const c = cfg as InternalAxiosRequestConfig
      calls.push({ url: c.url ?? '', token: tokenOf(c) })
      if (c.url === '/write') {
        await slow
        return { status: 401, statusText: '', data: env(Codes.TokenInvalid), headers: {}, config: c } as AxiosResponse
      }
      return { status: 200, statusText: '', data: env(0, { accessToken: 'B2', tokenType: 'Bearer', expiresIn: 900, mustChangePwd: false }), headers: {}, config: c } as AxiosResponse
    }
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter, notify })
    const pending = c.post('/write', { x: 1 }).catch((e: unknown) => e)
    // 请求已经发出去了（到了适配器）；没发出去之前换人的情况见 D-052 的用例
    await vi.waitFor(() => expect(calls).toHaveLength(1))
    // A 退出、B 在同一页面登录
    epoch = 3
    cur = 'B'
    release()
    const err = await pending
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe(Codes.Superseded)
    expect(calls.map((x) => x.url)).toEqual(['/write'])
    expect(notify).not.toHaveBeenCalled()
  })

  it('身份没变时照常刷新并重放', async () => {
    const store = { ...memToken(), epoch: () => 7 }
    let n = 0
    const be = fakeBackend({
      '/auth/refresh': () => ({ status: 200, body: env(0, fresh) }),
      '/write': () => (n++ === 0 ? { status: 401, body: env(Codes.TokenInvalid) } : { status: 200, body: env(0, 'done') }),
    })
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter })
    await expect(c.post('/write', {})).resolves.toBe('done')
    expect(be.calls.map((x) => x.url)).toEqual(['/write', '/auth/refresh', '/write'])
  })
})

/** 可以手动推进登录身份代数的令牌存储。 */
function epochToken() {
  const s = {
    cur: 'A' as string | null,
    epoch: 1,
    sets: [] as string[],
    clears: 0,
    get: () => s.cur,
    set: (t: TokenResponse) => {
      s.sets.push(t.accessToken)
      s.cur = t.accessToken
    },
    clear: () => {
      s.clears++
      s.cur = null
    },
    epochFn: () => s.epoch,
  }
  return { store: { get: s.get, set: s.set, clear: s.clear, epoch: s.epochFn }, s }
}

/** 假后端：指定的 url 等 release 之后才回。 */
function gatedBackend(routes: Record<string, Handler>, gated: string[]) {
  let release: () => void = () => undefined
  const gate = new Promise<void>((r) => (release = r))
  const be = fakeBackend(routes)
  const adapter: AxiosAdapter = async (cfg) => {
    if (gated.includes(cfg.url ?? '')) await gate
    return be.adapter(cfg)
  }
  return { adapter, calls: be.calls, release }
}

const tick = () => new Promise((r) => setTimeout(r, 0))

describe('换了登录身份：所有结果都作废（D-047）', () => {
  it('成功的响应也不交给现在的页面', async () => {
    const { store, s } = epochToken()
    const be = gatedBackend({ '/list': () => ({ status: 200, body: env(0, ['A 的数据']) }) }, ['/list'])
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, notify })
    const pending = c.get('/list').catch((e: unknown) => e)
    await tick()
    s.epoch = 2
    be.release()
    const err = await pending
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).code).toBe(Codes.Superseded)
    expect(notify).not.toHaveBeenCalled()
  })

  it('上一个人的 1006、2002 不触发锁屏和改密跳转', async () => {
    const { store, s } = epochToken()
    const be = gatedBackend(
      {
        '/a': () => ({ status: 200, body: env(Codes.SessionLocked) }),
        '/b': () => ({ status: 403, body: env(Codes.PwdChangeRequired) }),
      },
      ['/a', '/b'],
    )
    const onSessionLocked = vi.fn()
    const onPwdChangeRequired = vi.fn()
    const notify = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, onSessionLocked, onPwdChangeRequired, notify })
    const a = c.get('/a').catch((e: unknown) => e)
    const b = c.get('/b').catch((e: unknown) => e)
    await tick()
    s.epoch = 2
    be.release()
    expect(((await a) as ApiError).code).toBe(Codes.Superseded)
    expect(((await b) as ApiError).code).toBe(Codes.Superseded)
    expect(onSessionLocked).not.toHaveBeenCalled()
    expect(onPwdChangeRequired).not.toHaveBeenCalled()
    expect(notify).not.toHaveBeenCalled()
  })

  it('刷新在途时换了人：刷新结果不写令牌，失败也不清掉现在的登录', async () => {
    const { store, s } = epochToken()
    let n = 0
    const be = gatedBackend(
      {
        '/auth/refresh': () => ({ status: 200, body: env(0, { ...fresh, accessToken: 'A2' }) }),
        '/write': () => (n++ === 0 ? { status: 401, body: env(Codes.TokenInvalid) } : { status: 200, body: env(0, 'done') }),
      },
      ['/auth/refresh'],
    )
    const onAuthLost = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, onAuthLost })
    const pending = c.post('/write', {}).catch((e: unknown) => e)
    await tick()
    // A 的刷新还在途，B 登录了
    s.epoch = 2
    s.cur = 'B'
    be.release()
    expect(((await pending) as ApiError).code).toBe(Codes.Superseded)
    expect(s.sets).toEqual([])
    expect(s.cur).toBe('B')
    expect(s.clears).toBe(0)
    expect(onAuthLost).not.toHaveBeenCalled()
    expect(be.calls.filter((x) => x.url === '/write')).toHaveLength(1)

    // 刷新失败的情形同样不清
    const f = epochToken()
    const be2 = gatedBackend(
      { '/auth/refresh': () => ({ status: 401, body: env(Codes.TokenInvalid) }), '/write': () => ({ status: 401, body: env(Codes.TokenInvalid) }) },
      ['/auth/refresh'],
    )
    const c2 = createClient({ baseURL: '/api/p/v1', token: f.store, adapter: be2.adapter, onAuthLost })
    const p2 = c2.post('/write', {}).catch((e: unknown) => e)
    await tick()
    f.s.epoch = 2
    f.s.cur = 'B'
    be2.release()
    expect(((await p2) as ApiError).code).toBe(Codes.Superseded)
    expect(f.s.clears).toBe(0)
    expect(onAuthLost).not.toHaveBeenCalled()
  })

  it('现在的人的请求碰上上一个人的刷新：自己再刷新一次', async () => {
    const { store, s } = epochToken()
    let refreshes = 0
    let writes = 0
    const be = gatedBackend(
      {
        '/auth/refresh': () => ({ status: 200, body: env(0, { ...fresh, accessToken: `R${++refreshes}` }) }),
        '/old': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
        '/new': () => (writes++ === 0 ? { status: 401, body: env(Codes.TokenInvalid) } : { status: 200, body: env(0, 'done') }),
      },
      ['/auth/refresh'],
    )
    const onAuthLost = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, onAuthLost })
    const old = c.post('/old', {}).catch((e: unknown) => e)
    await tick()
    // A 的刷新在途；B 登录，B 的请求也 401，加入了 A 的刷新
    s.epoch = 2
    s.cur = 'B'
    const mine = c.post('/new', {})
    await tick()
    be.release()
    expect(((await old) as ApiError).code).toBe(Codes.Superseded)
    await expect(mine).resolves.toBe('done')
    expect(s.sets).toEqual(['R2'])
    expect(onAuthLost).not.toHaveBeenCalled()
  })

  it('登录失效时先通知再清：回调还能看到失效前是否已登录', async () => {
    let authenticated = true
    const seen: boolean[] = []
    const store = {
      get: () => 'A',
      set: () => undefined,
      clear: () => {
        authenticated = false
      },
      epoch: () => 1,
    }
    const be = fakeBackend({
      '/auth/refresh': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
      '/x': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
    })
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, onAuthLost: () => seen.push(authenticated) })
    await expect(c.get('/x')).rejects.toBeInstanceOf(ApiError)
    expect(seen).toEqual([true])
    expect(authenticated).toBe(false)
  })
})

describe('刷新绑定会话（D-048）', () => {
  it('知道自己的会话号时刷新带上 X-GA-Session；页面刚打开时不带', async () => {
    const seen: (string | undefined)[] = []
    const adapter: AxiosAdapter = async (cfg) => {
      const c = cfg as InternalAxiosRequestConfig
      seen.push(c.headers.get('X-GA-Session') as string | undefined)
      return { status: 200, statusText: '', data: env(0, fresh), headers: {}, config: c } as AxiosResponse
    }
    let sid: string | null = 'sA'
    const c = createClient({ baseURL: '/api/p/v1', token: { ...memToken(), session: () => sid }, adapter })
    await c.refresh()
    sid = null
    await c.refresh()
    expect(seen).toEqual(['sA', undefined])
  })
})

describe('刷新的时序与临时故障（D-050）', () => {
  it('拿到锁之后刷新超过 5 秒再失败：不在锁外再刷一次', async () => {
    let calls = 0
    const adapter: AxiosAdapter = async (cfg) => {
      calls++
      await new Promise((r) => setTimeout(r, 7000))
      throw Object.assign(new Error('timeout of 7000ms exceeded'), { config: cfg })
    }
    const request = vi.fn(async (_n: string, _o: { signal?: AbortSignal }, cb: () => Promise<unknown>) => cb())
    const nav = globalThis.navigator as unknown as { locks?: unknown }
    const orig = nav.locks
    Object.defineProperty(globalThis.navigator, 'locks', { value: { request }, configurable: true })
    vi.useFakeTimers()
    try {
      const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter })
      const p = c.refresh().catch((e: unknown) => e)
      await vi.advanceTimersByTimeAsync(20_000)
      expect(await p).toBeInstanceOf(Error)
      expect(calls).toBe(1)
    } finally {
      vi.useRealTimers()
      Object.defineProperty(globalThis.navigator, 'locks', { value: orig, configurable: true })
    }
  })

  it('刷新遇到 5xx 或断网：登录状态留着，这次请求失败，不回登录页', async () => {
    for (const refresh of [
      () => ({ status: 503, body: env(5003) }),
      () => {
        throw new Error('Network Error')
      },
    ]) {
      const be = fakeBackend({ '/auth/refresh': refresh, '/data': () => ({ status: 401, body: env(Codes.TokenInvalid) }) })
      const token = memToken()
      const onAuthLost = vi.fn()
      const c = createClient({ baseURL: '/api/p/v1', token, adapter: be.adapter, onAuthLost })
      const err = await c.get('/data').catch((e: unknown) => e)
      expect(err).toBeInstanceOf(ApiError)
      expect((err as ApiError).status).not.toBe(401)
      expect(onAuthLost).not.toHaveBeenCalled()
      expect(token.get()).toBe('old')
    }
  })

  it('刷新被拒的翻译键带到错误上：前端据此分辨"换了账号"', async () => {
    const be = fakeBackend({ '/auth/refresh': () => ({ status: 401, body: { ...env(Codes.TokenInvalid), key: 'auth.sessionSwitched' } }) })
    const c = createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter })
    const err = await c.refresh().catch((e: unknown) => e)
    expect((err as ApiError).key).toBe('auth.sessionSwitched')
  })
})

describe('身份绑在请求和刷新发起的那一刻（D-052，规范 §13.2 第 89 条）', () => {
  function identityStore() {
    const st = { epoch: 1, cur: 'A' as string | null, sid: 'a'.repeat(32) as string | null }
    const store = {
      get: () => st.cur,
      set: (t: TokenResponse) => {
        st.cur = t.accessToken
      },
      clear: () => {
        st.epoch++
        st.cur = null
        st.sid = null
      },
      epoch: () => st.epoch,
      session: () => st.sid,
    }
    return { st, store }
  }

  it('请求还没发出去就换了人：不发出去，更不会带着新身份的令牌发出去', async () => {
    const { st, store } = identityStore()
    const be = fakeBackend({ '/write': () => ({ status: 200, body: env(0) }) })
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter })
    const pending = c.post('/write', { x: 1 }).catch((e: unknown) => e)
    // 同一轮事件循环里（axios 的异步拦截器还没跑完）A 退出、B 登录
    st.epoch = 3
    st.cur = 'B'
    const err = await pending
    expect((err as ApiError).code).toBe(Codes.Superseded)
    expect(be.calls).toEqual([])

    // 身份没变的请求照常带自己的令牌
    await c.post('/write', { x: 2 })
    expect(be.calls.map((x) => x.token)).toEqual(['B'])
  })

  it('令牌在请求进入时就取好：发出时读到的不是后来的令牌', async () => {
    const { st, store } = identityStore()
    const be = fakeBackend({ '/x': () => ({ status: 200, body: env(0) }) })
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter })
    const p = c.get('/x')
    st.cur = 'A2' // 同一身份刷新了令牌（代数不变）：这次请求仍用进入时的令牌，服务端按同一个人验
    await p
    expect(be.calls[0]?.token).toBe('A')
  })

  it('刷新在等跨标签页的锁时这一页退出了：拿到锁之后不再刷新，不把令牌写回来', async () => {
    const { st, store } = identityStore()
    let grant: () => void = () => undefined
    const locks = {
      request: (_n: string, _o: unknown, cb: () => Promise<unknown>) => new Promise((r) => (grant = () => r(cb()))),
    }
    const nav = globalThis.navigator as unknown as { locks?: unknown }
    const saved = nav.locks
    Object.defineProperty(globalThis.navigator, 'locks', { value: locks, configurable: true })
    try {
      const be = fakeBackend({ '/auth/refresh': () => ({ status: 200, body: env(0, fresh) }) })
      const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter })
      const r = c.refresh().catch((e: unknown) => e)
      await Promise.resolve()
      store.clear() // 本地退出（服务端没确认，Cookie 还有效）
      grant()
      const err = await r
      expect((err as ApiError).code).toBe(Codes.Superseded)
      expect(be.calls).toEqual([])
      expect(st.cur).toBeNull()
      expect(st.sid).toBeNull()
    } finally {
      Object.defineProperty(globalThis.navigator, 'locks', { value: saved, configurable: true })
    }
  })

  it('刷新在重试间隔里这一页退出了：不再发下一次', async () => {
    const { store } = identityStore()
    let n = 0
    const be = fakeBackend({
      '/auth/refresh': () => {
        n++
        if (n === 1) setTimeout(() => store.clear(), 5) // 在等下一次重试的间隔里退出
        return { status: 409, body: env(Codes.RefreshRetry) }
      },
    })
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, refreshRetryDelays: [40, 40] })
    const err = await c.refresh().catch((e: unknown) => e)
    expect((err as ApiError).code).toBe(Codes.Superseded)
    expect(n).toBe(1)
  })
})

describe('退出确认只认"会话已经结束"（D-052，规范 §13.2 第 90 条）', () => {
  it('刷新 401 但不是 auth.sessionEnded（Cookie 属于别的会话、凭证对不上、没有 Cookie）：不算确认', async () => {
    for (const body of [{ ...env(1004), key: 'auth.sessionSwitched' }, env(1004)]) {
      const be = fakeBackend({ '/auth/logout': () => ({ status: 401, body: env(1004) }), '/auth/refresh': () => ({ status: 401, body }) })
      expect(await createClient({ baseURL: '/api/p/v1', token: memToken(), adapter: be.adapter }).logout()).toBe(false)
    }
  })
})

describe('没有登录身份的请求不自动刷新（D-055，规范 §13.2 第 100 条）', () => {
  it('发出时没有令牌：401 就是 401，不按浏览器里的 Cookie 刷新出另一个身份', async () => {
    let refreshes = 0
    const be = fakeBackend({
      '/x': () => ({ status: 401, body: env(Codes.TokenInvalid) }),
      '/auth/refresh': () => {
        refreshes++
        return { status: 200, body: env(0, fresh) }
      },
    })
    const store = { ...memToken() }
    store.clear()
    const onAuthLost = vi.fn()
    const c = createClient({ baseURL: '/api/p/v1', token: store, adapter: be.adapter, onAuthLost })
    const err = await c.get('/x', { silent: true }).catch((e: unknown) => e)
    expect((err as ApiError).status).toBe(401)
    expect(refreshes).toBe(0)
    expect(store.get()).toBeNull()

    // 显式的刷新（启动恢复）照常
    await c.refresh()
    expect(refreshes).toBe(1)
  })
})
