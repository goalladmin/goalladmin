<script setup lang="ts">
// 个人中心（docs/decisions.md D-038）：左侧是头像和四个面板的入口，右侧按面板显示——
// 基本设置（本人改显示名、邮箱、手机、简介）、安全设置（只显示真实状态，没有开关）、修改密码、新消息提醒（本地占位）。
// 接口只读写调用者本人（GET/PUT /system/profile），能改别人的仍然只有用户管理。
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { Bell, Key, Lock, Setting } from '@element-plus/icons-vue'
import { Codes, GaAvatar, describeApiError, formatTime, isApiError, useAuthStore, useI18n } from '@ga/shell'

import AvatarPicker from '../../components/AvatarPicker.vue'
import { profileApi } from '../../api/system'
import type { ProfileView } from '../../api/system'

type Panel = 'basic' | 'security' | 'password' | 'notify'

const { t, te } = useI18n()
const auth = useAuthStore()

const panels: { key: Panel; icon: typeof Setting }[] = [
  { key: 'basic', icon: Setting },
  { key: 'security', icon: Lock },
  { key: 'password', icon: Key },
  { key: 'notify', icon: Bell },
]
const active = ref<Panel>('basic')

const profile = ref<ProfileView | null>(null)
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    profile.value = await profileApi.get()
    Object.assign(form, { displayName: profile.value.displayName, email: profile.value.email, phone: profile.value.phone, bio: profile.value.bio })
  } finally {
    loading.value = false
  }
}
onMounted(load)

const name = computed(() => profile.value?.displayName || auth.user?.displayName || auth.user?.username || '')

// ---- 头像（D-040）：上传、选内置、恢复默认都在对话框里；换完同步到顶栏 ----
const avatarDialog = ref(false)
const avatar = computed(() => profile.value?.avatar ?? auth.user?.avatar ?? '')
function onAvatarChanged(v: string) {
  if (profile.value) profile.value.avatar = v
  if (auth.user) auth.user.avatar = v
}

// 把接口的错误说明翻译成一行给用户看（字段错误按键翻译，D-026）
function explain(e: unknown): string {
  if (isApiError(e)) return describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
  return t('shell.error.network')
}

// ---- 基本设置 ----
const form = reactive({ displayName: '', email: '', phone: '', bio: '' })
const formRef = ref<FormInstance>()
const saving = ref(false)
const rules: FormRules = {
  displayName: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  email: [{ type: 'email', message: () => t('err.validation.email'), trigger: 'blur' }],
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
    profile.value = await profileApi.update({ displayName: form.displayName.trim(), email: form.email.trim(), phone: form.phone.trim(), bio: form.bio })
    Object.assign(form, { displayName: profile.value.displayName, email: profile.value.email, phone: profile.value.phone, bio: profile.value.bio })
    // 顶栏和锁屏用的是 /auth/me 里的显示名，后端已让缓存失效，这里重新拉一次
    await auth.fetchMe()
    ElMessage.success(t('profile.saved'))
  } catch (e) {
    ElMessage.error(explain(e))
  } finally {
    saving.value = false
  }
}

