<script setup lang="ts">
// 全局搜索（D-027）：⌘K / Ctrl+K 打开，只搜当前用户看得到的菜单页面（来自 /auth/me），不访问后端。
// 方向键选择，回车打开，Esc 关闭。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { Search } from '@element-plus/icons-vue'

import { resolveIcon } from './icons'
import { useAuthStore } from '../auth/store'
import { menuTitle } from '../router/menu'
import type { MenuTree } from '../types'

interface Entry {
  name: string
  path: string
  title: string
  trail: string
  icon: string | undefined
}

const { t, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const open = ref(false)
const keyword = ref('')
const active = ref(0)
const inputRef = ref<HTMLInputElement>()
const listRef = ref<HTMLElement>()
const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent)

/** 把菜单树展开成可打开的页面（不含隐藏的详情页），带上级路径。 */
const entries = computed<Entry[]>(() => {
  const out: Entry[] = []
  const walk = (nodes: MenuTree[], trail: string[]) => {
    for (const n of [...nodes].sort((a, b) => a.sort - b.sort)) {
      if (n.hidden) continue
      const title = menuTitle(t, locale.value, n.titleKey, n.titles)
      if (n.component) out.push({ name: n.name, path: n.path, title, trail: trail.join(' / '), icon: n.icon })
      if (n.children?.length) walk(n.children, [...trail, title])
    }
  }
  walk(auth.menus, [])
  return out
})

const results = computed(() => {
  const k = keyword.value.trim().toLowerCase()
  if (!k) return entries.value
  return entries.value.filter((e) => e.title.toLowerCase().includes(k) || e.trail.toLowerCase().includes(k) || e.path.toLowerCase().includes(k))
})

watch(results, () => (active.value = 0))

function show() {
  keyword.value = ''
  active.value = 0
  open.value = true
}

// 对话框打开后会先把焦点放到自身容器上，所以要在它之后（open-auto-focus / opened）再聚焦输入框。
function focusInput() {
  inputRef.value?.focus()
}

function go(e: Entry | undefined) {
  if (!e) return
  open.value = false
  void router.push(e.path)
}

function move(delta: number) {
  const n = results.value.length
  if (!n) return
  active.value = (active.value + delta + n) % n
  void nextTick(() => listRef.value?.querySelector('.is-active')?.scrollIntoView({ block: 'nearest' }))
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    move(1)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    move(-1)
  } else if (e.key === 'Enter') {
    e.preventDefault()
    go(results.value[active.value])
  }
}

function onGlobalKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    if (open.value) open.value = false
    else show()
  }
}
onMounted(() => window.addEventListener('keydown', onGlobalKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onGlobalKey))
</script>

<template>
  <button type="button" class="ga-search__trigger" data-test="search-open" @click="show">
    <el-icon><Search /></el-icon>
    <span class="ga-search__label">{{ t('shell.search.placeholder') }}</span>
    <kbd class="ga-search__kbd">{{ isMac ? '⌘' : 'Ctrl' }} K</kbd>
  </button>
  <el-dialog v-model="open" class="ga-search" width="560px" :show-close="false" align-center append-to-body @open-auto-focus="focusInput" @opened="focusInput" @keydown="onKeydown">
    <template #header>
      <div class="ga-search__head">
        <el-icon class="ga-search__head-icon"><Search /></el-icon>
        <input ref="inputRef" v-model="keyword" class="ga-search__input" :placeholder="t('shell.search.input')" data-test="search-input" />
      </div>
    </template>
    <div ref="listRef" class="ga-search__list" data-test="search-results">
      <div
        v-for="(e, i) in results"
        :key="e.name"
        class="ga-search__item"
        :class="{ 'is-active': i === active }"
        @mouseenter="active = i"
        @click="go(e)"
      >
        <el-icon v-if="resolveIcon(e.icon)" class="ga-search__item-icon"><component :is="resolveIcon(e.icon)" /></el-icon>
        <span class="ga-search__item-title">{{ e.title }}</span>
        <span v-if="e.trail" class="ga-search__item-trail">{{ e.trail }}</span>
      </div>
      <el-empty v-if="!results.length" :description="t('shell.search.empty')" :image-size="64" />
    </div>
    <template #footer>
      <div class="ga-search__foot">
        <span><kbd>↑</kbd><kbd>↓</kbd> {{ t('shell.search.select') }}</span>
        <span><kbd>↵</kbd> {{ t('shell.search.open') }}</span>
        <span><kbd>Esc</kbd> {{ t('shell.search.close') }}</span>
      </div>
    </template>
  </el-dialog>
</template>
