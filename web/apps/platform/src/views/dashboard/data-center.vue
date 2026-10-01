<script setup lang="ts">
// 数据中心（docs/decisions.md D-027、D-030）：只显示系统里的真实数据。
// 第一行四张数字卡片（数字跳动、迷你趋势图）；中间是登录/操作趋势和活跃用户排名；
// 下面是常用操作、登录失败原因和按钟点的活跃分布。
import { computed, onActivated, ref } from 'vue'
import { GaCountUp, GaSparkline, useI18n, useLayoutStore } from '@ga/shell'

import { InfoFilled } from '@element-plus/icons-vue'

import EChart from '../../components/EChart.vue'
import { ChartMotion, axisStyle, chartTheme, tooltipStyle } from '../../components/echarts'
import type { EChartsCoreOption } from '../../components/echarts'
import { dashboardApi } from '../../api/system'
import type { DashboardData } from '../../api/system'

const { t, te, locale } = useI18n()
const layout = useLayoutStore()
const range = ref<7 | 30 | 90>(30)
const tab = ref<'logins' | 'ops'>('logins')
const data = ref<DashboardData | null>(null)
/** 当前显示的数据是哪个区间的：文字里的"近 N 天"跟着数据走，不跟着刚点的按钮走（D-045）。 */
const shown = ref<7 | 30 | 90>(30)
const loading = ref(false)

// 请求序号：快速切换区间时，先发出的请求可能后返回，只认最后一次发出的那次。
// 加载中整页显示加载态（换区间时旧图表不会被当成新区间）；失败由请求层提示，区间按钮退回到正在显示的那个
let seq = 0
async function load() {
  const my = ++seq
  const want = range.value
  loading.value = true
  try {
    const d = await dashboardApi.get(want)
    if (my === seq) {
      data.value = d
      shown.value = want
    }
  } catch {
    if (my === seq) range.value = shown.value
  } finally {
    if (my === seq) loading.value = false
  }
}
void load()
// 从别的页面切回来时刷新一次（页面被缓存，不会重新创建）
let first = true
onActivated(() => {
  if (first) first = false
  else void load()
})

const th = computed(() => chartTheme(layout.dark))
// 同一个实体在整页用同一种颜色：成功登录、失败登录、操作、用户
const C = computed(() => ({ success: th.value.colors[0]!, failed: th.value.colors[1]!, ops: th.value.colors[2]!, users: th.value.colors[3]! }))

const nf = computed(() => new Intl.NumberFormat(locale.value))
const fmt = (n: number | undefined) => nf.value.format(n ?? 0)
// 百分数按界面语言写小数点（德语、法语等用逗号）
const pf = computed(() => new Intl.NumberFormat(locale.value, { minimumFractionDigits: 1, maximumFractionDigits: 1 }))
const pct = (n: number) => pf.value.format(n)
const last = (a: number[] | undefined) => (a && a.length ? a[a.length - 1]! : 0)
const sum = (a: number[] | undefined) => (a ?? []).reduce((x, y) => x + y, 0)
const rate = (ok: number, bad: number) => (ok + bad ? (ok / (ok + bad)) * 100 : 0)

const dayLabels = computed(() => (data.value?.days ?? []).map((d) => d.slice(5)))
const loginsToday = computed(() => last(data.value?.logins.success))
const failedToday = computed(() => last(data.value?.logins.failed))
const periodRate = computed(() => rate(sum(data.value?.logins.success), sum(data.value?.logins.failed)))
const reasonLabel = (r: string) => (te(`loginReason.${r}`) ? t(`loginReason.${r}`) : r)
const opLabel = (a: string) => (te(`op.${a}`) ? t(`op.${a}`) : a)

// ---- 趋势（柱状图） ----
const trendOption = computed<EChartsCoreOption>(() => {
  const d = data.value
  const base = axisStyle(th.value)
  const common = {
    ...ChartMotion,
    grid: { left: 8, right: 12, top: 36, bottom: 8, containLabel: true },
    tooltip: tooltipStyle(th.value, 'axis'),
    xAxis: { type: 'category', data: dayLabels.value, ...base, splitLine: { show: false } },
    yAxis: { type: 'value', minInterval: 1, ...base, axisLine: { show: false } },
  }
  if (tab.value === 'ops') {
    return {
      ...common,
      color: [C.value.ops],
      series: [{ name: t('dashboard.operations'), type: 'bar', data: d?.operations ?? [], barMaxWidth: 22, itemStyle: { borderRadius: [4, 4, 0, 0] } }],
    }
  }
  return {
    ...common,
    color: [C.value.success, C.value.failed],
    legend: { top: 0, right: 0, textStyle: { color: th.value.text }, itemWidth: 12, itemHeight: 8 },
    series: [
      // 堆叠：失败在上、成功在下；只有顶上一段是圆角，两段之间留 2px 底色缝
      { name: t('dashboard.success'), type: 'bar', stack: 'logins', data: d?.logins.success ?? [], barMaxWidth: 22, itemStyle: { borderColor: th.value.surface, borderWidth: 1 } },
      { name: t('dashboard.failed'), type: 'bar', stack: 'logins', data: d?.logins.failed ?? [], barMaxWidth: 22, itemStyle: { borderRadius: [4, 4, 0, 0], borderColor: th.value.surface, borderWidth: 1 } },
    ],
  }
})

