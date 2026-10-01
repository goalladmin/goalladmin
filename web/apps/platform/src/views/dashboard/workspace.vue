<script setup lang="ts">
// 工作台（docs/decisions.md D-030）：只显示当前用户自己的数据——问候与上次登录、快捷入口（来自自己能看到的菜单）、
// 最近的操作、登录记录、在线设备和近 30 天的操作分布。
import { computed, onActivated, ref } from 'vue'
import { useRouter } from 'vue-router'
import { GaAvatar, GaCountUp, formatRelative, formatTime, menuTitle, resolveIcon, useAuthStore, useI18n, useLayoutStore } from '@ga/shell'
import type { MenuTree } from '@ga/shell'

import { CircleCheckFilled, CircleCloseFilled, Monitor as MonitorIcon } from '@element-plus/icons-vue'

import EChart from '../../components/EChart.vue'
import { ChartMotion, chartTheme, tooltipStyle } from '../../components/echarts'
import type { EChartsCoreOption } from '../../components/echarts'
import { dashboardApi } from '../../api/system'
import type { WorkspaceData } from '../../api/system'

const { t, te, locale } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const layout = useLayoutStore()
const data = ref<WorkspaceData | null>(null)
const loading = ref(false)
const now = ref(new Date())

// 请求序号：只认最后一次发出的请求；加载失败由请求层提示，这里只收起加载态
let seq = 0
async function load() {
  const my = ++seq
  loading.value = true
  try {
    const d = await dashboardApi.workspace()
    if (my === seq) {
      data.value = d
      now.value = new Date()
    }
  } catch {
    // 已由请求层提示
  } finally {
    if (my === seq) loading.value = false
  }
}
void load()
let first = true
onActivated(() => {
  if (first) first = false
  else void load()
})

const th = computed(() => chartTheme(layout.dark))
const opLabel = (a: string) => (te(`op.${a}`) ? t(`op.${a}`) : a)
const reasonLabel = (r: string) => (te(`loginReason.${r}`) ? t(`loginReason.${r}`) : r)
const rel = (v: string) => formatRelative(v, locale.value, now.value)

// ---- 问候 ----
const name = computed(() => auth.user?.displayName || auth.user?.username || '')
const greeting = computed(() => {
  const h = now.value.getHours()
  const key = h < 5 ? 'night' : h < 11 ? 'morning' : h < 13 ? 'noon' : h < 18 ? 'afternoon' : 'evening'
  return t(`workspace.greeting.${key}`, { name: name.value })
})

// ---- 快捷入口：自己能看到的页面（不含隐藏的详情页和工作台本身） ----
interface Entry {
  name: string
  path: string
  title: string
  trail: string
  icon?: string
}
const entries = computed<Entry[]>(() => {
  const out: Entry[] = []
  const walk = (nodes: MenuTree[], trail: string) => {
    for (const n of [...nodes].sort((a, b) => a.sort - b.sort)) {
      if (n.hidden) continue
      const title = menuTitle(t, locale.value, n.titleKey, n.titles)
      if (n.component && n.name !== 'dashboard-workspace') out.push({ name: n.name, path: n.path, title, trail, icon: n.icon })
      if (n.children?.length) walk(n.children, title)
    }
  }
  walk(auth.menus, '')
  return out.slice(0, 8)
})

// ---- 在线设备：从 User-Agent 粗略认出浏览器和系统 ----
function device(ua: string): string {
  const browser = /Edg\//.test(ua)
    ? 'Edge'
    : /OPR\//.test(ua)
      ? 'Opera'
      : /Firefox\//.test(ua)
        ? 'Firefox'
        : /Chrome\//.test(ua)
          ? 'Chrome'
          : /Safari\//.test(ua)
            ? 'Safari'
            : ''
  const os = /Windows/.test(ua) ? 'Windows' : /iPhone|iPad/.test(ua) ? 'iOS' : /Mac OS X/.test(ua) ? 'macOS' : /Android/.test(ua) ? 'Android' : /Linux/.test(ua) ? 'Linux' : ''
  return [browser, os].filter(Boolean).join(' · ') || t('workspace.unknownDevice')
}

