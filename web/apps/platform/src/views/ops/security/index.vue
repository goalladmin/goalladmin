<script setup lang="ts">
// 安全事件（D-032）：越权被拒、令牌异常、凭证重放等攻击迹象，只读。
// 地址栏可以带 ip、userId、sessionId、kind 过滤，别的页面据此跳过来。可以选看代理商端、商户端的（D-066）。
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { formatTime, hasPerm, useI18n, useTable } from '@ga/shell'

import IpBlockDialog from '../../../components/IpBlockDialog.vue'
import PortalSelect from '../../../components/PortalSelect.vue'
import { logApi, securityKinds } from '../../../api/system'
import type { SecurityEvent } from '../../../api/system'
import { opsPortals } from '../../../api/partner'
import type { OpsPortal } from '../../../api/partner'

const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const canTimeline = hasPerm('system:audit:timeline')
// 一键封禁这个来源（D-062）：打开黑名单对话框，IP 已填好
const canBlock = hasPerm('system:ip:deny')
const block = ref({ open: false, ip: '' })
// 时间线按同一个端查（D-066）
function toTimeline(key: 'ip' | 'userId' | 'sessionId', value: string | number) {
  const portal = table.query.portal === 'platform' ? {} : { portal: table.query.portal }
  if (value) void router.push({ path: '/ops/timeline', query: { ...portal, [key]: String(value) } })
}

interface Q extends Record<string, unknown> {
  portal: OpsPortal
  kind: string
  level: number | undefined
  username: string
  ip: string
  sessionId: string
  userId: string
  range: [string, string] | null
}

const q = (k: string) => (typeof route.query[k] === 'string' ? (route.query[k] as string) : '')
const routePortal = (opsPortals as readonly string[]).includes(q('portal')) ? (q('portal') as OpsPortal) : 'platform'

const table = useTable<Q, SecurityEvent>({
  query: { portal: routePortal, kind: q('kind'), level: undefined, username: '', ip: q('ip'), sessionId: q('sessionId'), userId: q('userId'), range: null },
  fetch: ({ range, userId, ...rest }) =>
    logApi.security({
      ...rest,
      userId: userId ? Number(userId) : undefined,
      from: range?.[0] ? new Date(range[0]).toISOString() : undefined,
      to: range?.[1] ? new Date(range[1]).toISOString() : undefined,
    }),
})

function kindLabel(kind: string) {
  const key = `ops.secevent.kinds.${kind}`
  return te(key) ? t(key) : kind
}
function kindHint(kind: string) {
  const key = `ops.secevent.hints.${kind}`
  return te(key) ? t(key) : ''
}
const levelType = (l: number) => (l >= 3 ? 'danger' : l === 2 ? 'warning' : 'info')
const levelLabel = (l: number) => t(l >= 3 ? 'ops.secevent.critical' : l === 2 ? 'ops.secevent.warning' : 'ops.secevent.info')
</script>

