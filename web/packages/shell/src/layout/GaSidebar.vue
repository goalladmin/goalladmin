<script setup lang="ts">
// 侧边栏（D-027）：品牌、菜单、底部的折叠按钮。配色跟随偏好设置里的"侧边栏深浅"。
// 动效（D-030）：悬停时背景过渡、图标轻移，点击菜单项时从点击位置泛起涟漪；系统设置了"减少动态效果"时不做。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { DArrowLeft, DArrowRight } from '@element-plus/icons-vue'

import GaMenuItem from './GaMenuItem.vue'
import GaLogo from '../components/GaLogo.vue'
import { prefersReducedMotion } from '../motion'
import { useAuthStore } from '../auth/store'
import { useLayoutStore } from './store'

const props = defineProps<{ collapsed: boolean }>()
const emit = defineEmits<{ navigate: [] }>()
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const layout = useLayoutStore()

const roots = computed(() => auth.menus.filter((m) => !m.hidden).sort((a, b) => a.sort - b.sort))

// 点击涟漪：在被点的菜单项里放一个从点击位置扩散的圆，动画结束后移除
function ripple(e: MouseEvent) {
  if (e.button !== 0 || prefersReducedMotion()) return
  const item = (e.target as HTMLElement | null)?.closest<HTMLElement>('.el-menu-item, .el-sub-menu__title')
  if (!item) return
  const rect = item.getBoundingClientRect()
  const size = Math.max(rect.width, rect.height) * 2
  const dot = document.createElement('span')
  dot.className = 'ga-ripple'
  dot.style.width = dot.style.height = `${size}px`
  dot.style.left = `${e.clientX - rect.left - size / 2}px`
  dot.style.top = `${e.clientY - rect.top - size / 2}px`
  dot.addEventListener('animationend', () => dot.remove(), { once: true })
  item.appendChild(dot)
}

function onSelect(index: string) {
  if (index.startsWith('menu:')) return
  if (index !== route.path) void router.push(index)
  emit('navigate')
}
</script>

<template>
  <div class="ga-sidebar" :class="[`ga-sidebar--${layout.prefs.sidebarTheme}`, { 'is-collapsed': props.collapsed }]" @mousedown="ripple">
    <div class="ga-sidebar__brand" @click="router.push('/')">
      <GaLogo class="ga-sidebar__logo" :size="30" />
      <span v-if="!props.collapsed" class="ga-sidebar__name">{{ t('shell.appName') }}</span>
    </div>
    <el-scrollbar class="ga-sidebar__scroll">
      <el-menu
        :default-active="route.path"
        :collapse="props.collapsed && !layout.isMobile"
        :collapse-transition="false"
        unique-opened
        @select="onSelect"
      >
        <GaMenuItem v-for="m in roots" :key="m.name" :node="m" />
      </el-menu>
    </el-scrollbar>
    <div v-if="!layout.isMobile" class="ga-sidebar__foot">
      <button
        type="button"
        class="ga-sidebar__collapse"
        :title="props.collapsed ? t('shell.layout.expand') : t('shell.layout.collapse')"
        data-test="sidebar-collapse"
        @click="layout.toggleCollapsed()"
      >
        <el-icon><DArrowRight v-if="props.collapsed" /><DArrowLeft v-else /></el-icon>
      </button>
    </div>
  </div>
</template>
