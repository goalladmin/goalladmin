// 字典（D-023）：同一轮的读取合并成一次请求、按语言缓存、清缓存后重新拉取、失败不缓存；
// useDict 的显示文字、下拉选项（只含启用项）和切换语言后的重新拉取。

import { describe, expect, it } from 'vitest'
import { defineComponent, h, nextTick } from 'vue'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import type { AxiosAdapter, AxiosResponse, InternalAxiosRequestConfig } from 'axios'

import { createClient } from '../request/client'
import { setPortalContext } from '../context'
import { createDictLoader, dictTagProps, enabledDictItems, findDictItem, useDict } from './index'
import type { DictData } from './index'

const priority = (lang: string): DictData => ({
  code: 'order.priority',
  name: lang === 'en-US' ? 'Priority' : '优先级',
  valueType: 'int',
  items: [
    { value: 1, label: lang === 'en-US' ? 'Low' : '低', color: 'info', status: 1 },
    { value: 2, label: lang === 'en-US' ? 'Medium' : '中', color: 'primary', status: 1 },
    { value: 3, label: lang === 'en-US' ? 'High' : '高', color: '#cc0000', status: 0 },
  ],
})

const region: DictData = {
  code: 'region',
  name: '地区',
  valueType: 'string',
  items: [
    {
      value: 'cn',
      label: '中国',
      status: 1,
      children: [
        { value: 'cn-bj', label: '北京', status: 1 },
        { value: 'cn-xx', label: '停用的', status: 0 },
      ],
    },
  ],
}

/** 假后端：记录每次 /dicts 请求的编码和语言。 */
function backend(opts: { fail?: boolean } = {}) {
  const calls: { codes: string[]; lang: string }[] = []
  let fail = opts.fail ?? false
  const adapter: AxiosAdapter = async (cfg) => {
    const c = cfg as InternalAxiosRequestConfig
    const codes = String(c.params?.codes ?? '').split(',')
    const lang = String(c.headers.get('Accept-Language') ?? '')
    calls.push({ codes, lang })
    if (fail) {
      return { status: 500, statusText: '', data: { code: 5000, data: null, msg: 'x', requestId: 'r' }, headers: {}, config: c } as AxiosResponse
    }
    const data: Record<string, DictData> = {}
    for (const code of codes) {
      if (code === 'order.priority') data[code] = priority(lang)
      if (code === 'region') data[code] = region
    }
    return { status: 200, statusText: '', data: { code: 0, data, msg: 'ok', requestId: 'r' }, headers: {}, config: c } as AxiosResponse
  }
  const client = createClient({ baseURL: '/api/p/v1', token: { get: () => 't', set: () => {}, clear: () => {} }, adapter })
  return { client, calls, setFail: (v: boolean) => (fail = v) }
}

describe('createDictLoader', () => {
  it('同一轮的读取合并成一次请求，按语言分开，结果缓存', async () => {
    const be = backend()
    const l = createDictLoader(be.client)
    const [a, b, c, zh] = await Promise.all([
      l.load('order.priority', 'en-US'),
      l.load('region', 'en-US'),
      l.load('nope', 'en-US'),
      l.load('order.priority', 'zh-CN'),
    ])
    expect(be.calls).toHaveLength(2)
    expect(be.calls.find((x) => x.lang === 'en-US')?.codes.sort()).toEqual(['nope', 'order.priority', 'region'])
    expect(a?.items[0]?.label).toBe('Low')
    expect(zh?.items[0]?.label).toBe('低')
    expect(b?.code).toBe('region')
    expect(c).toBeNull()

    await l.load('order.priority', 'en-US')
    expect(be.calls).toHaveLength(2) // 命中缓存
    await l.load('nope', 'en-US')
    expect(be.calls).toHaveLength(3) // 看不到的编码不缓存：后台稍后可能建出这本字典

    l.invalidate(['order.priority'])
    await l.load('order.priority', 'en-US')
    await l.load('region', 'en-US')
    expect(be.calls).toHaveLength(4)
    expect(be.calls[3]?.codes).toEqual(['order.priority'])
  })

  it('一次最多 50 个编码，超过的分批', async () => {
    const be = backend()
    const l = createDictLoader(be.client)
    await Promise.all(Array.from({ length: 120 }, (_, i) => l.load('c' + i, 'zh-CN')))
    expect(be.calls.map((c) => c.codes.length)).toEqual([50, 50, 20])
  })

  it('同一轮里清缓存后再次读取，两个等待者都能拿到结果', async () => {
    const be = backend()
    const l = createDictLoader(be.client)
    const a = l.load('region', 'zh-CN')
    l.invalidate()
    const b = l.load('region', 'zh-CN')
    const [ra, rb] = await Promise.all([a, b])
    expect(ra?.code).toBe('region')
    expect(rb?.code).toBe('region')
    expect(be.calls).toHaveLength(1)
  })

  it('请求失败不缓存，下次重试', async () => {
    const be = backend({ fail: true })
    const l = createDictLoader(be.client)
    expect(await l.load('region', 'zh-CN')).toBeNull()
    be.setFail(false)
    expect((await l.load('region', 'zh-CN'))?.code).toBe('region')
    expect(be.calls).toHaveLength(2)
  })
})

describe('字典工具函数', () => {
  it('按值查找（含子项，数字和字符串互认）、只保留启用项、标签颜色', () => {
    expect(findDictItem(region.items, 'cn-bj')?.label).toBe('北京')
    expect(findDictItem(priority('zh-CN').items, '2')?.label).toBe('中')
    expect(findDictItem(priority('zh-CN').items, 9)).toBeUndefined()
    expect(findDictItem(priority('zh-CN').items, null)).toBeUndefined()

    const opts = enabledDictItems(region.items)
    expect(opts[0]?.children?.map((c) => c.value)).toEqual(['cn-bj'])
    expect(enabledDictItems(priority('zh-CN').items).map((i) => i.value)).toEqual([1, 2])

    expect(dictTagProps('success')).toEqual({ type: 'success' })
    expect(dictTagProps('#00aa00').style?.color).toBe('#00aa00')
    expect(dictTagProps('red')).toEqual({})
    expect(dictTagProps(undefined)).toEqual({})
  })
})

describe('useDict', () => {
  it('显示文字、下拉只含启用项、切换语言重新拉取', async () => {
    const be = backend()
    setPortalContext({ portal: 'p', client: be.client })
    const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {}, 'en-US': {} } })
    let api: ReturnType<typeof useDict> | undefined
    const Comp = defineComponent({
      setup() {
        api = useDict('order.priority')
        return () => h('span', api?.label(3))
      },
    })
    const w = mount(Comp, { global: { plugins: [i18n] } })
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
    expect(w.text()).toBe('高') // 停用的项仍能显示
    expect(api?.options.map((o) => o.value)).toEqual([1, 2])
    expect(api?.label(99)).toBe('99')
    expect(api?.color(1)).toBe('info')

    i18n.global.locale.value = 'en-US'
    await new Promise((r) => setTimeout(r, 0))
    await nextTick()
    expect(w.text()).toBe('High')
    expect(be.calls.map((c) => c.lang)).toEqual(['zh-CN', 'en-US'])
    setPortalContext(null)
  })
})
