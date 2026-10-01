// 格式化（D-030）：字节数进位、相对时间、时长拆分。

import { describe, expect, it } from 'vitest'

import { formatBytes, formatRelative, splitDuration } from './format'

describe('formatBytes', () => {
  it('按 1024 进位，带单位', () => {
    expect(formatBytes(0, 'en-US')).toBe('0 byte')
    expect(formatBytes(512, 'en-US')).toBe('512 byte')
    expect(formatBytes(1536, 'en-US')).toBe('1.5 kB')
    expect(formatBytes(5 * 1024 * 1024, 'en-US')).toBe('5 MB')
    expect(formatBytes(3.25 * 1024 ** 3, 'en-US')).toBe('3.3 GB')
    expect(formatBytes(2 * 1024 ** 5, 'en-US')).toBe('2,048 TB')
  })
  it('负数、NaN、空值按 0', () => {
    expect(formatBytes(-5, 'en-US')).toBe('0 byte')
    expect(formatBytes(null, 'en-US')).toBe('0 byte')
    expect(formatBytes(Number.NaN, 'en-US')).toBe('0 byte')
  })
})

describe('formatRelative', () => {
  const now = new Date('2026-09-28T12:00:00Z')
  it('按距离选单位', () => {
    expect(formatRelative('2026-09-28T11:59:50Z', 'en-US', now)).toBe('now')
    expect(formatRelative('2026-09-28T11:57:00Z', 'en-US', now)).toBe('3 minutes ago')
    expect(formatRelative('2026-09-28T09:00:00Z', 'en-US', now)).toBe('3 hours ago')
    expect(formatRelative('2026-09-27T12:00:00Z', 'en-US', now)).toBe('yesterday')
    expect(formatRelative('2026-09-23T12:00:00Z', 'en-US', now)).toBe('5 days ago')
    expect(formatRelative('2026-06-01T12:00:00Z', 'en-US', now)).toBe('Jun 1, 2026')
    expect(formatRelative('2026-09-28T11:57:00Z', 'zh-CN', now)).toBe('3分钟前')
  })
  it('空值和坏值', () => {
    expect(formatRelative(null, 'en-US', now)).toBe('')
    expect(formatRelative('not a date', 'en-US', now)).toBe('not a date')
  })
})

describe('splitDuration', () => {
  it('拆成天时分秒', () => {
    expect(splitDuration(0)).toEqual({ d: 0, h: 0, m: 0, s: 0 })
    expect(splitDuration(90061)).toEqual({ d: 1, h: 1, m: 1, s: 1 })
    expect(splitDuration(-5)).toEqual({ d: 0, h: 0, m: 0, s: 0 })
    expect(splitDuration(59.9)).toEqual({ d: 0, h: 0, m: 0, s: 59 })
  })
})
