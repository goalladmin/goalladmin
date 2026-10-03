// 认证状态（规范 §5.1）：访问令牌只在内存里；刷新凭证在 HttpOnly Cookie 里，前端看不到。

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import { usePortal } from '../context'
import { HeaderClient } from '../request/client'
import { ApiError, Codes } from '../types'
import type { MeOrg, MeResponse, MeUser, MenuTree, PwdPolicy, TokenResponse } from '../types'

export interface LoginInput {
  /** 主体编号：主体端（代理商、商户）必填，平台端不传（D-061）。 */
  org?: string
  username: string
  password: string
  captchaId?: string
  captchaCode?: string
}

/**
 * 退出没得到服务端确认时记下的标记（按端），值是要补做退出的会话号（D-049）：下次启动先刷新，换来的正是其中一个会话
 * 才补做退出；Cookie 已经是别的会话（同一浏览器里另一个人登录了）就作废，照常恢复——标记不绑会话时，
 * 旧标签页留下的标记会把新登录的人退掉。
 *
 * 一个浏览器里几个标签页可能各有一次没确认的退出，所以标记是一组会话号（D-051），每条只由它自己的结果删除：
 * 旧标签页迟到的"退出成功"只删它自己那条，不能把别的标签页刚记下、还没补做的那条一起删掉。
 */
function pendingLogoutKey(): string {
  return `ga.${usePortal().portal}.logoutPending`
}

/**
 * 标记同时写 localStorage 和一个普通 Cookie（D-045、D-046）：存储有时写得进、下次却读不出（清理工具、权限变化），
 * 只写一处不保险。两处都读（取并集）、都写。Cookie 里只有会话号（32 位十六进制，用 "." 隔开），不是凭据；
 * SameSite=Strict，https 下带 Secure。
 *
 * https 下 Cookie 名带 __Host- 前缀（D-059）：同一主域下的其他主机写不进同名 Cookie，不能往标记里塞会话号、
 * 诱使这里恢复登录时替人补做退出。
 *
 * 旧名字（不带前缀）的标记在 https 下另外读出来，只作"不恢复"的依据、不作"补做退出"的依据（D-060）：旧版本写
 * localStorage 失败时标记只在旧 Cookie 里，直接忽略的话，没确认退出的会话会被恢复；可它也可能是同主域的其他主机
 * 塞进来的，不能凭它去吊销会话。所以启动时刷新换来的会话在旧标记里，就只清掉本地登录态、不恢复（用户重新登录即可），
 * 不发退出请求；换来的会话不在旧标记里、或刷新被拒，旧标记就没用了，删掉——刷新在途时又有新会话号写进来的除外（D-069）。
 */
export function logoutMarkCookieName(portal: string, https: boolean): string {
  return https ? `__Host-ga_lo_${portal}` : `ga_lo_${portal}`
}

function isHttps(): boolean {
  return typeof location !== 'undefined' && location.protocol === 'https:'
}

function pendingLogoutCookie(): string {
  return logoutMarkCookieName(usePortal().portal, isHttps())
}

function readCookie(name: string): string | undefined {
  const prefix = `${name}=`
  return document.cookie.split(';').map((c) => c.trim()).find((c) => c.startsWith(prefix))?.slice(prefix.length)
}

/** https 下旧名字的标记里的会话号（D-060）；http 下旧名字就是当前名字，没有"旧标记"。 */
function readLegacyLogouts(): string[] {
  if (!isHttps()) return []
  try {
    const v = readCookie(logoutMarkCookieName(usePortal().portal, false))
    return v ? [...new Set(v.split('.').filter((x) => sessionIdRe.test(x)))] : []
  } catch {
    return []
  }
}

/**
 * 删掉 https 下旧名字的标记，前提是里面的会话号都在 seen（启动时读到的那些）里（D-069）。启动时读、刷新回来才删，中间
 * 隔着一次网络请求：这期间别的标签页写进来的会话号不在 seen 里，整个删掉就把它也丢了——那个没确认退出的会话以后会被恢复。
 * 所以只要有 seen 之外的会话号，这次就不删，留到下次启动再判断。不改成"把剩下的写回去"：https 下不往旧名字里写东西（D-060）。
 */
function clearLegacyLogouts(seen: string[]): void {
  if (!isHttps()) return
  if (readLegacyLogouts().some((id) => !seen.includes(id))) return
  try {
    document.cookie = `${logoutMarkCookieName(usePortal().portal, false)}=${cookieAttrs(0)}`
  } catch {
    // 删不掉：下次启动再判断一次，结果一样
  }
}

