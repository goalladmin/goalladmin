// 字典（docs/decisions.md D-023）：按编码取"值 → 显示文字、颜色、下拉选项"的对照表。
//
//   const priority = useDict('order.priority')
//   priority.label(row.priority)     // 显示文字（按当前语言）
//   priority.options                 // 下拉选项：只含启用的项
//   <GaDictTag code="order.priority" :value="row.priority" />
//
// 同一时刻的多个 useDict 合并成一次请求；结果按"语言 + 编码"缓存，切换语言时自动重新拉取。
// 后台改了字典之后调用 invalidateDicts() 清缓存。

import { computed, reactive, ref, toValue, watch } from 'vue'
import type { MaybeRefOrGetter } from 'vue'
import { useI18n } from 'vue-i18n'

import { usePortal } from '../context'
import type { Client } from '../request/client'

export interface DictItem {
  /** 按字典的值类型：int 字典是数字，string 字典是字符串。 */
  value: string | number
  label: string
  color?: string
  extra?: string
  /** 1 启用，0 停用。停用的项仍用于显示历史数据，但不出现在下拉选项里。 */
  status: number
  children?: DictItem[]
}

export interface DictData {
  code: string
  name: string
  valueType: 'string' | 'int'
  items: DictItem[]
}

/** 批量加载器：同一轮事件循环里的请求合并成一次 GET /dicts?codes=...。 */
export interface DictLoader {
  load(code: string, locale: string): Promise<DictData | null>
  /** 清缓存；不传编码时全部清掉。 */
  invalidate(codes?: string[]): void
  /** 版本号：每次 invalidate 加一，useDict 据此重新拉取。 */
  readonly version: { value: number }
}

const MAX_CODES = 50

export function createDictLoader(client: Client): DictLoader {
  const cache = new Map<string, Promise<DictData | null>>() // key: locale + '\n' + code
  let pending = new Map<string, Map<string, ((d: DictData | null) => void)[]>>() // locale → code → 等待者
  let scheduled = false
  const version = ref(0)

  function flush() {
    scheduled = false
    const batch = pending
    pending = new Map()
    for (const [locale, waiters] of batch) {
      const codes = [...waiters.keys()]
      for (let i = 0; i < codes.length; i += MAX_CODES) {
        const chunk = codes.slice(i, i + MAX_CODES)
        client
          .get<Record<string, DictData>>('/dicts', { params: { codes: chunk.join(',') }, headers: { 'Accept-Language': locale }, silent: true })
          .then((data) => {
            for (const c of chunk) for (const r of waiters.get(c) ?? []) r(data?.[c] ?? null)
          })
          .catch(() => {
            // 失败不缓存，下次再试；页面上退化为显示原始值
            for (const c of chunk) for (const r of waiters.get(c) ?? []) r(null)
          })
      }
    }
  }

  return {
    version,
    load(code, locale) {
      const key = locale + '\n' + code
      const hit = cache.get(key)
      if (hit) return hit
      let failed = false
      const p = new Promise<DictData | null>((resolve) => {
        let waiters = pending.get(locale)
        if (!waiters) {
          waiters = new Map()
          pending.set(locale, waiters)
        }
        const list = waiters.get(code) ?? []
        list.push((d) => {
          resolve(d)
          if (d === null) failed = true
        })
        waiters.set(code, list)
        if (!scheduled) {
          scheduled = true
          queueMicrotask(flush)
        }
      })
      cache.set(key, p)
      // 请求失败或字典不存在都不缓存（后台稍后可能建出这本字典）；只删自己放进去的那一个，不误删更新的
      void p.then(() => {
        if (failed && cache.get(key) === p) cache.delete(key)
      })
      return p
    },
    invalidate(codes) {
      if (!codes) {
        cache.clear()
      } else {
        for (const key of [...cache.keys()]) {
          if (codes.includes(key.slice(key.indexOf('\n') + 1))) cache.delete(key)
        }
      }
      version.value++
    },
  }
}

const loaders = new WeakMap<Client, DictLoader>()

/** 当前端的字典加载器（每个请求客户端一个）。 */
export function useDictLoader(): DictLoader {
  const client = usePortal().client
  let l = loaders.get(client)
  if (!l) {
    l = createDictLoader(client)
    loaders.set(client, l)
  }
  return l
}

/** 后台改了字典后调用，让页面上的 useDict 重新拉取。 */
export function invalidateDicts(codes?: string[]): void {
  useDictLoader().invalidate(codes)
}

function same(a: unknown, b: unknown): boolean {
  return a !== null && a !== undefined && b !== null && b !== undefined && String(a) === String(b)
}

export function findDictItem(items: DictItem[] | undefined, value: unknown): DictItem | undefined {
  for (const it of items ?? []) {
    if (same(it.value, value)) return it
    const hit = findDictItem(it.children, value)
    if (hit) return hit
  }
  return undefined
}

/** 只保留启用的项（层级字典逐层过滤）。 */
export function enabledDictItems(items: DictItem[] | undefined): DictItem[] {
  return (items ?? [])
    .filter((it) => it.status === 1)
    .map((it) => (it.children?.length ? { ...it, children: enabledDictItems(it.children) } : it))
}

export interface UseDict {
  loading: boolean
  /** 字典本身；不存在或看不到时为 null。 */
  dict: DictData | null
  /** 全部项（含停用的），用于显示。 */
  items: DictItem[]
  /** 下拉选项：只含启用的项。 */
  options: DictItem[]
  item: (value: unknown) => DictItem | undefined
  /** 显示文字；找不到时返回值本身。 */
  label: (value: unknown) => string
  color: (value: unknown) => string | undefined
}

/** 在组件 setup 里调用。返回值是 reactive 对象：模板里直接用 priority.options、priority.label(v)。
 *  code 可以是字符串，也可以是 ref / getter（编码变了会重新拉取）。 */
export function useDict(code: MaybeRefOrGetter<string>): UseDict {
  const loader = useDictLoader()
  const { locale } = useI18n()
  const dict = ref<DictData | null>(null)
  const loading = ref(false)
  let seq = 0

  async function fetch() {
    const my = ++seq
    loading.value = true
    try {
      const d = await loader.load(toValue(code), String(locale.value))
      if (my === seq) dict.value = d
    } finally {
      if (my === seq) loading.value = false
    }
  }
  watch([() => toValue(code), locale, loader.version], () => void fetch(), { immediate: true })

  const items = computed(() => dict.value?.items ?? [])
  const options = computed(() => enabledDictItems(items.value))
  const item = (v: unknown) => findDictItem(items.value, v)
  return reactive({
    loading,
    dict,
    items,
    options,
    item,
    label: (v: unknown) => item(v)?.label ?? (v === null || v === undefined ? '' : String(v)),
    color: (v: unknown) => item(v)?.color || undefined,
  }) as UseDict
}

const namedColors = new Set(['primary', 'success', 'warning', 'danger', 'info'])

/** 把字典颜色翻译成 el-tag 的属性：命名颜色用 type，#RRGGBB 用自定义样式。 */
export function dictTagProps(color: string | undefined): { type?: 'primary' | 'success' | 'warning' | 'danger' | 'info'; style?: Record<string, string> } {
  if (!color) return {}
  if (namedColors.has(color)) return { type: color as 'primary' | 'success' | 'warning' | 'danger' | 'info' }
  if (/^#[0-9a-fA-F]{6}$/.test(color)) {
    return { style: { color, borderColor: color, backgroundColor: color + '1a' } }
  }
  return {}
}