// ---- 活跃用户排名 ----
const topUsers = computed(() => {
  const list = data.value?.topUsers ?? []
  const max = Math.max(1, ...list.map((u) => u.count))
  return list.slice(0, 7).map((u) => ({ ...u, pct: (u.count / max) * 100 }))
})

// ---- 常用操作 ----
const topActions = computed(() => {
  const list = data.value?.topActions ?? []
  const total = list.reduce((x, y) => x + y.count, 0) || 1
  return list.map((a) => ({ ...a, label: opLabel(a.action), share: (a.count / total) * 100 }))
})

// ---- 登录失败原因（环图，标签直接标在扇区外） ----
const reasonsOption = computed<EChartsCoreOption>(() => {
  const rows = data.value?.reasons ?? []
  return {
    ...ChartMotion,
    animationType: 'expansion',
    color: th.value.colors,
    tooltip: { ...tooltipStyle(th.value, 'item'), formatter: '{b}: {c} ({d}%)' },
    legend: { bottom: 0, textStyle: { color: th.value.text }, itemWidth: 10, itemHeight: 10 },
    series: [
      {
        type: 'pie',
        radius: ['48%', '70%'],
        center: ['50%', '44%'],
        padAngle: 1,
        itemStyle: { borderColor: th.value.surface, borderWidth: 2, borderRadius: 4 },
        label: { color: th.value.text, formatter: '{b}\n{c}' },
        labelLine: { lineStyle: { color: th.value.muted } },
        emphasis: { scale: true, scaleSize: 6 },
        data: rows.map((r) => ({ name: reasonLabel(r.reason), value: r.count })),
      },
    ],
  }
})

// ---- 活跃时段（按本地钟点） ----
const hourLabels = Array.from({ length: 24 }, (_, i) => `${String(i).padStart(2, '0')}:00`)
const hoursOption = computed<EChartsCoreOption>(() => {
  const h = data.value?.hours
  const base = axisStyle(th.value)
  const area = (c: string) => ({ color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: `${c}44` }, { offset: 1, color: `${c}05` }] } })
  return {
    ...ChartMotion,
    color: [C.value.success, C.value.ops],
    grid: { left: 8, right: 12, top: 36, bottom: 8, containLabel: true },
    tooltip: tooltipStyle(th.value, 'axis'),
    legend: { top: 0, right: 0, textStyle: { color: th.value.text }, itemWidth: 12, itemHeight: 8 },
    xAxis: { type: 'category', data: hourLabels, boundaryGap: false, ...base, splitLine: { show: false } },
    yAxis: { type: 'value', minInterval: 1, ...base, axisLine: { show: false } },
    series: [
      { name: t('dashboard.loginsSeries'), type: 'line', smooth: 0.3, smoothMonotone: 'x', showSymbol: false, symbolSize: 8, lineStyle: { width: 2 }, areaStyle: area(C.value.success), data: h?.logins ?? [] },
      { name: t('dashboard.operations'), type: 'line', smooth: 0.3, smoothMonotone: 'x', showSymbol: false, symbolSize: 8, lineStyle: { width: 2 }, areaStyle: area(C.value.ops), data: h?.operations ?? [] },
    ],
  }
})

async function setRange(v: string | number | boolean | undefined) {
  range.value = v === 7 ? 7 : v === 90 ? 90 : 30
  await load()
}
</script>

