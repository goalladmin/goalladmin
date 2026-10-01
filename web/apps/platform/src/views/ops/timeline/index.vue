<script setup lang="ts">
// 调查时间线（D-032）：按用户、IP 或会话，把登录、操作、安全事件串成一条线。
// 地址栏带 userId / ip / sessionId 时直接查询；每一条上的 IP、会话、账号都能点，换成按它查。
import { onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { formatTime, useI18n } from '@ga/shell'

import { logApi, userApi } from '../../../api/system'
import type { TimelineItem } from '../../../api/system'

type Subject = 'userId' | 'ip' | 'sessionId'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()

const form = reactive<{ subject: Subject; value: string }>({ subject: 'ip', value: '' })
const users = ref<{ id: number; displayName: string }[]>([])
const items = ref<TimelineItem[]>([])
const more = ref(false)
const next = ref('')
const loading = ref(false)
const searched = ref<{ subject: Subject; value: string } | null>(null)

// 查询序号：先查 A、马上改查 B 时，A 的慢响应（包括"查看更多"）不能盖住或拼进 B 的结果（D-046）。
// 换查询对象时先清空列表和游标，旧对象的"查看更多"也就无从拼接
let seq = 0
async function load(append: boolean) {
  if (!searched.value) return
  const my = ++seq
  const { subject, value } = searched.value
  if (!append) {
    items.value = []
    more.value = false
    next.value = ''
  }
  loading.value = true
  try {
    const cursor = append ? next.value : undefined
    const res = await logApi.timeline({ [subject]: subject === 'userId' ? Number(value) : value, cursor, limit: 100 })
    if (my !== seq) return
    items.value = append ? items.value.concat(res.items) : res.items
    more.value = res.more
    next.value = res.next ?? ''
  } finally {
    if (my === seq) loading.value = false
  }
}

function search() {
  const value = form.value.trim()
  if (!value) return
  // 查询条件写进地址栏：刷新、分享链接都还是这条线
  void router.replace({ path: route.path, query: { [form.subject]: value } })
}

/** 换成按某个 IP、会话、用户查。 */
function pivot(subject: Subject, value: string | number) {
  if (!value) return
  void router.push({ path: route.path, query: { [subject]: String(value) } })
}

function fromRoute() {
  for (const k of ['userId', 'ip', 'sessionId'] as Subject[]) {
    const v = route.query[k]
    if (typeof v === 'string' && v) {
      form.subject = k
      form.value = v
      searched.value = { subject: k, value: v }
      void load(false)
      return
    }
  }
  // 地址栏里没有查询条件（例如从菜单回到这页）：回到未查询的状态，在途的旧查询作废（D-047）
  seq++
  searched.value = null
  form.value = ''
  items.value = []
  more.value = false
  next.value = ''
  loading.value = false
}

// 页面缓存着（KeepAlive）时地址栏会随别的页面变化，只响应本页自己的地址
const myPath = route.path
watch(
  () => route.query,
  () => {
    if (route.path === myPath) fromRoute()
  },
)
onMounted(async () => {
  fromRoute()
  try {
    users.value = await userApi.options()
  } catch {
    // 没有也不影响按 IP、会话查
  }
})

function actionLabel(action = '') {
  const key = `op.${action}`
  return te(key) ? t(key) : action
}
function kindLabel(kind = '') {
  const key = `ops.secevent.kinds.${kind}`
  return te(key) ? t(key) : kind
}
function reasonLabel(reason = '') {
  const key = `loginReason.${reason}`
  return te(key) ? t(key) : reason
}

function color(it: TimelineItem): 'primary' | 'success' | 'warning' | 'danger' | 'info' {
  if (it.type === 'login') return it.success ? 'success' : 'danger'
  if (it.type === 'security') return (it.level ?? 2) >= 3 ? 'danger' : it.level === 2 ? 'warning' : 'info'
  return it.code === 0 ? 'primary' : 'danger'
}
function title(it: TimelineItem): string {
  if (it.type === 'login') return it.success ? t('ops.timeline.loginOk') : t('ops.timeline.loginFailed', { reason: reasonLabel(it.reason) })
  if (it.type === 'security') return kindLabel(it.kind)
  return actionLabel(it.action)
}
const shortSid = (sid: string) => (sid ? sid.slice(0, 8) : '')
</script>

<template>
  <div class="ga-page">
    <el-alert :title="t('ops.timeline.hint')" type="info" :closable="false" show-icon />
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="search">
        <el-form-item>
          <el-radio-group v-model="form.subject" data-test="timeline-subject" @change="form.value = ''">
            <el-radio-button value="ip">IP</el-radio-button>
            <el-radio-button value="userId">{{ t('ops.timeline.user') }}</el-radio-button>
            <el-radio-button value="sessionId">{{ t('ops.timeline.session') }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item>
          <el-select v-if="form.subject === 'userId'" v-model="form.value" filterable :placeholder="t('ops.timeline.pickUser')" style="width: 240px">
            <el-option v-for="u in users" :key="u.id" :value="String(u.id)" :label="`${u.displayName} (#${u.id})`" />
          </el-select>
          <el-input v-else v-model="form.value" clearable :placeholder="form.subject === 'ip' ? '203.0.113.10' : t('ops.timeline.sessionPlaceholder')" style="width: 300px" data-test="timeline-value" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit" data-test="timeline-search">{{ t('common.search') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card v-loading="loading">
      <el-empty v-if="!searched" :description="t('ops.timeline.empty')" />
      <el-empty v-else-if="!items.length && !loading" :description="t('ops.timeline.none')" />
      <el-timeline v-else data-test="timeline-list">
        <el-timeline-item v-for="it in items" :key="`${it.type}-${it.id}`" :timestamp="formatTime(it.at)" :type="color(it)" placement="top">
          <div class="ga-tl__row" :data-type="it.type">
            <el-tag size="small" :type="color(it)" effect="plain">{{ t(`ops.timeline.types.${it.type}`) }}</el-tag>
            <b class="ga-tl__title">{{ title(it) }}</b>
            <span v-if="it.type === 'security' && (it.count ?? 1) > 1" class="ga-tl__count">× {{ it.count }}</span>
            <el-tag v-if="it.type === 'operation' && it.code !== 0" size="small" type="danger">{{ it.code }}</el-tag>
          </div>
          <div v-if="it.path" class="ga-tl__meta ga-mono">{{ it.method }} {{ it.path }}</div>
          <div v-if="it.detail || it.error" class="ga-tl__meta">{{ it.detail || it.error }}</div>
          <div class="ga-tl__chips">
            <el-link v-if="it.username || it.userId" :underline="false" :disabled="!it.userId" @click="pivot('userId', it.userId)">
              {{ it.username || `#${it.userId}` }}
            </el-link>
            <el-link v-if="it.ip" :underline="false" class="ga-mono" @click="pivot('ip', it.ip)">{{ it.ip }}</el-link>
            <el-link v-if="it.sessionId" :underline="false" class="ga-mono" :title="it.sessionId" @click="pivot('sessionId', it.sessionId)">
              {{ t('ops.timeline.session') }} {{ shortSid(it.sessionId) }}
            </el-link>
            <span v-if="it.userAgent" class="ga-tl__ua" :title="it.userAgent">{{ it.userAgent }}</span>
          </div>
        </el-timeline-item>
      </el-timeline>
      <div v-if="more" class="ga-tl__more">
        <el-button :loading="loading" @click="load(true)">{{ t('ops.timeline.older') }}</el-button>
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.ga-tl__row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
.ga-tl__title {
  font-weight: 600;
}
.ga-tl__count {
  color: var(--el-color-danger);
  font-weight: 600;
}
.ga-tl__meta {
  margin-top: 4px;
  color: var(--el-text-color-regular);
  font-size: 13px;
  word-break: break-all;
}
.ga-tl__chips {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  align-items: center;
  margin-top: 6px;
  font-size: 12px;
}
.ga-tl__ua {
  max-width: 360px;
  overflow: hidden;
  color: var(--el-text-color-secondary);
  white-space: nowrap;
  text-overflow: ellipsis;
}
.ga-tl__more {
  display: flex;
  justify-content: center;
  margin-top: 8px;
}
</style>
