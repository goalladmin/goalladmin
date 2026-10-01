// 布局状态：折叠、暗色、标签页、移动端、偏好设置（D-027）。
// 暗色、折叠、偏好记在 localStorage：只是每个浏览器的显示偏好，不是敏感数据；读不到就用默认值。

import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

import { applyPrimaryColor } from './theme'
import { useAuthStore } from '../auth/store'

export interface TabItem {
  name: string
  path: string
  fullPath: string
  titleKey: string
  titles?: Record<string, string>
  icon?: string
  keepAlive: boolean
  /** 固定的标签：排在前面，不能关闭（首页默认固定）。 */
  affix?: boolean
}

/** 偏好设置（D-027）。 */
export interface Preferences {
  /** 主题色（#RRGGBB）。 */
  primary: string
  /** 侧边栏配色。 */
  sidebarTheme: 'light' | 'dark'
  showTabs: boolean
  showBreadcrumb: boolean
  /** 紧凑：组件用小号尺寸，间距收紧。 */
  compact: boolean
}

/** 可选的主题色。 */
export const PrimaryPresets = ['#2563eb', '#0891b2', '#059669', '#7c3aed', '#db2777', '#dc2626', '#ea580c', '#0f172a'] as const

export const DefaultPreferences: Preferences = {
  primary: '#2563eb',
  sidebarTheme: 'light',
  showTabs: true,
  showBreadcrumb: true,
  compact: false,
}

const KEY_DARK = 'ga.dark'
const KEY_COLLAPSED = 'ga.collapsed'
const KEY_PREFS = 'ga.prefs'

function readBool(key: string, def: boolean): boolean {
  try {
    const v = localStorage.getItem(key)
    return v === null ? def : v === '1'
  } catch {
    return def
  }
}

function writeBool(key: string, v: boolean): void {
  try {
    localStorage.setItem(key, v ? '1' : '0')
  } catch {
    // 忽略
  }
}

/** 读偏好：只接受认识的键和合法的值，其余用默认值（localStorage 里的内容不可信）。 */
export function readPreferences(raw: string | null): Preferences {
  const out = { ...DefaultPreferences }
  if (!raw) return out
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return out
  }
  // "null"、数组、字符串等都不是偏好对象
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return out
  const v = parsed as Record<string, unknown>
  if (typeof v.primary === 'string' && /^#[0-9a-fA-F]{6}$/.test(v.primary)) out.primary = v.primary.toLowerCase()
  if (v.sidebarTheme === 'light' || v.sidebarTheme === 'dark') out.sidebarTheme = v.sidebarTheme
  for (const k of ['showTabs', 'showBreadcrumb', 'compact'] as const) {
    if (typeof v[k] === 'boolean') out[k] = v[k]
  }
  return out
}

function loadPreferences(): Preferences {
  try {
    return readPreferences(localStorage.getItem(KEY_PREFS))
  } catch {
    return { ...DefaultPreferences }
  }
}

function prefersDark(): boolean {
  return typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-color-scheme: dark)').matches
}

export const useLayoutStore = defineStore('ga.layout', () => {
  const collapsed = ref(readBool(KEY_COLLAPSED, false))
  const dark = ref(readBool(KEY_DARK, prefersDark()))
  const isMobile = ref(false)
  const drawerOpen = ref(false)
  const tabs = ref<TabItem[]>([])
  const prefs = ref<Preferences>(loadPreferences())
  /** 内容区最大化：隐藏侧边栏、顶栏和标签栏。 */
  const maximized = ref(false)
  /** 正在刷新的页面：暂时移出缓存，并换一个 key 让它重新创建。 */
  const refreshing = ref<string | null>(null)
  const refreshSeq = ref<Record<string, number>>({})

  const cachedNames = computed(() => tabs.value.filter((t) => t.keepAlive && t.name !== refreshing.value).map((t) => t.name))

  function applyDark() {
    if (typeof document !== 'undefined') document.documentElement.classList.toggle('dark', dark.value)
    applyPrimaryColor(prefs.value.primary, dark.value)
  }
  applyDark()

  watch(
    prefs,
    (v) => {
      try {
        localStorage.setItem(KEY_PREFS, JSON.stringify(v))
      } catch {
        // 忽略
      }
      applyPrimaryColor(v.primary, dark.value)
    },
    { deep: true },
  )

  function toggleCollapsed() {
    collapsed.value = !collapsed.value
    writeBool(KEY_COLLAPSED, collapsed.value)
  }

  function setDark(v: boolean) {
    dark.value = v
    writeBool(KEY_DARK, v)
    applyDark()
  }

  function setMobile(v: boolean) {
    isMobile.value = v
    if (!v) drawerOpen.value = false
  }

  function resetPreferences() {
    prefs.value = { ...DefaultPreferences }
  }

  /** 固定的标签排在前面，其余按打开顺序。 */
  function sortTabs() {
    tabs.value = [...tabs.value.filter((t) => t.affix), ...tabs.value.filter((t) => !t.affix)]
  }

  function openTab(t: TabItem) {
    const i = tabs.value.findIndex((x) => x.name === t.name)
    if (i >= 0) tabs.value[i] = { ...t, affix: tabs.value[i]!.affix || t.affix }
    else tabs.value.push(t)
    sortTabs()
  }

  function setAffix(name: string, v: boolean) {
    const t = tabs.value.find((x) => x.name === name)
    if (!t) return
    t.affix = v
    sortTabs()
  }

  /** 关闭标签，返回应当跳转到的标签（关闭的是当前页时用），没有则返回 null。固定的标签不能关。 */
  function closeTab(name: string, currentName: string | null): TabItem | null {
    const i = tabs.value.findIndex((x) => x.name === name)
    if (i < 0 || tabs.value[i]!.affix) return null
    tabs.value.splice(i, 1)
    if (name !== currentName) return null
    return tabs.value[i - 1] ?? tabs.value[i] ?? null
  }

  function closeOthers(name: string) {
    tabs.value = tabs.value.filter((x) => x.name === name || x.affix)
  }

  function closeAll() {
    tabs.value = tabs.value.filter((x) => x.affix)
  }

  /** 登出时清空全部标签（包括固定的）：下一个登录的人看到的菜单可能不同。 */
  function resetTabs() {
    tabs.value = []
    maximized.value = false
  }

  // 登录身份一变（退出、登录失效、换人登录）就清空标签（D-048）：不靠每个退出入口记得调用，
  // 登录失效时跳回登录页的那条路也不会把上一个人的标签留给下一个人
  const auth = useAuthStore()
  watch(
    () => auth.epoch,
    () => resetTabs(),
    { flush: 'sync' },
  )

  /** 刷新某个页面：先移出缓存，等下一轮渲染再换 key 重新创建。 */
  function refreshPage(name: string) {
    refreshing.value = name
    refreshSeq.value = { ...refreshSeq.value, [name]: (refreshSeq.value[name] ?? 0) + 1 }
    setTimeout(() => {
      if (refreshing.value === name) refreshing.value = null
    }, 0)
  }

  return {
    collapsed,
    dark,
    isMobile,
    drawerOpen,
    tabs,
    prefs,
    maximized,
    refreshing,
    refreshSeq,
    cachedNames,
    toggleCollapsed,
    setDark,
    setMobile,
    resetPreferences,
    openTab,
    setAffix,
    closeTab,
    closeOthers,
    closeAll,
    resetTabs,
    refreshPage,
  }
})
