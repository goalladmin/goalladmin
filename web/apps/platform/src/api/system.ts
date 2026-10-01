// 系统管理接口的类型化封装（docs/api.md）。页面只调用这里的函数，不直接拼 URL。

import { useRequest } from '@ga/shell'
import type { PageData, PageQuery } from '@ga/shell'

export interface RoleRef {
  id: number
  code: string
  name: string
  isSuper: boolean
}

export interface UserView {
  id: number
  username: string
  displayName: string
  email: string
  phone: string
  avatar: string
  /** 个人简介，本人在个人中心维护（D-038）。 */
  bio: string
  status: number
  mustChangePwd: boolean
  lastLoginAt: string | null
  lastLoginIp: string
  sort: number
  remark: string
  createdAt: string
  updatedAt: string
  roles: RoleRef[]
  /** 所属部门（D-033），0 表示未分配。 */
  deptId: number
  deptName: string
  posts: PostRef[]
}

export interface PostRef {
  id: number
  code: string
  name: string
}

export interface CreateUserInput {
  username: string
  password?: string
  displayName?: string
  email?: string
  phone?: string
  roleIds?: number[]
  deptId?: number
  postIds?: number[]
  sort?: number
  remark?: string
}

export interface UpdateUserInput {
  displayName: string
  email?: string
  phone?: string
  bio?: string
  /** 不传表示不改。 */
  deptId?: number
  /** 不传表示不改；空数组表示清空。 */
  postIds?: number[]
  sort?: number
  remark?: string
}

export interface Role {
  id: number
  portal: string
  code: string
  name: string
  isSuper: boolean
  status: number
  sort: number
  remark: string
  createdAt: string
  updatedAt: string
}

export interface RoleInput {
  code?: string
  name: string
  status?: number
  sort?: number
  remark?: string
}

export interface PermGroup {
  group: string
  perms: { code: string; name: string; sensitive: boolean }[]
}

/** 数据范围（D-039），从窄到宽。 */
export type DataScope = 'self' | 'dept' | 'dept_tree' | 'all'

/** 本端声明的一个数据资源。 */
export interface DataResource {
  code: string
  name: string
  perms: string[]
  default: DataScope
  scopes: DataScope[]
}

export interface SessionView {
  sid: string
  portal: string
  userId: number
  username: string
  ip: string
  userAgent: string
  createdAt: string
  lastSeenAt: string
  expiresAt: string
  /** 这是超管账号的会话：非超管不能让它下线（D-035）。 */
  superAccount: boolean
}

export interface OperationLog {
  id: number
  requestId: string
  portal: string
  userId: number
  username: string
  sessionId: string
  action: string
  method: string
  path: string
  query: string
  body: string
  httpStatus: number
  code: number
  latencyMs: number
  ip: string
  userAgent: string
  error: string
  createdAt: string
}

export interface LoginLog {
  id: number
  portal: string
  username: string
  userId: number
  sessionId: string
  success: boolean
  reason: string
  ip: string
  userAgent: string
  requestId: string
  createdAt: string
}

export const userApi = {
  list: (params: PageQuery & { status?: number; deptId?: number; withChildren?: number }) => useRequest().get<PageData<UserView>>('/system/users', { params }),
  get: (id: number) => useRequest().get<UserView>(`/system/users/${id}`),
  create: (input: CreateUserInput) => useRequest().post<{ user: UserView; initialPassword: string }>('/system/users', input),
  update: (id: number, input: UpdateUserInput) => useRequest().put<UserView>(`/system/users/${id}`, input),
  setStatus: (id: number, status: number) => useRequest().post<null>(`/system/users/${id}/status`, { status }),
  resetPassword: (id: number) => useRequest().post<{ initialPassword: string }>(`/system/users/${id}/reset-password`),
  assignRoles: (id: number, roleIds: number[]) => useRequest().put<UserView>(`/system/users/${id}/roles`, { roleIds }),
  options: () => useRequest().get<{ id: number; displayName: string }[]>('/system/options/users'),
  /** 管理员只能清除别人的头像（D-040）。 */
  clearAvatar: (id: number) => useRequest().delete<null>(`/system/users/${id}/avatar`),
}

