// 与后端接口约定一一对应的类型（docs/api.md）。业务页面从 '@ga/shell' 引用它们。

/** 统一响应信封。 */
export interface Envelope<T = unknown> {
  code: number
  data: T
  msg: string
  /** 业务自定的失败说明的翻译键和参数（D-026）：前端有 err.<key> 的翻译时用它代替 msg。 */
  key?: string
  params?: Record<string, unknown>
  requestId: string
}

/** 分页响应的 data。 */
export interface PageData<T> {
  list: T[]
  total: number
  page: number
  pageSize: number
}

/** 列表接口的通用查询参数。 */
export interface PageQuery {
  page?: number
  pageSize?: number
  keyword?: string
  sortBy?: string
  sortOrder?: 'asc' | 'desc'
}

/** 字段级校验错误。 */
export interface FieldError {
  field: string
  /** 英文兜底说明。 */
  message: string
  /** 翻译键（D-026），前端显示 err.<key> 的翻译并代入 params。 */
  key?: string
  params?: Record<string, unknown>
}

/** 登录、刷新的出参。 */
export interface TokenResponse {
  accessToken: string
  tokenType: string
  expiresIn: number
  mustChangePwd: boolean
  /** 必须改密的原因是密码过期。 */
  pwdExpired?: boolean
  /** 这个令牌所属的会话（D-048）：刷新时放进 X-GA-Session，Cookie 已经换成别的会话时刷新被拒。 */
  sessionId?: string
}

/** /auth/me 里的用户。 */
export interface MeUser {
  id: number
  username: string
  displayName: string
  avatar: string
  mustChangePwd: boolean
  /** 必须改密的原因是密码过期（而不是首次登录或被重置）。 */
  pwdExpired?: boolean
  super: boolean
}

/** 当前端的密码策略（后端配置 portals.<code>.password）。前端据此提示和预校验，安全以后端为准。 */
export interface PwdPolicy {
  minLength: number
  requireUpper: boolean
  requireLower: boolean
  requireSymbol: boolean
  /** 密码有效期（天），0 表示不过期。 */
  maxAgeDays: number
}

/** 后端下发的菜单节点（已按权限过滤）。 */
export interface MenuTree {
  name: string
  path: string
  component?: string
  titleKey: string
  /** 菜单管理里改过的显示名（按语言）；优先于 titleKey 的翻译。分组只有它，没有 titleKey。 */
  titles?: Record<string, string>
  icon?: string
  keepAlive: boolean
  hidden: boolean
  sort: number
  children?: MenuTree[]
}

/** /auth/me 的出参。 */
/** 主体端（代理商、商户）当前账号所属的主体（D-061）。 */
export interface MeOrg {
  code: string
  name: string
}

export interface MeResponse {
  user: MeUser
  perms: string[]
  menus: MenuTree[]
  pwdPolicy?: PwdPolicy
  /** 会话处于锁屏状态（D-027）；这时 perms、menus 为空。 */
  locked?: boolean
  /** 只有主体端有：当前账号所属的主体。 */
  org?: MeOrg
}

/** 业务错误码（server/core/httpx/codes.go）。前端只关心需要特殊处理的几个。 */
export const Codes = {
  OK: 0,
  LoginFailed: 1001,
  CaptchaRequired: 1002,
  Locked: 1003,
  TokenInvalid: 1004,
  RefreshRetry: 1005,
  Forbidden: 2001,
  PwdChangeRequired: 2002,
  /** 当前 IP 不允许访问：IP 黑名单、白名单（D-062）。 */
  IPDenied: 2003,
  SessionLocked: 1006,
  Validation: 3001,
  BadRequest: 3002,
  NotFound: 4004,
  Internal: 5000,
  /** 前端自己产生，不来自服务端：请求发出之后换了登录身份（退出、登录失效、换人登录），结果作废（D-047）。 */
  Superseded: -2,
} as const

/** 接口失败时抛出的错误：code 为 0 以外的信封，或网络错误（code 为 -1）。 */
export class ApiError extends Error {
  readonly code: number
  readonly data: unknown
  readonly requestId: string
  readonly status: number
  /** msg 的翻译键和参数（D-026），没有时为空。 */
  readonly key: string
  readonly params: Record<string, unknown>

  constructor(
    code: number,
    msg: string,
    opts: { data?: unknown; requestId?: string; status?: number; key?: string; params?: Record<string, unknown>; cause?: unknown } = {},
  ) {
    super(msg, opts.cause === undefined ? undefined : { cause: opts.cause })
    this.name = 'ApiError'
    this.code = code
    this.data = opts.data
    this.requestId = opts.requestId ?? ''
    this.status = opts.status ?? 0
    this.key = opts.key ?? ''
    this.params = opts.params ?? {}
  }

  /** 字段级校验错误明细（code 3001 时有）。 */
  get fields(): FieldError[] {
    const d = this.data as { fields?: FieldError[] } | undefined
    return d?.fields ?? []
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError
}
