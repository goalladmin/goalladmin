<script setup lang="ts">
import { ElMessage, ElMessageBox } from 'element-plus'
import { formatTime, useAuthStore, useI18n, useTable } from '@ga/shell'

import { sessionApi } from '../../../api/system'
import type { SessionView } from '../../../api/system'

const { t } = useI18n()
const auth = useAuthStore()
// 超管的会话只有超管能让它下线（D-035）；这里只是置灰，挡住越权的是后端
const lockedForMe = (s: SessionView) => s.superAccount && !auth.user?.super

const table = useTable<{ userId: number | undefined }, SessionView>({
  query: { userId: undefined },
  fetch: (p) => sessionApi.list(p),
})

async function revoke(s: SessionView) {
  try {
    await ElMessageBox.confirm(t('sys.session.confirmRevoke', { name: s.username || s.userId }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await sessionApi.revoke(s.sid)
  ElMessage.success(t('common.success'))
  await table.reload()
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('sys.session.userId')">
          <el-input-number v-model="table.query.userId" :min="1" :controls="false" style="width: 140px" />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe>
        <el-table-column :label="t('common.username')" min-width="140">
          <template #default="{ row }">
            {{ (row as SessionView).username }}
            <el-tag v-if="(row as SessionView).superAccount" size="small" type="danger" class="ga-session__super">{{ t('sys.session.superAccount') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="userId" :label="t('sys.session.userId')" width="90" />
        <el-table-column prop="ip" label="IP" min-width="130" />
        <el-table-column prop="userAgent" label="User-Agent" min-width="240" show-overflow-tooltip />
        <el-table-column :label="t('sys.session.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as SessionView).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('sys.session.lastSeenAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as SessionView).lastSeenAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('sys.session.expiresAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as SessionView).expiresAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="100" fixed="right">
          <template #default="{ row }">
            <el-button
              v-perm="'system:session:revoke'"
              link
              type="danger"
              :disabled="lockedForMe(row as SessionView)"
              :title="lockedForMe(row as SessionView) ? t('sys.session.superProtectedHint') : ''"
              :data-test="`session-revoke-${(row as SessionView).username}`"
              @click="revoke(row as SessionView)"
            >
              {{ t('sys.session.revoke') }}
            </el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="ga-pagination">
        <el-pagination
          v-model:current-page="table.page"
          v-model:page-size="table.pageSize"
          :total="table.total"
          :page-sizes="[10, 20, 50, 100]"
          layout="total, sizes, prev, pager, next"
          @current-change="table.onPageChange"
          @size-change="table.onSizeChange"
        />
      </div>
    </el-card>
  </div>
</template>

<style scoped>
.ga-session__super {
  margin-left: 6px;
}
</style>
