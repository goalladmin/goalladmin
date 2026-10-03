<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import GaAuthShell from '../components/GaAuthShell.vue'
import { onboardingApi } from '../onboarding/api'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const ready = ref(false)
const enabled = ref(false)
const failed = ref(false)
const busy = ref(false)
const reference = ref('')
const image = ref('')
const invitationToken = new URLSearchParams(route.hash.slice(1)).get('invite') ?? ''
const invalidInvite = route.hash !== '' && !/^[a-f0-9]{64}$/.test(invitationToken)
const form = reactive({ name: '', contactName: '', contactPhone: '', ownerUsername: '', invitationToken, captchaId: '', captchaCode: '' })
// 凭证只留在本页内存，去掉地址栏片段；不写本地存储。
if (route.hash) void router.replace({ path: route.path, hash: '' })
// 同页打开另一条邀请链接时按新页面初始化，不沿用旧申请状态。
watch(() => route.hash, hash => {
  if (hash) router.go(0)
})
async function captcha() {
  const c = await onboardingApi.captcha()
  form.captchaId = c.captchaId
  form.captchaCode = ''
  image.value = c.image
}
onMounted(async () => {
  try {
    enabled.value = (await onboardingApi.config()).enabled
    if (enabled.value && !invalidInvite) await captcha()
  } catch { failed.value = true } finally { ready.value = true }
})
async function refreshCaptcha() {
  try { await captcha() } catch { image.value = ''; form.captchaId = '' }
}
async function submit() {
  if (busy.value || !enabled.value || invalidInvite || !form.captchaId) return
  busy.value = true
  try { reference.value = (await onboardingApi.apply({ ...form })).reference }
  catch { await refreshCaptcha() }
  finally { busy.value = false }
}
</script>

<template>
  <GaAuthShell>
    <h1 class="ga-auth__title">{{ t('onboarding.title') }}</h1>
    <p>{{ t('onboarding.explain') }}</p>
    <el-alert v-if="failed" :title="t('shell.error.network')" type="error" :closable="false" />
    <el-alert v-else-if="ready && !enabled" :title="t('onboarding.closed')" type="info" :closable="false" data-test="register-closed" />
    <el-alert v-else-if="invalidInvite" :title="t('onboarding.invalidInvite')" type="error" :closable="false" />
    <template v-else-if="reference">
      <el-alert :title="t('onboarding.submitted')" type="success" :closable="false" />
      <p data-test="register-reference">{{ t('onboarding.reference') }}: {{ reference }}</p>
      <p>{{ t('onboarding.delivery') }}</p>
    </template>
    <el-form v-else-if="enabled && ready" label-position="top" @submit.prevent="submit">
      <el-alert v-if="invitationToken" :title="t('onboarding.invited')" type="info" :closable="false" />
      <el-form-item :label="t('onboarding.name')"><el-input v-model="form.name" required maxlength="64" data-test="register-name" /></el-form-item>
      <el-form-item :label="t('onboarding.contactName')"><el-input v-model="form.contactName" required maxlength="64" data-test="register-contact" /></el-form-item>
      <el-form-item :label="t('onboarding.contactPhone')"><el-input v-model="form.contactPhone" required maxlength="32" data-test="register-phone" /></el-form-item>
      <el-form-item :label="t('onboarding.username')"><el-input v-model="form.ownerUsername" required maxlength="64" data-test="register-username" /></el-form-item>
      <el-form-item :label="t('shell.login.captcha')">
        <div class="ga-register__captcha">
          <el-input v-model="form.captchaCode" required maxlength="5" data-test="register-captcha" />
          <el-button class="ga-register__captcha-refresh" text :title="t('shell.login.refreshCaptcha')" @click="refreshCaptcha"><img v-if="image" :src="image" :alt="t('shell.login.refreshCaptcha')" /></el-button>
        </div>
      </el-form-item>
      <el-button native-type="submit" type="primary" :loading="busy" :disabled="!form.captchaId" data-test="register-submit">{{ t('onboarding.submit') }}</el-button>
    </el-form>
    <el-button text @click="router.push('/login')">{{ t('onboarding.back') }}</el-button>
  </GaAuthShell>
</template>

<style scoped>
.ga-register__captcha {
  display: flex;
  gap: 12px;
  width: 100%;
}

.ga-register__captcha .el-input {
  flex: 1;
  min-width: 0;
}

.ga-register__captcha-refresh {
  flex: 0 0 140px;
  height: 48px;
  padding: 0;
  overflow: hidden;
  border-radius: 8px;
}

.ga-register__captcha-refresh img {
  display: block;
  width: 140px;
  height: 48px;
}
</style>
