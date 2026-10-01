<script setup lang="ts">
// 根组件：按界面语言切换 Element Plus 的语言包（D-026），同步 <html lang>；
// 按偏好切换组件尺寸，会话锁屏时只显示锁屏界面（D-027）。
import { computed, watchEffect } from 'vue'
import { useI18n } from 'vue-i18n'
import bn from 'element-plus/es/locale/lang/bn'
import de from 'element-plus/es/locale/lang/de'
import en from 'element-plus/es/locale/lang/en'
import fr from 'element-plus/es/locale/lang/fr'
import ja from 'element-plus/es/locale/lang/ja'
import ko from 'element-plus/es/locale/lang/ko'
import ms from 'element-plus/es/locale/lang/ms'
import ru from 'element-plus/es/locale/lang/ru'
import ta from 'element-plus/es/locale/lang/ta'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import zhTw from 'element-plus/es/locale/lang/zh-tw'

import GaLockScreen from './components/GaLockScreen.vue'
import { useAuthStore } from './auth/store'
import { useLayoutStore } from './layout/store'

const packs = { 'en-US': en, 'zh-CN': zhCn, 'zh-TW': zhTw, 'ja-JP': ja, 'ko-KR': ko, 'ms-MY': ms, 'ta-IN': ta, 'bn-BD': bn, 'ru-RU': ru, 'fr-FR': fr, 'de-DE': de } as const

const auth = useAuthStore()
const layout = useLayoutStore()
const { locale } = useI18n()
const elLocale = computed(() => packs[locale.value as keyof typeof packs] ?? en)
watchEffect(() => {
  if (typeof document !== 'undefined') document.documentElement.lang = locale.value
})
</script>

<template>
  <el-config-provider :locale="elLocale" :size="layout.prefs.compact ? 'small' : 'default'">
    <!-- 锁屏时卸载页面：锁屏层下面不留着已经显示过的数据（缓存的标签页也一并丢弃） -->
    <GaLockScreen v-if="auth.authenticated && auth.locked" />
    <RouterView v-else />
  </el-config-provider>
</template>