function cookieAttrs(maxAge: number): string {
  const secure = isHttps() ? '; Secure' : ''
  return `; Path=/; Max-Age=${maxAge}; SameSite=Strict${secure}`
}

const sessionIdRe = /^[0-9a-f]{32}$/

/** 同时最多记几条：够覆盖一个浏览器里几个标签页各自没确认的退出，Cookie 也不会太长。 */
const pendingLogoutMax = 8

/** 读标记里的会话号。旧版本写的 "1" 之类不是会话号（不知道是哪个会话，补不了），读到时顺手清掉。 */
function readPendingLogouts(): string[] {
  const raw: string[] = []
  try {
    const v = localStorage.getItem(pendingLogoutKey())
    if (v) raw.push(...v.split('.'))
  } catch {
    // 读不了 localStorage：看 Cookie
  }
  try {
    const v = readCookie(pendingLogoutCookie())
    if (v) raw.push(...v.split('.'))
  } catch {
    // Cookie 也读不了
  }
  const out = [...new Set(raw.filter((x) => sessionIdRe.test(x)))]
  if (raw.some((x) => x !== '' && !sessionIdRe.test(x))) writePendingLogouts(out)
  return out
}

function writePendingLogouts(sids: string[]): void {
  const v = sids.slice(-pendingLogoutMax).join('.')
  try {
    if (v) localStorage.setItem(pendingLogoutKey(), v)
    else localStorage.removeItem(pendingLogoutKey())
  } catch {
    // 还有 Cookie
  }
  try {
    document.cookie = v ? `${pendingLogoutCookie()}=${v}${cookieAttrs(30 * 24 * 3600)}` : `${pendingLogoutCookie()}=${cookieAttrs(0)}`
  } catch {
    // Cookie 也写不了：退不掉的会话只能靠过期（这时浏览器多半也存不下刷新 Cookie）
  }
}

function addPendingLogout(sid: string): void {
  writePendingLogouts([...readPendingLogouts().filter((x) => x !== sid), sid])
}

/** 只删给定的这几条（D-051）：读到之后别的标签页新记下的不动。 */
function removePendingLogouts(sids: string[]): void {
  if (sids.length === 0) return
  const cur = readPendingLogouts()
  const next = cur.filter((x) => !sids.includes(x))
  if (next.length !== cur.length) writePendingLogouts(next)
}

/**
 * 这个标签页登录的会话号，存在 sessionStorage 里（按标签页、重载后还在，D-050）。同一浏览器的标签页共用一个刷新 Cookie：
 * 别的标签页换了人登录之后再重载这一页，启动时的刷新带上它，服务端发现 Cookie 已经是别人的会话就拒绝，
 * 这一页回到登录页并说明原因，而不是悄悄变成另一个人。退出、登录失效（服务端 401）、提示过换了账号时一并删掉：
 * 之后再重载就按 Cookie 恢复（那是用户看过提示之后的选择）。启动时断网、超时、服务端临时出错不删（D-051）：
 * 这时什么也没确认，删了的话下次重载就不带会话号，会不声不响地按 Cookie 恢复成另一个账号。
 */
function tabSessionKey(): string {
  return `ga.${usePortal().portal}.sid`
}

function readTabSession(): string | null {
  try {
    const v = sessionStorage.getItem(tabSessionKey())
    return v && sessionIdRe.test(v) ? v : null
  } catch {
    return null
  }
}

function writeTabSession(sid: string | null): void {
  try {
    if (sid) sessionStorage.setItem(tabSessionKey(), sid)
    else sessionStorage.removeItem(tabSessionKey())
  } catch {
    // 存不了：重载后按 Cookie 恢复，和以前一样
  }
}

/** 服务端拒绝刷新、因为 Cookie 属于别的会话时的翻译键（D-048、D-050）。 */
export const SessionSwitchedKey = 'auth.sessionSwitched'

