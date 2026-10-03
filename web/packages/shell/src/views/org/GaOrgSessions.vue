<script setup lang="ts">
// 主体端的在线会话（D-067）：只有本主体的。主账号的会话只有主账号自己能让它下线；这里只是置灰，挡住越权的是后端。
import { useI18n } from 'vue-i18n'
import { ElMessage, ElMessageBox } from 'element-plus'

import { useAuthStore } from '../../auth/store'
import { useTable } from '../../composables/useTable'
import { formatTime } from '../../format'
import { orgApi, orgPerm } from '../../org/api'
import type { OrgSession } from '../../org/api'

const { t } = useI18n()
const auth = useAuthStore()
const canRevoke = orgPerm('session:revoke')
const lockedForMe = (s: OrgSession) => s.owner && !auth.user?.super

const table = useTable<Record<string, unknown>, OrgSession>({ query: {}, fetch: (p) => orgApi.sessions(p) })

async function revoke(s: OrgSession) {
  try {
    await ElMessageBox.confirm(t('org.session.confirmRevoke', { name: s.username || s.userId }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await orgApi.revokeSession(s.sid)
  ElMessage.success(t('common.success'))
  await table.reload()
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <div class="ga-toolbar">
        <el-button @click="table.reload()">{{ t('common.reset') }}</el-button>
      </div>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="org-session-table">
        <el-table-column :label="t('common.username')" min-width="180">
          <template #default="{ row }">
            <span class="ga-org-session__who">
              {{ (row as OrgSession).username }}
              <el-tag v-if="(row as OrgSession).owner" size="small" type="warning">{{ t('org.account.owner') }}</el-tag>
              <el-tag v-if="(row as OrgSession).current" size="small" type="success">{{ t('org.session.current') }}</el-tag>
              <el-tag v-if="(row as OrgSession).locked" size="small" type="info">{{ t('org.session.locked') }}</el-tag>
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="ip" label="IP" min-width="130" />
        <el-table-column prop="userAgent" label="User-Agent" min-width="240" show-overflow-tooltip />
        <el-table-column :label="t('org.session.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgSession).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('org.session.lastSeenAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgSession).lastSeenAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('org.session.expiresAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as OrgSession).expiresAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="100" fixed="right">
          <template #default="{ row }">
            <el-tooltip :disabled="!lockedForMe(row as OrgSession)" :content="t('org.session.ownerHint')" placement="top">
              <span>
                <el-button v-perm="canRevoke" link type="danger" :disabled="lockedForMe(row as OrgSession)" data-test="org-session-revoke" @click="revoke(row as OrgSession)">
                  {{ t('org.session.revoke') }}
                </el-button>
              </span>
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
  </div>
</template>

<style scoped>
.ga-org-session__who {
  display: inline-flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
}
</style>
