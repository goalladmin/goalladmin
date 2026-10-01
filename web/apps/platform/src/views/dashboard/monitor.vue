<script setup lang="ts">
// 监控中心（docs/decisions.md D-030）：上半部分是安全态势（最近 24 小时，每 30 秒刷新），
// 下半部分是服务器状态（进程自身的只读指标，每 5 秒刷新）。离开页面或浏览器标签页隐藏时暂停刷新。
// 配置关掉服务器状态时（D-031）下半部分只显示一行说明，也不再请求。
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { GaCountUp, GaLiquid, GaSparkline, formatBytes, formatRelative, formatTime, splitDuration, useI18n, useLayoutStore } from '@ga/shell'

import { CircleCheckFilled, CircleCloseFilled, Refresh } from '@element-plus/icons-vue'

import EChart from '../../components/EChart.vue'
import { ChartMotion, axisStyle, chartTheme, tooltipStyle } from '../../components/echarts'
import type { EChartsCoreOption } from '../../components/echarts'
import { dashboardApi } from '../../api/system'
import type { MonitorSecurity, MonitorServer } from '../../api/system'

const SECURITY_EVERY = 30_000
const SERVER_EVERY = 5_000

const { t, te, locale } = useI18n()
const layout = useLayoutStore()
const sec = ref<MonitorSecurity | null>(null)
const srv = ref<MonitorServer | null>(null)
const secAt = ref<Date | null>(null)
const srvAt = ref<Date | null>(null)
const secFailed = ref(false)
const srvFailed = ref(false)
const srvDisabled = ref(false)
const tick = ref(Date.now())

// 两类请求各自最多一个在途：上一次还没回来时跳过这一轮，而不是再发一个把它作废——否则每次都比刷新间隔慢的
// 服务器上，每个成功的响应都会被后发的请求作废，指标就再也不更新了（D-045）。
// 页面停下（离开、缓存）时序号加一，之后返回的结果不再处理
let secSeq = 0
let srvSeq = 0
let secBusy = -1
let srvBusy = -1
async function loadSecurity() {
  if (secBusy === secSeq) return
  const mine = ++secSeq
  secBusy = mine
  try {
    const r = await dashboardApi.security()
    if (mine !== secSeq) return
    sec.value = r
    secAt.value = new Date()
    secFailed.value = false
  } catch {
    if (mine === secSeq) secFailed.value = true
  } finally {
    if (secBusy === mine) secBusy = -1
  }
}
async function loadServer() {
  if (srvDisabled.value || srvBusy === srvSeq) return
  const mine = ++srvSeq
  srvBusy = mine
  try {
    const r = await dashboardApi.server()
    if (mine !== srvSeq) return
    if (!r.enabled) {
      srvDisabled.value = true
      srv.value = null
      return
    }
    srv.value = r
    srvAt.value = new Date()
    srvFailed.value = false
  } catch {
    if (mine === srvSeq) srvFailed.value = true
  } finally {
    if (srvBusy === mine) srvBusy = -1
  }
}

// ---- 定时刷新：只在页面可见时进行 ----
const timers: number[] = []
function start() {
  stop()
  void loadSecurity()
  void loadServer()
  const visible = () => typeof document === 'undefined' || !document.hidden
  timers.push(window.setInterval(() => visible() && void loadSecurity(), SECURITY_EVERY))
  timers.push(window.setInterval(() => visible() && void loadServer(), SERVER_EVERY))
  timers.push(window.setInterval(() => (tick.value = Date.now()), 1000))
}
function stop() {
  while (timers.length) window.clearInterval(timers.pop())
  // 在途的请求作废
  secSeq++
  srvSeq++
}
onMounted(start)
onActivated(() => timers.length || start())
onDeactivated(stop)
onBeforeUnmount(stop)

