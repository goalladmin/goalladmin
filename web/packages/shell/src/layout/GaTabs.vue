<script setup lang="ts">
// 标签栏（D-027）：带菜单图标；固定的标签排在前面、不能关闭；右侧是刷新当前页、内容区最大化和更多操作。
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'

import { ArrowDown, Close, FullScreen, Refresh } from '@element-plus/icons-vue'

import { resolveIcon } from './icons'
import { useLayoutStore } from './store'
import type { TabItem } from './store'
import { RouteNames } from '../router'
import { menuTitle } from '../router/menu'

const route = useRoute()
const router = useRouter()
const layout = useLayoutStore()
const { t, locale } = useI18n()
const tabTitle = (tab: TabItem) => menuTitle(t, locale.value, tab.titleKey, tab.titles)
const current = computed(() => layout.tabs.find((x) => x.name === route.name))

function isActive(name: string) {
  return route.name === name
}

function open(fullPath: string) {
  if (fullPath !== route.fullPath) void router.push(fullPath)
}

function close(name: string) {
  const next = layout.closeTab(name, String(route.name ?? ''))
  if (next) void router.push(next.fullPath)
  else if (layout.tabs.length === 0 && route.name === name) void router.push('/')
}

function closeOthers() {
  layout.closeOthers(String(route.name ?? ''))
}

function closeAll() {
  layout.closeAll()
  const first = layout.tabs[0]
  if (first) void router.push(first.fullPath)
  else void router.push({ name: RouteNames.root })
}

function refresh() {
  if (route.name) layout.refreshPage(String(route.name))
}

function onCommand(cmd: string) {
  const name = String(route.name ?? '')
  if (cmd === 'refresh') refresh()
  else if (cmd === 'pin') layout.setAffix(name, true)
  else if (cmd === 'unpin') layout.setAffix(name, false)
  else if (cmd === 'others') closeOthers()
  else if (cmd === 'all') closeAll()
}
</script>

<template>
  <div class="ga-tabs">
    <el-scrollbar class="ga-tabs__scroll">
      <div class="ga-tabs__list">
        <div
          v-for="tab in layout.tabs"
          :key="tab.name"
          class="ga-tabs__item"
          :class="{ 'is-active': isActive(tab.name), 'is-affix': tab.affix }"
          :data-test="`tab-${tab.name}`"
          @click="open(tab.fullPath)"
          @click.middle.prevent="close(tab.name)"
        >
          <el-icon v-if="resolveIcon(tab.icon)" class="ga-tabs__icon"><component :is="resolveIcon(tab.icon)" /></el-icon>
          <span>{{ tabTitle(tab) }}</span>
          <svg
            v-if="tab.affix"
            class="ga-tabs__pin"
            viewBox="0 0 24 24"
            :title="t('shell.layout.unpin')"
            aria-hidden="true"
            @click.stop="layout.setAffix(tab.name, false)"
          >
            <path fill="currentColor" d="M16 3a1 1 0 0 1 .7 1.7L15 6.4v4.2l2.7 2.7a1 1 0 0 1-.7 1.7h-4v5.5a1 1 0 0 1-2 0V15H7a1 1 0 0 1-.7-1.7L9 10.6V6.4L7.3 4.7A1 1 0 0 1 8 3h8Z" />
          </svg>
          <el-icon v-else class="ga-tabs__close" @click.stop="close(tab.name)"><Close /></el-icon>
        </div>
      </div>
    </el-scrollbar>
    <div class="ga-tabs__tools">
      <button type="button" class="ga-tabs__tool" :title="t('shell.layout.refresh')" data-test="tab-refresh" @click="refresh">
        <el-icon><Refresh /></el-icon>
      </button>
      <button type="button" class="ga-tabs__tool" :title="t('shell.layout.maximize')" data-test="tab-maximize" @click="layout.maximized = true">
        <el-icon><FullScreen /></el-icon>
      </button>
      <el-dropdown trigger="click" @command="onCommand">
        <button type="button" class="ga-tabs__tool" :title="t('shell.layout.more')">
          <el-icon><ArrowDown /></el-icon>
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="refresh">{{ t('shell.layout.refresh') }}</el-dropdown-item>
            <el-dropdown-item v-if="current && !current.affix" command="pin">{{ t('shell.layout.pin') }}</el-dropdown-item>
            <el-dropdown-item v-if="current && current.affix" command="unpin">{{ t('shell.layout.unpin') }}</el-dropdown-item>
            <el-dropdown-item command="others" divided>{{ t('shell.layout.closeOthers') }}</el-dropdown-item>
            <el-dropdown-item command="all">{{ t('shell.layout.closeAll') }}</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </div>
  </div>
</template>
