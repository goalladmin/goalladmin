<script setup lang="ts">
// 字典管理（docs/decisions.md D-023）。
// 左边是字典列表，右边是选中字典的项（树形）。代码声明的字典只读：不能改字典本身、不能加项删项改值；
// 能改项的显示文字、颜色、扩展值、排序和启用状态，改过的项可以恢复成代码里的默认值。
import { computed, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { GaI18nInputs, dictTagProps, formatTime, hasPerm, invalidateDicts, useI18n, useLanguages, useTable } from '@ga/shell'
import type { LocaleCode } from '@ga/shell'

import { dictApi } from '../../../api/system'
import type { DictDetail, DictInfo, DictInput, DictItemInfo, DictItemInput } from '../../../api/system'

const { t, locale } = useI18n()
const DEFAULT_LANG: LocaleCode = 'zh-CN'
const COLORS = ['primary', 'success', 'warning', 'danger', 'info']

// ---- 字典列表 ----
const table = useTable<{ keyword: string; source: string | undefined }, DictInfo>({
  query: { keyword: '', source: undefined },
  fetch: (p) => dictApi.list(p),
  pageSize: 20,
})

const portals = ref<string[]>([])
void dictApi.portals().then((ps) => (portals.value = ps))

// ---- 选中的字典 ----
const current = ref<DictDetail | null>(null)
const detailLoading = ref(false)
const isCode = computed(() => current.value?.source === 'code')

async function select(d: DictInfo | null) {
  if (!d) return
  detailLoading.value = true
  try {
    current.value = await dictApi.get(d.id)
  } finally {
    detailLoading.value = false
  }
}

async function reloadCurrent() {
  if (!current.value) return
  invalidateDicts([current.value.code])
  current.value = await dictApi.get(current.value.id)
}

// ---- 新建 / 编辑字典（只对后台字典） ----
const dictDialog = reactive({ visible: false, id: 0, saving: false })
const dictForm = reactive({ code: '', name: '', nameI18n: {} as Record<string, string>, portal: '*', valueType: 'string' as 'string' | 'int', status: 1, sort: 0, remark: '' })
const dictFormRef = ref<FormInstance>()
const dictRules: FormRules = {
  code: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { pattern: /^[a-z][a-z0-9_]*$/, message: () => t('sys.dict.codeRule'), trigger: 'blur' },
  ],
  name: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
}

function openCreateDict() {
  dictDialog.id = 0
  Object.assign(dictForm, { code: '', name: '', nameI18n: {}, portal: '*', valueType: 'string', status: 1, sort: 0, remark: '' })
  dictDialog.visible = true
}

function openEditDict() {
  const d = current.value
  if (!d) return
  dictDialog.id = d.id
  Object.assign(dictForm, { code: d.code, name: d.name, nameI18n: { ...d.nameI18n }, portal: d.portal, valueType: d.valueType, status: d.status, sort: d.sort, remark: d.remark })
  dictDialog.visible = true
}

// 默认语言写在主字段里；其他语言只列端启用的，没启用的语言原样保留（D-026）
function i18nOf(edited: Record<string, string>, original: Record<string, string> = {}): Record<string, string> {
  const enabled = new Set<string>(useLanguages().map((l) => l.code))
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(original)) if (!enabled.has(k) && k !== DEFAULT_LANG) out[k] = v
  for (const [k, v] of Object.entries(edited)) if (k !== DEFAULT_LANG && v.trim()) out[k] = v.trim()
  return out
}

async function submitDict() {
  if (!dictFormRef.value) return
  try {
    await dictFormRef.value.validate()
  } catch {
    return
  }
  dictDialog.saving = true
  const input: DictInput = { name: dictForm.name, nameI18n: i18nOf(dictForm.nameI18n, dictDialog.id ? current.value?.nameI18n : {}), portal: dictForm.portal, status: dictForm.status, sort: dictForm.sort, remark: dictForm.remark }
  try {
    if (dictDialog.id === 0) {
      const d = await dictApi.create({ ...input, code: dictForm.code, valueType: dictForm.valueType })
      await table.reload()
      await select(d)
    } else {
      await dictApi.update(dictDialog.id, input)
      await table.reload()
      await reloadCurrent()
    }
    dictDialog.visible = false
    ElMessage.success(t('common.success'))
  } catch {
    // 已提示
  } finally {
    dictDialog.saving = false
  }
}

