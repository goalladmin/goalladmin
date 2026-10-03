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