// ---- 安全设置：只显示真实状态 ----
function maskEmail(v: string): string {
  const at = v.indexOf('@')
  if (at <= 0) return v
  return `${v[0]}***${v.slice(at)}`
}
function maskPhone(v: string): string {
  const digits = v.replace(/\s+/g, '')
  if (digits.length < 7) return `${digits.slice(0, 2)}****`
  return `${digits.slice(0, 3)}****${digits.slice(-4)}`
}
const policy = computed(() => auth.pwdPolicy ?? { minLength: 10, requireUpper: false, requireLower: false, requireSymbol: false, maxAgeDays: 0 })
const ruleText = computed(() => {
  const p = policy.value
  const extra = [p.requireUpper && t('shell.pwd.upper'), p.requireLower && t('shell.pwd.lower'), p.requireSymbol && t('shell.pwd.symbol')].filter(Boolean)
  return t('shell.pwd.ruleBase', { n: p.minLength }) + (extra.length ? t('shell.pwd.ruleExtra', { items: extra.join(t('shell.pwd.sep')) }) : '')
})
const revoking = ref(false)
async function revokeOthers() {
  try {
    await ElMessageBox.confirm(t('profile.security.revokeConfirm'), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  revoking.value = true
  try {
    const { revoked } = await profileApi.revokeOthers()
    ElMessage.success(t('profile.security.revoked', { n: revoked }))
    await load()
  } finally {
    revoking.value = false
  }
}

// ---- 修改密码：规则跟着 /auth/me 下发的策略走，强度条只是提示，安全以后端校验为准 ----
const pwd = reactive({ oldPassword: '', newPassword: '', confirm: '' })
const pwdRef = ref<FormInstance>()
const changing = ref(false)

function checkPolicy(v: string): boolean {
  const p = policy.value
  if ([...v].length < p.minLength || !/\p{L}/u.test(v) || !/\p{N}/u.test(v)) return false
  if (p.requireUpper && !/\p{Lu}/u.test(v)) return false
  if (p.requireLower && !/\p{Ll}/u.test(v)) return false
  if (p.requireSymbol && !/[^\p{L}\p{N}\s]/u.test(v)) return false
  return true
}
/** 强度 0–5：长度够、更长、大小写都有、有数字、有符号各计一分。 */
function strength(v: string): number {
  if (!v) return 0
  const n = [...v].length
  let s = 0
  if (n >= policy.value.minLength) s++
  if (n >= 14) s++
  if (/\p{Lu}/u.test(v) && /\p{Ll}/u.test(v)) s++
  if (/\p{N}/u.test(v)) s++
  if (/[^\p{L}\p{N}\s]/u.test(v)) s++
  return s
}
const newStrength = computed(() => strength(pwd.newPassword))
const confirmStrength = computed(() => (pwd.confirm && pwd.confirm === pwd.newPassword ? 5 : 0))
const strengthLevel = (s: number) => (s >= 4 ? 'strong' : s >= 3 ? 'medium' : 'weak')

const pwdRules: FormRules = {
  oldPassword: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  newPassword: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { validator: (_r, v, cb) => cb(checkPolicy(String(v ?? '')) ? undefined : new Error(ruleText.value)), trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { validator: (_r, v, cb) => cb(v !== pwd.newPassword ? new Error(t('shell.pwd.mismatch')) : undefined), trigger: 'blur' },
  ],
}

async function changePassword() {
  if (!pwdRef.value) return
  try {
    await pwdRef.value.validate()
  } catch {
    return
  }
  changing.value = true
  try {
    await auth.changePassword(pwd.oldPassword, pwd.newPassword)
    ElMessage.success(t('shell.pwd.done'))
    Object.assign(pwd, { oldPassword: '', newPassword: '', confirm: '' })
    pwdRef.value.clearValidate()
    await load()
  } catch (e) {
    // 改密在途时登录身份变了：结果作废，不提示（D-053）
    if (isApiError(e) && e.code === Codes.Superseded) return
    ElMessage.error(explain(e))
  } finally {
    changing.value = false
  }
}

// ---- 新消息提醒：系统还没有消息功能，开关只存在本浏览器里，不影响任何东西（D-038 第 5 条） ----
const notifyKey = 'ga.profile.notify'
type NotifyKind = 'message' | 'system' | 'todo'
const notifyKinds: NotifyKind[] = ['message', 'system', 'todo']
const notify = reactive<Record<NotifyKind, boolean>>({ message: true, system: true, todo: true })
try {
  const raw = localStorage.getItem(notifyKey)
  const saved = raw ? (JSON.parse(raw) as unknown) : null
  if (saved && typeof saved === 'object') {
    for (const k of notifyKinds) {
      const v = (saved as Record<string, unknown>)[k]
      if (typeof v === 'boolean') notify[k] = v
    }
  }
} catch {
  // 本地存储不可用或内容损坏：用默认值
}
watch(notify, (v) => {
  try {
    localStorage.setItem(notifyKey, JSON.stringify(v))
  } catch {
    // 忽略
  }
})
</script>

<template>
  <div v-loading="loading && !profile" class="ga-page ga-profile">
    <el-card shadow="never" class="ga-profile__side">
      <div class="ga-profile__me">
        <button type="button" class="ga-profile__avatar-btn" :title="t('profile.avatar.change')" data-test="profile-avatar-change" @click="avatarDialog = true">
          <GaAvatar :size="104" :value="avatar" :name="profile?.username ?? auth.user?.username" data-test="profile-avatar" />
          <span class="ga-profile__avatar-edit">{{ t('profile.avatar.change') }}</span>
        </button>
        <div class="ga-profile__name" data-test="profile-name">{{ name }}</div>
        <div class="ga-profile__username">{{ profile?.username ?? auth.user?.username }}</div>
        <div v-if="profile?.roles.length" class="ga-profile__roles">
          <el-tag v-for="r in profile.roles" :key="r.id" size="small" :type="r.isSuper ? 'danger' : 'info'" effect="light" round>{{ r.name }}</el-tag>
        </div>
      </div>
      <nav class="ga-profile__nav">
        <button
          v-for="p in panels"
          :key="p.key"
          type="button"
          class="ga-profile__nav-item"
          :class="{ 'is-active': active === p.key }"
          :data-test="`profile-nav-${p.key}`"
          @click="active = p.key"
        >
          <el-icon><component :is="p.icon" /></el-icon>
          <span>{{ t(`profile.nav.${p.key}`) }}</span>
        </button>
      </nav>
    </el-card>

    <el-card shadow="never" class="ga-profile__main">
      <!-- 基本设置 -->
      <div v-if="active === 'basic'" key="basic" class="ga-profile__panel ga-rise">
        <h3 class="ga-profile__title">{{ t('profile.nav.basic') }}</h3>
        <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" class="ga-profile__form" @submit.prevent="save">
          <el-form-item prop="displayName" :label="t('profile.basic.displayName')">
            <el-input v-model="form.displayName" maxlength="64" data-test="profile-display-name" />
          </el-form-item>
          <el-form-item :label="t('profile.basic.username')">
            <el-input :model-value="profile?.username ?? ''" disabled />
          </el-form-item>
          <el-form-item :label="t('profile.basic.roles')">
            <div class="ga-profile__tags">
              <el-tag v-for="r in profile?.roles ?? []" :key="r.id" :type="r.isSuper ? 'danger' : undefined" effect="plain">{{ r.name }}</el-tag>
              <span v-if="!profile?.roles.length" class="ga-profile__muted">{{ t('profile.basic.noRoles') }}</span>
            </div>
          </el-form-item>
          <el-form-item v-if="profile?.deptName || profile?.posts.length" :label="t('profile.basic.org')">
            <div class="ga-profile__tags">
              <el-tag v-if="profile?.deptName" effect="plain" type="info">{{ profile.deptName }}</el-tag>
              <el-tag v-for="p in profile?.posts ?? []" :key="p.id" effect="plain" type="info">{{ p.name }}</el-tag>
            </div>
          </el-form-item>
          <el-form-item prop="email" :label="t('profile.basic.email')">
            <el-input v-model="form.email" maxlength="128" type="email" autocomplete="email" data-test="profile-email" />
          </el-form-item>
          <el-form-item prop="phone" :label="t('profile.basic.phone')">
            <el-input v-model="form.phone" maxlength="32" autocomplete="tel" data-test="profile-phone" />
          </el-form-item>
          <el-form-item prop="bio" :label="t('profile.basic.bio')">
            <el-input v-model="form.bio" type="textarea" :rows="3" maxlength="255" show-word-limit data-test="profile-bio" />
          </el-form-item>
          <el-form-item>
            <el-button type="primary" native-type="submit" :loading="saving" data-test="profile-save">{{ t('profile.basic.save') }}</el-button>
          </el-form-item>
        </el-form>
      </div>

      <!-- 安全设置：只显示真实状态，没有的防护不假装有 -->
      <div v-else-if="active === 'security'" key="security" class="ga-profile__panel">
        <h3 class="ga-profile__title">{{ t('profile.nav.security') }}</h3>
        <div class="ga-profile__list ga-rise">
          <div class="ga-profile__row">
            <div class="ga-profile__row-main">
              <div class="ga-profile__row-title">{{ t('profile.security.password') }}</div>
              <div class="ga-profile__row-desc">
                {{ profile?.pwdChangedAt ? t('profile.security.pwdChangedAt', { time: formatTime(profile.pwdChangedAt) }) : t('profile.security.pwdNever') }}
                · {{ policy.maxAgeDays > 0 ? t('profile.security.pwdExpiry', { days: policy.maxAgeDays }) : t('profile.security.pwdNoExpiry') }}
                <br />
                {{ ruleText }}
              </div>
            </div>
            <el-button link type="primary" data-test="profile-go-password" @click="active = 'password'">{{ t('profile.security.change') }}</el-button>
          </div>
          <div class="ga-profile__row">
            <div class="ga-profile__row-main">
              <div class="ga-profile__row-title">{{ t('profile.security.email') }}</div>
              <div class="ga-profile__row-desc">{{ profile?.email ? t('profile.security.emailBound', { email: maskEmail(profile.email) }) : t('profile.security.emailNone') }}</div>
            </div>
            <el-button link type="primary" @click="active = 'basic'">{{ t('profile.security.edit') }}</el-button>
          </div>
          <div class="ga-profile__row">
            <div class="ga-profile__row-main">
              <div class="ga-profile__row-title">{{ t('profile.security.phone') }}</div>
              <div class="ga-profile__row-desc">{{ profile?.phone ? t('profile.security.phoneBound', { phone: maskPhone(profile.phone) }) : t('profile.security.phoneNone') }}</div>
            </div>
            <el-button link type="primary" @click="active = 'basic'">{{ t('profile.security.edit') }}</el-button>
          </div>
          <div class="ga-profile__row">
            <div class="ga-profile__row-main">
              <div class="ga-profile__row-title">{{ t('profile.security.sessions') }}</div>
              <div class="ga-profile__row-desc" data-test="profile-sessions">
                {{ t('profile.security.sessionsCount', { n: profile?.sessions ?? 1 }) }}
                <template v-if="profile?.lastLoginAt"> · {{ t('profile.security.lastLogin', { time: formatTime(profile.lastLoginAt), ip: profile.lastLoginIp }) }}</template>
              </div>
            </div>
            <el-button type="danger" plain size="small" :disabled="(profile?.sessions ?? 1) <= 1" :loading="revoking" data-test="profile-revoke-others" @click="revokeOthers">
              {{ t('profile.security.revokeOthers') }}
            </el-button>
          </div>
          <div class="ga-profile__row">
            <div class="ga-profile__row-main">
              <div class="ga-profile__row-title">{{ t('profile.security.mfa') }}</div>
              <div class="ga-profile__row-desc">{{ t('profile.security.mfaPlanned') }}</div>
            </div>
            <el-tag type="info" effect="plain">{{ t('profile.security.notEnabled') }}</el-tag>
          </div>
        </div>
      </div>

      <!-- 修改密码 -->
      <div v-else-if="active === 'password'" key="password" class="ga-profile__panel ga-rise">
        <h3 class="ga-profile__title">{{ t('profile.nav.password') }}</h3>
        <el-form ref="pwdRef" :model="pwd" :rules="pwdRules" label-width="auto" class="ga-profile__form ga-profile__form--narrow" @submit.prevent="changePassword">
          <el-form-item prop="oldPassword" :label="t('shell.pwd.old')">
            <el-input v-model="pwd.oldPassword" type="password" show-password autocomplete="current-password" data-test="profile-pwd-old" />
          </el-form-item>
          <el-form-item prop="newPassword" :label="t('shell.pwd.new')">
            <el-input v-model="pwd.newPassword" type="password" show-password autocomplete="new-password" data-test="profile-pwd-new" />
            <div class="ga-profile__strength" :class="`is-${strengthLevel(newStrength)}`" :aria-label="t('profile.password.strength')">
              <i v-for="i in 5" :key="i" :class="{ 'is-on': i <= newStrength }" />
            </div>
            <div class="ga-form-hint">{{ ruleText }}</div>
          </el-form-item>
          <el-form-item prop="confirm" :label="t('shell.pwd.confirm')">
            <el-input v-model="pwd.confirm" type="password" show-password autocomplete="new-password" data-test="profile-pwd-confirm" />
            <div class="ga-profile__strength is-strong">
              <i v-for="i in 5" :key="i" :class="{ 'is-on': i <= confirmStrength }" />
            </div>
          </el-form-item>
          <el-form-item>
            <el-button type="primary" native-type="submit" :loading="changing" data-test="profile-pwd-submit">{{ t('profile.password.submit') }}</el-button>
          </el-form-item>
        </el-form>
      </div>

      <!-- 新消息提醒：本地占位 -->
      <div v-else key="notify" class="ga-profile__panel">
        <h3 class="ga-profile__title">{{ t('profile.nav.notify') }}</h3>
        <el-alert type="info" :closable="false" show-icon :title="t('profile.notify.placeholder')" class="ga-profile__alert" />
        <div class="ga-profile__list ga-rise">
          <div v-for="k in notifyKinds" :key="k" class="ga-profile__row">
            <div class="ga-profile__row-main">
              <div class="ga-profile__row-title">{{ t(`profile.notify.${k}`) }}</div>
              <div class="ga-profile__row-desc">{{ t(`profile.notify.${k}Desc`) }}</div>
            </div>
            <el-switch v-model="notify[k]" :data-test="`profile-notify-${k}`" />
          </div>
        </div>
      </div>
    </el-card>
    <AvatarPicker v-model="avatarDialog" :current="avatar" :username="profile?.username ?? auth.user?.username ?? ''" @changed="onAvatarChanged" />
  </div>
</template>

<style scoped>
.ga-profile {
  flex-direction: row;
  align-items: flex-start;
}

.ga-profile__side {
  flex: 0 0 272px;
  width: 272px;
}

.ga-profile__main {
  flex: 1 1 0;
  min-width: 0;
}

.ga-profile__me {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 16px 0 20px;
  border-bottom: 1px solid var(--ga-border);
}

.ga-profile__avatar-btn {
  position: relative;
  padding: 0;
  border: 0;
  border-radius: 50%;
  background: none;
  cursor: pointer;
}

.ga-profile__avatar-edit {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  background: rgb(0 0 0 / 45%);
  color: #fff;
  font-size: 13px;
  opacity: 0;
  transition: opacity 0.15s;
}

.ga-profile__avatar-btn:hover .ga-profile__avatar-edit,
.ga-profile__avatar-btn:focus-visible .ga-profile__avatar-edit {
  opacity: 1;
}

.ga-profile__name {
  margin-top: 8px;
  font-size: 20px;
  font-weight: 700;
  color: var(--ga-text);
}

.ga-profile__username {
  color: var(--ga-text-secondary);
}

.ga-profile__roles {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 6px;
  margin-top: 4px;
}

.ga-profile__nav {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding-top: 16px;
}

.ga-profile__nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 0 14px;
  height: 44px;
  border: 0;
  border-radius: var(--ga-radius);
  background: transparent;
  color: var(--ga-text);
  font: inherit;
  font-size: 15px;
  text-align: left;
  cursor: pointer;
  transition:
    background 0.18s,
    color 0.18s,
    transform 0.18s;
}

.ga-profile__nav-item:hover {
  background: var(--ga-hover);
  transform: translateX(2px);
}

.ga-profile__nav-item.is-active,
.ga-profile__nav-item.is-active:hover {
  background: var(--el-color-primary);
  color: #fff;
  transform: none;
}

.ga-profile__title {
  margin: 0 0 20px;
  font-size: 17px;
  font-weight: 600;
}

.ga-profile__form {
  max-width: 720px;
}

.ga-profile__form--narrow {
  max-width: 440px;
}

.ga-profile__tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  min-height: 32px;
  align-items: center;
}