<template>
  <div v-loading="loading" class="ga-page ga-dc">
    <div class="ga-dc__cards ga-rise">
      <el-card class="ga-dc__card ga-lift" shadow="never" data-test="dash-card-users">
        <div class="ga-dc__label">{{ t('dashboard.users') }}</div>
        <GaCountUp class="ga-dc__value" :value="data?.users.total" />
        <GaSparkline :data="data?.newUsers ?? []" :labels="dayLabels" type="area" :color="C.users" />
        <div class="ga-dc__foot">{{ t('dashboard.usersFoot', { enabled: fmt(data?.users.enabled), new: fmt(data?.users.new) }) }}</div>
      </el-card>
      <el-card class="ga-dc__card ga-lift" shadow="never" data-test="dash-card-logins">
        <div class="ga-dc__label">{{ t('dashboard.loginsToday') }}</div>
        <GaCountUp class="ga-dc__value" :value="loginsToday" />
        <GaSparkline :data="data?.logins.success ?? []" :labels="dayLabels" type="bar" :color="C.success" />
        <div class="ga-dc__foot">
          {{ t('dashboard.loginsFoot', { failed: fmt(failedToday) }) }} · {{ t('dashboard.rateToday', { rate: pct(rate(loginsToday, failedToday)) }) }}
        </div>
      </el-card>
      <el-card class="ga-dc__card ga-lift" shadow="never" data-test="dash-card-ops">
        <div class="ga-dc__label">{{ t('dashboard.opsToday') }}</div>
        <GaCountUp class="ga-dc__value" :value="last(data?.operations)" />
        <GaSparkline :data="data?.operations ?? []" :labels="dayLabels" type="area" :color="C.ops" />
        <div class="ga-dc__foot">{{ t('dashboard.opsFoot', { days: shown, total: fmt(sum(data?.operations)) }) }}</div>
      </el-card>
      <el-card class="ga-dc__card ga-lift" shadow="never" data-test="dash-card-rate">
        <div class="ga-dc__label">
          {{ t('dashboard.successRate') }}
          <el-tooltip :content="t('dashboard.successRateHint', { days: shown })" placement="top">
            <el-icon class="ga-dc__info"><InfoFilled /></el-icon>
          </el-tooltip>
        </div>
        <GaCountUp class="ga-dc__value" :value="periodRate" :decimals="1" suffix="%" />
        <div class="ga-dc__progress">
          <el-progress :percentage="Number(periodRate.toFixed(1))" :stroke-width="10" :show-text="false" :color="C.success" />
        </div>
        <div class="ga-dc__foot">{{ t('dashboard.sessionsNow', { n: fmt(data?.sessions) }) }}</div>
      </el-card>
    </div>

    <el-card shadow="never" class="ga-dc__rise">
      <div class="ga-dc__bar">
        <el-segmented v-model="tab" :options="[{ label: t('dashboard.loginTrend'), value: 'logins' }, { label: t('dashboard.opTrend'), value: 'ops' }]" />
        <el-radio-group :model-value="range" size="small" data-test="dash-range" @update:model-value="setRange">
          <el-radio-button :value="7">{{ t('dashboard.last7') }}</el-radio-button>
          <el-radio-button :value="30">{{ t('dashboard.last30') }}</el-radio-button>
          <el-radio-button :value="90">{{ t('dashboard.last90') }}</el-radio-button>
        </el-radio-group>
      </div>
      <div class="ga-dc__trend">
        <EChart :option="trendOption" height="340px" data-test="dash-trend" />
        <div class="ga-dc__rank" data-test="dash-top-users">
          <div class="ga-dc__title">{{ t('dashboard.topUsers') }}</div>
          <el-empty v-if="!topUsers.length" :description="t('dashboard.empty')" :image-size="64" />
          <ol v-else class="ga-dc__rank-list ga-rise">
            <li v-for="(u, i) in topUsers" :key="u.userId" class="ga-dc__rank-item">
              <span class="ga-dc__rank-no" :class="{ 'is-top': i < 3 }">{{ i + 1 }}</span>
              <span class="ga-dc__rank-name">
                <span class="ga-dc__rank-user">{{ u.username }}</span>
                <span class="ga-dc__rank-track"><span class="ga-dc__rank-fill" :style="{ width: `${u.pct}%`, background: C.ops }" /></span>
              </span>
              <span class="ga-dc__rank-count">{{ fmt(u.count) }}</span>
            </li>
          </ol>
        </div>
      </div>
    </el-card>

    <div class="ga-dc__row">
      <el-card shadow="never">
        <div class="ga-dc__title">{{ t('dashboard.topActions') }}</div>
        <el-empty v-if="!topActions.length" :description="t('dashboard.empty')" :image-size="64" />
        <table v-else class="ga-dc__table" data-test="dash-top-actions">
          <thead>
            <tr>
              <th>{{ t('dashboard.rank') }}</th>
              <th>{{ t('dashboard.action') }}</th>
              <th class="is-num">{{ t('dashboard.count') }}</th>
              <th class="is-share">{{ t('dashboard.share') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(a, i) in topActions" :key="a.action">
              <td>{{ i + 1 }}</td>
              <td>{{ a.label }}</td>
              <td class="is-num">{{ fmt(a.count) }}</td>
              <td class="is-share">
                <span class="ga-dc__share">
                  <span class="ga-dc__rank-track"><span class="ga-dc__rank-fill" :style="{ width: `${a.share}%`, background: C.ops }" /></span>
                  <span>{{ pct(a.share) }}%</span>
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </el-card>
      <el-card shadow="never">
        <div class="ga-dc__title">{{ t('dashboard.reasons') }}</div>
        <EChart v-if="data?.reasons.length" :option="reasonsOption" height="300px" />
        <el-empty v-else :description="t('dashboard.empty')" :image-size="80" />
      </el-card>
    </div>

    <el-card shadow="never">
      <div class="ga-dc__title">
        {{ t('dashboard.hours') }}
        <span class="ga-dc__sub">{{ t('dashboard.hoursHint', { days: shown }) }}</span>
      </div>
      <EChart :option="hoursOption" height="280px" data-test="dash-hours" />
    </el-card>
  </div>
</template>

<style scoped>
.ga-dc__cards {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--ga-gap);
}
@media (max-width: 1200px) {
  .ga-dc__cards {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .ga-dc__cards {
    grid-template-columns: 1fr;
  }
}
.ga-dc__card :deep(.el-card__body) {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 18px 22px 16px;
}
.ga-dc__label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: var(--ga-text-secondary);
}
.ga-dc__info {
  cursor: help;
}
.ga-dc__value {
  font-size: 30px;
  font-weight: 600;
  line-height: 1.15;
  color: var(--ga-text);
}
.ga-dc__progress {
  display: flex;
  align-items: center;
  height: 46px;
}
.ga-dc__progress :deep(.el-progress) {
  width: 100%;
}
.ga-dc__progress :deep(.el-progress-bar__inner) {
  transition: width 1.2s cubic-bezier(0.2, 0.8, 0.2, 1);
}
.ga-dc__foot {
  padding-top: 10px;
  border-top: 1px solid var(--ga-border);
  font-size: 13px;
  color: var(--ga-text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ga-dc__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.ga-dc__trend {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 300px;
  gap: 24px;
}
@media (max-width: 1000px) {
  .ga-dc__trend {
    grid-template-columns: 1fr;
  }
}
.ga-dc__title {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 10px;
  font-size: 15px;
  font-weight: 600;
}
.ga-dc__sub {
  font-size: 12px;
  font-weight: 400;
  color: var(--ga-text-secondary);
}
.ga-dc__rank-list {
  margin: 0;
  padding: 0;
  list-style: none;
}
.ga-dc__rank-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 6px;
  border-radius: 8px;
  transition: background-color 0.2s ease;
}
.ga-dc__rank-item:hover {
  background: var(--el-fill-color-light);
}
.ga-dc__rank-no {
  flex: none;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  border-radius: 50%;
  font-size: 12px;
  font-weight: 600;
  background: var(--el-fill-color);
  color: var(--ga-text-secondary);
}
.ga-dc__rank-no.is-top {
  background: var(--ga-text);
  color: var(--ga-surface);
}
.ga-dc__rank-name {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.ga-dc__rank-user {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ga-dc__rank-track {
  display: block;
  height: 4px;
  border-radius: 2px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.ga-dc__rank-fill {
  display: block;
  height: 100%;
  border-radius: 2px;
  animation: ga-dc-fill 1s cubic-bezier(0.2, 0.8, 0.2, 1) both;
  transform-origin: left center;
}
@keyframes ga-dc-fill {
  from {
    transform: scaleX(0);
  }
}
.ga-dc__rank-count {
  flex: none;
  font-variant-numeric: tabular-nums;
  color: var(--ga-text-secondary);
}
.ga-dc__row {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--ga-gap);
}
@media (max-width: 900px) {
  .ga-dc__row {
    grid-template-columns: 1fr;
  }
}
.ga-dc__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.ga-dc__table th {
  padding: 10px 8px;
  text-align: left;
  font-weight: 600;
  color: var(--ga-text-secondary);
  background: var(--el-fill-color-lighter);
}
.ga-dc__table td {
  padding: 10px 8px;
  border-bottom: 1px solid var(--ga-border);
}
.ga-dc__table tbody tr {
  transition: background-color 0.2s ease;
}
.ga-dc__table tbody tr:hover {
  background: var(--el-fill-color-light);
}
.ga-dc__table .is-num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.ga-dc__table .is-share {
  width: 34%;
}
.ga-dc__share {
  display: grid;
  grid-template-columns: 1fr 48px;
  align-items: center;
  gap: 8px;
  font-variant-numeric: tabular-nums;
  text-align: right;
}
@media (prefers-reduced-motion: reduce) {
  .ga-dc__rank-fill {
    animation: none;
  }
}
</style>
