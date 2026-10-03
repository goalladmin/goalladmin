import { afterEach, describe, expect, it, vi } from 'vitest'
import type { AxiosAdapter } from 'axios'
import { setPortalContext } from '../context'
import { createClient } from '../request/client'
import { onboardingApi } from './api'

describe('入驻申请请求契约（D-087）', () => {
  afterEach(() => setPortalContext(null))

  it.each(['agent', 'merchant'])('%s 发送浏览器标识与 JSON，但不带已有访问令牌', async portal => {
    const input = {
      name: '测试主体', contactName: '测试联系人', contactPhone: '00000000000',
      ownerUsername: 'test_owner', invitationToken: '', captchaId: 'challenge', captchaCode: '12345',
    }
    const adapter = vi.fn<AxiosAdapter>(async config => {
      expect(config.baseURL).toBe(`/api/${portal}/v1`)
      expect(config.method).toBe('post')
      expect(config.url).toBe('/onboarding/applications')
      expect(config.headers.get('X-GA-Client')).toBe('web')
      expect(config.headers.get('Content-Type')).toBe('application/json')
      expect(config.headers.has('Authorization')).toBe(false)
      expect(JSON.parse(config.data as string)).toEqual(input)
      return { status: 200, statusText: 'OK', headers: {}, config, data: { code: 0, msg: 'ok', data: { reference: 'test-reference' } } }
    })
    const token = { get: vi.fn(() => 'existing-access-token'), set: vi.fn(), clear: vi.fn() }
    const client = createClient({ baseURL: `/api/${portal}/v1`, token, adapter })
    setPortalContext({ portal, scoped: true, client })

    await expect(onboardingApi.apply(input)).resolves.toEqual({ reference: 'test-reference' })
    expect(adapter).toHaveBeenCalledTimes(1)
    expect(token.get).not.toHaveBeenCalled()
    expect(token.set).not.toHaveBeenCalled()
    expect(token.clear).not.toHaveBeenCalled()
  })
})