const th = computed(() => chartTheme(layout.dark))
const C = computed(() => ({ success: th.value.colors[0]!, failed: th.value.colors[1]!, ops: th.value.colors[2]!, amber: th.value.colors[3]!, pink: th.value.colors[4]! }))
const nf = computed(() => new Intl.NumberFormat(locale.value))
const fmt = (n: number | undefined | null) => nf.value.format(n ?? 0)
const ms = (n: number | undefined) => `${new Intl.NumberFormat(locale.value, { maximumFractionDigits: n !== undefined && n < 10 ? 1 : 0 }).format(n ?? 0)} ms`
const sum = (a: number[] | undefined) => (a ?? []).reduce((x, y) => x + y, 0)
// 百分数按界面语言写小数点
const pf = computed(() => new Intl.NumberFormat(locale.value, { minimumFractionDigits: 1, maximumFractionDigits: 1 }))
const clock = (v: string | Date) => new Date(v).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' })
const updated = (d: Date | null) => (d ? d.toLocaleTimeString(locale.value) : '—')
const reasonLabel = (r: string) => (te(`loginReason.${r}`) ? t(`loginReason.${r}`) : r)

// ================= 安全态势 =================
const hourLabels = computed(() => (sec.value?.hours ?? []).map((h) => clock(h)))
const ok24 = computed(() => sum(sec.value?.success))
const bad24 = computed(() => sum(sec.value?.failed))
const rate24 = computed(() => (ok24.value + bad24.value ? (ok24.value / (ok24.value + bad24.value)) * 100 : null))

const loginsOption = computed<EChartsCoreOption>(() => {
  const s = sec.value
  const base = axisStyle(th.value)
  const area = (c: string) => ({ color: { type: 'linear', x: 0, y: 0, x2: 0, y2: 1, colorStops: [{ offset: 0, color: `${c}44` }, { offset: 1, color: `${c}05` }] } })
  return {
    ...ChartMotion,
    color: [C.value.success, C.value.failed],
    grid: { left: 8, right: 12, top: 36, bottom: 8, containLabel: true },
    tooltip: tooltipStyle(th.value, 'axis'),
    legend: { top: 0, right: 0, textStyle: { color: th.value.text }, itemWidth: 12, itemHeight: 8 },
    xAxis: { type: 'category', data: hourLabels.value, boundaryGap: false, ...base, splitLine: { show: false } },
    yAxis: { type: 'value', minInterval: 1, ...base, axisLine: { show: false } },
    series: [
      { name: t('monitor.success'), type: 'line', smooth: 0.3, smoothMonotone: 'x', showSymbol: false, symbolSize: 8, lineStyle: { width: 2 }, areaStyle: area(C.value.success), data: s?.success ?? [] },
      { name: t('monitor.failed'), type: 'line', smooth: 0.3, smoothMonotone: 'x', showSymbol: false, symbolSize: 8, lineStyle: { width: 2 }, areaStyle: area(C.value.failed), data: s?.failed ?? [] },
    ],
  }
})

function gauge(value: number | null, color: string, name: string): EChartsCoreOption {
  return {
    ...ChartMotion,
    series: [
      {
        type: 'gauge',
        name,
        startAngle: 210,
        endAngle: -30,
        min: 0,
        max: 100,
        radius: '92%',
        center: ['50%', '58%'],
        progress: { show: true, width: 14, roundCap: true, itemStyle: { color } },
        axisLine: { lineStyle: { width: 14, color: [[1, th.value.border]] }, roundCap: true },
        pointer: { show: true, length: '58%', width: 5, itemStyle: { color } },
        anchor: { show: true, size: 12, itemStyle: { color: th.value.surface, borderColor: color, borderWidth: 3 } },
        axisTick: { distance: -24, splitNumber: 4, lineStyle: { color: th.value.muted, width: 1 } },
        splitLine: { distance: -28, length: 10, lineStyle: { color: th.value.muted, width: 2 } },
        axisLabel: { distance: -8, color: th.value.muted, fontSize: 11 },
        title: { show: false },
        detail: {
          valueAnimation: true,
          offsetCenter: [0, '42%'],
          fontSize: 26,
          fontWeight: 600,
          color: th.value.text,
          formatter: (v: number) => (value === null ? '—' : `${pf.value.format(v)}%`),
        },
        data: [{ value: value ?? 0, name }],
      },
    ],
  }
}
const rateOption = computed(() => gauge(rate24.value, C.value.success, t('monitor.successRate')))

