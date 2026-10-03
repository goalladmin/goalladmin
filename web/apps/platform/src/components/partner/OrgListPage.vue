<script setup lang="ts">
// 代理商列表、商户列表（D-065、D-066）：两种主体共用这一页，kind 决定接口和权限码；商户另有所属代理商的筛选、
// 新建时选代理商、改归属。开通时同时建主账号，初始密码只显示一次。
import { computed, onActivated, onBeforeUnmount, onDeactivated, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { formatTime, useAuthStore, useI18n, useTable } from '@ga/shell'

import { merchantApi, orgApi, orgPerms } from '../../api/partner'
import type { OrgBrief, OrgKind, OrgView } from '../../api/partner'
import OrgDetailDrawer from './OrgDetailDrawer.vue'
import OwnerPasswordDialog from './OwnerPasswordDialog.vue'

const props = defineProps<{ kind: OrgKind }>()
const { t } = useI18n()
const auth = useAuthStore()
const api = orgApi(props.kind)
const perms = orgPerms(props.kind)
const isMerchant = props.kind === 'merchant'

// ---- 列表 ----
interface Q extends Record<string, unknown> {
  keyword: string
  status: number | undefined
  agentId: number | undefined
}
const table = useTable<Q, OrgView>({
  query: { keyword: '', status: undefined, agentId: undefined },
  fetch: (p) => api.list(p),
})

// ---- 代理商选项（只对商户）：筛选时停用的也能选，新建和改归属时不能 ----
const agents = ref<OrgBrief[]>([])
const agentsLoading = ref(false)
// 边打字边查：先发的慢响应不能盖住后发的
let agentsSeq = 0
async function searchAgents(keyword = '') {
  if (!isMerchant) return
  const my = ++agentsSeq
  agentsLoading.value = true
  try {
    const list = await merchantApi.agentOptions(keyword)
    if (my === agentsSeq) agents.value = list
  } catch {
    // 已提示
  } finally {
    if (my === agentsSeq) agentsLoading.value = false
  }
}
void searchAgents()
const agentLabel = (a: OrgBrief) => `${a.name} (${a.code})`

function belongsTo(o: OrgView) {
  return o.agentId ? `${o.agentName ?? ''} (${o.agentCode ?? o.agentId})` : t('partner.direct')
}

// ---- 新建 / 编辑 ----
type Mode = 'create' | 'edit'
const dialog = reactive({ visible: false, mode: 'create' as Mode, id: 0, saving: false })
const form = reactive({ name: '', contactName: '', contactPhone: '', agentId: 0, sort: 0, remark: '', ownerUsername: '', ownerDisplayName: '' })
const formRef = ref<FormInstance>()
const rules = computed<FormRules>(() => ({
  name: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  ownerUsername: [{ required: dialog.mode === 'create', message: () => t('common.required'), trigger: 'blur' }],
}))

function openCreate() {
  formSeq++
  Object.assign(dialog, { mode: 'create', id: 0 })
  Object.assign(form, { name: '', contactName: '', contactPhone: '', agentId: 0, sort: 0, remark: '', ownerUsername: '', ownerDisplayName: '' })
  dialog.visible = true
}
function openEdit(o: OrgView) {
  formSeq++
  Object.assign(dialog, { mode: 'edit', id: o.id })
  Object.assign(form, { name: o.name, contactName: o.contactName, contactPhone: o.contactPhone, agentId: o.agentId, sort: o.sort, remark: o.remark, ownerUsername: o.ownerUsername, ownerDisplayName: '' })
  dialog.visible = true
}

const reveal = reactive({ visible: false, code: '', username: '', password: '' })
let pageActive = true
let pageSeq = 0
let formSeq = 0
function clearSecrets() {
  pageSeq++
  dialog.visible = false
  dialog.saving = false
  Object.assign(reveal, { visible: false, code: '', username: '', password: '' })
}
onActivated(() => { pageActive = true })
onDeactivated(() => { pageActive = false; clearSecrets() })
onBeforeUnmount(() => { pageActive = false; clearSecrets() })
watch(() => [props.kind, auth.epoch, auth.locked], clearSecrets, { flush: 'sync' })
watch(() => dialog.visible, (open) => {
  formSeq++
  if (!open) dialog.saving = false
}, { flush: 'sync' })
// 关掉就清空（D-098）
watch(() => reveal.visible, (open) => {
  if (!open) Object.assign(reveal, { code: '', username: '', password: '' })
}, { flush: 'sync' })

async function submit() {
  if (!formRef.value || !pageActive || !dialog.visible || dialog.saving || auth.locked) return
  const target = { page: pageSeq, form: formSeq, epoch: auth.epoch, kind: props.kind, mode: dialog.mode, id: dialog.id }
  const current = () => pageActive && !auth.locked && pageSeq === target.page && formSeq === target.form && auth.epoch === target.epoch && props.kind === target.kind && dialog.visible && dialog.mode === target.mode && dialog.id === target.id
  dialog.saving = true
  try {
    await formRef.value.validate()
    if (!current()) return
    const base = { name: form.name.trim(), contactName: form.contactName.trim(), contactPhone: form.contactPhone.trim(), sort: form.sort, remark: form.remark }
    if (target.mode === 'create') {
      const res = await orgApi(target.kind).create({
        ...base,
        ...(target.kind === 'merchant' ? { agentId: form.agentId || 0 } : {}),
        ownerUsername: form.ownerUsername.trim(),
        ownerDisplayName: form.ownerDisplayName.trim(),
      })
      if (!current()) return
      dialog.visible = false
      const created = res.agent ?? res.merchant
      Object.assign(reveal, { visible: true, code: created?.code ?? '', username: created?.ownerUsername ?? '', password: res.initialPassword })
    } else {
      await orgApi(target.kind).update(target.id, base)
      if (!current()) return
      dialog.visible = false
      ElMessage.success(t('common.success'))
    }
    await table.reload()
  } catch {
    // 统一提示已经弹过
  } finally {
    if (current()) dialog.saving = false
  }
}

// ---- 启停 ----
async function toggleStatus(o: OrgView) {
  const next = o.status === 1 ? 0 : 1
  const msg = next === 1 ? t('partner.confirmEnable', { name: o.name }) : t(`partner.${props.kind}.confirmDisable`, { name: o.name })
  try {
    await ElMessageBox.confirm(msg, t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await api.setStatus(o.id, next)
  ElMessage.success(t('common.success'))
  await table.reload()
}

// ---- 改归属（只对商户） ----
const transfer = reactive({ visible: false, id: 0, name: '', agentId: 0, saving: false })
function openTransfer(o: OrgView) {
  // 当前的代理商不在已加载的选项里时补上，选择框才显示名字而不是 ID。它停用了也让它可选：选回原值服务端什么都不做
  if (o.agentId && !agents.value.some((a) => a.id === o.agentId)) {
    agents.value = [{ id: o.agentId, code: o.agentCode ?? '', name: o.agentName ?? '', status: 1 }, ...agents.value]
  }
  Object.assign(transfer, { visible: true, id: o.id, name: o.name, agentId: o.agentId, saving: false })
}
async function saveTransfer() {
  transfer.saving = true
  try {
    await merchantApi.transfer(transfer.id, transfer.agentId || 0)
    transfer.visible = false
    ElMessage.success(t('common.success'))
    await table.reload()
  } catch {
    // 已提示
  } finally {
    transfer.saving = false
  }
}

// ---- 详情 ----
const detail = reactive({ visible: false, id: 0 })
function openDetail(o: OrgView) {
  detail.id = o.id
  detail.visible = true
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('common.keyword')">
          <el-input v-model="table.query.keyword" clearable :placeholder="t('partner.keywordPlaceholder')" style="width: 220px" @keyup.enter="table.search()" />
        </el-form-item>
        <el-form-item v-if="isMerchant" :label="t('partner.belongsTo')">
          <el-select
            v-model="table.query.agentId"
            clearable
            filterable
            remote
            :remote-method="searchAgents"
            :loading="agentsLoading"
            :placeholder="t('common.all')"
            style="width: 200px"
            data-test="org-filter-agent"
          >
            <el-option :value="0" :label="t('partner.direct')" />
            <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="agentLabel(a)" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('common.status')">
          <el-select v-model="table.query.status" clearable :placeholder="t('common.all')" style="width: 120px">
            <el-option :value="1" :label="t('common.enabled')" />
            <el-option :value="0" :label="t('common.disabled')" />
          </el-select>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" native-type="submit">{{ t('common.search') }}</el-button>
          <el-button @click="table.reset()">{{ t('common.reset') }}</el-button>
        </el-form-item>
        <div class="ga-toolbar__spacer" />
        <el-button v-perm="perms.create" type="primary" data-test="org-create" @click="openCreate">{{ t(`partner.${kind}.create`) }}</el-button>
      </el-form>
    </el-card>

    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="org-table">
        <el-table-column :label="t('partner.code')" width="130">
          <template #default="{ row }"><span class="ga-mono">{{ (row as OrgView).code }}</span></template>
        </el-table-column>
        <el-table-column prop="name" :label="t('partner.name')" min-width="160" show-overflow-tooltip />
        <el-table-column v-if="isMerchant" :label="t('partner.belongsTo')" min-width="170" show-overflow-tooltip>
          <template #default="{ row }">
            <span :class="{ 'ga-org__direct': !(row as OrgView).agentId }">{{ belongsTo(row as OrgView) }}</span>
          </template>
        </el-table-column>
        <el-table-column :label="t('partner.contactName')" min-width="150">
          <template #default="{ row }">
            {{ (row as OrgView).contactName || '-' }}
            <span v-if="(row as OrgView).contactPhone" class="ga-org__phone">{{ (row as OrgView).contactPhone }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="ownerUsername" :label="t('partner.owner')" min-width="110" />
        <el-table-column :label="t('common.status')" width="90">
          <template #default="{ row }">
            <el-tag :type="(row as OrgView).status === 1 ? 'success' : 'info'" size="small">{{ (row as OrgView).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.createdAt')" min-width="160">
          <template #default="{ row }">{{ formatTime((row as OrgView).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" :width="isMerchant ? 260 : 200" fixed="right">
          <template #default="{ row }">
            <span :data-test="`org-actions-${(row as OrgView).code}`">
              <el-button link type="primary" data-test="org-detail-open" @click="openDetail(row as OrgView)">{{ t('common.detail') }}</el-button>
              <el-button v-perm="perms.update" link type="primary" @click="openEdit(row as OrgView)">{{ t('common.edit') }}</el-button>
              <el-button v-if="isMerchant" v-perm="perms.transfer" link type="primary" data-test="org-transfer" @click="openTransfer(row as OrgView)">{{ t('partner.transfer') }}</el-button>
              <el-button
                v-perm="perms.status"
                link
                :type="(row as OrgView).status === 1 ? 'danger' : 'success'"
                data-test="org-toggle"
                @click="toggleStatus(row as OrgView)"
              >
                {{ (row as OrgView).status === 1 ? t('common.disable') : t('common.enable') }}
              </el-button>
            </span>
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

    <el-dialog v-model="dialog.visible" :title="dialog.mode === 'create' ? t(`partner.${kind}.create`) : t(`partner.${kind}.edit`)" width="540px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="name" :label="t('partner.name')">
          <el-input v-model="form.name" maxlength="64" data-test="org-form-name" />
        </el-form-item>
        <el-form-item prop="contactName" :label="t('partner.contactName')">
          <el-input v-model="form.contactName" maxlength="64" />
        </el-form-item>
        <el-form-item prop="contactPhone" :label="t('partner.contactPhone')">
          <el-input v-model="form.contactPhone" maxlength="32" />
        </el-form-item>
        <el-form-item v-if="isMerchant && dialog.mode === 'create'" prop="agentId" :label="t('partner.belongsTo')">
          <el-select v-model="form.agentId" filterable remote :remote-method="searchAgents" :loading="agentsLoading" style="width: 100%" data-test="org-form-agent">
            <el-option :value="0" :label="t('partner.direct')" />
            <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="agentLabel(a)" :disabled="a.status !== 1" />
          </el-select>
        </el-form-item>
        <template v-if="dialog.mode === 'create'">
          <el-divider content-position="left">{{ t('partner.owner') }}</el-divider>
          <el-form-item prop="ownerUsername" :label="t('common.username')">
            <el-input v-model="form.ownerUsername" maxlength="64" data-test="org-form-owner" />
          </el-form-item>
          <el-form-item prop="ownerDisplayName" :label="t('common.displayName')">
            <el-input v-model="form.ownerDisplayName" maxlength="64" />
            <div class="ga-form-hint">{{ t(`partner.${kind}.ownerHint`) }}</div>
          </el-form-item>
          <el-divider />
        </template>
        <el-form-item prop="sort" :label="t('common.sort')">
          <el-input-number v-model="form.sort" :min="0" :max="1000000" />
        </el-form-item>
        <el-form-item prop="remark" :label="t('common.remark')">
          <el-input v-model="form.remark" type="textarea" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="org-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-if="isMerchant" v-model="transfer.visible" :title="t('partner.transferTitle', { name: transfer.name })" width="460px">
      <el-alert :title="t('partner.transferHint')" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
      <el-select v-model="transfer.agentId" filterable remote :remote-method="searchAgents" :loading="agentsLoading" style="width: 100%" data-test="org-transfer-agent">
        <el-option :value="0" :label="t('partner.direct')" />
        <el-option v-for="a in agents" :key="a.id" :value="a.id" :label="agentLabel(a)" :disabled="a.status !== 1" />
      </el-select>
      <template #footer>
        <el-button @click="transfer.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="transfer.saving" data-test="org-transfer-save" @click="saveTransfer">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <OwnerPasswordDialog v-model="reveal.visible" :title="t(`partner.${kind}.created`)" :code="reveal.code" :username="reveal.username" :password="reveal.password" />
    <OrgDetailDrawer v-model="detail.visible" :kind="kind" :org-id="detail.id" @changed="table.reload()" />
  </div>
</template>

<style scoped>
.ga-org__phone {
  margin-left: 6px;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.ga-org__direct {
  color: var(--el-text-color-secondary);
}
</style>
