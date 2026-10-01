<script setup lang="ts">
// 锁屏界面（D-027）。锁定由服务端判定：这里只负责显示和提交密码。
// 刷新页面后 /auth/me 返回 locked，依然显示这里；输错到上限会话被吊销，回到登录页。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'

import { Lock, Unlock } from '@element-plus/icons-vue'

import { useAuthStore } from '../auth/store'
import { usePortal } from '../context'
import GaAvatar from './GaAvatar.vue'
import { describeApiError } from '../request/errors'
import { RouteNames } from '../router'
import { Codes, isApiError } from '../types'

const { t, te, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const password = ref('')
const error = ref('')
const loading = ref(false)
const now = ref(new Date())
let timer: ReturnType<typeof setInterval> | undefined

onMounted(() => {
  timer = setInterval(() => (now.value = new Date()), 1000)
})
onBeforeUnmount(() => clearInterval(timer))

const name = () => auth.user?.displayName || auth.user?.username || ''
const time = () => now.value.toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
const date = () => now.value.toLocaleDateString(locale.value, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' })

async function unlock() {
  if (!password.value || loading.value) return
  loading.value = true
  error.value = ''
  try {
    await auth.unlock(password.value)
    password.value = ''
    // 锁屏期间菜单是空的：解锁后重建路由，再回到原来的页面
    usePortal().installMenuRoutes?.()
    await router.replace(router.currentRoute.value.fullPath)
  } catch (e) {
    // 解锁在途时登录身份变了：结果作废，不提示（D-053）
    if (isApiError(e) && e.code === Codes.Superseded) return
    if (isApiError(e) && (e.code === Codes.TokenInvalid || e.status === 401)) {
      auth.clear()
      await router.replace({ name: RouteNames.login })
      return
    }
    error.value = isApiError(e) ? describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep')) : t('shell.error.network')
    password.value = ''
  } finally {
    loading.value = false
  }
}

async function logout() {
  const confirmed = await auth.logout()
  await router.replace({ name: RouteNames.login })
  if (!confirmed) ElMessage.warning(t('shell.error.logoutUnconfirmed'))
}
</script>

<template>
  <div class="ga-lock" role="dialog" aria-modal="true" data-test="lock-screen-overlay">
    <div class="ga-lock__clock">
      <div class="ga-lock__time">{{ time() }}</div>
      <div class="ga-lock__date">{{ date() }}</div>
    </div>
    <form class="ga-lock__card" @submit.prevent="unlock">
      <GaAvatar :size="72" :value="auth.user?.avatar" :name="auth.user?.username" />
      <div class="ga-lock__name">{{ name() }}</div>
      <div class="ga-lock__hint">
        <el-icon><Lock /></el-icon>
        <span>{{ t('shell.lock.locked') }}</span>
      </div>
      <el-input
        v-model="password"
        type="password"
        show-password
        autocomplete="current-password"
        :placeholder="t('shell.lock.password')"
        data-test="unlock-password"
        autofocus
      />
      <div v-if="error" class="ga-lock__error" data-test="unlock-error">{{ error }}</div>
      <el-button type="primary" native-type="submit" :loading="loading" :icon="Unlock" class="ga-lock__btn" data-test="unlock-submit">
        {{ t('shell.lock.unlock') }}
      </el-button>
      <el-button link class="ga-lock__logout" @click="logout">{{ t('shell.lock.logout') }}</el-button>
    </form>
  </div>
</template>