const failedIps = computed(() => {
  const list = sec.value?.failedIps ?? []
  const max = Math.max(1, ...list.map((x) => x.count))
  return list.map((x) => ({ ...x, pct: (x.count / max) * 100 }))
})
const recent = computed(() => (sec.value?.recent ?? []).map((r) => ({ ...r, key: `${r.createdAt}|${r.username}|${r.ip}` })))

// ================= 服务器状态 =================
const minutes = computed(() => srv.value?.requests.minutes ?? [])
const minuteLabels = computed(() => minutes.value.map((m) => clock(m.at)))
const cpuSeries = computed(() => minutes.value.map((m) => m.cpu ?? 0))
const heapSeries = computed(() => minutes.value.map((m) => m.heapInuse ?? 0))
const goroutineSeries = computed(() => minutes.value.map((m) => m.goroutines ?? 0))
const req60 = computed(() => minutes.value.reduce((x, m) => x + m.count, 0))
const err60 = computed(() => minutes.value.reduce((x, m) => x + m.serverErrors, 0))

// 运行时长按本地时钟每秒走，不必等下一次刷新
const uptime = computed(() => {
  if (!srv.value || !srvAt.value) return null
  return splitDuration(srv.value.uptime + (tick.value - srvAt.value.getTime()) / 1000)
})
const uptimeText = computed(() => {
  const u = uptime.value
  if (!u) return '—'
  return u.d ? t('monitor.uptimeDays', u) : t('monitor.uptimeHours', u)
})

const requestsOption = computed<EChartsCoreOption>(() => {
  const base = axisStyle(th.value)
  const okCounts = minutes.value.map((m) => m.count - m.clientErrors - m.serverErrors)
  return {
    ...ChartMotion,
    color: [C.value.success, C.value.amber, C.value.failed],
    grid: { left: 8, right: 12, top: 36, bottom: 8, containLabel: true },
    tooltip: tooltipStyle(th.value, 'axis'),
    legend: { top: 0, right: 0, textStyle: { color: th.value.text }, itemWidth: 12, itemHeight: 8 },
    xAxis: { type: 'category', data: minuteLabels.value, ...base, splitLine: { show: false }, axisLabel: { ...base.axisLabel, interval: 9 } },
    yAxis: { type: 'value', minInterval: 1, ...base, axisLine: { show: false } },
    series: [
      { name: t('monitor.ok'), type: 'bar', stack: 'req', data: okCounts, barMaxWidth: 10, itemStyle: { borderColor: th.value.surface, borderWidth: 1 } },
      { name: '4xx', type: 'bar', stack: 'req', data: minutes.value.map((m) => m.clientErrors), barMaxWidth: 10, itemStyle: { borderColor: th.value.surface, borderWidth: 1 } },
      { name: '5xx', type: 'bar', stack: 'req', data: minutes.value.map((m) => m.serverErrors), barMaxWidth: 10, itemStyle: { borderRadius: [3, 3, 0, 0], borderColor: th.value.surface, borderWidth: 1 } },
    ],
  }
})