export const roleApi = {
  list: () => useRequest().get<Role[]>('/system/roles'),
  create: (input: RoleInput) => useRequest().post<Role>('/system/roles', input),
  update: (id: number, input: RoleInput) => useRequest().put<Role>(`/system/roles/${id}`, input),
  remove: (id: number) => useRequest().delete<null>(`/system/roles/${id}`),
  perms: (id: number) => useRequest().get<string[]>(`/system/roles/${id}/perms`),
  /** 功能权限和数据范围一起保存（一个事务，D-039）；dataScopes 不传表示范围不变。 */
  grant: (id: number, codes: string[], dataScopes?: Record<string, DataScope>) =>
    useRequest().put<null>(`/system/roles/${id}/perms`, dataScopes ? { codes, dataScopes } : { codes }),
  dataScopes: (id: number) => useRequest().get<Record<string, DataScope>>(`/system/roles/${id}/data-scopes`),
  dataResources: () => useRequest().get<DataResource[]>('/system/data-resources'),
  permTree: () => useRequest().get<PermGroup[]>('/system/perms/tree'),
}

export const sessionApi = {
  list: (params: PageQuery & { userId?: number }) => useRequest().get<PageData<SessionView>>('/system/sessions', { params }),
  revoke: (sid: string) => useRequest().post<null>(`/system/sessions/${sid}/revoke`),
}

export interface OplogQuery extends PageQuery {
  userId?: number
  username?: string
  action?: string
  path?: string
  method?: string
  ip?: string
  sessionId?: string
  failed?: number
  from?: string
  to?: string
}

export interface LoginLogQuery extends PageQuery {
  userId?: number
  username?: string
  ip?: string
  sessionId?: string
  success?: number
  from?: string
  to?: string
}

/** 错误日志（D-032）：同一类服务端故障一行，累加次数。 */
export interface ErrorLog {
  id: number
  fingerprint: string
  kind: 'panic' | 'error'
  portal: string
  method: string
  route: string
  code: number
  httpStatus: number
  message: string
  /** 只有详情接口返回，只有 panic 才有。 */
  stack?: string
  count: number
  firstAt: string
  lastAt: string
  lastRequestId: string
  lastUserId: number
  lastUsername: string
  lastSessionId: string
  lastIp: string
}

export interface ErrorLogQuery extends PageQuery {
  kind?: string
  route?: string
  from?: string
  to?: string
}

/** 安全事件（D-032）：同一来源同一分钟合并成一行。level：1 提示、2 警告、3 严重。 */
export interface SecurityEvent {
  id: number
  portal: string
  kind: string
  level: 1 | 2 | 3
  userId: number
  username: string
  sessionId: string
  ip: string
  userAgent: string
  method: string
  path: string
  detail: string
  requestId: string
  count: number
  firstAt: string
  lastAt: string
}

export interface SecurityEventQuery extends PageQuery {
  kind?: string
  level?: number
  userId?: number
  username?: string
  ip?: string
  sessionId?: string
  from?: string
  to?: string
}

/** 框架记录的安全事件类型；模块自己记录的类型没有翻译时显示编码。 */
export const securityKinds = [
  'forbidden',
  'cross_portal',
  'token_invalid',
  'token_mismatch',
  'session_revoked',
  'refresh_reuse',
  'refresh_mismatch',
  'refresh_cookie_dup',
  'bad_origin',
  'login_locked',
  'login_rate_limited',
  'unlock_exhausted',
  'pwd_change_throttled',
  'cli',
] as const

/** 调查时间线上的一条（D-032）：type 决定哪些字段有值。 */
export interface TimelineItem {
  type: 'login' | 'operation' | 'security'
  id: number
  at: string
  userId: number
  username: string
  sessionId: string
  ip: string
  userAgent?: string
  requestId?: string
  success?: boolean
  reason?: string
  action?: string
  method?: string
  path?: string
  httpStatus?: number
  code?: number
  error?: string
  kind?: string
  level?: 1 | 2 | 3
  detail?: string
  count?: number
  lastAt?: string
}

export interface TimelineQuery {
  userId?: number
  ip?: string
  sessionId?: string
  /** 上一页返回的 next。 */
  cursor?: string
  limit?: number
}

export const logApi = {
  operations: (params: OplogQuery) => useRequest().get<PageData<OperationLog>>('/system/operation-logs', { params }),
  logins: (params: LoginLogQuery) => useRequest().get<PageData<LoginLog>>('/system/login-logs', { params }),
  errors: (params: ErrorLogQuery) => useRequest().get<PageData<ErrorLog>>('/system/error-logs', { params }),
  error: (id: number) => useRequest().get<ErrorLog>(`/system/error-logs/${id}`),
  security: (params: SecurityEventQuery) => useRequest().get<PageData<SecurityEvent>>('/system/security-events', { params }),
  timeline: (params: TimelineQuery) => useRequest().get<{ items: TimelineItem[]; more: boolean; next?: string }>('/system/audit/timeline', { params }),
}

