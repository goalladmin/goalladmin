import { usePortal } from '../context'
import { HeaderClient } from '../request/client'
export interface ApplicationInput {
  name: string
  contactName: string
  contactPhone: string
  ownerUsername: string
  invitationToken: string
  captchaId: string
  captchaCode: string
}
export const onboardingApi = {
  config: () => usePortal().client.get<{ enabled: boolean }>('/onboarding/config', { skipAuth: true, silent: true }),
  captcha: () => usePortal().client.get<{ captchaId: string; image: string }>('/auth/captcha', { skipAuth: true, headers: { 'X-GA-Client': 'web' } }),
  apply: (data: ApplicationInput) => usePortal().client.post<{ reference: string }>('/onboarding/applications', data, { skipAuth: true, headers: { [HeaderClient]: 'web' } }),
}
