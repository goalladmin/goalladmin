<script setup lang="ts">
// 代理商端、商户端的操作日志（D-066）：平台看这个端的全部，可以按主体编号筛选（操作人所属的主体）。
// 只读；查看本身记操作日志。动作名由那个端的模块定义，平台这里有翻译就显示翻译，没有就显示编码。
import { reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { formatTime, hasPerm, useI18n, useTable } from '@ga/shell'

import { orgApi } from '../../api/partner'
import type { OrgKind, OrgOperationLog } from '../../api/partner'

const props = defineProps<{ kind: OrgKind }>()
const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const api = orgApi(props.kind)
const canTimeline = hasPerm('system:audit:timeline')

interface Q extends Record<string, unknown> {
  orgCode: string
  username: string
  action: string
  path: string
  failed: number | undefined
  range: [string, string] | null
}

const codeFromRoute = () => (typeof route.query.orgCode === 'string' ? route.query.orgCode : '')
// 重置时清掉编号（初始值为空），打开时按地址栏里的编号查
const table = useTable<Q, OrgOperationLog>({
  query: { orgCode: '', username: '', action: '', path: '', failed: undefined, range: null },
  fetch: ({ range, ...rest }) =>
    api.operationLogs({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
  immediate: false,
})
table.query.orgCode = codeFromRoute()
void table.search()
const myPath = route.path
watch(
  () => route.query.orgCode,
  () => {
    if (route.path !== myPath) return
    table.query.orgCode = codeFromRoute()
    void table.search()
  },
)

function actionLabel(action: string) {
  const key = `op.${action}`
  return te(key) ? t(key) : action
}

const detail = reactive({ visible: false })
const current = ref<OrgOperationLog | null>(null)
function openDetail(row: OrgOperationLog) {
  current.value = row
  detail.visible = true
}
function toTimeline(key: 'ip' | 'sessionId', value: string) {
  if (!value) return
  detail.visible = false
  void router.push({ path: '/ops/timeline', query: { portal: props.kind, [key]: value } })
}
function pretty(s: string) {
  if (!s) return ''
  try {
    return JSON.stringify(JSON.parse(s), null, 2)
  } catch {
    return s
  }
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('partner.code')">
          <el-input v-model="table.query.orgCode" clearable style="width: 130px" data-test="orgoplog-code" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('common.username')">
          <el-input v-model="table.query.username" clearable style="width: 130px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.oplog.action')">
          <el-input v-model="table.query.action" clearable style="width: 150px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.oplog.path')">
          <el-input v-model="table.query.path" clearable :placeholder="`/api/${kind}/v1`" style="width: 200px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.oplog.result')">
          <el-select v-model="table.query.failed" clearable :placeholder="t('common.all')" style="width: 110px">
            <el-option :value="0" :label="t('sys.oplog.ok')" />
            <el-option :value="1" :label="t('sys.oplog.failed')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.oplog.time')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="orgoplog-table">
        <el-table-column :label="t('sys.oplog.time')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgOperationLog).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t(`partner.${kind}.label`)" min-width="160">
          <template #default="{ row }">
            <span class="ga-mono">{{ (row as OrgOperationLog).orgCode }}</span>
            <span v-if="(row as OrgOperationLog).orgName" class="ga-orglog__name">{{ (row as OrgOperationLog).orgName }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="username" :label="t('common.username')" min-width="110" />
        <el-table-column :label="t('sys.oplog.action')" min-width="130">
          <template #default="{ row }">{{ actionLabel((row as OrgOperationLog).action) }}</template>
        </el-table-column>
        <el-table-column :label="t('sys.oplog.request')" min-width="260" show-overflow-tooltip>
          <template #default="{ row }"><span class="ga-mono">{{ (row as OrgOperationLog).method }} {{ (row as OrgOperationLog).path }}</span></template>
        </el-table-column>
        <el-table-column :label="t('sys.oplog.result')" width="100">
          <template #default="{ row }">
            <el-tag :type="(row as OrgOperationLog).code === 0 ? 'success' : 'danger'" size="small">{{ (row as OrgOperationLog).code === 0 ? t('sys.oplog.ok') : (row as OrgOperationLog).code }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="IP" min-width="120">
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as OrgOperationLog).ip" :underline="false" class="ga-mono" @click="toTimeline('ip', (row as OrgOperationLog).ip)">{{ (row as OrgOperationLog).ip }}</el-link>
            <span v-else class="ga-mono">{{ (row as OrgOperationLog).ip }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="80" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row as OrgOperationLog)">{{ t('common.detail') }}</el-button>
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

    <el-dialog v-model="detail.visible" :title="t('sys.oplog.detail')" width="720px">
      <el-descriptions v-if="current" :column="2" border size="small">
        <el-descriptions-item :label="t('sys.oplog.time')">{{ formatTime(current.createdAt) }}</el-descriptions-item>
        <el-descriptions-item :label="t(`partner.${kind}.label`)">{{ current.orgCode }} {{ current.orgName }}</el-descriptions-item>
        <el-descriptions-item :label="t('common.username')">{{ current.username }} (#{{ current.userId }})</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.action')">{{ actionLabel(current.action) }}</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.result')">HTTP {{ current.httpStatus }} / code {{ current.code }}</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.latency')">{{ current.latencyMs }} ms</el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.request')" :span="2"><span class="ga-mono">{{ current.method }} {{ current.path }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.query')" :span="2"><span class="ga-mono">{{ current.query }}</span></el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.body')" :span="2"><pre class="ga-mono">{{ pretty(current.body) }}</pre></el-descriptions-item>
        <el-descriptions-item :label="t('sys.oplog.error')" :span="2">{{ current.error }}</el-descriptions-item>
        <el-descriptions-item label="IP">{{ current.ip }}</el-descriptions-item>
        <el-descriptions-item :label="t('ops.secevent.session')">
          <el-link v-if="canTimeline && current.sessionId" :underline="false" class="ga-mono" @click="toTimeline('sessionId', current.sessionId)">{{ current.sessionId }}</el-link>
          <span v-else class="ga-mono">{{ current.sessionId }}</span>
        </el-descriptions-item>
        <el-descriptions-item label="User-Agent" :span="2">{{ current.userAgent }}</el-descriptions-item>
        <el-descriptions-item label="Request-Id" :span="2"><span class="ga-mono">{{ current.requestId }}</span></el-descriptions-item>
      </el-descriptions>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-orglog__name {
  margin-left: 6px;
  color: var(--el-text-color-secondary);
}
</style>