// ---- 部门、岗位（docs/decisions.md D-033）：只是组织资料，不影响权限 ----

export interface Dept {
  id: number
  parentId: number
  name: string
  leaderUserId: number
  leaderName: string
  /** 有负责人但不在你"查看用户"的范围内：leaderUserId 为 0、leaderName 为空（D-055）。 */
  leaderHidden: boolean
  phone: string
  email: string
  status: number
  sort: number
  remark: string
  userCount: number
  createdAt: string
  updatedAt: string
}

export interface DeptInput {
  parentId: number
  name: string
  /** 不给：改部门时保持原来的负责人；0：清空（D-055）。 */
  leaderUserId?: number
  phone: string
  email: string
  status: number
  sort: number
  remark: string
}

export interface DeptOption {
  id: number
  parentId: number
  name: string
  status: number
}

export interface Post {
  id: number
  code: string
  name: string
  status: number
  sort: number
  remark: string
  userCount: number
  createdAt: string
  updatedAt: string
}

export interface PostInput {
  code?: string
  name: string
  status: number
  sort: number
  remark: string
}

export interface PostOption {
  id: number
  code: string
  name: string
  status: number
}

export const deptApi = {
  list: () => useRequest().get<Dept[]>('/system/depts'),
  create: (input: DeptInput) => useRequest().post<Dept>('/system/depts', input),
  update: (id: number, input: DeptInput) => useRequest().put<Dept>(`/system/depts/${id}`, input),
  remove: (id: number) => useRequest().delete<null>(`/system/depts/${id}`),
  options: () => useRequest().get<DeptOption[]>('/system/options/depts'),
}

export const postApi = {
  list: (params: PageQuery & { status?: number }) => useRequest().get<PageData<Post>>('/system/posts', { params }),
  create: (input: PostInput) => useRequest().post<Post>('/system/posts', input),
  update: (id: number, input: PostInput) => useRequest().put<Post>(`/system/posts/${id}`, input),
  remove: (id: number) => useRequest().delete<null>(`/system/posts/${id}`),
  options: () => useRequest().get<PostOption[]>('/system/options/posts'),
}

export type TreeNode<T> = T & { children?: TreeNode<T>[] }

/** el-tree-select 的选项。 */
export interface TreeOption {
  value: number
  label: string
  disabled?: boolean
  children?: TreeOption[]
}

/** 把带 id、parentId 的扁平列表拼成树；上级不在列表里的当作顶级。保持原来的先后顺序。 */
export function buildTree<T extends { id: number; parentId: number }>(rows: readonly T[]): TreeNode<T>[] {
  const byId = new Map<number, TreeNode<T>>()
  for (const r of rows) byId.set(r.id, { ...r })
  const roots: TreeNode<T>[] = []
  for (const n of byId.values()) {
    const parent = n.parentId ? byId.get(n.parentId) : undefined
    if (parent) (parent.children ??= []).push(n)
    else roots.push(n)
  }
  return roots
}

// ---- 字典（docs/decisions.md D-023） ----

export type I18nText = Record<string, string>

export interface DictInfo {
  id: number
  portal: string
  code: string
  name: string
  nameI18n: I18nText
  valueType: 'string' | 'int'
  /** code：代码声明（只读，不能加项、删项、改值）；admin：后台新建。 */
  source: 'code' | 'admin'
  status: number
  sort: number
  remark: string
  createdAt: string
  updatedAt: string
}

export interface DictItemInfo {
  id: number
  parentId: number
  value: string
  label: string
  labelI18n: I18nText
  color: string
  extra: string
  /** 代码声明的项：值不可改、不可删。 */
  locked: boolean
  /** 显示字段被后台改过，同步时不覆盖。 */
  overridden: boolean
  status: number
  sort: number
  remark: string
  updatedAt: string
  children?: DictItemInfo[]
}

export interface DictDetail extends DictInfo {
  items: DictItemInfo[]
}

export interface DictInput {
  code?: string
  name: string
  nameI18n?: I18nText
  portal?: string
  valueType?: 'string' | 'int'
  status?: number
  sort?: number
  remark?: string
}

export interface DictItemInput {
  parentId?: number
  value: string
  label: string
  labelI18n?: I18nText
  color?: string
  extra?: string
  status?: number
  sort?: number
  remark?: string
}