const latencyOption = computed<EChartsCoreOption>(() => {
  const base = axisStyle(th.value)
  // 没有请求的分钟不画点（null），不当作 0 耗时
  const pick = (k: 'avgMs' | 'p95Ms') => minutes.value.map((m) => (m.count ? Number(m[k].toFixed(2)) : null))
  return {
    ...ChartMotion,
    color: [C.value.success, C.value.pink],
    grid: { left: 8, right: 12, top: 36, bottom: 8, containLabel: true },
    tooltip: { ...tooltipStyle(th.value, 'axis'), valueFormatter: (v: number | null) => (v === null ? '—' : `${v} ms`) },
    legend: { top: 0, right: 0, textStyle: { color: th.value.text }, itemWidth: 12, itemHeight: 8 },
    xAxis: { type: 'category', data: minuteLabels.value, boundaryGap: false, ...base, splitLine: { show: false }, axisLabel: { ...base.axisLabel, interval: 9 } },
    yAxis: { type: 'value', name: 'ms', nameTextStyle: { color: th.value.muted }, ...base, axisLine: { show: false } },
    series: [
      // 显示小圆点：前后都没有请求的那一分钟是孤立的点，不画点就看不见
      { name: t('monitor.avg'), type: 'line', smooth: 0.3, smoothMonotone: 'x', connectNulls: false, showSymbol: true, symbol: 'circle', symbolSize: 5, lineStyle: { width: 2 }, data: pick('avgMs') },
      { name: 'P95', type: 'line', smooth: 0.3, smoothMonotone: 'x', connectNulls: false, showSymbol: true, symbol: 'circle', symbolSize: 5, lineStyle: { width: 2 }, data: pick('p95Ms') },
    ],
  }
})

const cpuOption = computed(() => gauge(srv.value?.cpu.supported ? srv.value.cpu.percent : null, C.value.ops, t('monitor.cpu')))

const poolPct = computed(() => {
  const d = srv.value?.db
  if (!d) return 0
  const cap = d.maxOpen || Math.max(d.open, 1)
  return (d.inUse / cap) * 100
})

type TagType = 'info' | 'success' | 'warning' | 'danger'
const methodTypes: Record<string, TagType> = { GET: 'info', POST: 'success', PUT: 'warning', PATCH: 'warning', DELETE: 'danger' }
const methodType = (m: string): TagType => methodTypes[m] ?? 'info'
</script>

