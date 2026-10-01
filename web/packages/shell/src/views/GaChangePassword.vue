<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import GaAuthShell from '../components/GaAuthShell.vue'
import GaLogo from '../components/GaLogo.vue'
import { useAuthStore } from '../auth/store'
import { usePortal } from '../context'
import { RouteNames } from '../router'
import { describeApiError } from '../request/errors'
import { Codes, isApiError } from '../types'

const { t, te } = useI18n()
const router = useRouter()
const auth = useAuthStore()

const form = reactive({ oldPassword: '', newPassword: '', confirm: '' })
const formRef = ref<FormInstance>()
const loading = ref(false)
const error = ref('')

// 规则跟着后端下发的密码策略走（portals.<code>.password）；这里只是提前提示，最终以后端校验为准
const policy = computed(() => auth.pwdPolicy ?? { minLength: 10, requireUpper: false, requireLower: false, requireSymbol: false, maxAgeDays: 0 })
const ruleText = computed(() => {
  const p = policy.value
  const extra = [p.requireUpper && t('shell.pwd.upper'), p.requireLower && t('shell.pwd.lower'), p.requireSymbol && t('shell.pwd.symbol')].filter(Boolean)
  return t('shell.pwd.ruleBase', { n: p.minLength }) + (extra.length ? t('shell.pwd.ruleExtra', { items: extra.join(t('shell.pwd.sep')) }) : '')
})

function checkPolicy(v: string): boolean {
  const p = policy.value
  if ([...v].length < p.minLength || !/\p{L}/u.test(v) || !/\p{N}/u.test(v)) return false
  if (p.requireUpper && !/\p{Lu}/u.test(v)) return false
  if (p.requireLower && !/\p{Ll}/u.test(v)) return false
  if (p.requireSymbol && !/[^\p{L}\p{N}\s]/u.test(v)) return false
  return true
}

const rules: FormRules = {
  oldPassword: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  newPassword: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { validator: (_r, v, cb) => cb(checkPolicy(String(v ?? '')) ? undefined : new Error(ruleText.value)), trigger: 'blur' },
  ],
  confirm: [{ validator: (_r, v, cb) => cb(v !== form.newPassword ? new Error(t('shell.pwd.mismatch')) : undefined), trigger: 'blur' }],
}

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
    await auth.changePassword(form.oldPassword, form.newPassword)
    ElMessage.success(t('shell.pwd.done'))
    usePortal().installMenuRoutes?.()
    await router.replace('/')
  } catch (e) {
    // 改密在途时登录身份变了：结果作废，不提示（D-053）
    if (isApiError(e) && e.code === Codes.Superseded) return
    if (isApiError(e)) {
      error.value = describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
    } else {
      error.value = t('shell.error.network')
    }
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
  <GaAuthShell>
    <div class="ga-auth__brand">
      <GaLogo class="ga-auth__logo" :size="56" />
      <h1 class="ga-auth__title ga-auth__title--sm">{{ t('shell.pwd.title') }}</h1>
    </div>
    <el-alert v-if="auth.mustChangePwd" :title="auth.user?.pwdExpired ? t('shell.pwd.expired') : t('shell.pwd.forced')" type="warning" :closable="false" show-icon class="ga-auth__error" />
    <el-form ref="formRef" :model="form" :rules="rules" size="large" label-position="top" class="ga-auth__form" @submit.prevent="submit">
      <el-form-item prop="oldPassword" :label="t('shell.pwd.old')">
        <el-input v-model="form.oldPassword" type="password" show-password autocomplete="current-password" data-test="pwd-old" />
      </el-form-item>
      <el-form-item prop="newPassword" :label="t('shell.pwd.new')">
        <el-input v-model="form.newPassword" type="password" show-password autocomplete="new-password" data-test="pwd-new" />
        <div class="ga-form-hint ga-auth__hint">{{ ruleText }}</div>
      </el-form-item>
      <el-form-item prop="confirm" :label="t('shell.pwd.confirm')">
        <el-input v-model="form.confirm" type="password" show-password autocomplete="new-password" data-test="pwd-confirm" />
      </el-form-item>
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon class="ga-auth__error" data-test="pwd-error" />
      <el-button type="primary" native-type="submit" :loading="loading" class="ga-auth__submit" data-test="pwd-submit">
        {{ t('shell.pwd.submit') }}
      </el-button>
      <el-button link class="ga-auth__link" @click="logout">{{ t('shell.layout.logout') }}</el-button>
    </el-form>
  </GaAuthShell>
</template>
