// Axios 实例工厂（规范 §3.5、§5.5）：
//   - 自动带 Bearer；
//   - 401 时单飞刷新后重放原请求；刷新遇到 409 REFRESH_RETRY 隔一小段时间再试（几次都在服务端的宽限期内）；
//     同源的标签页之间用 Web Locks 把刷新串起来；刷新被服务端拒绝（401）视为登录失效，重试用尽的 409 只让这次请求失败；
//   - 信封统一拆包：成功返回 data，失败抛 ApiError 并（除非 silent）统一提示。
// 业务页面拿到的是 Client，不直接接触 axios。

import axios from 'axios'
import type { AxiosAdapter, AxiosError, AxiosInstance, AxiosRequestConfig, AxiosResponse } from 'axios'

import { RefreshGate } from './refresh'
import { ApiError, Codes } from '../types'
import type { Envelope, TokenResponse } from '../types'

declare module 'axios' {
  export interface AxiosRequestConfig {
    /** 失败时不弹统一提示（页面自己处理，例如登录表单）。 */
    silent?: boolean
    /** 不带 Authorization。 */
    skipAuth?: boolean
    /** 内部：已经刷新并重放过一次。 */
    _retried?: boolean
    /** 内部：请求进入 request() 时的登录身份代数；重放沿用第一次的（D-052）。 */
    _epoch?: number
    /** 内部：请求进入 request() 时取下的访问令牌，发送时用它，不在发送那一刻再去读（D-052）。 */
    _token?: string | null
  }
}

/** 登录、刷新、登出接口要求的客户端标识头（规范 §5.2，登录从 D-051 起）。 */
export const HeaderClient = 'X-GA-Client'

/** 刷新时声明要续的会话（D-048）。 */
export const HeaderSession = 'X-GA-Session'

/**
 * 刷新失败、且服务端确定会话已经结束时的翻译键（D-052）。退出没被确认时，只有刷新回的是它才能算"会话本来就没了"；
 * 别的 401（Cookie 属于别的会话、凭证对不上、没有 Cookie）只说明这次刷新做不成，会话可能还活着。
 */
export const SessionEndedKey = 'auth.sessionEnded'

export interface TokenStore {
  get(): string | null
  set(t: TokenResponse): void
  clear(): void
  /**
   * 登录身份的代数：登录、退出、登录失效时变，刷新令牌不变（D-046、D-047）。请求和刷新发出时记下它；结果回来时代数已经变了，
   * 说明这期间换了人（A 退出、B 在同一页面登录），这个结果属于上一个人：不刷新、不重放、不写令牌、不触发锁屏或改密跳转、
   * 不提示，一律按 Codes.Superseded 失败——否则 A 的请求会以 B 的身份重放，A 的刷新结果、/auth/me 会盖掉 B 的登录状态。
   * 不提供时不做这项检查。
   */
  epoch?(): number
  /**
   * 当前登录的会话号（D-048）。同一浏览器的标签页共用一个刷新 Cookie：另一个标签页退出、换人登录之后，Cookie 就是别人的会话。
   * 刷新时带上它，服务端发现 Cookie 不是这个会话就拒绝、不动 Cookie，这个标签页按登录失效处理，而不是悄悄变成别人。
   * 返回空（页面刚打开、还不知道自己是谁）时按 Cookie 刷新。不提供时不带。
   */
  session?(): string | null
}

export interface ClientOptions {
  /** 例如 /api/platform/v1 */
  baseURL: string
  token: TokenStore
  /** 刷新失败：登录已失效。先于 TokenStore.clear 调用，回调里还能看到失效前是否已登录。 */
  onAuthLost?: () => void
  /** 收到 2002：必须先修改密码。 */
  onPwdChangeRequired?: () => void
  /** 收到 1006：会话已锁屏（D-027）。静默请求也会触发，锁屏界面不能因为调用方不提示而漏掉。 */
  onSessionLocked?: () => void
  /** 统一提示。 */
  notify?: (err: ApiError) => void
  /** 当前界面语言，每个请求放进 Accept-Language，后端的提示随之变化（D-026）。 */
  locale?: () => string
  /** 测试用：替换 axios 的适配器。 */
  adapter?: AxiosAdapter
  timeout?: number
  /**
   * 刷新收到 409 后每次重试前等待的毫秒数（默认 RefreshRetryDelays）：多个标签页同时刷新时，第一个的成功响应还没到达浏览器，
   * 其余的仍带着旧 Cookie，会拿到 409；隔一会儿再试，Cookie 就换成新的了。全部用完仍是 409 才放弃（测试用它缩短等待）。
   */
  refreshRetryDelays?: number[]
}

