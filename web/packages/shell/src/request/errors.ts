// 把接口错误翻成给人看的一句话（D-026）：字段错误和业务说明优先用 err.<key> 的翻译，没有翻译时用后端给的英文说明。

import type { ApiError } from '../types'

type Translate = (key: string, params?: Record<string, unknown>) => string
type Has = (key: string) => boolean

/** 一个错误的全部说明：有字段明细时逐条列出（去重），否则是业务说明或错误码的默认文案。 */
export function describeApiError(err: ApiError, t: Translate, te: Has): string[] {
  const one = (key: string | undefined, params: Record<string, unknown> | undefined, fallback: string) =>
    key && te(`err.${key}`) ? t(`err.${key}`, params ?? {}) : fallback
  if (err.fields.length) {
    return [...new Set(err.fields.map((f) => one(f.key, f.params, f.message)).filter(Boolean))]
  }
  return [one(err.key, err.params, err.message)]
}

/** describeApiError 的结果拼成一行；有字段明细时前面加上错误码的总说明（分隔符随语言）。 */
export function formatApiError(err: ApiError, t: Translate, te: Has): string {
  const parts = describeApiError(err, t, te)
  if (!err.fields.length) return parts[0] ?? err.message
  const sep = te('shell.error.listSep') ? t('shell.error.listSep') : '; '
  const detail = parts.join(sep)
  return te('shell.error.withDetail') ? t('shell.error.withDetail', { msg: err.message, detail }) : `${err.message}: ${detail}`
}