export const dictApi = {
  list: (params: PageQuery & { portal?: string; source?: string }) => useRequest().get<PageData<DictInfo>>('/system/dicts', { params }),
  get: (id: number) => useRequest().get<DictDetail>(`/system/dicts/${id}`),
  create: (input: DictInput) => useRequest().post<DictInfo>('/system/dicts', input),
  update: (id: number, input: DictInput) => useRequest().put<DictInfo>(`/system/dicts/${id}`, input),
  remove: (id: number) => useRequest().delete<null>(`/system/dicts/${id}`),
  createItem: (id: number, input: DictItemInput) => useRequest().post<DictItemInfo>(`/system/dicts/${id}/items`, input),
  updateItem: (id: number, itemId: number, input: DictItemInput) => useRequest().put<DictItemInfo>(`/system/dicts/${id}/items/${itemId}`, input),
  removeItem: (id: number, itemId: number) => useRequest().delete<null>(`/system/dicts/${id}/items/${itemId}`),
  resetItem: (id: number, itemId: number) => useRequest().post<DictItemInfo>(`/system/dicts/${id}/items/${itemId}/reset`),
  portals: () => useRequest().get<string[]>('/system/options/portals'),
}

// ---- 菜单管理（D-025）：只能改显示名、图标、隐藏和位置；路径、组件、权限码由代码决定 ----

export type MenuKind = 'dir' | 'page' | 'group'

export interface MenuAdminNode {
  name: string
  kind: MenuKind
  parent: string
  sort: number
  path: string
  component: string
  perm: string
  titleKey: string
  titles: Record<string, string>
  icon: string
  hidden: boolean
  /** 代码里声明为隐藏（详情页这类），不能取消隐藏。 */
  codeHidden: boolean
  /** 代码菜单有后台调整。 */
  customized: boolean
  /** 看到它要具备的全部权限码（自己的加代码祖先的）。 */
  need: string[]
  /** 代码菜单在代码里的原值。 */
  default?: { parent: string; sort: number; icon: string }
}

export interface MenuDisplayInput {
  titles: Record<string, string>
  icon: string
  hidden: boolean
  /** 同级排序值（越小越靠前）；不传表示不改。上级不变。 */
  sort?: number
}

export interface MenuGroupInput extends MenuDisplayInput {
  parent: string
  sort: number
}

export interface MenuPosition {
  name: string
  parent: string
  sort: number
}

export const menuApi = {
  list: () => useRequest().get<MenuAdminNode[]>('/system/menus'),
  update: (name: string, input: MenuDisplayInput) => useRequest().put<null>(`/system/menus/${encodeURIComponent(name)}`, input),
  reset: (name: string) => useRequest().post<null>(`/system/menus/${encodeURIComponent(name)}/reset`),
  saveLayout: (items: MenuPosition[]) => useRequest().put<null>('/system/menu-layout', { items }),
  createGroup: (input: MenuGroupInput) => useRequest().post<{ name: string }>('/system/menu-groups', input),
  removeGroup: (name: string) => useRequest().delete<null>(`/system/menu-groups/${encodeURIComponent(name)}`),
}

// ---- 控制台（D-027、D-030） ----

const tz = () => -new Date().getTimezoneOffset()

export interface DashboardData {
  /** 连续的本地日期，最后一天是今天。 */
  days: string[]
  users: { total: number; enabled: number; new: number }
  /** 每天新建的用户数，与 days 对齐。 */
  newUsers: number[]
  sessions: number
  logins: { success: number[]; failed: number[] }
  operations: number[]
  reasons: { reason: string; count: number }[]
  topActions: { action: string; count: number }[]
  /** 期间操作最多的人。 */
  topUsers: { userId: number; username: string; count: number }[]
  /** 按本地钟点（下标 0–23）的成功登录和操作次数。 */
  hours: { logins: number[]; operations: number[] }
}

export interface MonitorSecurity {
  /** 24 个 UTC 整点，最后一个是当前小时。 */
  hours: string[]
  success: number[]
  failed: number[]
  sessions: number
  lockedSessions: number
  operations: number
  failedIps: { ip: string; count: number }[]
  recent: { username: string; success: boolean; reason: string; ip: string; createdAt: string }[]
}

export interface MonitorMinute {
  at: string
  count: number
  clientErrors: number
  serverErrors: number
  avgMs: number
  p95Ms: number
  maxMs: number
  cpu: number | null
  heapInuse: number | null
  goroutines: number | null
}