/** 409 REFRESH_RETRY 的默认重试间隔：累计约 3.7 秒，远在服务端 10 秒的宽限期之内（D-049）。 */
export const RefreshRetryDelays = [200, 500, 1000, 2000]

/** 跨标签页刷新锁等待的上限（毫秒）：拿不到锁就不等了，直接刷新（靠 409 重试兜底），免得被一个卡住的标签页拖住。 */
const RefreshLockWait = 5000

interface LockManagerLike {
  request<T>(name: string, options: { signal?: AbortSignal }, callback: () => Promise<T>): Promise<T>
}

/**
 * 同源的标签页之间把刷新串起来（Web Locks，D-049）：同一个会话的两个标签页同时刷新，后一个会拿着已经换掉的旧 Cookie，
 * 服务端只能回 409；排队之后，轮到它时浏览器里已经是新 Cookie。拿不到 Web Locks（不安全的上下文、旧浏览器、测试环境）
 * 或等锁超时时直接执行，仍有 409 重试兜底。
 */
async function withRefreshLock<T>(name: string, fn: () => Promise<T>): Promise<T> {
  const locks = (globalThis.navigator as { locks?: LockManagerLike } | undefined)?.locks
  if (!locks || typeof locks.request !== 'function' || typeof AbortController === 'undefined') return fn()
  const ac = new AbortController()
  const timer = setTimeout(() => ac.abort(), RefreshLockWait)
  // 拿到锁就停掉计时（D-050）：锁内的刷新花了 5 秒以上再失败时，失败不能被当成"等锁超时"再在锁外刷一次——
  // 那次刷新可能已经在服务端轮换了凭证、只是响应丢了，第二次带着旧 Cookie 过了宽限期就会被当成重放、整个会话被吊销
  let granted = false
  try {
    return await locks.request(name, { signal: ac.signal }, () => {
      granted = true
      clearTimeout(timer)
      return fn()
    })
  } catch (e) {
    if (!granted && ac.signal.aborted) return fn()
    throw e
  } finally {
    clearTimeout(timer)
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms))
}

export interface Client {
  request<T = unknown>(config: AxiosRequestConfig): Promise<T>
  get<T = unknown>(url: string, config?: AxiosRequestConfig): Promise<T>
  post<T = unknown>(url: string, body?: unknown, config?: AxiosRequestConfig): Promise<T>
  put<T = unknown>(url: string, body?: unknown, config?: AxiosRequestConfig): Promise<T>
  delete<T = unknown>(url: string, config?: AxiosRequestConfig): Promise<T>
  /** 单飞刷新：并发调用共享一次请求。成功后令牌已写入 TokenStore。 */
  refresh(): Promise<TokenResponse>
  /** 登出（吊销当前会话）。失败也不抛，返回服务端是否确认吊销（会话本来已失效也算）：本地状态无论如何都会清掉。 */
  logout(): Promise<boolean>
  readonly axios: AxiosInstance
  /** 真实发起过的刷新次数（测试用）。 */
  readonly refreshCount: number
}

const AUTH_PATHS = ['/auth/login', '/auth/refresh', '/auth/captcha']

function isEnvelope(v: unknown): v is Envelope {
  return typeof v === 'object' && v !== null && 'code' in v && 'msg' in v
}

function isAuthPath(url: string | undefined): boolean {
  if (!url) return false
  return AUTH_PATHS.some((p) => url.endsWith(p))
}

