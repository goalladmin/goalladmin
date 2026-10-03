<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { Lock, OfficeBuilding, User } from '@element-plus/icons-vue'

import GaAuthShell from '../components/GaAuthShell.vue'
import GaLogo from '../components/GaLogo.vue'
import { useAuthStore } from '../auth/store'
import { isScoped, usePortal } from '../context'
import { onboardingApi } from '../onboarding/api'
import { RouteNames } from '../router'
import { Codes, isApiError } from '../types'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

// 主体端（代理商、商户）按"编号 + 账号 + 密码"登录（D-061）。上次输入的编号记在这个浏览器里，下次自动填上（D-067）；
// 读写失败（隐私模式、存储被禁用）就当没有
const scoped = isScoped()
const registration = ref(false)
function orgKey(): string {
  return `ga.${usePortal().portal}.orgCode`
}
function rememberedOrg(): string {
  if (!scoped) return ''
  try {
    return localStorage.getItem(orgKey()) ?? ''
  } catch {
    return ''
  }
}
function rememberOrg(code: string) {
  try {
    localStorage.setItem(orgKey(), code)
  } catch {
    // 记不住就下次再输
  }
}

const form = reactive({ org: rememberedOrg(), username: '', password: '', captchaCode: '' })
const formRef = ref<FormInstance>()
const loading = ref(false)
const captchaRequired = ref(false)
const captchaId = ref('')
const captchaImage = ref('')
const error = ref('')

const rules: FormRules = {
  org: scoped ? [{ required: true, whitespace: true, message: () => t('shell.login.orgRequired'), trigger: 'blur' }] : [],
  username: [{ required: true, message: () => t('shell.login.usernameRequired'), trigger: 'blur' }],
  password: [{ required: true, message: () => t('shell.login.passwordRequired'), trigger: 'blur' }],
  captchaCode: [{ validator: (_r, v, cb) => cb(captchaRequired.value && !v ? new Error(t('shell.login.captchaRequired')) : undefined), trigger: 'blur' }],
}

async function loadCaptcha() {
  try {
    const c = await usePortal().client.get<{ captchaId: string; image: string }>('/auth/captcha', { skipAuth: true, silent: true, headers: { 'X-GA-Client': 'web' } })
    captchaId.value = c.captchaId
    captchaImage.value = c.image
    form.captchaCode = ''
  } catch {
    // 验证码取不到时留空，提交会再报错
  }
}

onMounted(() => {
  if (scoped) void onboardingApi.config().then(c => { registration.value = c.enabled }).catch(() => {})
  // 已登录用户直接回首页（守卫也会拦，这里避免闪一下）
  if (auth.authenticated) void router.replace('/')
})

async function submit() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  loading.value = true
  error.value = ''
  try {
    const org = scoped ? form.org.trim().toUpperCase() : undefined
    const tok = await auth.login({
      org,
      username: form.username.trim(),
      password: form.password,
      captchaId: captchaRequired.value ? captchaId.value : undefined,
      captchaCode: captchaRequired.value ? form.captchaCode : undefined,
    })
    if (org) rememberOrg(org)
    if (tok.mustChangePwd) {
      await router.replace({ name: RouteNames.changePassword })
      return
    }
    usePortal().installMenuRoutes?.()
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.replace(redirect.startsWith('/') ? redirect : '/')
  } catch (e) {
    if (!isApiError(e)) {
      ElMessage.error(t('shell.error.network'))
      return
    }
    // 登录在途时本页的登录身份变了（退出、别的登录）：这次结果作废，不提示（D-053）
    if (e.code === Codes.Superseded) return
    const data = (e.data ?? {}) as { captchaRequired?: boolean; lockedUntil?: string }
    if (e.code === Codes.CaptchaRequired || data.captchaRequired) {
      captchaRequired.value = true
      await loadCaptcha()
    }
    if (e.code === Codes.Locked) {
      error.value = t('shell.login.locked', { until: data.lockedUntil ? new Date(data.lockedUntil).toLocaleString() : '' })
    } else if (e.code === Codes.CaptchaRequired) {
      error.value = t('shell.login.captchaRequired')
    } else if (e.code === Codes.LoginFailed) {
      error.value = t('shell.login.failed')
      if (captchaRequired.value) await loadCaptcha()
    } else {
      error.value = e.message
    }
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <GaAuthShell>
    <div class="ga-auth__brand">
      <GaLogo class="ga-auth__logo" :size="88" />
      <h1 class="ga-auth__title">{{ t('shell.appName') }}</h1>
      <p class="ga-auth__subtitle">{{ t('shell.login.tagline') }}</p>
    </div>
    <el-form ref="formRef" :model="form" :rules="rules" size="large" label-position="top" class="ga-auth__form" @submit.prevent="submit">
      <el-form-item v-if="scoped" prop="org" :label="t('shell.login.org')">
        <el-input v-model="form.org" name="org" autocomplete="organization" :placeholder="t('shell.login.orgPlaceholder')" :prefix-icon="OfficeBuilding" maxlength="32" data-test="login-org" />
      </el-form-item>
      <el-form-item prop="username" :label="t('shell.login.username')">
        <el-input v-model="form.username" name="username" autocomplete="username" :prefix-icon="User" data-test="login-username" />
      </el-form-item>
      <el-form-item prop="password" :label="t('shell.login.password')">
        <el-input v-model="form.password" name="password" type="password" show-password autocomplete="current-password" :prefix-icon="Lock" data-test="login-password" />
      </el-form-item>
      <el-form-item v-if="captchaRequired" prop="captchaCode" :label="t('shell.login.captcha')">
        <div class="ga-auth__captcha">
          <el-input v-model="form.captchaCode" name="captcha" :placeholder="t('shell.login.captchaPlaceholder')" data-test="login-captcha" />
          <img v-if="captchaImage" :src="captchaImage" :title="t('shell.login.refreshCaptcha')" alt="captcha" @click="loadCaptcha" />
        </div>
      </el-form-item>
      <el-alert v-if="auth.accountSwitched && !error" :title="t('shell.login.accountSwitched')" type="warning" :closable="false" show-icon class="ga-auth__error" data-test="login-switched" />
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon class="ga-auth__error" data-test="login-error" />
      <el-button type="primary" native-type="submit" :loading="loading" class="ga-auth__submit" data-test="login-submit">
        {{ t('shell.login.submit') }}
      </el-button>
    </el-form>
    <el-button v-if="registration" text data-test="register-link" @click="router.push('/register')">{{ t('onboarding.title') }}</el-button>
    <template #foot>{{ t('shell.login.foot') }}</template>
  </GaAuthShell>
</template>
