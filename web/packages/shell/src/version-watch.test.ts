// 版本提示：服务器上换了新版本时提示刷新。

import { afterEach, describe, expect, it, vi } from 'vitest'

import { watchVersion } from './version-watch'

afterEach(() => {
  vi.useRealTimers()
})

describe('版本提示', () => {
  it('服务器上的编号变了才提示，而且只提示一次', async () => {
    vi.useFakeTimers()
    let build = 'a'
    const onUpdate = vi.fn()
    const stop = watchVersion({ current: 'a', url: '/version.json', onUpdate, intervalMs: 1000, fetcher: async () => ({ build }) })
    await vi.advanceTimersByTimeAsync(3000)
    expect(onUpdate).not.toHaveBeenCalled()
    build = 'b'
    await vi.advanceTimersByTimeAsync(1000)
    expect(onUpdate).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5000)
    expect(onUpdate).toHaveBeenCalledTimes(1)
    stop()
  })

  it('开发服务器（没有编号）不检查；读不到、格式不对不提示', async () => {
    vi.useFakeTimers()
    const fetcher = vi.fn(async () => ({ build: 'x' }))
    const onUpdate = vi.fn()
    watchVersion({ current: '', url: '/version.json', onUpdate, intervalMs: 1000, fetcher })
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetcher).not.toHaveBeenCalled()

    const bad = vi.fn(async (): Promise<{ build?: unknown } | null> => {
      throw new Error('offline')
    })
    const stop = watchVersion({ current: 'a', url: '/version.json', onUpdate, intervalMs: 1000, fetcher: bad })
    await vi.advanceTimersByTimeAsync(2000)
    expect(bad).toHaveBeenCalled()
    stop()
    const stop2 = watchVersion({ current: 'a', url: '/version.json', onUpdate, intervalMs: 1000, fetcher: async () => ({ build: 5 }) })
    await vi.advanceTimersByTimeAsync(2000)
    stop2()
    expect(onUpdate).not.toHaveBeenCalled()
  })
})
