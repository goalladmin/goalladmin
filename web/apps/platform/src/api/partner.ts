// 代理商管理、商户管理接口的类型化封装（docs/api.md，D-065、D-066）。两种主体的接口一一对应，按 kind 拼路径；
// 商户另有改归属代理商和选代理商。页面只调用这里的函数，不直接拼 URL。

import { useRequest } from '@ga/shell'
import type { PageData, PageQuery } from '@ga/shell'

import type { IPRule, LoginLog, LoginLogQuery, OperationLog, OplogQuery } from './system'

/** 主体的种类：代理商或商户。也是它们所在的端代号。 */
export type OrgKind = 'agent' | 'merchant'

/** 平台的运维中心能看的端（D-066）：平台端，以及和平台共用一个库的代理商端、商户端。 */
export const opsPortals = ['platform', 'agent', 'merchant'] as const
export type OpsPortal = (typeof opsPortals)[number]

/** 主体（代理商或商户）的资料。agentId、agentCode、agentName 只有商户有。 */
export interface OrgView {
  id: number
  code: string
  name: string
  contactName: string
  contactPhone: string
  agentId: number
  agentCode?: string
  agentName?: string
  ownerUserId: number
  ownerUsername: string
  status: number
  sort: number
  remark: string
  createdAt: string
  updatedAt: string
  createdBy: number
  updatedBy: number
}

export interface OrgInput {
  name: string
  contactName: string
  contactPhone: string
  sort: number
  remark: string
}

export interface OrgCreateInput extends OrgInput {
  /** 只对商户：所属代理商，0 表示直属平台。 */
  agentId?: number
  ownerUsername: string
  ownerDisplayName: string
}

/** 主体的账号（平台只读）。 */
export interface OrgAccount {
  id: number
  orgId: number
  username: string
  displayName: string
  email: string
  phone: string
  avatar: string
  /** 是不是这个主体的主账号。 */
  owner: boolean
  mustChangePwd: boolean
  lastLoginAt: string | null
  lastLoginIp: string
  status: number
  createdAt: string
}

/** 主体账号的一个有效会话。 */
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
}

/** 主体的编号和名称（选代理商用）。 */
export interface OrgBrief {
  id: number
  code: string
  name: string
  status: number
}

/** 主体端的登录日志：带主体的编号（登录时输入的）和名称。 */
export interface OrgLoginLog extends LoginLog {
  orgId: number
  orgCode: string
  orgName: string
}

/** 主体端的操作日志：带操作人所属主体的编号和名称。 */
export interface OrgOperationLog extends OperationLog {
  orgId: number
  orgCode: string
  orgName: string
}

export interface OrgCreated {
  agent?: OrgView
  merchant?: OrgView
  ownerId: number
  /** 主账号的初始密码：只在这次响应里出现。 */
  initialPassword: string
}

export function orgApi(kind: OrgKind) {
  const base = `/${kind}/${kind}s`
  return {
    list: (params: PageQuery & { status?: number; agentId?: number }) => useRequest().get<PageData<OrgView>>(base, { params }),
    get: (id: number) => useRequest().get<OrgView>(`${base}/${id}`),
    create: (input: OrgCreateInput) => useRequest().post<OrgCreated>(base, input),
    update: (id: number, input: OrgInput) => useRequest().put<null>(`${base}/${id}`, input),
    setStatus: (id: number, status: number) => useRequest().post<null>(`${base}/${id}/status`, { status }),
    resetOwnerPassword: (id: number) => useRequest().post<{ initialPassword: string }>(`${base}/${id}/reset-owner-password`),
    changeOwner: (id: number, userId: number) => useRequest().put<null>(`${base}/${id}/owner`, { userId }),
    accounts: (id: number, params: PageQuery & { status?: number }) => useRequest().get<PageData<OrgAccount>>(`${base}/${id}/accounts`, { params }),
    sessions: (id: number, params: PageQuery) => useRequest().get<PageData<OrgSession>>(`${base}/${id}/sessions`, { params }),
    revokeSession: (id: number, sid: string) => useRequest().post<null>(`${base}/${id}/sessions/${sid}/revoke`),
    ipAllow: (id: number) => useRequest().get<{ items: IPRule[] }>(`${base}/${id}/ip-allow`),
    clearIPAllow: (id: number) => useRequest().delete<{ removed: number }>(`${base}/${id}/ip-allow`),
    /** 主体自己设的 IP 黑名单（含已过期的记录）：平台只能看和整体清空（D-102）。 */
    ipDeny: (id: number, params: PageQuery) => useRequest().get<PageData<IPRule>>(`${base}/${id}/ip-deny`, { params }),
    clearIPDeny: (id: number) => useRequest().delete<{ removed: number }>(`${base}/${id}/ip-deny`),
    loginLogs: (params: LoginLogQuery & { orgCode?: string }) => useRequest().get<PageData<OrgLoginLog>>(`/${kind}/login-logs`, { params }),
    operationLogs: (params: OplogQuery & { orgCode?: string }) => useRequest().get<PageData<OrgOperationLog>>(`/${kind}/operation-logs`, { params }),
  }
}

/** 只对商户：改归属代理商（agentId 为 0 表示改为直属平台）、选代理商。 */
export const merchantApi = {
  transfer: (id: number, agentId: number) => useRequest().put<null>(`/merchant/merchants/${id}/agent`, { agentId }),
  agentOptions: (keyword = '') => useRequest().get<OrgBrief[]>('/merchant/options/agents', { params: { keyword } }),
}

/** 两种主体各自的权限码（和后端 modules/agent、modules/merchant 的声明一致）。 */
export function orgPerms(kind: OrgKind) {
  const p = `partner:${kind}`
  return {
    list: `${p}:list`,
    create: `${p}:create`,
    update: `${p}:update`,
    status: `${p}:status`,
    owner: `${p}:owner`,
    log: `${p}:log`,
    transfer: `${p}:transfer`, // 只有商户有
  }
}
