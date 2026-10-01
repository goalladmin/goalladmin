// D-026：接口错误说明按键翻译，没有翻译时用后端的英文说明。

import { describe, expect, it } from 'vitest'

import { describeApiError, formatApiError } from './errors'
import { ApiError, Codes } from '../types'

const dict: Record<string, string> = {
  'err.rbac.menu.maxDepth': '菜单最多 {limit} 层',
  'err.validation.required': '必填',
  'shell.error.listSep': '；',
  'shell.error.withDetail': '{msg}：{detail}',
}
const te = (k: string) => k in dict
const t = (k: string, p: Record<string, unknown> = {}) => (dict[k] ?? k).replace(/\{(\w+)\}/g, (_, n: string) => String(p[n] ?? ''))

describe('describeApiError / formatApiError', () => {
  it('字段错误有翻译时代入参数，没有翻译时用英文 message；重复的合并', () => {
    const err = new ApiError(Codes.Validation, '参数校验失败', {
      data: {
        fields: [
          { field: 'items', message: 'at most 4 levels', key: 'rbac.menu.maxDepth', params: { limit: 4 } },
          { field: 'title', message: 'required', key: 'validation.required' },
          { field: 'name', message: 'required', key: 'validation.required' },
          { field: 'x', message: 'something new', key: 'no.such.key' },
        ],
      },
    })
    expect(describeApiError(err, t, te)).toEqual(['菜单最多 4 层', '必填', 'something new'])
    expect(formatApiError(err, t, te)).toBe('参数校验失败：菜单最多 4 层；必填；something new')
  })
  it('没有字段时用信封的 key，没有翻译用 msg', () => {
    expect(formatApiError(new ApiError(4001, 'the menus have changed', { key: 'rbac.menu.maxDepth', params: { limit: 3 } }), t, te)).toBe('菜单最多 3 层')
    expect(formatApiError(new ApiError(4001, '资源冲突'), t, te)).toBe('资源冲突')
  })
})
