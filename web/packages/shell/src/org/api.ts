import type { DashboardData } from '../data-center/types'
// 主体端（代理商、商户）自己的后台的接口（D-067，后端 server/core/orgportal）。路径相对端的前缀 /api/<端>/v1。
// 主体只取登录会话里的主体，请求里不能带 orgId、agentId、merchantId（后端回 3002）。

import { usePortal } from '../context'
import type { PageData, PageQuery } from '../types'

const client = () => usePortal().client

/** 端的完整权限码：perm('account:list') → 'merchant:account:list'。 */
export function orgPerm(action: string): string {
  return `${usePortal().portal}:${action}`
}

export interface OrgSummary {
  id: number
  code: string
  name: string
  contactName: string
  contactPhone: string
  status: number
  createdAt: string
}

export interface OrgOverview {
  org: OrgSummary
  /** 调用者是不是主账号。 */
  owner: boolean
  /** 调用者有查看权限的几项计数：accounts、sessions、roles，代理商端另有 merchants。 */
  counts: Record<string, number>
}

export interface OrgRoleRef {
  id: number
  code: string
  name: string
  status: number
}

export interface OrgAccount {
  id: number
  orgId: number
  username: string
  displayName: string
  email: string
  phone: string
  avatar: string
  /** 本主体的主账号。 */
  owner: boolean
  mustChangePwd: boolean
  lastLoginAt: string | null
  lastLoginIp: string
  status: number
  sort: number
  remark: string
  createdAt: string
  roles: OrgRoleRef[]
}

export interface OrgAccountInput {
  username: string
  password?: string
  displayName?: string
  email?: string
  phone?: string
  roleIds?: number[]
  sort?: number
  remark?: string
}

export interface OrgAccountUpdate {
  displayName: string
  email: string
  phone: string
  sort: number
  remark: string
}

export interface OrgRole {
  id: number
  code: string
  name: string
  status: number
  sort: number
  remark: string
  createdAt: string
}

export interface OrgRoleInput {
  code: string
  name: string
  status: number
  sort: number
  remark: string
}

export interface OrgPermGroup {
  group: string
  perms: { code: string; name: string; sensitive: boolean }[]
}

export interface OrgSession {
  sid: string
  userId: number
  username: string
  ip: string
  userAgent: string
  locked: boolean
  createdAt: string
  lastSeenAt: string
  expiresAt: string
  /** 主账号的会话：员工不能让它下线。 */
  owner: boolean
  /** 调用者这一次的会话。 */
  current: boolean
}

export interface OrgLoginLog {
  id: number
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

export interface OrgOperationLog {
  id: number
  requestId: string
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

export interface OrgIPEntry {
  cidr: string
  remark: string
}

export interface OrgIPAllow {
  items: (OrgIPEntry & { id: number; createdAt: string })[]
  yourIp: string
}

/** 个人中心的本人视图：不含管理者写在账号上的备注、排序和状态（D-099）。 */
export interface OrgProfile extends Omit<OrgAccount, 'status' | 'sort' | 'remark'> {
  orgCode: string
  orgName: string
  /** 当前有效的会话数，含本次。 */
  sessions: number
}

export interface OrgLogQuery extends PageQuery {
  username?: string
  ip?: string
  from?: string
  to?: string
}

export interface OrgIPRule extends OrgIPEntry {
  id: number
  expiresAt: string | null
  createdAt: string
}

export const orgApi = {
  dashboard: (days: number) => client().get<DashboardData>('/org/dashboard', { params: { days, tz: -new Date().getTimezoneOffset() } }),
  denyList: (params: PageQuery & { includeExpired?: number }) => client().get<PageData<OrgIPRule>>('/org/ip-deny', { params }),
  addDeny: (input: { cidr: string; expiresIn: number; remark: string }) => client().post<OrgIPRule>('/org/ip-deny', input, { silent: true }),
  removeDeny: (id: number) => client().delete<null>(`/org/ip-deny/${id}`),
  overview: () => client().get<OrgOverview>('/org/overview'),
  ipAllow: () => client().get<OrgIPAllow>('/org/ip-allow'),
  setIPAllow: (items: OrgIPEntry[]) => client().put<OrgIPAllow>('/org/ip-allow', { items }),

  accounts: (params: PageQuery & { status?: number }) => client().get<PageData<OrgAccount>>('/org/accounts', { params }),
  account: (id: number) => client().get<OrgAccount>(`/org/accounts/${id}`),
  createAccount: (input: OrgAccountInput) => client().post<{ account: OrgAccount; initialPassword: string }>('/org/accounts', input),
  updateAccount: (id: number, input: OrgAccountUpdate) => client().put<OrgAccount>(`/org/accounts/${id}`, input),
  setAccountStatus: (id: number, status: number) => client().post<null>(`/org/accounts/${id}/status`, { status }),
  resetPassword: (id: number) => client().post<{ initialPassword: string }>(`/org/accounts/${id}/reset-password`),
  assignRoles: (id: number, roleIds: number[]) => client().put<OrgAccount>(`/org/accounts/${id}/roles`, { roleIds }),

  roles: () => client().get<OrgRole[]>('/org/roles'),
  createRole: (input: OrgRoleInput) => client().post<OrgRole>('/org/roles', input),
  updateRole: (id: number, input: OrgRoleInput) => client().put<OrgRole>(`/org/roles/${id}`, input),
  removeRole: (id: number) => client().delete<null>(`/org/roles/${id}`),
  rolePerms: (id: number) => client().get<string[]>(`/org/roles/${id}/perms`),
  grant: (id: number, codes: string[]) => client().put<null>(`/org/roles/${id}/perms`, { codes }),
  permTree: () => client().get<OrgPermGroup[]>('/org/perms/tree'),

  sessions: (params: PageQuery) => client().get<PageData<OrgSession>>('/org/sessions', { params }),
  revokeSession: (sid: string) => client().post<null>(`/org/sessions/${sid}/revoke`),

  loginLogs: (params: OrgLogQuery & { success?: number }) => client().get<PageData<OrgLoginLog>>('/org/login-logs', { params }),
  operationLogs: (params: OrgLogQuery & { action?: string; failed?: number }) => client().get<PageData<OrgOperationLog>>('/org/operation-logs', { params }),

  profile: () => client().get<OrgProfile>('/org/profile'),
  updateProfile: (input: { displayName: string; email: string; phone: string }) => client().put<null>('/org/profile', input),
  setAvatar: (preset: string) => client().put<{ avatar: string }>('/org/profile/avatar', { preset }),
  revokeOtherSessions: () => client().post<{ revoked: number }>('/org/profile/revoke-other-sessions'),
}
