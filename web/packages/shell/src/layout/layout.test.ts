// 布局（D-027）：偏好从 localStorage 读取时只接受合法值；主题色梯度；固定标签不能关闭、关闭其他/全部时保留。

import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { DefaultPreferences, readPreferences, useLayoutStore } from './store'
import type { TabItem } from './store'
import { applyPrimaryColor, mix, parseHex, primaryShades } from './theme'
import { useAuthStore } from '../auth/store'

describe('readPreferences', () => {
  it('空值、坏 JSON、非对象都回到默认值', () => {
    expect(readPreferences(null)).toEqual(DefaultPreferences)
    expect(readPreferences('{bad')).toEqual(DefaultPreferences)
    expect(readPreferences('"x"')).toEqual(DefaultPreferences)
    expect(readPreferences('null')).toEqual(DefaultPreferences)
    expect(readPreferences('[1,2]')).toEqual(DefaultPreferences)
  })

  it('只接受认识的键和合法的值', () => {
    const p = readPreferences(
      JSON.stringify({
        primary: '#AABBCC',
        sidebarTheme: 'dark',
        showTabs: false,
        showBreadcrumb: 'no',
        compact: true,
        extra: 1,
      }),
    )
    expect(p).toEqual({ ...DefaultPreferences, primary: '#aabbcc', sidebarTheme: 'dark', showTabs: false, compact: true })
    expect(p).not.toHaveProperty('extra')
  })

  it('主题色必须是 #RRGGBB，拒绝可以注入样式的值', () => {
    for (const bad of ['red', '#abc', '#aabbcc; background:url(x)', 'url(x)', '#gggggg']) {
      expect(readPreferences(JSON.stringify({ primary: bad })).primary).toBe(DefaultPreferences.primary)
    }
    expect(readPreferences(JSON.stringify({ sidebarTheme: 'blue' })).sidebarTheme).toBe('light')
  })
})

describe('theme', () => {
  it('parseHex 与 mix', () => {
    expect(parseHex('#2563eb')).toEqual([0x25, 0x63, 0xeb])
    expect(parseHex('2563eb')).toBeNull()
    expect(mix('#000000', '#ffffff', 0.5)).toBe('#808080')
    expect(mix('#123456', '#ffffff', 0)).toBe('#123456')
    expect(mix('#123456', '#ffffff', 1)).toBe('#ffffff')
    expect(mix('bad', '#ffffff', 0.5)).toBe('bad')
  })

  it('亮色向白色混、暗色向底色混，梯度越高越接近底色', () => {
    const light = primaryShades('#2563eb', false)
    expect(light['--el-color-primary']).toBe('#2563eb')
    expect(light['--el-color-primary-light-9']).toBe(mix('#2563eb', '#ffffff', 0.9))
    const dark = primaryShades('#2563eb', true)
    expect(dark['--el-color-primary-light-9']).toBe(mix('#2563eb', '#141a26', 0.9))
    expect(Object.keys(light)).toEqual(Object.keys(dark))
  })

  it('非法颜色不写入 CSS 变量', () => {
    const style = document.documentElement.style
    style.removeProperty('--el-color-primary')
    applyPrimaryColor('red; x', false)
    expect(style.getPropertyValue('--el-color-primary')).toBe('')
    applyPrimaryColor('#059669', false)
    expect(style.getPropertyValue('--el-color-primary')).toBe('#059669')
  })
})

describe('标签页', () => {
  const tab = (name: string, affix = false): TabItem => ({
    name,
    path: `/${name}`,
    fullPath: `/${name}`,
    titleKey: `menu.${name}`,
    keepAlive: true,
    affix,
  })

  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
  })

  it('登录身份一变就清空全部标签，登录失效回到登录页时也一样（D-048）', () => {
    const s = useLayoutStore()
    s.openTab(tab('home', true))
    s.openTab(tab('a'))
    useAuthStore().clear()
    expect(s.tabs).toEqual([])
  })

  it('固定标签排在前面、不能关闭', () => {
    const s = useLayoutStore()
    s.openTab(tab('a'))
    s.openTab(tab('home', true))
    s.openTab(tab('b'))
    expect(s.tabs.map((t) => t.name)).toEqual(['home', 'a', 'b'])
    expect(s.closeTab('home', 'home')).toBeNull()
    expect(s.tabs.map((t) => t.name)).toEqual(['home', 'a', 'b'])
    // 关闭当前页时返回左侧的标签
    expect(s.closeTab('b', 'b')?.name).toBe('a')
  })

  it('重新打开已固定的标签不会取消固定', () => {
    const s = useLayoutStore()
    s.openTab(tab('home', true))
    s.openTab(tab('home'))
    expect(s.tabs[0]!.affix).toBe(true)
  })

  it('关闭其他/全部保留固定标签，登出清空全部', () => {
    const s = useLayoutStore()
    s.openTab(tab('home', true))
    s.openTab(tab('a'))
    s.openTab(tab('b'))
    s.setAffix('b', true)
    expect(s.tabs.map((t) => t.name)).toEqual(['home', 'b', 'a'])
    s.closeOthers('a')
    expect(s.tabs.map((t) => t.name)).toEqual(['home', 'b', 'a'])
    s.setAffix('b', false)
    s.closeAll()
    expect(s.tabs.map((t) => t.name)).toEqual(['home'])
    s.maximized = true
    s.resetTabs()
    expect(s.tabs).toEqual([])
    expect(s.maximized).toBe(false)
  })

  it('刷新页面时暂时移出缓存并换 key', async () => {
    const s = useLayoutStore()
    s.openTab(tab('a'))
    s.refreshPage('a')
    expect(s.cachedNames).not.toContain('a')
    expect(s.refreshSeq.a).toBe(1)
    await new Promise((r) => setTimeout(r, 0))
    expect(s.cachedNames).toContain('a')
  })

  it('偏好修改后写入 localStorage', async () => {
    const s = useLayoutStore()
    s.prefs.compact = true
    await new Promise((r) => setTimeout(r, 0))
    expect(readPreferences(localStorage.getItem('ga.prefs')).compact).toBe(true)
    s.resetPreferences()
    expect(s.prefs).toEqual(DefaultPreferences)
  })
})
