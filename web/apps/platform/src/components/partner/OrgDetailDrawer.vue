<script setup lang="ts">
// 代理商、商户的详情（D-065、D-066）：资料、账号（只读，可以把主账号换成别的启用账号）、在线会话（可以下线）、
// IP 白名单、IP 黑名单（主体自己在它的后台设，平台只能看和清空，D-102）。主体自己的账号、角色在它自己的程序里管。
import { computed, onBeforeUnmount, onDeactivated, reactive, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { formatTime, hasPerm, useAuthStore, useI18n } from '@ga/shell'

import { orgApi, orgPerms } from '../../api/partner'
import type { OrgAccount, OrgKind, OrgSession, OrgView } from '../../api/partner'
import type { IPRule } from '../../api/system'
import OwnerPasswordDialog from './OwnerPasswordDialog.vue'

const props = defineProps<{ modelValue: boolean; kind: OrgKind; orgId: number }>()
const emit = defineEmits<{ 'update:modelValue': [boolean]; changed: [] }>()
const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const api = computed(() => orgApi(props.kind))
const perms = computed(() => orgPerms(props.kind))

const org = ref<OrgView | null>(null)
const tab = ref('info')
const accounts = reactive({ list: [] as OrgAccount[], total: 0, page: 1, pageSize: 10, loading: false })
const sessions = reactive({ list: [] as OrgSession[], total: 0, page: 1, pageSize: 10, loading: false })
const ip = reactive({ items: [] as IPRule[], loading: false })
const deny = reactive({ list: [] as IPRule[], total: 0, page: 1, pageSize: 10, loading: false })
const expired = (r: IPRule) => r.expiresAt !== null && new Date(r.expiresAt).getTime() <= Date.now()

// 迟到的旧响应不能盖住新的（同 D-046 的做法）：每个列表各有一个序号，换了主体、重新打开、翻页、刷新都作废之前在途的
const seq = { org: 0, accounts: 0, sessions: 0, ip: 0, deny: 0 }
const reveal = reactive({ visible: false, password: '', code: '', username: '' })
const resetting = ref(false)
let resetSeq = 0
let actionSeq = 0
function invalidateReset() {
  actionSeq++
  resetSeq++
  resetting.value = false
  Object.assign(reveal, { visible: false, password: '', code: '', username: '' })
}
onBeforeUnmount(invalidateReset)
onDeactivated(invalidateReset)
watch(() => [auth.epoch, auth.locked], invalidateReset, { flush: 'sync' })
async function loadOrg() {
  const my = ++seq.org
  try {
    const v = await api.value.get(props.orgId)
    if (my === seq.org) org.value = v
  } catch {
    // 已提示；拿不到资料就关掉，不留一个空白的抽屉
    if (my === seq.org) emit('update:modelValue', false)
  }
}
async function loadAccounts() {
  const my = ++seq.accounts
  accounts.loading = true
  try {
    const res = await api.value.accounts(props.orgId, { page: accounts.page, pageSize: accounts.pageSize })
    if (my !== seq.accounts) return
    accounts.list = res.list ?? []
    accounts.total = res.total
  } catch {
    // 已提示
  } finally {
    if (my === seq.accounts) accounts.loading = false
  }
}
async function loadSessions() {
  const my = ++seq.sessions
  sessions.loading = true
  try {
    const res = await api.value.sessions(props.orgId, { page: sessions.page, pageSize: sessions.pageSize })
    if (my !== seq.sessions) return
    sessions.list = res.list ?? []
    sessions.total = res.total
  } catch {
    // 已提示
  } finally {
    if (my === seq.sessions) sessions.loading = false
  }
}
async function loadIP() {
  const my = ++seq.ip
  ip.loading = true
  try {
    const res = await api.value.ipAllow(props.orgId)
    if (my === seq.ip) ip.items = res.items ?? []
  } catch {
    // 已提示
  } finally {
    if (my === seq.ip) ip.loading = false
  }
}
async function loadDeny() {
  const my = ++seq.deny
  deny.loading = true
  try {
    const res = await api.value.ipDeny(props.orgId, { page: deny.page, pageSize: deny.pageSize })
    if (my !== seq.deny) return
    deny.list = res.list ?? []
    deny.total = res.total
  } catch {
    // 已提示
  } finally {
    if (my === seq.deny) deny.loading = false
  }
}

function loadTab(name: string) {
  if (name === 'accounts') void loadAccounts()
  else if (name === 'sessions') void loadSessions()
  else if (name === 'ip') void loadIP()
  else if (name === 'ipDeny') void loadDeny()
}

watch(
  () => [props.modelValue, props.orgId, props.kind] as const,
  ([open]) => {
    invalidateReset()
    seq.org++
    // 作废上一个主体在途的请求
    seq.accounts++
    seq.sessions++
    seq.ip++
    seq.deny++
    org.value = null
    tab.value = 'info'
    accounts.page = 1
    sessions.page = 1
    deny.page = 1
    Object.assign(accounts, { list: [], total: 0, loading: false })
    Object.assign(sessions, { list: [], total: 0, loading: false })
    Object.assign(ip, { items: [], loading: false })
    Object.assign(deny, { list: [], total: 0, loading: false })
    if (!open) return
    void loadOrg()
  },
  { immediate: true, flush: 'sync' },
)

// ---- 主账号 ----
// 关掉就清空（D-098）
watch(() => reveal.visible, (open) => {
  if (!open) reveal.password = ''
})
async function resetOwner() {
  if (!props.modelValue || !org.value || resetting.value) return
  const target = { id: org.value.id, name: org.value.name, code: org.value.code, username: org.value.ownerUsername, kind: props.kind, epoch: auth.epoch }
  const my = ++resetSeq
  const current = () => my === resetSeq && !auth.locked && auth.epoch === target.epoch && props.modelValue && props.kind === target.kind && props.orgId === target.id && org.value?.id === target.id
  resetting.value = true
  try {
    await ElMessageBox.confirm(t('partner.confirmResetOwner', { name: target.name }), t('common.confirmTitle'), { type: 'warning' })
    if (!current()) return
    const res = await orgApi(target.kind).resetOwnerPassword(target.id)
    if (!current()) return
    Object.assign(reveal, { password: res.initialPassword, code: target.code, username: target.username, visible: true })
    if (tab.value === 'sessions') void loadSessions()
  } catch {
    // 取消或请求失败：请求层已提示，密码保持空。
  } finally {
    if (my === resetSeq) resetting.value = false
  }
}

// 确认文字、提交目标和响应均绑定同一个抽屉身份（D-121）。
function beginAction() {
  if (!props.modelValue || !org.value || auth.locked) return null
  const target = { kind: props.kind, id: props.orgId, name: org.value.name, epoch: auth.epoch }
  const my = ++actionSeq
  return { ...target, current: () => my === actionSeq && !auth.locked && auth.epoch === target.epoch && props.modelValue && props.kind === target.kind && props.orgId === target.id && org.value?.id === target.id }
}

async function makeOwner(a: OrgAccount) {
  const target = beginAction()
  if (!target) return
  const accountId = a.id
  try {
    await ElMessageBox.confirm(t('partner.confirmMakeOwner', { username: a.username }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  if (!target.current()) return
  await orgApi(target.kind).changeOwner(target.id, accountId)
  if (!target.current()) return
  ElMessage.success(t('common.success'))
  await Promise.all([loadOrg(), loadAccounts()])
  emit('changed')
}

async function revoke(s: OrgSession) {
  const target = beginAction()
  if (!target) return
  const sid = s.sid
  try {
    await ElMessageBox.confirm(t('partner.confirmRevoke', { username: s.username }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  if (!target.current()) return
  await orgApi(target.kind).revokeSession(target.id, sid)
  if (!target.current()) return
  ElMessage.success(t('common.success'))
  await loadSessions()
}

async function clearIP() {
  const target = beginAction()
  if (!target) return
  try {
    await ElMessageBox.confirm(t('partner.confirmIpClear', { name: target.name }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  if (!target.current()) return
  await orgApi(target.kind).clearIPAllow(target.id)
  if (!target.current()) return
  ElMessage.success(t('common.success'))
  await loadIP()
}

async function clearDeny() {
  const target = beginAction()
  if (!target) return
  try {
    await ElMessageBox.confirm(t('partner.confirmIpDenyClear', { name: target.name }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  if (!target.current()) return
  await orgApi(target.kind).clearIPDeny(target.id)
  if (!target.current()) return
  ElMessage.success(t('common.success'))
  deny.page = 1
  await loadDeny()
}

function toLogs(path: 'login-logs' | 'operation-logs') {
  if (!org.value) return
  emit('update:modelValue', false)
  void router.push({ path: `/${props.kind}/${path}`, query: { orgCode: org.value.code } })
}

function agentLabel(o: OrgView) {
  return o.agentId ? `${o.agentName ?? ''} (${o.agentCode ?? o.agentId})` : t('partner.direct')
}
</script>

<template>
  <el-drawer :model-value="modelValue" size="760px" :title="org ? `${org.name} · ${org.code}` : ''" data-test="org-detail" @update:model-value="emit('update:modelValue', $event)">
    <template v-if="org">
      <div class="ga-org__bar">
        <el-tag :type="org.status === 1 ? 'success' : 'info'">{{ org.status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
        <div class="ga-toolbar__spacer" />
        <el-button v-if="hasPerm(perms.log)" @click="toLogs('login-logs')">{{ t('partner.loginLogs') }}</el-button>
        <el-button v-if="hasPerm(perms.log)" @click="toLogs('operation-logs')">{{ t('partner.operationLogs') }}</el-button>
        <el-button v-perm="perms.owner" type="warning" :loading="resetting" :disabled="resetting" data-test="org-reset-owner" @click="resetOwner">{{ t('partner.resetOwner') }}</el-button>
      </div>

      <el-tabs v-model="tab" @tab-change="(n: string | number) => loadTab(String(n))">
        <el-tab-pane :label="t('partner.tabs.info')" name="info">
          <el-descriptions :column="2" border>
            <el-descriptions-item :label="t('partner.code')"><span class="ga-mono">{{ org.code }}</span></el-descriptions-item>
            <el-descriptions-item :label="t('partner.name')">{{ org.name }}</el-descriptions-item>
            <el-descriptions-item :label="t('partner.contactName')">{{ org.contactName || '-' }}</el-descriptions-item>
            <el-descriptions-item :label="t('partner.contactPhone')">{{ org.contactPhone || '-' }}</el-descriptions-item>
            <el-descriptions-item v-if="kind === 'merchant'" :label="t('partner.belongsTo')" :span="2">{{ agentLabel(org) }}</el-descriptions-item>
            <el-descriptions-item :label="t('partner.owner')">{{ org.ownerUsername || '-' }}</el-descriptions-item>
            <el-descriptions-item :label="t('common.sort')">{{ org.sort }}</el-descriptions-item>
            <el-descriptions-item :label="t('common.createdAt')">{{ formatTime(org.createdAt) }}</el-descriptions-item>
            <el-descriptions-item :label="t('common.updatedAt')">{{ formatTime(org.updatedAt) }}</el-descriptions-item>
            <el-descriptions-item :label="t('common.remark')" :span="2"><span class="ga-org__remark">{{ org.remark || '-' }}</span></el-descriptions-item>
          </el-descriptions>
        </el-tab-pane>

        <el-tab-pane :label="t('partner.tabs.accounts')" name="accounts">
          <el-alert :title="t('partner.accountsHint')" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
          <el-table v-loading="accounts.loading" :data="accounts.list" stripe size="small" data-test="org-accounts">
            <el-table-column :label="t('common.username')" min-width="140">
              <template #default="{ row }">
                {{ (row as OrgAccount).username }}
                <el-tag v-if="(row as OrgAccount).owner" size="small" type="danger" style="margin-left: 6px">{{ t('partner.owner') }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column prop="displayName" :label="t('common.displayName')" min-width="110" />
            <el-table-column :label="t('common.status')" width="110">
              <template #default="{ row }">
                <el-tag :type="(row as OrgAccount).status === 1 ? 'success' : 'info'" size="small">{{ (row as OrgAccount).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
                <el-tag v-if="(row as OrgAccount).mustChangePwd" size="small" type="warning" style="margin-left: 4px">{{ t('partner.mustChange') }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('partner.lastLogin')" min-width="150">
              <template #default="{ row }">{{ formatTime((row as OrgAccount).lastLoginAt) }}</template>
            </el-table-column>
            <el-table-column :label="t('common.actions')" width="110">
              <template #default="{ row }">
                <el-button
                  v-if="!(row as OrgAccount).owner"
                  v-perm="perms.owner"
                  link
                  type="primary"
                  :disabled="(row as OrgAccount).status !== 1"
                  data-test="org-make-owner"
                  @click="makeOwner(row as OrgAccount)"
                >
                  {{ t('partner.makeOwner') }}
                </el-button>
              </template>
            </el-table-column>
          </el-table>
          <div class="ga-pagination">
            <el-pagination
              v-model:current-page="accounts.page"
              :page-size="accounts.pageSize"
              :total="accounts.total"
              layout="total, prev, pager, next"
              @current-change="loadAccounts"
            />
          </div>
        </el-tab-pane>

        <el-tab-pane :label="t('partner.tabs.sessions')" name="sessions">
          <el-table v-loading="sessions.loading" :data="sessions.list" stripe size="small" :empty-text="t('partner.noSessions')" data-test="org-sessions">
            <el-table-column prop="username" :label="t('common.username')" min-width="110" />
            <el-table-column label="IP" min-width="120">
              <template #default="{ row }"><span class="ga-mono">{{ (row as OrgSession).ip }}</span></template>
            </el-table-column>
            <el-table-column prop="userAgent" label="User-Agent" min-width="180" show-overflow-tooltip />
            <el-table-column :label="t('partner.lastSeen')" min-width="150">
              <template #default="{ row }">
                {{ formatTime((row as OrgSession).lastSeenAt) }}
                <el-tag v-if="(row as OrgSession).locked" size="small" type="warning" style="margin-left: 4px">{{ t('partner.locked') }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column :label="t('common.actions')" width="80">
              <template #default="{ row }">
                <el-button v-perm="perms.status" link type="danger" data-test="org-revoke" @click="revoke(row as OrgSession)">{{ t('partner.revoke') }}</el-button>
              </template>
            </el-table-column>
          </el-table>
          <div class="ga-pagination">
            <el-pagination
              v-model:current-page="sessions.page"
              :page-size="sessions.pageSize"
              :total="sessions.total"
              layout="total, prev, pager, next"
              @current-change="loadSessions"
            />
          </div>
        </el-tab-pane>

        <el-tab-pane :label="t('partner.tabs.ip')" name="ip">
          <el-alert :title="t(`partner.${kind}.ipHint`)" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
          <el-table v-loading="ip.loading" :data="ip.items" size="small" :empty-text="t('partner.ipEmpty')" data-test="org-ip">
            <el-table-column :label="t('partner.ipCidr')" min-width="160">
              <template #default="{ row }"><span class="ga-mono">{{ (row as IPRule).cidr }}</span></template>
            </el-table-column>
            <el-table-column prop="remark" :label="t('common.remark')" min-width="160" />
            <el-table-column :label="t('common.updatedAt')" min-width="150">
              <template #default="{ row }">{{ formatTime((row as IPRule).updatedAt) }}</template>
            </el-table-column>
          </el-table>
          <div v-if="ip.items.length" class="ga-org__ip-actions">
            <el-button v-perm="perms.owner" type="danger" plain data-test="org-ip-clear" @click="clearIP">{{ t('partner.ipClear') }}</el-button>
          </div>
        </el-tab-pane>

        <el-tab-pane :label="t('partner.tabs.ipDeny')" name="ipDeny">
          <el-alert :title="t(`partner.${kind}.ipDenyHint`)" type="info" :closable="false" show-icon style="margin-bottom: 12px" />
          <el-table v-loading="deny.loading" :data="deny.list" size="small" :empty-text="t('partner.ipDenyEmpty')" data-test="org-ip-deny">
            <el-table-column :label="t('partner.ipCidr')" min-width="150">
              <template #default="{ row }"><span class="ga-mono">{{ (row as IPRule).cidr }}</span></template>
            </el-table-column>
            <el-table-column :label="t('ipacl.until')" min-width="170">
              <template #default="{ row }">
                <el-tag v-if="(row as IPRule).expiresAt === null" type="danger" size="small">{{ t('ipacl.forever') }}</el-tag>
                <span v-else>
                  {{ formatTime((row as IPRule).expiresAt) }}
                  <el-tag v-if="expired(row as IPRule)" type="info" size="small" style="margin-left: 4px">{{ t('ipacl.expired') }}</el-tag>
                </span>
              </template>
            </el-table-column>
            <el-table-column prop="remark" :label="t('common.remark')" min-width="140" />
            <el-table-column :label="t('common.updatedAt')" min-width="150">
              <template #default="{ row }">{{ formatTime((row as IPRule).updatedAt) }}</template>
            </el-table-column>
          </el-table>
          <div class="ga-pagination">
            <el-pagination
              v-model:current-page="deny.page"
              :page-size="deny.pageSize"
              :total="deny.total"
              layout="total, prev, pager, next"
              @current-change="loadDeny"
            />
          </div>
          <div v-if="deny.total" class="ga-org__ip-actions">
            <el-button v-perm="perms.owner" type="danger" plain data-test="org-ip-deny-clear" @click="clearDeny">{{ t('partner.ipDenyClear') }}</el-button>
          </div>
        </el-tab-pane>
      </el-tabs>
    </template>

    <OwnerPasswordDialog v-model="reveal.visible" :title="t('partner.resetDone')" :code="reveal.code" :username="reveal.username" :password="reveal.password" />
  </el-drawer>
</template>

<style scoped>
.ga-org__bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 12px;
}
.ga-org__remark {
  white-space: pre-wrap;
}
.ga-org__ip-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 12px;
}
</style>
