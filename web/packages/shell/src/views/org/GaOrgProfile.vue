<script setup lang="ts">
// 主体端的个人中心（D-067）：只读写本人。能改显示名、邮箱、手机，选内置头像（这一版不能上传），下线本人的其他设备；
// 改密走端自带的改密页（/auth/password）。资料、头像改完重新拉一次 /auth/me，顶栏随之更新。
import { computed, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import GaAvatar from '../../components/GaAvatar.vue'
import { AVATAR_PRESETS, parseAvatar } from '../../avatar'
import { useAuthStore } from '../../auth/store'
import { formatTime } from '../../format'
import { RouteNames } from '../../router'
import { orgApi } from '../../org/api'
import type { OrgProfile } from '../../org/api'

const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()

const me = ref<OrgProfile | null>(null)
const loading = ref(false)
const form = reactive({ displayName: '', email: '', phone: '' })
const formRef = ref<FormInstance>()
const saving = ref(false)
const rules: FormRules = {
  displayName: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  email: [{ type: 'email', message: () => t('err.validation.email'), trigger: 'blur' }],
}

async function load() {
  loading.value = true
  try {
    const v = await orgApi.profile()
    me.value = v
    Object.assign(form, { displayName: v.displayName, email: v.email, phone: v.phone })
  } catch {
    // 已提示
  } finally {
    loading.value = false
  }
}
void load()

/** 资料改完：重新拉 /auth/me（顶栏的显示名、头像），再刷新本页。 */
async function refresh() {
  try {
    await auth.fetchMe()
  } catch {
    // 拉不到就等下次
  }
  await load()
}

async function save() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  saving.value = true
  try {
    await orgApi.updateProfile({ displayName: form.displayName.trim(), email: form.email.trim(), phone: form.phone.trim() })
    ElMessage.success(t('common.success'))
    await refresh()
  } catch {
    // 已提示
  } finally {
    saving.value = false
  }
}

const currentPreset = computed(() => {
  const a = parseAvatar(me.value?.avatar)
  return a.kind === 'preset' ? a.name : ''
})

async function pickAvatar(name: string) {
  try {
    await orgApi.setAvatar(name)
    ElMessage.success(t('common.success'))
    await refresh()
  } catch {
    // 已提示
  }
}

async function revokeOthers() {
  try {
    await ElMessageBox.confirm(t('org.profile.confirmRevokeOthers'), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  try {
    const r = await orgApi.revokeOtherSessions()
    ElMessage.success(t('org.profile.revoked', { n: r.revoked }))
    await load()
  } catch {
    // 已提示
  }
}
</script>

<template>
  <div v-loading="loading" class="ga-page" data-test="org-profile">
    <div v-if="me" class="ga-org-profile">
      <el-card class="ga-org-profile__card">
        <div class="ga-org-profile__who">
          <GaAvatar :size="72" :value="me.avatar" :name="me.username" />
          <div>
            <div class="ga-org-profile__name" data-test="org-profile-name">{{ me.displayName || me.username }}</div>
            <div class="ga-org-profile__sub">{{ me.username }}</div>
          </div>
        </div>
        <el-descriptions :column="1" size="small" border>
          <el-descriptions-item :label="t('org.profile.org')">{{ me.orgName }} <span class="ga-mono">({{ me.orgCode }})</span></el-descriptions-item>
          <el-descriptions-item :label="t('org.account.roles')">
            <el-tag v-if="me.owner" type="warning" size="small" class="ga-tag">{{ t('org.account.owner') }}</el-tag>
            <el-tag v-for="r in me.roles" :key="r.id" size="small" class="ga-tag">{{ r.name }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item :label="t('org.profile.lastLogin')">{{ formatTime(me.lastLoginAt) }} {{ me.lastLoginIp }}</el-descriptions-item>
          <el-descriptions-item :label="t('common.createdAt')">{{ formatTime(me.createdAt) }}</el-descriptions-item>
        </el-descriptions>
      </el-card>

      <div class="ga-org-profile__main">
        <el-card>
          <template #header>{{ t('org.profile.basic') }}</template>
          <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" style="max-width: 480px" @submit.prevent="save">
            <el-form-item prop="displayName" :label="t('common.displayName')">
              <el-input v-model="form.displayName" maxlength="64" data-test="org-profile-display-name" />
            </el-form-item>
            <el-form-item prop="email" :label="t('org.account.email')">
              <el-input v-model="form.email" maxlength="128" />
            </el-form-item>
            <el-form-item prop="phone" :label="t('org.account.phone')">
              <el-input v-model="form.phone" maxlength="32" />
            </el-form-item>
            <el-form-item>
              <el-button type="primary" native-type="submit" :loading="saving" data-test="org-profile-save">{{ t('common.save') }}</el-button>
            </el-form-item>
          </el-form>
        </el-card>

        <el-card>
          <template #header>{{ t('org.profile.avatar') }}</template>
          <p class="ga-form-hint">{{ t('org.profile.avatarHint') }}</p>
          <div class="ga-org-profile__presets">
            <button
              v-for="p in AVATAR_PRESETS"
              :key="p.name"
              type="button"
              class="ga-org-profile__preset"
              :class="{ 'is-active': currentPreset === p.name }"
              :title="p.name"
              :data-test="`org-avatar-${p.name}`"
              @click="pickAvatar(p.name)"
            >
              <GaAvatar :size="44" :value="`preset:${p.name}`" :name="me.username" />
            </button>
          </div>
          <el-button size="small" :disabled="!me.avatar" style="margin-top: 12px" @click="pickAvatar('')">{{ t('org.profile.clearAvatar') }}</el-button>
        </el-card>

        <el-card>
          <template #header>{{ t('org.profile.security') }}</template>
          <div class="ga-org-profile__row">
            <span>{{ t('org.profile.passwordHint') }}</span>
            <el-button @click="router.push({ name: RouteNames.changePassword })">{{ t('org.profile.changePassword') }}</el-button>
          </div>
          <div class="ga-org-profile__row">
            <span data-test="org-profile-sessions">{{ t('org.profile.sessions', { n: me.sessions }) }}</span>
            <el-button type="danger" plain :disabled="me.sessions <= 1" data-test="org-profile-revoke-others" @click="revokeOthers">{{ t('org.profile.revokeOthers') }}</el-button>
          </div>
        </el-card>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ga-org-profile {
  display: grid;
  grid-template-columns: minmax(260px, 340px) 1fr;
  gap: 16px;
  align-items: start;
}

@media (max-width: 900px) {
  .ga-org-profile {
    grid-template-columns: 1fr;
  }
}

.ga-org-profile__main {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.ga-org-profile__who {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
}

.ga-org-profile__name {
  font-size: 18px;
  font-weight: 600;
}

.ga-org-profile__sub {
  color: var(--ga-text-secondary);
}

.ga-org-profile__presets {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.ga-org-profile__preset {
  padding: 2px;
  border: 2px solid transparent;
  border-radius: 50%;
  background: none;
  cursor: pointer;
}

.ga-org-profile__preset.is-active {
  border-color: var(--ga-primary);
}

.ga-org-profile__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 8px 0;
}
</style>