.ga-profile__muted {
  color: var(--ga-text-secondary);
}

.ga-profile__list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.ga-profile__row {
  display: flex;
  align-items: center;
  gap: 16px;
  padding: 16px 20px;
  border: 1px solid var(--ga-border);
  border-radius: var(--ga-radius);
  background: var(--ga-surface-2);
  transition:
    border-color 0.18s,
    box-shadow 0.18s;
}

.ga-profile__row:hover {
  border-color: var(--el-color-primary-light-5);
  box-shadow: var(--ga-shadow);
}

.ga-profile__row-main {
  flex: 1 1 auto;
  min-width: 0;
}

.ga-profile__row-title {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 4px;
}

.ga-profile__row-desc {
  color: var(--ga-text-secondary);
  line-height: 1.6;
  overflow-wrap: anywhere;
}

.ga-profile__alert {
  margin-bottom: 16px;
}

/* 密码强度条：5 段，随分数点亮，颜色按等级 */
.ga-profile__strength {
  display: flex;
  gap: 5px;
  width: 100%;
  margin-top: 8px;
}

.ga-profile__strength i {
  flex: 1 1 0;
  height: 5px;
  border-radius: 3px;
  background: var(--ga-border);
  transition: background 0.2s;
}

.ga-profile__strength.is-weak i.is-on {
  background: var(--el-color-danger);
}

.ga-profile__strength.is-medium i.is-on {
  background: var(--el-color-warning);
}

.ga-profile__strength.is-strong i.is-on {
  background: var(--el-color-success);
}

@media (max-width: 860px) {
  .ga-profile {
    flex-direction: column;
  }

  .ga-profile__side,
  .ga-profile__main {
    /* el-card 有 overflow: hidden，竖排时 flex-basis 为 0 会把卡片压成一条线，改回按内容算高度 */
    flex: 0 0 auto;
    width: 100%;
  }

  .ga-profile__nav {
    flex-direction: row;
    flex-wrap: wrap;
  }

  .ga-profile__nav-item {
    width: auto;
    flex: 1 1 40%;
  }
}
</style>