<template>
  <div class="ga-page ga-mon">
    <!-- ======== 安全态势 ======== -->
    <div class="ga-mon__head">
      <h3>{{ t('monitor.security') }}</h3>
      <span class="ga-mon__meta">
        {{ t('monitor.securityMeta') }} · {{ t('monitor.updated', { time: updated(secAt) }) }}
        <el-tag v-if="secFailed" type="warning" size="small">{{ t('monitor.refreshFailed') }}</el-tag>
      </span>
      <el-button link :icon="Refresh" @click="loadSecurity">{{ t('monitor.refresh') }}</el-button>
    </div>

    <div class="ga-mon__cards ga-rise">
      <el-card class="ga-mon__card ga-lift" shadow="never" data-test="mon-card-sessions">
        <div class="ga-mon__label">{{ t('monitor.sessions') }}</div>
        <GaCountUp class="ga-mon__value" :value="sec?.sessions" />
        <div class="ga-mon__foot">{{ t('monitor.lockedSessions', { n: fmt(sec?.lockedSessions) }) }}</div>
      </el-card>
      <el-card class="ga-mon__card ga-lift" shadow="never">
        <div class="ga-mon__label">{{ t('monitor.success24') }}</div>
        <GaCountUp class="ga-mon__value" :value="ok24" />
        <GaSparkline :data="sec?.success ?? []" :labels="hourLabels" type="bar" :color="C.success" :height="36" />
      </el-card>
      <el-card class="ga-mon__card ga-lift" shadow="never">
        <div class="ga-mon__label">{{ t('monitor.failed24') }}</div>
        <GaCountUp class="ga-mon__value" :value="bad24" />
        <GaSparkline :data="sec?.failed ?? []" :labels="hourLabels" type="bar" :color="C.failed" :height="36" />
      </el-card>
      <el-card class="ga-mon__card ga-lift" shadow="never">
        <div class="ga-mon__label">{{ t('monitor.ops24') }}</div>
        <GaCountUp class="ga-mon__value" :value="sec?.operations" />
        <div class="ga-mon__foot">{{ t('monitor.opsPerHour', { n: fmt(Math.round((sec?.operations ?? 0) / 24)) }) }}</div>
      </el-card>
    </div>

    <div class="ga-mon__row ga-mon__row--wide">
      <el-card shadow="never">
        <div class="ga-mon__title">{{ t('monitor.loginTrend') }}</div>
        <EChart :option="loginsOption" height="300px" live data-test="mon-logins" />
      </el-card>
      <el-card shadow="never">
        <div class="ga-mon__title">{{ t('monitor.successRate') }}</div>
        <EChart :option="rateOption" height="300px" live />
      </el-card>
    </div>

    <div class="ga-mon__row ga-mon__row--wide">
      <el-card shadow="never">
        <div class="ga-mon__title">{{ t('monitor.recentLogins') }}</div>
        <el-empty v-if="!recent.length" :description="t('monitor.empty')" :image-size="64" />
        <TransitionGroup v-else tag="ul" name="ga-mon-list" class="ga-mon__stream" data-test="mon-recent">
          <li v-for="r in recent" :key="r.key" class="ga-mon__stream-item">
            <el-icon :class="r.success ? 'is-ok' : 'is-bad'" :style="{ color: r.success ? C.success : C.failed }">
              <CircleCheckFilled v-if="r.success" /><CircleCloseFilled v-else />
            </el-icon>
            <span class="ga-mon__stream-user">{{ r.username || '—' }}</span>
            <span class="ga-mon__stream-result">{{ r.success ? t('monitor.loginOk') : reasonLabel(r.reason) }}</span>
            <code class="ga-mon__stream-ip">{{ r.ip }}</code>
            <span class="ga-mon__stream-time" :title="formatTime(r.createdAt)">{{ formatRelative(r.createdAt, locale, new Date(tick)) }}</span>
          </li>
        </TransitionGroup>
      </el-card>
      <el-card shadow="never">
        <div class="ga-mon__title">{{ t('monitor.failedIps') }}</div>
        <el-empty v-if="!failedIps.length" :description="t('monitor.noFailures')" :image-size="64" />
        <ul v-else class="ga-mon__bars ga-rise">
          <li v-for="x in failedIps" :key="x.ip">
            <code>{{ x.ip }}</code>
            <span class="ga-mon__track"><span class="ga-mon__fill" :style="{ width: `${x.pct}%`, background: C.failed }" /></span>
            <span class="ga-mon__num">{{ fmt(x.count) }}</span>
          </li>
        </ul>
      </el-card>
    </div>

    <!-- ======== 服务器状态 ======== -->
    <el-card v-if="srvDisabled" shadow="never" data-test="mon-server-disabled">
      <div class="ga-mon__title">{{ t('monitor.server') }}</div>
      <div class="ga-mon__meta">{{ t('monitor.serverDisabled') }}</div>
    </el-card>
    <template v-else>
      <div class="ga-mon__head">
        <h3>{{ t('monitor.server') }}</h3>
        <span class="ga-mon__meta">
          {{ t('monitor.serverMeta') }} · {{ t('monitor.updated', { time: updated(srvAt) }) }}
          <el-tag v-if="srvFailed" type="warning" size="small">{{ t('monitor.refreshFailed') }}</el-tag>
        </span>
      </div>

      <div class="ga-mon__cards ga-rise">
        <el-card class="ga-mon__card ga-lift" shadow="never" data-test="mon-card-uptime">
          <div class="ga-mon__label">{{ t('monitor.uptime') }}</div>
          <div class="ga-mon__value ga-mon__value--text">{{ uptimeText }}</div>
          <div class="ga-mon__foot">{{ t('monitor.startedAt', { time: srv ? formatTime(srv.startedAt) : '—' }) }}</div>
        </el-card>
        <el-card class="ga-mon__card ga-lift" shadow="never" data-test="mon-card-cpu">
          <div class="ga-mon__label">{{ t('monitor.cpu') }}</div>
          <GaCountUp v-if="srv?.cpu.supported" class="ga-mon__value" :value="srv.cpu.percent" :decimals="1" suffix="%" />
          <div v-else class="ga-mon__value ga-mon__value--text">—</div>
          <GaSparkline :data="cpuSeries" :labels="minuteLabels" type="area" :color="C.ops" :height="36" />
        </el-card>
        <el-card class="ga-mon__card ga-lift" shadow="never" data-test="mon-card-memory">
          <div class="ga-mon__label">{{ t('monitor.memory') }}</div>
          <div class="ga-mon__value ga-mon__value--text">{{ formatBytes(srv?.memory.sys, locale) }}</div>
          <GaSparkline :data="heapSeries" :labels="minuteLabels" type="area" :color="C.amber" :height="36" />
          <div class="ga-mon__foot">{{ t('monitor.heap', { heap: formatBytes(srv?.memory.heapInuse, locale) }) }}</div>
        </el-card>
        <el-card class="ga-mon__card ga-lift" shadow="never">
          <div class="ga-mon__label">{{ t('monitor.goroutines') }}</div>
          <GaCountUp class="ga-mon__value" :value="srv?.runtime.goroutines" />
          <GaSparkline :data="goroutineSeries" :labels="minuteLabels" type="area" :color="C.pink" :height="36" />
          <div class="ga-mon__foot">{{ t('monitor.gc', { n: fmt(srv?.memory.numGc) }) }}</div>
        </el-card>
      </div>

      <div class="ga-mon__row ga-mon__row--wide">
        <el-card shadow="never">
          <div class="ga-mon__title">
            {{ t('monitor.requests') }}
            <span class="ga-mon__sub">{{ t('monitor.requestsSum', { n: fmt(req60), err: fmt(err60) }) }}</span>
          </div>
          <EChart :option="requestsOption" height="280px" live data-test="mon-requests" />
        </el-card>
        <el-card shadow="never">
          <div class="ga-mon__title">{{ t('monitor.cpu') }}</div>
          <EChart v-if="srv?.cpu.supported !== false" :option="cpuOption" height="280px" live />
          <el-empty v-else :description="t('monitor.cpuUnsupported')" :image-size="64" />
        </el-card>
      </div>

      <div class="ga-mon__row ga-mon__row--wide">
        <el-card shadow="never">
          <div class="ga-mon__title">{{ t('monitor.latency') }}</div>
          <EChart :option="latencyOption" height="280px" live />
        </el-card>
        <el-card shadow="never" data-test="mon-db">
          <div class="ga-mon__title">
            {{ t('monitor.database') }}
            <el-tag v-if="srv" :type="srv.db.ok ? 'success' : 'danger'" size="small" effect="light">{{ srv.db.ok ? t('monitor.dbOk') : t('monitor.dbDown') }}</el-tag>
          </div>
          <div class="ga-mon__db">
            <GaLiquid :percent="poolPct" :label="t('monitor.poolUsage')" :size="150" :color="C.success" />
            <dl class="ga-mon__kv">
              <dt>{{ t('monitor.dbVersion') }}</dt>
              <dd>{{ srv?.db.version || '—' }}</dd>
              <dt>{{ t('monitor.dbLatency') }}</dt>
              <dd>{{ srv?.db.ok ? ms(srv.db.latencyMs) : '—' }}</dd>
              <dt>{{ t('monitor.dbConns') }}</dt>
              <dd>{{ t('monitor.dbConnsValue', { open: fmt(srv?.db.open), inUse: fmt(srv?.db.inUse), idle: fmt(srv?.db.idle) }) }}</dd>
              <dt>{{ t('monitor.dbMax') }}</dt>
              <dd>{{ srv?.db.maxOpen ? fmt(srv.db.maxOpen) : t('monitor.unlimited') }}</dd>
              <dt>{{ t('monitor.dbWait') }}</dt>
              <dd>{{ t('monitor.dbWaitValue', { n: fmt(srv?.db.waitCount), time: ms(srv?.db.waitMs) }) }}</dd>
            </dl>
          </div>
        </el-card>
      </div>

      <div class="ga-mon__row ga-mon__row--wide">
        <el-card shadow="never">
          <div class="ga-mon__title">{{ t('monitor.routes') }}</div>
          <el-empty v-if="!srv?.requests.routes.length" :description="t('monitor.empty')" :image-size="64" />
          <table v-else class="ga-mon__table" data-test="mon-routes">
            <thead>
              <tr>
                <th>{{ t('monitor.route') }}</th>
                <th class="is-num">{{ t('monitor.count') }}</th>
                <th class="is-num">5xx</th>
                <th class="is-num">{{ t('monitor.avg') }}</th>
                <th class="is-num">P95</th>
              </tr>
            </thead>
            <TransitionGroup tag="tbody" name="ga-mon-list">
              <tr v-for="r in srv.requests.routes" :key="`${r.method} ${r.route}`">
                <td class="ga-mon__route">
                  <el-tag :type="methodType(r.method)" size="small" effect="plain">{{ r.method || '—' }}</el-tag>
                  <code>{{ r.route || t('monitor.unmatched') }}</code>
                </td>
                <td class="is-num">{{ fmt(r.count) }}</td>
                <td class="is-num">{{ fmt(r.serverErrors) }}</td>
                <td class="is-num">{{ ms(r.avgMs) }}</td>
                <td class="is-num">{{ ms(r.p95Ms) }}</td>
              </tr>
            </TransitionGroup>
          </table>
        </el-card>
        <el-card shadow="never">
          <div class="ga-mon__title">{{ t('monitor.runtime') }}</div>
          <dl class="ga-mon__kv ga-mon__kv--wide">
            <dt>{{ t('monitor.goVersion') }}</dt>
            <dd>{{ srv?.runtime.goVersion ?? '—' }}</dd>
            <dt>{{ t('monitor.platform') }}</dt>
            <dd>{{ srv ? `${srv.runtime.os}/${srv.runtime.arch}` : '—' }}</dd>
            <dt>{{ t('monitor.cores') }}</dt>
            <dd>{{ srv ? t('monitor.coresValue', { cpu: srv.runtime.numCpu, procs: srv.runtime.gomaxprocs }) : '—' }}</dd>
            <dt>{{ t('monitor.build') }}</dt>
            <dd>{{ srv?.runtime.version || '—' }}</dd>
            <dt>{{ t('monitor.heapObjects') }}</dt>
            <dd>{{ fmt(srv?.memory.heapObjects) }}</dd>
            <dt>{{ t('monitor.lastGc') }}</dt>
            <dd>{{ srv?.memory.lastGc ? formatRelative(srv.memory.lastGc, locale, new Date(tick)) : '—' }}</dd>
            <dt>{{ t('monitor.gcPause') }}</dt>
            <dd>{{ ms(srv?.memory.pauseTotalMs) }}</dd>
          </dl>
        </el-card>
      </div>
    </template>
  </div>