async function removeDict() {
  const d = current.value
  if (!d) return
  try {
    await ElMessageBox.confirm(t('sys.dict.confirmDeleteDict', { name: d.name }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await dictApi.remove(d.id)
  invalidateDicts([d.code])
  current.value = null
  ElMessage.success(t('common.success'))
  await table.reload()
}

// ---- 新建 / 编辑项 ----
const itemDialog = reactive({ visible: false, id: 0, parentId: 0, locked: false, saving: false })
// 代码字典里已不在声明中的遗留项：值不能改，也不能重新启用（D-058）
const legacyItem = computed(() => isCode.value && itemDialog.id !== 0 && !itemDialog.locked)
const itemOriginal = ref<Record<string, string>>({})
const itemForm = reactive({ value: '', label: '', labelI18n: {} as Record<string, string>, color: '', extra: '', status: 1, sort: 0, remark: '' })
const itemFormRef = ref<FormInstance>()
const itemRules: FormRules = {
  value: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
  label: [{ required: true, message: () => t('common.required'), trigger: 'blur' }],
}

function openCreateItem(parent?: DictItemInfo) {
  Object.assign(itemDialog, { id: 0, parentId: parent?.id ?? 0, locked: false })
  itemOriginal.value = {}
  Object.assign(itemForm, { value: '', label: '', labelI18n: {}, color: '', extra: '', status: 1, sort: 0, remark: '' })
  itemDialog.visible = true
}

function openEditItem(it: DictItemInfo) {
  Object.assign(itemDialog, { id: it.id, parentId: it.parentId, locked: it.locked })
  itemOriginal.value = { ...it.labelI18n }
  Object.assign(itemForm, { value: it.value, label: it.label, labelI18n: { ...it.labelI18n }, color: it.color, extra: it.extra, status: it.status, sort: it.sort, remark: it.remark })
  itemDialog.visible = true
}

async function submitItem() {
  const d = current.value
  if (!d || !itemFormRef.value) return
  try {
    await itemFormRef.value.validate()
  } catch {
    return
  }
  itemDialog.saving = true
  const input: DictItemInput = {
    value: itemForm.value.trim(), label: itemForm.label, labelI18n: i18nOf(itemForm.labelI18n, itemOriginal.value), color: itemForm.color,
    extra: itemForm.extra, status: itemForm.status, sort: itemForm.sort, remark: itemForm.remark,
  }
  try {
    if (itemDialog.id === 0) await dictApi.createItem(d.id, { ...input, parentId: itemDialog.parentId })
    else await dictApi.updateItem(d.id, itemDialog.id, input)
    itemDialog.visible = false
    ElMessage.success(t('common.success'))
    await reloadCurrent()
  } catch {
    // 已提示
  } finally {
    itemDialog.saving = false
  }
}

async function removeItem(it: DictItemInfo) {
  const d = current.value
  if (!d) return
  try {
    await ElMessageBox.confirm(t('sys.dict.confirmDeleteItem', { name: it.label }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await dictApi.removeItem(d.id, it.id)
  ElMessage.success(t('common.success'))
  await reloadCurrent()
}

async function resetItem(it: DictItemInfo) {
  const d = current.value
  if (!d) return
  try {
    await ElMessageBox.confirm(t('sys.dict.confirmReset', { name: it.label }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await dictApi.resetItem(d.id, it.id)
  ElMessage.success(t('common.success'))
  await reloadCurrent()
}

const canUpdate = computed(() => hasPerm('system:dict:update'))
const portalLabel = (p: string) => (p === '*' ? t('sys.dict.allPortals') : p)
</script>

<template>
  <div class="ga-page ga-dict">
    <el-card class="ga-dict__list">
      <div class="ga-dict__bar">
        <el-input v-model="table.query.keyword" clearable :placeholder="t('sys.dict.keywordPlaceholder')" @keyup.enter="table.search()" @clear="table.search()" />
        <el-select v-model="table.query.source" clearable :placeholder="t('sys.dict.source')" style="width: 96px; flex: none" @change="table.search()">
          <el-option value="code" :label="t('sys.dict.sourceCode')" />
          <el-option value="admin" :label="t('sys.dict.sourceAdmin')" />
        </el-select>
        <el-button v-perm="'system:dict:create'" type="primary" data-test="dict-create" @click="openCreateDict">{{ t('common.create') }}</el-button>
      </div>
      <el-table v-loading="table.loading" :data="table.list" highlight-current-row data-test="dict-table" @current-change="select">
        <el-table-column :label="t('sys.dict.name')" min-width="150">
          <template #default="{ row }">
            <div class="ga-dict__name">{{ (row as DictInfo).name }}</div>
            <div class="ga-dict__code">{{ (row as DictInfo).code }}</div>
          </template>
        </el-table-column>
        <el-table-column :label="t('sys.dict.source')" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="(row as DictInfo).source === 'code' ? 'primary' : 'info'">
              {{ (row as DictInfo).source === 'code' ? t('sys.dict.sourceCode') : t('sys.dict.sourceAdmin') }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('common.status')" min-width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="(row as DictInfo).status === 1 ? 'success' : 'info'">{{ (row as DictInfo).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
      <div class="ga-pagination">
        <el-pagination
          v-model:current-page="table.page"
          v-model:page-size="table.pageSize"
          :total="table.total"
          small
          layout="total, prev, pager, next"
          @current-change="table.onPageChange"
        />
      </div>
    </el-card>

    <el-card v-loading="detailLoading" class="ga-dict__detail">
      <el-empty v-if="!current" :description="t('sys.dict.pickHint')" />
      <template v-else>
        <div class="ga-dict__head">
          <div>
            <div class="ga-dict__title">
              {{ current.name }}
              <el-tag size="small" :type="isCode ? 'primary' : 'info'">{{ isCode ? t('sys.dict.sourceCode') : t('sys.dict.sourceAdmin') }}</el-tag>
              <el-tag size="small" type="info">{{ current.valueType }}</el-tag>
            </div>
            <div class="ga-dict__meta">
              <code>{{ current.code }}</code> · {{ t('sys.dict.portal') }}：{{ portalLabel(current.portal) }} · {{ t('common.updatedAt') }}：{{ formatTime(current.updatedAt) }}
            </div>
            <div v-if="isCode" class="ga-dict__hint">{{ t('sys.dict.codeHint') }}</div>
          </div>
          <div v-if="!isCode" class="ga-dict__actions">
            <el-button v-perm="'system:dict:update'" @click="openEditDict">{{ t('common.edit') }}</el-button>
            <el-button v-perm="'system:dict:delete'" type="danger" plain @click="removeDict">{{ t('common.delete') }}</el-button>
            <el-button v-perm="'system:dict:update'" type="primary" data-test="dict-item-create" @click="openCreateItem()">{{ t('sys.dict.addItem') }}</el-button>
          </div>
        </div>
        <el-table :data="current.items" row-key="id" default-expand-all data-test="dict-item-table">
          <el-table-column :label="t('sys.dict.label')" min-width="160">
            <template #default="{ row }">
              <el-tag v-bind="dictTagProps((row as DictItemInfo).color)" size="small" disable-transitions>{{ (row as DictItemInfo).label }}</el-tag>
              <el-tag v-if="(row as DictItemInfo).overridden" size="small" type="warning" effect="plain" class="ga-dict__flag">{{ t('sys.dict.overridden') }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column v-if="locale !== DEFAULT_LANG" :label="t('sys.dict.labelCurrent')" min-width="120">
            <template #default="{ row }">{{ (row as DictItemInfo).labelI18n[locale] ?? '' }}</template>
          </el-table-column>
          <el-table-column prop="value" :label="t('sys.dict.value')" min-width="100" />
          <el-table-column prop="extra" :label="t('sys.dict.extra')" min-width="100" show-overflow-tooltip />
          <el-table-column :label="t('common.status')" min-width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="(row as DictItemInfo).status === 1 ? 'success' : 'info'">{{ (row as DictItemInfo).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="sort" :label="t('common.sort')" min-width="120" />
          <el-table-column v-if="canUpdate" :label="t('common.actions')" width="200" fixed="right">
            <template #default="{ row }">
              <el-button v-if="!isCode" link type="primary" @click="openCreateItem(row as DictItemInfo)">{{ t('sys.dict.addChild') }}</el-button>
              <el-button link type="primary" @click="openEditItem(row as DictItemInfo)">{{ t('common.edit') }}</el-button>
              <el-button v-if="(row as DictItemInfo).locked && (row as DictItemInfo).overridden" link type="warning" @click="resetItem(row as DictItemInfo)">{{ t('sys.dict.reset') }}</el-button>
              <el-button v-if="!(row as DictItemInfo).locked" link type="danger" @click="removeItem(row as DictItemInfo)">{{ t('common.delete') }}</el-button>
            </template>
          </el-table-column>
        </el-table>
      </template>
    </el-card>

    <el-dialog v-model="dictDialog.visible" :title="dictDialog.id === 0 ? t('sys.dict.createTitle') : t('sys.dict.editTitle')" width="600px" destroy-on-close>
      <el-form ref="dictFormRef" :model="dictForm" :rules="dictRules" label-width="auto" @submit.prevent="submitDict">
        <el-form-item prop="code" :label="t('sys.dict.code')">
          <el-input v-model="dictForm.code" :disabled="dictDialog.id !== 0" maxlength="64" data-test="dict-form-code" />
          <div class="ga-form-hint">{{ t('sys.dict.codeRule') }}</div>
        </el-form-item>
        <el-form-item prop="name" :label="t('sys.dict.name')">
          <el-input v-model="dictForm.name" maxlength="64" data-test="dict-form-name" />
        </el-form-item>
        <el-form-item :label="t('sys.dict.translations')">
          <GaI18nInputs v-model="dictForm.nameI18n" :exclude="[DEFAULT_LANG]" :maxlength="64" test-id="dict-form-name" />
        </el-form-item>
        <el-form-item :label="t('sys.dict.portal')">
          <el-select v-model="dictForm.portal" style="width: 100%">
            <el-option value="*" :label="t('sys.dict.allPortals')" />
            <el-option v-for="p in portals" :key="p" :value="p" :label="p" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.dict.valueType')">
          <el-radio-group v-model="dictForm.valueType" :disabled="dictDialog.id !== 0">
            <el-radio value="string">{{ t('sys.dict.typeString') }}</el-radio>
            <el-radio value="int">{{ t('sys.dict.typeInt') }}</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item :label="t('common.status')">
          <el-switch v-model="dictForm.status" :active-value="1" :inactive-value="0" />
        </el-form-item>
        <el-form-item :label="t('common.sort')">
          <el-input-number v-model="dictForm.sort" :min="0" />
        </el-form-item>
        <el-form-item :label="t('common.remark')">
          <el-input v-model="dictForm.remark" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dictDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dictDialog.saving" data-test="dict-form-submit" @click="submitDict">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>

    <el-dialog v-model="itemDialog.visible" :title="itemDialog.id === 0 ? t('sys.dict.addItem') : t('sys.dict.editItem')" width="600px" destroy-on-close>
      <el-form ref="itemFormRef" :model="itemForm" :rules="itemRules" label-width="auto" @submit.prevent="submitItem">
        <el-form-item prop="value" :label="t('sys.dict.value')">
          <el-input v-model="itemForm.value" :disabled="itemDialog.locked || legacyItem" maxlength="64" data-test="dict-item-value" />
          <div v-if="itemDialog.locked" class="ga-form-hint">{{ t('sys.dict.lockedHint') }}</div>
        </el-form-item>
        <el-form-item prop="label" :label="t('sys.dict.label')">
          <el-input v-model="itemForm.label" maxlength="128" data-test="dict-item-label" />
        </el-form-item>
        <el-form-item :label="t('sys.dict.translations')">
          <GaI18nInputs v-model="itemForm.labelI18n" :exclude="[DEFAULT_LANG]" :maxlength="128" test-id="dict-item-label" />
        </el-form-item>
        <el-form-item :label="t('sys.dict.color')">
          <el-select v-model="itemForm.color" clearable filterable allow-create :placeholder="t('sys.dict.colorHint')" style="width: 100%">
            <el-option v-for="c in COLORS" :key="c" :value="c" :label="c">
              <el-tag :type="c as 'primary'" size="small">{{ c }}</el-tag>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.dict.extra')">
          <el-input v-model="itemForm.extra" maxlength="255" />
        </el-form-item>
        <el-form-item :label="t('common.status')">
          <el-switch v-model="itemForm.status" :active-value="1" :inactive-value="0" :disabled="legacyItem" />
          <div v-if="legacyItem" class="ga-form-hint">{{ t('err.dict.legacyItem') }}</div>
        </el-form-item>
        <el-form-item :label="t('common.sort')">
          <el-input-number v-model="itemForm.sort" :min="0" />
        </el-form-item>
        <el-form-item :label="t('common.remark')">
          <el-input v-model="itemForm.remark" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="itemDialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="itemDialog.saving" data-test="dict-item-submit" @click="submitItem">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-dict {
  display: grid;
  grid-template-columns: minmax(300px, 380px) 1fr;
  gap: 16px;
  align-items: start;
}
@media (max-width: 900px) {
  .ga-dict {
    grid-template-columns: 1fr;
  }
}
.ga-dict__bar {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}
.ga-dict__name {
  font-weight: 500;
}
.ga-dict__code {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  font-family: var(--el-font-family-mono, monospace);
}
.ga-dict__head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.ga-dict__title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 16px;
  font-weight: 600;
}
.ga-dict__meta {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.ga-dict__hint {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-color-warning);
}
.ga-dict__flag {
  margin-left: 6px;
}
</style>
