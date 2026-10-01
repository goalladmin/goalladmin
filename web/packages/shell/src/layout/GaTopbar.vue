<script setup lang="ts">
// 顶栏（D-027）：左边折叠开关和面包屑；右边搜索、偏好设置、暗色、语言、全屏、锁屏和用户菜单。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'

import { Aim, Expand, Fold, FullScreen, Lock, Moon, Setting, Sunny } from '@element-plus/icons-vue'

import GaAvatar from '../components/GaAvatar.vue'
import GaBreadcrumb from './GaBreadcrumb.vue'
import GaPreferences from './GaPreferences.vue'
import GaSearch from './GaSearch.vue'
import { useLayoutStore } from './store'
import { useAuthStore } from '../auth/store'
import { RouteNames } from '../router'
import GaLanguageSwitch from '../components/GaLanguageSwitch.vue'

const emit = defineEmits<{ toggle: [] }>()
const { t } = useI18n()
const router = useRouter()
const layout = useLayoutStore()
const auth = useAuthStore()
const prefsOpen = ref(false)

const displayName = computed(() => auth.user?.displayName || auth.user?.username || '')

// 全屏
const fullscreen = ref(false)
const canFullscreen = typeof document !== 'undefined' && !!document.fullscreenEnabled
function onFsChange() {
  fullscreen.value = !!document.fullscreenElement
}
async function toggleFullscreen() {
  try {
    if (document.fullscreenElement) await document.exitFullscreen()
    else await document.documentElement.requestFullscreen()
  } catch {
    // 浏览器拒绝（例如不是用户手势触发）时什么也不做
  }
}
onMounted(() => document.addEventListener('fullscreenchange', onFsChange))
onBeforeUnmount(() => document.removeEventListener('fullscreenchange', onFsChange))

async function lock() {
  try {
    await auth.lock()
  } catch {
    // 已提示
  }
}

async function logout() {
  try {
    await ElMessageBox.confirm(t('shell.layout.confirmLogout'), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  const confirmed = await auth.logout()
  await router.push({ name: RouteNames.login })
  if (!confirmed) ElMessage.warning(t('shell.error.logoutUnconfirmed'))
}
</script>

<template>
  <header class="ga-topbar">
    <button type="button" class="ga-topbar__btn" :title="layout.collapsed ? t('shell.layout.expand') : t('shell.layout.collapse')" @click="emit('toggle')">
      <el-icon><Expand v-if="layout.collapsed" /><Fold v-else /></el-icon>
    </button>
    <GaBreadcrumb v-if="!layout.isMobile && layout.prefs.showBreadcrumb" />
    <div class="ga-topbar__spacer" />
    <GaSearch />
    <button type="button" class="ga-topbar__btn" :title="t('shell.prefs.title')" data-test="open-prefs" @click="prefsOpen = true">
      <el-icon><Setting /></el-icon>
    </button>
    <button
      type="button"
      class="ga-topbar__btn"
      :title="layout.dark ? t('shell.layout.light') : t('shell.layout.dark')"
      data-test="toggle-dark"
      @click="layout.setDark(!layout.dark)"
    >
      <el-icon><Sunny v-if="layout.dark" /><Moon v-else /></el-icon>
    </button>
    <GaLanguageSwitch class="ga-topbar__btn" />
    <button
      v-if="canFullscreen && !layout.isMobile"
      type="button"
      class="ga-topbar__btn"
      :title="fullscreen ? t('shell.layout.exitFullscreen') : t('shell.layout.fullscreen')"
      data-test="toggle-fullscreen"
      @click="toggleFullscreen"
    >
      <el-icon><Aim v-if="fullscreen" /><FullScreen v-else /></el-icon>
    </button>
    <button type="button" class="ga-topbar__btn" :title="t('shell.lock.lock')" data-test="lock-screen" @click="lock">
      <el-icon><Lock /></el-icon>
    </button>
    <el-dropdown trigger="click">
      <span class="ga-topbar__user">
        <GaAvatar :size="30" :value="auth.user?.avatar" :name="auth.user?.username" />
        <span class="ga-topbar__username">{{ displayName }}</span>
      </span>
      <template #dropdown>
        <el-dropdown-menu>
          <el-dropdown-item @click="router.push({ name: RouteNames.profile })">{{ t('shell.layout.profile') }}</el-dropdown-item>
          <el-dropdown-item @click="lock">{{ t('shell.lock.lock') }}</el-dropdown-item>
          <el-dropdown-item divided @click="logout">{{ t('shell.layout.logout') }}</el-dropdown-item>
        </el-dropdown-menu>
      </template>
    </el-dropdown>
    <GaPreferences v-model="prefsOpen" />
  </header>
</template>