</template>

<style scoped>
.ga-mon__head {
  display: flex;
  align-items: baseline;
  gap: 12px;
  flex-wrap: wrap;
}
.ga-mon__head h3 {
  margin: 0;
  font-size: 17px;
  font-weight: 600;
}
.ga-mon__meta {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--ga-text-secondary);
}
.ga-mon__head .el-button {
  margin-left: auto;
}
.ga-mon__cards {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: var(--ga-gap);
}
@media (max-width: 1200px) {
  .ga-mon__cards {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 600px) {
  .ga-mon__cards {
    grid-template-columns: 1fr;
  }
}
.ga-mon__card :deep(.el-card__body) {
  display: flex;
  flex-direction: column;
  gap: 8px;
  min-height: 118px;
  padding: 18px 22px 16px;
}
.ga-mon__label {
  font-size: 14px;
  color: var(--ga-text-secondary);
}
.ga-mon__value {
  font-size: 28px;
  font-weight: 600;
  line-height: 1.15;
  color: var(--ga-text);
}
.ga-mon__value--text {
  font-variant-numeric: tabular-nums;
}
.ga-mon__foot {
  margin-top: auto;
  padding-top: 8px;
  border-top: 1px solid var(--ga-border);
  font-size: 13px;
  color: var(--ga-text-secondary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ga-mon__row {
  display: grid;
  gap: var(--ga-gap);
}
.ga-mon__row--wide {
  grid-template-columns: minmax(0, 2fr) minmax(0, 1fr);
}
@media (max-width: 1000px) {
  .ga-mon__row--wide {
    grid-template-columns: 1fr;
  }
}
.ga-mon__title {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
  font-size: 15px;
  font-weight: 600;
}
.ga-mon__sub {
  font-size: 12px;
  font-weight: 400;
  color: var(--ga-text-secondary);
}
.ga-mon__stream {
  margin: 0;
  padding: 0;
  list-style: none;
  max-height: 330px;
  overflow: auto;
}
.ga-mon__stream-item {
  display: grid;
  grid-template-columns: 18px minmax(80px, 1fr) minmax(80px, 1fr) minmax(90px, auto) 90px;
  align-items: center;
  gap: 10px;
  padding: 7px 6px;
  border-radius: 8px;
  font-size: 13px;
  transition: background-color 0.2s ease;
}
.ga-mon__stream-item:hover {
  background: var(--el-fill-color-light);
}
.ga-mon__stream-user,
.ga-mon__stream-result {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ga-mon__stream-result,
.ga-mon__stream-time {
  color: var(--ga-text-secondary);
}
.ga-mon__stream-time {
  text-align: right;
}
.ga-mon__bars {
  margin: 0;
  padding: 0;
  list-style: none;
}
.ga-mon__bars li {
  display: grid;
  grid-template-columns: minmax(96px, auto) 1fr 44px;
  align-items: center;
  gap: 10px;
  padding: 7px 4px;
  font-size: 13px;
}
.ga-mon__track {
  height: 6px;
  border-radius: 3px;
  background: var(--el-fill-color);
  overflow: hidden;
}
.ga-mon__fill {
  display: block;
  height: 100%;
  border-radius: 3px;
  transform-origin: left center;
  animation: ga-mon-fill 1s cubic-bezier(0.2, 0.8, 0.2, 1) both;
  transition: width 0.6s ease;
}
@keyframes ga-mon-fill {
  from {
    transform: scaleX(0);
  }
}
.ga-mon__num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.ga-mon__db {
  display: flex;
  align-items: center;
  gap: 20px;
  flex-wrap: wrap;
}
.ga-mon__kv {
  flex: 1;
  min-width: 180px;
  display: grid;
  grid-template-columns: auto 1fr;
  gap: 8px 14px;
  margin: 0;
  font-size: 13px;
}
.ga-mon__kv dt {
  color: var(--ga-text-secondary);
}
.ga-mon__kv dd {
  margin: 0;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.ga-mon__kv--wide {
  gap: 12px 18px;
}
.ga-mon__table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.ga-mon__table th {
  padding: 9px 8px;
  text-align: left;
  font-weight: 600;
  color: var(--ga-text-secondary);
  background: var(--el-fill-color-lighter);
}
.ga-mon__table td {
  padding: 9px 8px;
  border-bottom: 1px solid var(--ga-border);
}
.ga-mon__table tbody tr:hover {
  background: var(--el-fill-color-light);
}
.ga-mon__table .is-num {
  text-align: right;
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.ga-mon__route {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.ga-mon__route code {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 新到的登录记录、接口排名变化：滑入 / 挪位 */
.ga-mon-list-enter-active,
.ga-mon-list-leave-active {
  transition:
    opacity 0.35s ease,
    transform 0.35s ease;
}
.ga-mon-list-enter-from {
  opacity: 0;
  transform: translateX(-12px);
}
.ga-mon-list-leave-to {
  opacity: 0;
}
.ga-mon-list-leave-active {
  position: absolute;
}
.ga-mon-list-move {
  transition: transform 0.35s ease;
}
@media (prefers-reduced-motion: reduce) {
  .ga-mon__fill {
    animation: none;
  }
}
</style>
