import { useRequest } from '@ga/shell'
import type { PageData, PageQuery } from '@ga/shell'
export interface Invitation { id: number; expiresAt: string; usedAt: string | null; revokedAt: string | null; createdAt: string }
export const invitationsApi = {
  config: () => useRequest().get<{ invitationsEnabled: boolean }>('/onboarding/config', { skipAuth: true }),
  list: (params: PageQuery) => useRequest().get<PageData<Invitation>>('/onboarding/invitations', { params }),
  create: () => useRequest().post<{ url: string }>('/onboarding/invitations'),
  revoke: (id: number) => useRequest().delete(`/onboarding/invitations/${id}`),
}