<template>
  <div class="ga-page">
    <el-alert :title="t('ops.secevent.hint')" type="info" :closable="false" show-icon />
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('ops.portal.label')">
          <PortalSelect v-model="table.query.portal" @update:model-value="table.search()" />
        </el-form-item>
        <el-form-item :label="t('ops.secevent.level')">
          <el-select v-model="table.query.level" clearable :placeholder="t('common.all')" style="width: 130px" data-test="secevent-level">
            <el-option :value="2" :label="t('ops.secevent.warningUp')" />
            <el-option :value="3" :label="t('ops.secevent.critical')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('ops.secevent.kind')">
          <el-select v-model="table.query.kind" clearable filterable :placeholder="t('common.all')" style="width: 200px">
            <el-option v-for="k in securityKinds" :key="k" :value="k" :label="kindLabel(k)" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('common.username')">
          <el-input v-model="table.query.username" clearable style="width: 130px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item label="IP">
          <el-input v-model="table.query.ip" clearable style="width: 150px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('ops.secevent.session')">
          <el-input v-model="table.query.sessionId" clearable style="width: 180px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('ops.secevent.time')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="secevent-table">
        <el-table-column :label="t('ops.secevent.time')" min-width="160">
          <template #default="{ row }">{{ formatTime((row as SecurityEvent).firstAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.level')" width="90">
          <template #default="{ row }">
            <el-tag :type="levelType((row as SecurityEvent).level)" size="small" effect="dark">{{ levelLabel((row as SecurityEvent).level) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.kind')" min-width="150">
          <template #default="{ row }">
            <el-tooltip :content="kindHint((row as SecurityEvent).kind)" :disabled="!kindHint((row as SecurityEvent).kind)" placement="top">
              <span>{{ kindLabel((row as SecurityEvent).kind) }}</span>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.who')" min-width="120" show-overflow-tooltip>
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as SecurityEvent).userId" :underline="false" @click="toTimeline('userId', (row as SecurityEvent).userId)">
              {{ (row as SecurityEvent).username || `#${(row as SecurityEvent).userId}` }}
            </el-link>
            <span v-else-if="(row as SecurityEvent).username || (row as SecurityEvent).userId">{{ (row as SecurityEvent).username || `#${(row as SecurityEvent).userId}` }}</span>
            <span v-else class="ga-sec__muted">-</span>
          </template>
        </el-table-column>
        <el-table-column label="IP" min-width="170">
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as SecurityEvent).ip" :underline="false" class="ga-mono" data-test="secevent-ip" @click="toTimeline('ip', (row as SecurityEvent).ip)">
              {{ (row as SecurityEvent).ip }}
            </el-link>
            <span v-else class="ga-mono">{{ (row as SecurityEvent).ip }}</span>
            <el-button v-if="canBlock && (row as SecurityEvent).ip" link type="danger" size="small" class="ga-sec__block" data-test="secevent-block" @click="block = { open: true, ip: (row as SecurityEvent).ip }">
              {{ t('ipacl.block') }}
            </el-button>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.session')" min-width="100">
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as SecurityEvent).sessionId" :underline="false" class="ga-mono" :title="(row as SecurityEvent).sessionId" @click="toTimeline('sessionId', (row as SecurityEvent).sessionId)">
              {{ (row as SecurityEvent).sessionId.slice(0, 8) }}
            </el-link>
            <span v-else class="ga-mono" :title="(row as SecurityEvent).sessionId">{{ (row as SecurityEvent).sessionId.slice(0, 8) }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.request')" min-width="220" show-overflow-tooltip>
          <template #default="{ row }"><span class="ga-mono">{{ (row as SecurityEvent).method }} {{ (row as SecurityEvent).path }}</span></template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.detail')" min-width="140" show-overflow-tooltip>
          <template #default="{ row }"><span class="ga-mono">{{ (row as SecurityEvent).detail }}</span></template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.count')" width="80" align="right" fixed="right">
          <template #default="{ row }">
            <el-tooltip :content="t('ops.secevent.span', { from: formatTime((row as SecurityEvent).firstAt), to: formatTime((row as SecurityEvent).lastAt) })" placement="top">
              <b>{{ (row as SecurityEvent).count }}</b>
            </el-tooltip>
          </template>
        </el-table-column>
      </el-table>
      <div class="ga-pagination">
        <el-pagination
          v-model:current-page="table.page"
          v-model:page-size="table.pageSize"
          :total="table.total"
          :page-sizes="[20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @current-change="table.onPageChange"
          @size-change="table.onSizeChange"
        />
      </div>
    </el-card>
    <IpBlockDialog v-model="block.open" :ip="block.ip" />
  </div>
</template>

<style scoped>
.ga-sec__muted {
  color: var(--el-text-color-secondary);
}
.ga-sec__block {
  margin-left: 6px;
}
</style>