export function createClient(opts: ClientOptions): Client {
  // 真正发出去的前一刻再核对一次登录身份（D-052）：请求进入 request() 之后、适配器发出之前还隔着 axios 的异步拦截器链，
  // 这段时间里换了人（退出、登录失效、换人登录）的话，请求直接作废、不发出去——写操作一旦发出，服务端就执行了，
  // 响应回来时再作废已经晚了。
  const baseAdapter = axios.getAdapter(opts.adapter ?? axios.defaults.adapter)
  const inst = axios.create({
    baseURL: opts.baseURL,
    timeout: opts.timeout ?? 30_000,
    adapter: (config) => {
      if (config._epoch !== undefined && opts.token.epoch?.() !== config._epoch) {
        return Promise.reject(superseded())
      }
      return baseAdapter(config)
    },
    headers: { 'Content-Type': 'application/json' },
  })

  inst.interceptors.request.use((config) => {
    // 令牌在请求进入 request() 时就取好了（_token），和那一刻的登录身份绑在一起；这里不再读"现在的"令牌（D-052）：
    // 拦截器是异步执行的，读现在的令牌的话，排队期间换了人，旧页面的请求就会带着新身份的令牌发出去
    if (!config.skipAuth && config._token) config.headers.set('Authorization', `Bearer ${config._token}`)
    const lang = opts.locale?.()
    if (lang && !config.headers.has('Accept-Language')) config.headers.set('Accept-Language', lang)
    return config
  })

  const retryDelays = opts.refreshRetryDelays ?? RefreshRetryDelays
  const lockName = `ga.refresh:${opts.baseURL}`

  // 一次刷新：POST /auth/refresh；409 REFRESH_RETRY 时隔一会儿再试（等浏览器收到别的标签页换来的新 Cookie），
  // 间隔用完仍是 409 才放弃。整个过程在跨标签页的刷新锁里。
  // 登录身份和会话号在发起刷新的那一刻就记下（D-052），不是拿到锁之后：等锁期间这一页退出了（本地已经清掉），
  // 拿到锁之后再记的话，记下的是"退出之后"的身份，刷新就会按浏览器里还有效的 Cookie 把令牌写回来。
  const refreshOnce = () => {
    const epoch = opts.token.epoch?.()
    const sid = opts.token.session?.()
    return withRefreshLock(lockName, () => refreshInLock(epoch, sid))
  }
  async function refreshInLock(epoch: number | undefined, sid: string | null | undefined): Promise<TokenResponse> {
    const changed = () => epoch !== undefined && opts.token.epoch?.() !== epoch
    const send = () => {
      // 每次发出去之前都核对：等锁、等重试间隔的时候都可能换了人
      if (changed()) return Promise.reject(superseded())
      return inst.post<Envelope<TokenResponse>>('/auth/refresh', null, {
        skipAuth: true,
        silent: true,
        headers: sid ? { [HeaderClient]: 'web', [HeaderSession]: sid } : { [HeaderClient]: 'web' },
        validateStatus: () => true, // 自己判 code，不让 axios 先抛
      })
    }
    let res = await send()
    for (const wait of retryDelays) {
      if (res.data?.code !== Codes.RefreshRetry) break
      if (changed()) break
      await sleep(wait)
      res = await send()
    }
    if (changed()) {
      // 刷新在途时换了登录身份：结果属于上一个人，不写令牌（D-047）
      throw superseded()
    }
    const env = res.data
    if (!isEnvelope(env) || env.code !== Codes.OK) {
      throw new ApiError(env?.code ?? Codes.TokenInvalid, env?.msg ?? 'refresh failed', {
        status: res.status,
        requestId: env?.requestId,
        key: env?.key,
      })
    }
    opts.token.set(env.data)
    return env.data
  }
  const gate = new RefreshGate<TokenResponse>(refreshOnce)

  // 单飞刷新可能是上一个人发起的：它作废了而这个请求属于现在的人，就自己再刷新一次
  async function refreshFor(stale: () => boolean): Promise<TokenResponse> {
    try {
      return await gate.refresh()
    } catch (e) {
      if (e instanceof ApiError && e.code === Codes.Superseded && !stale()) return gate.refresh()
      throw e
    }
  }

  function superseded(cause?: unknown): ApiError {
    return new ApiError(Codes.Superseded, 'superseded: the signed-in identity changed', { cause })
  }

  function fail(err: ApiError, config: AxiosRequestConfig | undefined): never {
    if (err.code === Codes.PwdChangeRequired) opts.onPwdChangeRequired?.()
    if (err.code === Codes.SessionLocked) opts.onSessionLocked?.()
    if (!config?.silent) opts.notify?.(err)
    throw err
  }

  function toApiError(res: AxiosResponse, cause?: unknown): ApiError {
    const env = res.data
    if (isEnvelope(env)) {
      return new ApiError(env.code, env.msg, { data: env.data, requestId: env.requestId, status: res.status, key: env.key, params: env.params, cause })
    }
    return new ApiError(Codes.Internal, `HTTP ${res.status}`, { status: res.status, cause })
  }

  async function request<T>(config: AxiosRequestConfig): Promise<T> {
    // 重放沿用第一次进来时的代数（D-052）：重放是为同一个身份做的
    const epoch = config._epoch ?? opts.token.epoch?.()
    // 发出之后换了登录身份：结果属于上一个人（D-046、D-047）
    const stale = () => epoch !== undefined && opts.token.epoch?.() !== epoch
    if (stale()) throw superseded()
    // 令牌此刻取好、跟着这个请求走（D-052），发送时不再读当时的令牌
    const token = config.skipAuth ? null : opts.token.get()
    let res: AxiosResponse
    try {
      res = await inst.request({ ...config, _epoch: epoch, _token: token, validateStatus: () => true })
    } catch (e) {
      if (stale()) throw e instanceof ApiError && e.code === Codes.Superseded ? e : superseded(e)
      const ae = e as AxiosError
      throw fail(new ApiError(-1, ae.message || 'network error'), config)
    }
    // 成功、失败一视同仁：数据不交给现在这个人的页面，1006、2002 也不触发锁屏或改密跳转，不提示
    if (stale()) throw superseded(toApiError(res))

    // 401 且不是认证接口本身：刷新后重放一次。只有发出时带着令牌的请求才刷新（D-055）：没有令牌说明这一页此刻没有登录身份
    //（刚退出、登录失效之后，旧组件又发了新请求），这时按浏览器里的 Cookie 刷新，恢复出来的可能是另一个标签页登录的账号。
    // 恢复登录态只走启动时显式的刷新（bootstrap）
    if (res.status === 401 && token && !config._retried && !config.skipAuth && !isAuthPath(config.url)) {
      try {
        await refreshFor(stale)
      } catch (refreshErr) {
        // 刷新在途时换了人：失效的是上一个人的登录，不能把现在的登录清掉
        if (stale()) throw superseded(refreshErr)
        // 重试用尽仍是 409：会话没有失效，只是别的标签页换来的新 Cookie 还没到；这次请求失败，登录状态留着，下一次再刷新
        if (refreshErr instanceof ApiError && refreshErr.code === Codes.RefreshRetry) {
          throw fail(refreshErr, config)
        }
        // 只有服务端明确拒绝（401）才是登录失效（D-050）：断网、超时、5xx 只是这次没刷成，会话多半还在，
        // 登录状态留着，这次请求失败，下一次再刷新
        if (!(refreshErr instanceof ApiError) || refreshErr.status !== 401) {
          throw fail(refreshErr instanceof ApiError ? refreshErr : new ApiError(-1, (refreshErr as Error)?.message || 'network error', { cause: refreshErr }), config)
        }
        // 先通知再清：回调要看清失效前是否已登录，决定要不要提示并回登录页
        opts.onAuthLost?.()
        opts.token.clear()
        // 抛原来的 401，但把刷新为什么失败挂在 cause 上：调用方能分辨"会话确实失效"和"刷新时网络或服务端出了问题"
        throw fail(toApiError(res, refreshErr), config)
      }
      if (stale()) throw superseded()
      return request<T>({ ...config, _retried: true, _epoch: epoch })
    }

    const env = res.data
    if (!isEnvelope(env)) {
      throw fail(new ApiError(Codes.Internal, `HTTP ${res.status}`, { status: res.status }), config)
    }
    if (env.code !== Codes.OK) {
      throw fail(toApiError(res), config)
    }
    return env.data as T
  }

  return {
    request,
    get: (url, config) => request({ ...config, method: 'GET', url }),
    post: (url, body, config) => request({ ...config, method: 'POST', url, data: body }),
    put: (url, body, config) => request({ ...config, method: 'PUT', url, data: body }),
    delete: (url, config) => request({ ...config, method: 'DELETE', url }),
    refresh: () => gate.refresh(),
    // 登出：返回服务端是否确认吊销了会话。本地状态总由调用方清理；但网络故障或服务端出错时，
    // 刷新 Cookie 和服务端会话仍然有效，调用方要提醒用户（D-043）。会话本来就已失效也算确认——
    // 但 401 之后 request 会先试着刷新，只有刷新也被服务端明确拒绝（401）才说明会话确实没了；
    // 刷新因为网络或服务端故障失败时，会话很可能还在，不算确认；刷新 401 也只有带 SessionEndedKey 的才算（D-052）。
    async logout(): Promise<boolean> {
      try {
        await request({ method: 'POST', url: '/auth/logout', silent: true, headers: { [HeaderClient]: 'web' } })
        return true
      } catch (e) {
        // 登出回 401、随后的刷新也失败：只有服务端明确说"这个会话已经结束"（SessionEndedKey）才算确认（D-052）。
        // Cookie 属于别的会话、凭证对不上、没有 Cookie 这些 401 只说明刷新做不成，这个会话可能还活着
        if (!(e instanceof ApiError) || e.status !== 401) return false
        const c = e.cause
        return c instanceof ApiError && c.status === 401 && c.key === SessionEndedKey
      }
    },
    axios: inst,
    get refreshCount() {
      return gate.count
    },
  }
}