export const useAuthStore = defineStore('ga.auth', () => {
  const token = ref<string | null>(null)
  const expiresAt = ref(0)
  const user = ref<MeUser | null>(null)
  /** 主体端：当前账号所属的主体（顶栏显示），平台端为 null。 */
  const org = ref<MeOrg | null>(null)
  const perms = ref<string[]>([])
  const menus = ref<MenuTree[]>([])
  const pwdPolicy = ref<PwdPolicy | null>(null)
  /** 会话处于锁屏状态（D-027）：由服务端判定，前端只负责显示锁屏界面。 */
  const locked = ref(false)
  /** 启动时的刷新 + /auth/me 是否已经做过（无论成功与否）。 */
  const ready = ref(false)
  /** 登录身份的代数：登录、清除（退出、登录失效）时加一，刷新令牌不变（D-046、D-047：请求层据此作废换了人之前发出的请求和刷新）。 */
  const epoch = ref(0)
  /** 当前登录的会话号（D-048）：刷新时声明要续的是它，别的标签页换了人时刷新被拒而不是变成别人。 */
  const sessionId = ref<string | null>(null)
  /** 启动恢复时发现这个浏览器已经换成别的账号（D-050）：登录页据此说明为什么要重新登录。登录成功后复位。 */
  const accountSwitched = ref(false)

  const authenticated = computed(() => user.value !== null)
  const mustChangePwd = computed(() => user.value?.mustChangePwd === true)
  const permSet = computed(() => new Set(perms.value))

  function setToken(t: TokenResponse) {
    token.value = t.accessToken
    if (t.sessionId) {
      sessionId.value = t.sessionId
      writeTabSession(t.sessionId)
    }
    expiresAt.value = Date.now() + t.expiresIn * 1000
    if (user.value) {
      user.value.mustChangePwd = t.mustChangePwd
      user.value.pwdExpired = t.pwdExpired ?? false
    }
  }

  /** 清除登录状态。keepTabSession：只清这一页的登录态，保留这个标签页绑定的会话号（启动时的临时故障，D-051）。 */
  function clear(opts?: { keepTabSession?: boolean }) {
    epoch.value++
    token.value = null
    if (!opts?.keepTabSession) {
      sessionId.value = null
      writeTabSession(null)
    }
    expiresAt.value = 0
    user.value = null
    org.value = null
    perms.value = []
    menus.value = []
    pwdPolicy.value = null
    locked.value = false
  }

  /**
   * 认证状态的动作在每次等待之后、写状态或发下一步请求之前都核对登录身份的代数（D-053）：请求层只在响应回来时核对，
   * 从请求结果交回来到这里继续执行之间还隔着微任务，这段时间里退出或换了人的话，结果属于上一个人，不能写进状态，
   * 也不能替现在的人接着做下一步。
   */
  function ensureSame(started: number): void {
    if (epoch.value !== started) throw new ApiError(Codes.Superseded, 'superseded: the signed-in identity changed')
  }

  async function fetchMe(): Promise<MeResponse> {
    const started = epoch.value
    const me = await usePortal().client.get<MeResponse>('/auth/me')
    ensureSame(started)
    user.value = me.user
    org.value = me.org ?? null
    perms.value = me.perms ?? []
    menus.value = me.menus ?? []
    pwdPolicy.value = me.pwdPolicy ?? null
    locked.value = me.locked === true
    return me
  }

  /** 登录。失败抛 ApiError（code 1001/1002/1003），由登录页处理验证码与锁定提示。 */
  async function login(input: LoginInput): Promise<TokenResponse> {
    // 登录也要带客户端标识（D-051，服务端据此拒绝跨站页面发来的登录）
    const before = readPendingLogouts()
    const started = epoch.value
    const t = await usePortal().client.post<TokenResponse>('/auth/login', input, { silent: true, headers: { [HeaderClient]: 'web' } })
    ensureSame(started)
    // 新会话建立后旧的刷新 Cookie 已被换掉，之前没确认的退出在这个浏览器里已经补不了，不用再补；
    // 只删登录之前就在的那些，登录在途时别的标签页新记下的不动（D-051）
    removePendingLogouts(before)
    epoch.value++
    accountSwitched.value = false
    setToken(t)
    await fetchMe()
    return t
  }

  /**
   * 页面刷新后恢复登录态：先刷新，成功再拉 /auth/me；失败就当作未登录。
   * 上次退出没得到服务端确认的（标记还在），刷新换来的正是标记里的会话时先补做退出而不是把它恢复回来：刷新 Cookie
   * 还在浏览器里，不补做的话恢复网络后一刷新页面就又登回去了；补做仍不成功就保留标记，下次再试。
   * 换来的是别的会话（同一浏览器里另一个人登录了）或刷新被服务端拒绝（那个会话的 Cookie 已经不在这个浏览器里），
   * 标记作废：这个浏览器已经没有那个会话的凭据，补不了也不需要补（D-049）。
   */
  async function bootstrap(): Promise<void> {
    if (ready.value) return
    // 恢复在途时用户已经在登录页登录了（或退出了）：恢复的结果作废，也不能把那次登录清掉（D-047）
    const started = epoch.value
    const pending = readPendingLogouts()
    const legacy = readLegacyLogouts()
    // 这个标签页重载之前登录的会话：刷新时带上，Cookie 已经换成别人的会话就不恢复（D-050）
    if (!sessionId.value) sessionId.value = readTabSession()
    try {
      const t = await usePortal().client.refresh()
      // 恢复在途时用户已经登录或退出了：后面的补做退出、拉 /auth/me 都不再替现在的人做（D-053）
      if (epoch.value !== started) return
      if (t.sessionId && pending.includes(t.sessionId)) {
        if (await usePortal().client.logout()) removePendingLogouts([t.sessionId])
        if (epoch.value === started) clear()
        return
      }
      // 旧名字标记里的会话（D-060）：不恢复，也不凭它补做退出；旧标记留着，下次启动换来的还是它就照样不恢复
      // 刷新在途时别的标签页刚写进旧标记的也算（D-069）：再读一遍，和启动时读到的合起来看
      if (t.sessionId && (legacy.includes(t.sessionId) || readLegacyLogouts().includes(t.sessionId))) {
        if (epoch.value === started) clear()
        return
      }
      // Cookie 是别的会话：读到的这些在这个浏览器里已经没有凭据，补不了也不需要补
      removePendingLogouts(pending)
      clearLegacyLogouts(legacy)
      await fetchMe()
    } catch (e) {
      const rejected = e instanceof ApiError && e.status === 401
      const switched = rejected && e.key === SessionSwitchedKey
      // 刷新被服务端拒绝、Cookie 本身不能用：读到的会话在这里已经补不了退出。Cookie 属于别的会话（switched）时
      // 那个会话可能正是某条待补退出的，不动；网络故障、服务端临时出错时也都留着
      if (rejected && !switched) {
        removePendingLogouts(pending)
        clearLegacyLogouts(legacy)
      }
      if (epoch.value === started) {
        accountSwitched.value = switched
        // 临时故障：只清这一页的登录态，标签页绑定的会话号留着，下次重载照样带上（D-051）
        clear({ keepTabSession: !rejected })
      }
    } finally {
      ready.value = true
    }
  }

  /**
   * 登出：本地状态无论如何都清掉。返回服务端是否确认吊销了会话——网络故障或服务端出错时刷新 Cookie
   * 和服务端会话仍然有效；这时标记留着，下次启动先补做退出（bootstrap），调用方也要提醒用户（shell.error.logoutUnconfirmed）。
   */
  async function logout(): Promise<boolean> {
    const started = epoch.value
    // 标记在发请求之前就写、绑定这次要退出的会话：请求在途时关掉标签页，下次打开也会补做退出
    const sid = sessionId.value
    if (sid) addPendingLogout(sid)
    const confirmed = await usePortal().client.logout()
    // 确认了就删自己这一条，别的标签页记下的不动（D-051）
    if (confirmed && sid) removePendingLogouts([sid])
    // 退出在途时已经有人重新登录了（D-048）：不清掉新登录
    if (epoch.value !== started) return confirmed
    clear()
    return confirmed
  }

  async function changePassword(oldPassword: string, newPassword: string): Promise<void> {
    const started = epoch.value
    await usePortal().client.put('/auth/password', { oldPassword, newPassword }, { silent: true })
    ensureSame(started)
    await fetchMe()
  }

  /** 进入锁屏状态：丢掉权限和菜单（解锁后由 fetchMe 重新取），页面由 GaRoot 卸载。 */
  function markLocked() {
    locked.value = true
    perms.value = []
    menus.value = []
  }

  /** 锁屏：服务端锁定当前会话，之后除查看自己、解锁、登出外的接口都会被拒绝。 */
  async function lock(): Promise<void> {
    const started = epoch.value
    await usePortal().client.post('/auth/lock')
    ensureSame(started)
    markLocked()
  }

  /** 解锁：校验当前账号的密码。失败抛 ApiError（输错 3001；次数用完会话被吊销，401）。 */
  async function unlock(password: string): Promise<void> {
    const started = epoch.value
    await usePortal().client.post('/auth/unlock', { password }, { silent: true })
    ensureSame(started)
    await fetchMe()
  }

  /** 是否拥有权限码。超管恒为 true。mode 为 all 时要求全部拥有。 */
  function hasPerm(codes: string | string[], mode: 'any' | 'all' = 'any'): boolean {
    if (user.value?.super) return true
    const list = Array.isArray(codes) ? codes : [codes]
    if (list.length === 0) return true
    return mode === 'all' ? list.every((c) => permSet.value.has(c)) : list.some((c) => permSet.value.has(c))
  }

  return {
    token,
    epoch,
    sessionId,
    accountSwitched,
    expiresAt,
    user,
    org,
    perms,
    menus,
    pwdPolicy,
    locked,
    markLocked,
    ready,
    authenticated,
    mustChangePwd,
    setToken,
    clear,
    fetchMe,
    login,
    bootstrap,
    logout,
    changePassword,
    lock,
    unlock,
    hasPerm,
  }
})
