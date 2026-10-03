<script setup lang="ts">
// 主体白名单独立页面，接口仅允许当前主账号访问（D-090）。
import { computed, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '../../auth/store'
import { describeApiError } from '../../request/errors'
import { isApiError } from '../../types'
import { orgApi } from '../../org/api'
import type { OrgIPEntry } from '../../org/api'
const { t, te } = useI18n()
const auth = useAuthStore()
// ---- IP 白名单：整份编辑、整份保存，空表示不限制；会不会把自己挡在外面由服务端判断 ----
const maxRules = 100
const ip = reactive({ items: [] as OrgIPEntry[], yourIp: '', saving: false, error: '', loaded: false })
async function loadIP() {
  const v = await orgApi.ipAllow()
  ip.items = v.items.map((r) => ({ cidr: r.cidr, remark: r.remark }))
  ip.yourIp = v.yourIp
  ip.loaded = true
}
// 单个地址保存后是 /32、/128 的写法
const hasMine = computed(() => ip.items.some((e) => [ip.yourIp, `${ip.yourIp}/32`, `${ip.yourIp}/128`].includes(e.cidr.trim())))
function addRow(cidr = '', remark = '') {
  if (ip.items.length < maxRules) ip.items = [...ip.items, { cidr, remark }]
}
function removeRow(i: number) {
  ip.items = ip.items.filter((_, j) => j !== i)
}
async function saveIP() {
  ip.error = ''
  ip.saving = true
  try {
    const items = ip.items.filter((e) => e.cidr.trim() !== '').map((e) => ({ cidr: e.cidr.trim(), remark: e.remark.trim() }))
    const v = await orgApi.setIPAllow(items)
    ip.items = v.items.map((r) => ({ cidr: r.cidr, remark: r.remark }))
    ElMessage.success(t('common.success'))
  } catch (e) {
    if (isApiError(e)) ip.error = describeApiError(e, (k, p) => t(k, p ?? {}), (k) => te(k) || te(k, 'en-US')).join(t('shell.error.listSep'))
  } finally {
    ip.saving = false
  }
}

void loadIP().catch(() => { /* 请求层已提示 */ })
</script>
<template>
  <div class="ga-page">
    <el-card v-if="ip.loaded" data-test="org-ip-allow">
      <template #header>{{ t('org.ip.title') }}</template>
      <el-alert :title="t('org.ip.hint')" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
      <div class="ga-org-overview__ipbar">
        <span>
          {{ t('org.ip.yourIp') }}<b class="ga-mono" data-test="org-ip-your">{{ ip.yourIp || '-' }}</b>
        </span>
        <span>
          <el-button size="small" :disabled="!ip.yourIp || hasMine || ip.items.length >= maxRules" data-test="org-ip-add-mine" @click="addRow(ip.yourIp, t('org.ip.mine'))">
            {{ t('org.ip.addMine') }}
          </el-button>
          <el-button size="small" :disabled="ip.items.length >= maxRules" @click="addRow()">{{ t('org.ip.addRow') }}</el-button>
        </span>
      </div>
      <p class="ga-form-hint">{{ t('ipacl.wildcardHint') }}</p>
      <el-table :data="ip.items" size="small" :empty-text="t('org.ip.empty')">
        <el-table-column :label="t('org.ip.cidr')" min-width="200">
          <template #default="{ $index }">
            <el-input v-model="ip.items[$index]!.cidr" :placeholder="t('org.ip.cidrPlaceholder')" maxlength="64" :data-test="`org-ip-cidr-${$index}`" />
          </template>
        </el-table-column>
        <el-table-column :label="t('common.remark')" min-width="200">
          <template #default="{ $index }">
            <el-input v-model="ip.items[$index]!.remark" maxlength="255" />
          </template>
        </el-table-column>
        <el-table-column width="70">
          <template #default="{ $index }">
            <el-button link type="danger" @click="removeRow($index)">{{ t('common.delete') }}</el-button>
          </template>
        </el-table-column>
      </el-table>
      <p class="ga-form-hint">{{ t('org.ip.max', { max: maxRules }) }}</p>
      <el-alert v-if="ip.error" :title="ip.error" type="error" :closable="false" show-icon style="margin-top: 8px" />
      <div class="ga-org-overview__ipsave">
        <el-button type="primary" :loading="ip.saving" :disabled="!auth.user?.super" data-test="org-ip-save" @click="saveIP">{{ t('common.save') }}</el-button>
      </div>
    </el-card>
  </div>
</template>
<style scoped>
.ga-org-overview__ipbar { display: flex; justify-content: space-between; gap: 12px; flex-wrap: wrap; margin-bottom: 8px; }
.ga-org-overview__ipsave { display: flex; justify-content: flex-end; margin-top: 12px; }
</style>
