<script setup lang="ts">
// 代理商端、商户端的登录日志（D-066）：平台看这个端的全部，可以按主体编号筛选（从列表、详情点过来时已经带上）。
// 编号不存在的失败登录也在这里（主体那一列为空）。只读；查看本身记操作日志。
import { ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { formatTime, hasPerm, useI18n, useTable } from '@ga/shell'

import IpBlockDialog from '../IpBlockDialog.vue'
import { orgApi } from '../../api/partner'
import type { OrgKind, OrgLoginLog } from '../../api/partner'

const props = defineProps<{ kind: OrgKind }>()
const { t, te } = useI18n()
const route = useRoute()
const router = useRouter()
const api = orgApi(props.kind)
// 有调查时间线权限时，IP 和会话可以点过去看这条线（D-032），时间线先选好这个端（D-066）
const canTimeline = hasPerm('system:audit:timeline')
const canBlock = hasPerm('system:ip:deny')
const block = ref({ open: false, ip: '' })
function toTimeline(key: 'ip' | 'sessionId', value: string) {
  if (value) void router.push({ path: '/ops/timeline', query: { portal: props.kind, [key]: value } })
}

interface Q extends Record<string, unknown> {
  orgCode: string
  username: string
  ip: string
  success: number | undefined
  range: [string, string] | null
}

const codeFromRoute = () => (typeof route.query.orgCode === 'string' ? route.query.orgCode : '')
// 重置时清掉编号（初始值为空），打开时按地址栏里的编号查
const table = useTable<Q, OrgLoginLog>({
  query: { orgCode: '', username: '', ip: '', success: undefined, range: null },
  fetch: ({ range, ...rest }) =>
    api.loginLogs({ ...rest, from: range?.[0] ? new Date(range[0]).toISOString() : undefined, to: range?.[1] ? new Date(range[1]).toISOString() : undefined }),
  immediate: false,
})
table.query.orgCode = codeFromRoute()
void table.search()
// 已经打开着这一页时从列表又点过来（换了编号）：按新的编号查
const myPath = route.path
watch(
  () => route.query.orgCode,
  () => {
    if (route.path !== myPath) return
    table.query.orgCode = codeFromRoute()
    void table.search()
  },
)

function reasonLabel(reason: string) {
  if (!reason) return ''
  const key = `loginReason.${reason}`
  return te(key) ? t(key) : reason
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('partner.code')">
          <el-input v-model="table.query.orgCode" clearable style="width: 130px" data-test="orglog-code" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('common.username')">
          <el-input v-model="table.query.username" clearable style="width: 130px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item label="IP">
          <el-input v-model="table.query.ip" clearable style="width: 140px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item :label="t('sys.loginlog.result')">
          <el-select v-model="table.query.success" clearable :placeholder="t('common.all')" style="width: 110px">
            <el-option :value="1" :label="t('sys.loginlog.success')" />
            <el-option :value="0" :label="t('sys.loginlog.failed')" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.loginlog.time')">
          <el-date-picker v-model="table.query.range" type="datetimerange" :start-placeholder="t('common.from')" :end-placeholder="t('common.to')" style="width: 340px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="orglog-table">
        <el-table-column :label="t('sys.loginlog.time')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgLoginLog).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t(`partner.${kind}.label`)" min-width="160">
          <template #default="{ row }">
            <span class="ga-mono">{{ (row as OrgLoginLog).orgCode }}</span>
            <span v-if="(row as OrgLoginLog).orgName" class="ga-orglog__name">{{ (row as OrgLoginLog).orgName }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="username" :label="t('common.username')" min-width="120" />
        <el-table-column :label="t('sys.loginlog.result')" width="90">
          <template #default="{ row }">
            <el-tag :type="(row as OrgLoginLog).success ? 'success' : 'danger'" size="small">{{ (row as OrgLoginLog).success ? t('sys.loginlog.success') : t('sys.loginlog.failed') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('sys.loginlog.reason')" min-width="130">
          <template #default="{ row }">{{ reasonLabel((row as OrgLoginLog).reason) }}</template>
        </el-table-column>
        <el-table-column label="IP" min-width="170">
          <template #default="{ row }">
            <el-link v-if="canTimeline && (row as OrgLoginLog).ip" :underline="false" class="ga-mono" @click="toTimeline('ip', (row as OrgLoginLog).ip)">{{ (row as OrgLoginLog).ip }}</el-link>
            <span v-else class="ga-mono">{{ (row as OrgLoginLog).ip }}</span>
            <el-button v-if="canBlock && (row as OrgLoginLog).ip" link type="danger" size="small" class="ga-orglog__block" @click="block = { open: true, ip: (row as OrgLoginLog).ip }">
              {{ t('ipacl.block') }}
            </el-button>
          </template>
        </el-table-column>
        <el-table-column :label="t('ops.secevent.session')" min-width="110">
          <template #default="{ row }">
            <el-link
              v-if="canTimeline && (row as OrgLoginLog).sessionId"
              :underline="false"
              class="ga-mono"
              :title="(row as OrgLoginLog).sessionId"
              @click="toTimeline('sessionId', (row as OrgLoginLog).sessionId)"
            >
              {{ (row as OrgLoginLog).sessionId.slice(0, 8) }}
            </el-link>
            <span v-else class="ga-mono" :title="(row as OrgLoginLog).sessionId">{{ (row as OrgLoginLog).sessionId.slice(0, 8) }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="userAgent" label="User-Agent" min-width="220" show-overflow-tooltip />
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
.ga-orglog__name {
  margin-left: 6px;
  color: var(--el-text-color-secondary);
}
.ga-orglog__block {
  margin-left: 6px;
}
</style>
