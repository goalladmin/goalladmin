import { useRequest } from '@ga/shell'
import type { PageData, PageQuery } from '@ga/shell'
export interface Application {
  id: number; reference: string; portal: string; name: string; contactName: string; contactPhone: string
  ownerUsername: string; agentId: number; reviewState: string; reviewNote: string; orgCode: string; createdAt: string
}
export interface Approval { org: { code: string; name: string; ownerUsername: string }; password: string }
export const onboardingApi = {
  list: (params: PageQuery & { state: string }) => useRequest().get<PageData<Application>>('/onboarding/applications', { params }),
  review: (id: number, decision: 'approve' | 'reject', note: string) => useRequest().post<Approval | null>(`/onboarding/applications/${id}/review`, { decision, note }),
}