// ---- 操作分布（环图） ----
const actionsOption = computed<EChartsCoreOption>(() => ({
  ...ChartMotion,
  animationType: 'expansion',
  color: th.value.colors,
  tooltip: { ...tooltipStyle(th.value, 'item'), formatter: '{b}: {c} ({d}%)' },
  legend: { bottom: 0, textStyle: { color: th.value.text }, itemWidth: 10, itemHeight: 10, type: 'scroll' },
  series: [
    {
      type: 'pie',
      radius: ['50%', '72%'],
      center: ['50%', '42%'],
      padAngle: 1,
      itemStyle: { borderColor: th.value.surface, borderWidth: 2, borderRadius: 4 },
      label: { show: false },
      emphasis: { scale: true, scaleSize: 6, label: { show: true, color: th.value.text, formatter: '{b}\n{c}', fontWeight: 600 } },
      data: (data.value?.topActions ?? []).map((a) => ({ name: opLabel(a.action), value: a.count })),
    },
  ],
}))
</script>

<template>
  <div v-loading="loading && !data" class="ga-page ga-ws">
    <el-card shadow="never" class="ga-ws__hello">
      <div class="ga-ws__hello-main">
        <div class="ga-ws__avatar">
          <GaAvatar :size="64" :value="auth.user?.avatar" :name="auth.user?.username" />
        </div>
        <div>
          <h2 class="ga-ws__greeting" data-test="ws-greeting">{{ greeting }}</h2>
          <div class="ga-ws__sub">
            <template v-if="data?.lastLogin">{{ t('workspace.lastLogin', { time: rel(data.lastLogin.createdAt), ip: data.lastLogin.ip }) }}</template>
            <template v-else-if="data">{{ t('workspace.firstLogin') }}</template>
          </div>
        </div>
      </div>
      <div class="ga-ws__stats ga-rise">
        <div class="ga-ws__stat">
          <span>{{ t('workspace.roles') }}</span>
          <el-tooltip :content="(data?.roles ?? []).join('、') || '—'" placement="bottom">
            <GaCountUp class="ga-ws__stat-value" :value="data?.roles.length" />
          </el-tooltip>
        </div>
        <div class="ga-ws__stat">
          <span>{{ t('workspace.devices') }}</span>
          <GaCountUp class="ga-ws__stat-value" :value="data?.sessions.length" />
        </div>
        <div class="ga-ws__stat">
          <span>{{ t('workspace.opsToday') }}</span>
          <GaCountUp class="ga-ws__stat-value" :value="data?.opsToday" />
        </div>
        <div class="ga-ws__stat">
          <span>{{ t('workspace.ops30d') }}</span>
          <GaCountUp class="ga-ws__stat-value" :value="data?.ops30d" />
        </div>
      </div>
    </el-card>

    <div class="ga-ws__grid">
      <div class="ga-ws__col">
        <el-card shadow="never">
          <div class="ga-ws__title">{{ t('workspace.shortcuts') }}</div>
          <el-empty v-if="!entries.length" :description="t('workspace.noShortcuts')" :image-size="64" />
          <div v-else class="ga-ws__nav ga-rise" data-test="ws-shortcuts">
            <button v-for="e in entries" :key="e.name" type="button" class="ga-ws__nav-item ga-lift" @click="router.push(e.path)">
              <span class="ga-ws__nav-icon">
                <el-icon v-if="resolveIcon(e.icon)"><component :is="resolveIcon(e.icon)" /></el-icon>
              </span>
              <span class="ga-ws__nav-title">{{ e.title }}</span>
              <span v-if="e.trail" class="ga-ws__nav-trail">{{ e.trail }}</span>
            </button>
          </div>
        </el-card>

        <el-card shadow="never">
          <div class="ga-ws__title">{{ t('workspace.recent') }}</div>
          <el-empty v-if="!data?.recent.length" :description="t('workspace.noRecent')" :image-size="64" />
          <ul v-else class="ga-ws__feed ga-rise" data-test="ws-recent">
            <li v-for="(o, i) in data.recent" :key="i" class="ga-ws__feed-item">
              <span class="ga-ws__dot" :class="{ 'is-bad': o.code !== 0 }" />
              <div class="ga-ws__feed-main">
                <div>
                  <strong>{{ opLabel(o.action) }}</strong>
                  <el-tag v-if="o.code !== 0" type="danger" size="small" effect="plain">{{ t('workspace.failed') }}</el-tag>
                </div>
                <code>{{ o.method }} {{ o.path }}</code>
              </div>
              <span class="ga-ws__time" :title="formatTime(o.createdAt)">{{ rel(o.createdAt) }}</span>
            </li>
          </ul>
        </el-card>
      </div>

      <div class="ga-ws__col">
        <el-card shadow="never">
          <div class="ga-ws__title">{{ t('workspace.actions') }}</div>
          <EChart v-if="data?.topActions.length" :option="actionsOption" height="260px" />
          <el-empty v-else :description="t('workspace.noRecent')" :image-size="64" />
        </el-card>

        <el-card shadow="never">
          <div class="ga-ws__title">{{ t('workspace.devicesTitle') }}</div>
          <ul class="ga-ws__list" data-test="ws-devices">
            <li v-for="(s, i) in data?.sessions ?? []" :key="i" class="ga-ws__list-item">
              <el-icon class="ga-ws__list-icon"><MonitorIcon /></el-icon>
              <div class="ga-ws__list-main">
                <div>
                  {{ device(s.userAgent) }}
                  <el-tag v-if="s.current" type="success" size="small" effect="light">{{ t('workspace.thisDevice') }}</el-tag>
                </div>
                <span class="ga-ws__muted">{{ s.ip }} · {{ t('workspace.active', { time: rel(s.lastSeenAt) }) }}</span>
              </div>
            </li>
          </ul>
        </el-card>

        <el-card shadow="never">
          <div class="ga-ws__title">{{ t('workspace.logins') }}</div>
          <ul class="ga-ws__list" data-test="ws-logins">
            <li v-for="(l, i) in data?.logins ?? []" :key="i" class="ga-ws__list-item">
              <el-icon class="ga-ws__list-icon" :class="l.success ? 'is-ok' : 'is-bad'">
                <CircleCheckFilled v-if="l.success" /><CircleCloseFilled v-else />
              </el-icon>
              <div class="ga-ws__list-main">
                <div>{{ l.success ? t('workspace.loginOk') : reasonLabel(l.reason) }}</div>
                <span class="ga-ws__muted">{{ l.ip }}</span>
              </div>
              <span class="ga-ws__time" :title="formatTime(l.createdAt)">{{ rel(l.createdAt) }}</span>
            </li>
          </ul>
        </el-card>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ga-ws__hello :deep(.el-card__body) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  flex-wrap: wrap;
  padding: 22px 26px;
}
.ga-ws__hello-main {
  display: flex;
  align-items: center;
  gap: 18px;
  min-width: 0;
}
.ga-ws__avatar {
  flex: none;
  display: flex;
  border-radius: 50%;
  animation: ga-ws-pop 0.6s cubic-bezier(0.34, 1.56, 0.64, 1) both;
}
@keyframes ga-ws-pop {
  from {
    transform: scale(0.6);
    opacity: 0;
  }
}
.ga-ws__greeting {
  margin: 0 0 6px;
  font-size: 20px;
  font-weight: 600;
}
.ga-ws__sub,
.ga-ws__muted,
.ga-ws__time {
  font-size: 13px;
  color: var(--ga-text-secondary);
}
.ga-ws__stats {
  display: flex;
  gap: 34px;
  flex-wrap: wrap;
}
.ga-ws__stat {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 4px;
  font-size: 13px;
  color: var(--ga-text-secondary);
}
.ga-ws__stat-value {
  font-size: 26px;
  font-weight: 600;
  color: var(--ga-text);
}
.ga-ws__grid {
  display: grid;
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
  gap: var(--ga-gap);
  align-items: start;
}
@media (max-width: 1000px) {
  .ga-ws__grid {
    grid-template-columns: 1fr;
  }
}
.ga-ws__col {
  display: flex;
  flex-direction: column;
  gap: var(--ga-gap);
  min-width: 0;
}
.ga-ws__title {
  margin-bottom: 12px;
  font-size: 15px;
  font-weight: 600;
}
.ga-ws__nav {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
}
@media (max-width: 700px) {
  .ga-ws__nav {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
.ga-ws__nav-item {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 18px 10px;
  border: 1px solid var(--ga-border);
  border-radius: 12px;
  background: var(--ga-surface);
  color: var(--ga-text);
  font: inherit;
  cursor: pointer;
}
.ga-ws__nav-item:hover {
  border-color: var(--el-color-primary-light-5);
}
.ga-ws__nav-item:active {
  transform: scale(0.97);
}
.ga-ws__nav-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 42px;
  height: 42px;
  border-radius: 12px;
  font-size: 20px;
  color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  transition: transform 0.3s cubic-bezier(0.34, 1.56, 0.64, 1);
}
.ga-ws__nav-item:hover .ga-ws__nav-icon {
  transform: translateY(-2px) rotate(-6deg) scale(1.08);
}
.ga-ws__nav-title {
  font-size: 14px;
  font-weight: 500;
  text-align: center;
}
.ga-ws__nav-trail {
  font-size: 12px;
  color: var(--ga-text-secondary);
}
.ga-ws__feed,
.ga-ws__list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.ga-ws__feed-item {
  position: relative;
  display: grid;
  grid-template-columns: 14px minmax(0, 1fr) auto;
  gap: 12px;
  padding: 10px 6px;
  border-radius: 8px;
  transition: background-color 0.2s ease;
}
.ga-ws__feed-item:hover,
.ga-ws__list-item:hover {
  background: var(--el-fill-color-light);
}
.ga-ws__feed-item::before {
  content: '';
  position: absolute;
  left: 12px;
  top: 26px;
  bottom: -10px;
  width: 2px;
  background: var(--ga-border);
}
.ga-ws__feed-item:last-child::before {
  display: none;
}
.ga-ws__dot {
  position: relative;
  z-index: 1;
  width: 10px;
  height: 10px;
  margin: 5px 0 0 2px;
  border-radius: 50%;
  background: var(--el-color-primary);
  box-shadow: 0 0 0 3px var(--el-color-primary-light-8);
}
.ga-ws__dot.is-bad {
  background: var(--el-color-danger);
  box-shadow: 0 0 0 3px var(--el-color-danger-light-8);
}
.ga-ws__feed-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}
.ga-ws__feed-main > div {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ga-ws__feed-main code {
  font-size: 12px;
  color: var(--ga-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ga-ws__list-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 9px 6px;
  border-radius: 8px;
  transition: background-color 0.2s ease;
}
.ga-ws__list-icon {
  flex: none;
  font-size: 18px;
  color: var(--ga-text-secondary);
}
.ga-ws__list-icon.is-ok {
  color: var(--el-color-success);
}
.ga-ws__list-icon.is-bad {
  color: var(--el-color-danger);
}
.ga-ws__list-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 14px;
}
.ga-ws__list-main > div {
  display: flex;
  align-items: center;
  gap: 8px;
}
@media (prefers-reduced-motion: reduce) {
  .ga-ws__avatar {
    animation: none;
  }
}
</style>