export interface MonitorServer {
  /** 配置关掉服务器状态时（D-031）接口只回 { enabled: false }。 */
  enabled: true
  now: string
  startedAt: string
  /** 秒。 */
  uptime: number
  runtime: { goVersion: string; os: string; arch: string; version: string; numCpu: number; gomaxprocs: number; goroutines: number }
  cpu: { supported: boolean; percent: number }
  memory: { sys: number; heapAlloc: number; heapInuse: number; stackInuse: number; heapObjects: number; numGc: number; pauseTotalMs: number; lastGc: string | null }
  db: { ok: boolean; latencyMs: number; version: string; maxOpen: number; open: number; inUse: number; idle: number; waitCount: number; waitMs: number }
  requests: {
    minutes: MonitorMinute[]
    routes: { method: string; route: string; count: number; serverErrors: number; avgMs: number; p95Ms: number }[]
  }
}

export interface WorkspaceData {
  roles: string[]
  sessions: { current: boolean; ip: string; userAgent: string; createdAt: string; lastSeenAt: string }[]
  opsToday: number
  ops30d: number
  recent: { action: string; method: string; path: string; code: number; createdAt: string }[]
  logins: { success: boolean; reason: string; ip: string; createdAt: string }[]
  lastLogin: { success: boolean; reason: string; ip: string; createdAt: string } | null
  topActions: { action: string; count: number }[]
}

export const dashboardApi = {
  /** tz 是浏览器的时区偏移（分钟，东区为正）。 */
  get: (days: number) => useRequest().get<DashboardData>('/system/dashboard', { params: { days, tz: tz() } }),
  // 定时刷新的请求出错时不弹提示（silent），页面自己显示"刷新失败"
  security: () => useRequest().get<MonitorSecurity>('/system/monitor/security', { silent: true }),
  server: () => useRequest().get<MonitorServer | { enabled: false }>('/system/monitor/server', { silent: true }),
  workspace: () => useRequest().get<WorkspaceData>('/system/workspace', { params: { tz: tz() } }),
}

// ---- 安全设置（docs/decisions.md D-034）：只读，策略只能在配置文件里改（D-024） ----

export type PolicyGroup = 'captcha' | 'password' | 'rate' | 'lock' | 'expiry'
export type PolicyKind = 'bool' | 'times' | 'perMinute' | 'seconds' | 'days' | 'chars'
export type PolicySource = 'config' | 'code' | 'default'

export interface PolicyItem {
  group: PolicyGroup
  /** 配置文件里的键名，如 lockAfterFailures。 */
  key: string
  /** 完整配置路径，如 portals.platform.login.lockAfterFailures。 */
  configKey: string
  kind: PolicyKind
  /** kind 为 bool 时是布尔值，其余是整数（时长单位秒，有效期单位天，0 表示不过期）。 */
  value: boolean | number
  default: boolean | number
  /** 底线允许的范围；开关项没有。 */
  min?: number
  max?: number
  source: PolicySource
}

export const securityApi = {
  policy: () => useRequest().get<{ portal: string; items: PolicyItem[] }>('/system/security-policy'),
}

// ---- 个人中心（docs/decisions.md D-038）：只读写调用者本人，没有指定用户的参数 ----

export interface ProfileView {
  id: number
  username: string
  displayName: string
  email: string
  phone: string
  avatar: string
  bio: string
  super: boolean
  roles: RoleRef[]
  deptName: string
  posts: PostRef[]
  /** 上次改密时间；为空表示从未改过。 */
  pwdChangedAt: string | null
  lastLoginAt: string | null
  lastLoginIp: string
  createdAt: string
  /** 本人当前有效的会话数，含本次。 */
  sessions: number
}

export interface ProfileInput {
  displayName: string
  email: string
  phone: string
  bio: string
}

export const profileApi = {
  get: () => useRequest().get<ProfileView>('/system/profile'),
  // 字段错误由页面自己显示在表单里（silent）
  update: (input: ProfileInput) => useRequest().put<ProfileView>('/system/profile', input, { silent: true }),
  /** 本人除当前会话外的全部会话下线。 */
  revokeOthers: () => useRequest().post<{ revoked: number }>('/system/profile/revoke-other-sessions'),
  // 头像（D-040）：请求体就是图片本身；返回新的 avatar 值。错误由对话框自己显示（silent）
  uploadAvatar: (image: Blob) =>
    useRequest().post<{ avatar: string }>('/system/avatar', image, { headers: { 'Content-Type': image.type || 'image/jpeg' }, silent: true }),
  setPresetAvatar: (preset: string) => useRequest().put<{ avatar: string }>('/system/avatar', { preset }, { silent: true }),
  clearAvatar: () => useRequest().delete<{ avatar: string }>('/system/avatar', { silent: true }),
}
