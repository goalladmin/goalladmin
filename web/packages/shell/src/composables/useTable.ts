// 列表页的通用状态：查询条件、分页、加载、刷新。业务页面只需要提供"怎么拉数据"。

import { reactive, ref } from 'vue'
import type { Ref } from 'vue'

import type { PageData } from '../types'

export interface UseTableOptions<Q extends Record<string, unknown>, T> {
  /** 查询条件的初始值（重置时回到这里）。 */
  query: Q
  /** 拉一页数据。 */
  fetch: (params: Q & { page: number; pageSize: number }) => Promise<PageData<T>>
  pageSize?: number
  /** 是否在创建时自动加载。默认 true。 */
  immediate?: boolean
}

/** 返回值是 reactive 对象：模板里直接用 table.list、table.page，不用 .value。 */
export interface UseTable<Q, T> {
  query: Q
  list: T[]
  total: number
  page: number
  pageSize: number
  loading: boolean
  /** 回到第一页并查询。 */
  search: () => Promise<void>
  /** 清空条件、回到第一页并查询。 */
  reset: () => Promise<void>
  /** 停在当前页重新查询。 */
  reload: () => Promise<void>
  onPageChange: (p: number) => void
  onSizeChange: (s: number) => void
}

export function useTable<Q extends Record<string, unknown>, T>(opts: UseTableOptions<Q, T>): UseTable<Q, T> {
  const initial = { ...opts.query }
  const query = reactive({ ...opts.query }) as Q
  const list = ref<T[]>([]) as Ref<T[]>
  const total = ref(0)
  const page = ref(1)
  const pageSize = ref(opts.pageSize ?? 20)
  const loading = ref(false)
  let seq = 0

  async function reload() {
    const my = ++seq
    loading.value = true
    try {
      const res = await opts.fetch({ ...query, page: page.value, pageSize: pageSize.value })
      if (my !== seq) return // 已有更新的请求
      list.value = res.list ?? []
      total.value = res.total
    } finally {
      if (my === seq) loading.value = false
    }
  }

  async function search() {
    page.value = 1
    await reload()
  }

  async function reset() {
    Object.assign(query, initial)
    await search()
  }

  function onPageChange(p: number) {
    page.value = p
    void reload()
  }

  function onSizeChange(s: number) {
    pageSize.value = s
    page.value = 1
    void reload()
  }

  if (opts.immediate !== false) void reload()

  return reactive({ query, list, total, page, pageSize, loading, search, reset, reload, onPageChange, onSizeChange }) as UseTable<Q, T>
}
