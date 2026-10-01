<script setup lang="ts">
// 菜单管理（docs/decisions.md D-025）。
// 菜单的路径、页面组件和权限码由代码决定，这里只能改显示名、图标、隐藏、排序值，以及拖动调整位置和顺序；
// 后台可以建分组（纯目录）来归类。所有规则由后端校验，这个页面只是把操作提交上去。
import { computed, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { AllowDropFunction, FormInstance, FormRules } from 'element-plus'
import { GaI18nInputs, hasPerm, iconNames, menuTitle, resolveIcon, useAuthStore, useI18n, useLanguages, usePortal } from '@ga/shell'
import type { LocaleCode } from '@ga/shell'

import { menuApi } from '../../../api/system'
import type { MenuAdminNode, MenuPosition } from '../../../api/system'

interface TreeNode extends MenuAdminNode {
  children: TreeNode[]
}

const { t, locale } = useI18n()
const DEFAULT_LANG: LocaleCode = 'zh-CN'
// 与后端 rbac.MaxMenuSort 一致
const MAX_SORT = 1000000
const icons = iconNames()

const loading = ref(false)
const saving = ref(false)
const dirty = ref(false)
const tree = ref<TreeNode[]>([])
const flat = ref<MenuAdminNode[]>([])
const canUpdate = computed(() => hasPerm('system:menu:update'))

const titleOf = (n: MenuAdminNode) => menuTitle(t, locale.value, n.titleKey, n.titles) || n.name

function buildTree(list: MenuAdminNode[]): TreeNode[] {
  const byName = new Map<string, TreeNode>()
  for (const n of list) byName.set(n.name, { ...n, children: [] })
  const roots: TreeNode[] = []
  for (const n of byName.values()) {
    const p = n.parent ? byName.get(n.parent) : undefined
    if (p) p.children.push(n)
    else roots.push(n)
  }
  const sortRec = (nodes: TreeNode[]) => {
    nodes.sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name))
    nodes.forEach((c) => sortRec(c.children))
  }
  sortRec(roots)
  return roots
}

async function load() {
  loading.value = true
  try {
    flat.value = await menuApi.list()
    tree.value = buildTree(flat.value)
    dirty.value = false
  } finally {
    loading.value = false
  }
}
void load()

// 改完之后刷新侧边栏：重新拉 /auth/me 并重建菜单路由
async function refreshSidebar() {
  await useAuthStore().fetchMe()
  usePortal().installMenuRoutes?.()
}

async function afterChange() {
  ElMessage.success(t('common.success'))
  await Promise.all([load(), refreshSidebar()])
}

// ---- 拖动 ----
// 和后端同一条规则（D-025）：上级必须是目录或分组（代码里的上级总是可以），且上级要求的权限码拖动的节点都要求，
// 否则挪进去会改变谁能看到它。层数、成环由后端整体校验，这里只挡掉明显放不进去的位置。
function accepts(parent: TreeNode | null, child: TreeNode): boolean {
  if (!parent) return true
  if (parent.name === child.name) return false
  if (child.kind !== 'group' && child.default?.parent === parent.name) return true
  return parent.kind !== 'page' && parent.need.every((p) => child.need.includes(p))
}
const allowDrop: AllowDropFunction = (dragging, drop, type) => {
  const child = dragging.data as TreeNode
  if (type === 'inner') return accepts(drop.data as TreeNode, child)
  const parent = drop.parent && drop.parent.level > 0 ? (drop.parent.data as TreeNode) : null
  return accepts(parent, child)
}
const allowDrag = () => canUpdate.value

function onDrop() {
  dirty.value = true
}

// 收集拖动后的位置。没被拖动改过的层（成员和顺序都没变）原样保留排序值；改过的层如果原排序值仍然严格递增也保留，
// 否则按 10、20、30 重排。这样只有真正动过的菜单才会产生调整，没碰过的菜单以后仍跟着代码走。
function collectPositions(): MenuPosition[] {
  const original = new Map<string, string[]>()
  for (const n of [...flat.value].sort((a, b) => a.sort - b.sort || a.name.localeCompare(b.name))) {
    const list = original.get(n.parent) ?? []
    list.push(n.name)
    original.set(n.parent, list)
  }
  const out: MenuPosition[] = []
  const walk = (nodes: TreeNode[], parent: string) => {
    const before = original.get(parent) ?? []
    const unchanged = before.length === nodes.length && nodes.every((n, i) => before[i] === n.name)
    const increasing = nodes.every((n, i) => i === 0 || n.sort > nodes[i - 1]!.sort)
    nodes.forEach((n, i) => {
      out.push({ name: n.name, parent, sort: unchanged || increasing ? n.sort : (i + 1) * 10 })
      walk(n.children, n.name)
    })
  }
  walk(tree.value, '')
  return out
}

async function saveLayout() {
  saving.value = true
  try {
    await menuApi.saveLayout(collectPositions())
    await afterChange()
  } catch {
    // 已提示；位置保持在拖动后的样子，可以继续调整或撤销
  } finally {
    saving.value = false
  }
}

function undo() {
  tree.value = buildTree(flat.value)
  dirty.value = false
}

onBeforeRouteLeave(async () => {
  if (!dirty.value) return true
  try {
    await ElMessageBox.confirm(t('sys.menu.confirmLeave'), t('common.confirmTitle'), { type: 'warning' })
    return true
  } catch {
    return false
  }
})

// ---- 编辑显示 / 新建分组 ----
const dialog = reactive({ visible: false, mode: 'edit' as 'edit' | 'group', saving: false, node: null as MenuAdminNode | null })
const form = reactive({ titles: {} as Record<string, string>, icon: '', hidden: false, parent: '', sort: 0 })
const formRef = ref<FormInstance>()
const isGroupForm = computed(() => dialog.mode === 'group' || dialog.node?.kind === 'group')
const rules = computed<FormRules>(() =>
  isGroupForm.value
    ? {
        titles: [
          {
            validator: (_r: unknown, v: Record<string, string>, cb: (e?: Error) => void) =>
              v[DEFAULT_LANG]?.trim() ? cb() : cb(new Error(t('err.rbac.menu.groupNeedsDefault'))),
            trigger: 'blur',
          },
        ],
      }
    : {},
)

// 可以当上级的节点：目录和分组
const containers = computed(() => flat.value.filter((n) => n.kind !== 'page'))

function openEdit(n: MenuAdminNode) {
  dialog.mode = 'edit'
  dialog.node = n
  Object.assign(form, { titles: { ...n.titles }, icon: n.kind === 'group' || n.icon !== n.default?.icon ? n.icon : '', hidden: n.hidden, parent: '', sort: n.sort })
  dialog.visible = true
}

// 新分组默认排在所选上级的最后
function nextSort(parent: string): number {
  const sorts = flat.value.filter((n) => n.parent === parent).map((n) => n.sort)
  return Math.min((sorts.length ? Math.max(...sorts) : 0) + 10, MAX_SORT)
}
watch(
  () => form.parent,
  (p) => {
    if (dialog.visible && dialog.mode === 'group') form.sort = nextSort(p)
  },
)

function openGroup() {
  dialog.mode = 'group'
  dialog.node = null
  Object.assign(form, { titles: {}, icon: 'Folder', hidden: false, parent: '', sort: nextSort('') })
  dialog.visible = true
}

// 对话框只列端启用的语言；没启用的语言（接口允许）原样带回去，不因为编辑一次就丢掉。
function titlesOf(): Record<string, string> {
  const enabled = new Set<string>(useLanguages().map((l) => l.code))
  const out: Record<string, string> = {}
  for (const [k, v] of Object.entries(dialog.node?.titles ?? {})) if (!enabled.has(k)) out[k] = v
  for (const [k, v] of Object.entries(form.titles)) if (v.trim()) out[k] = v.trim()
  return out
}

async function submit() {
  if (!formRef.value) return
  try {
    await formRef.value.validate()
  } catch {
    return
  }
  dialog.saving = true
  try {
    if (dialog.mode === 'group') {
      await menuApi.createGroup({ titles: titlesOf(), icon: form.icon, hidden: form.hidden, parent: form.parent, sort: form.sort })
    } else if (dialog.node) {
      await menuApi.update(dialog.node.name, { titles: titlesOf(), icon: form.icon, hidden: form.hidden, ...(dirty.value ? {} : { sort: form.sort }) })
    }
    dialog.visible = false
    await afterChange()
  } catch {
    // 已提示
  } finally {
    dialog.saving = false
  }
}

async function reset(n: MenuAdminNode) {
  try {
    await ElMessageBox.confirm(t('sys.menu.confirmReset', { name: titleOf(n) }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await menuApi.reset(n.name)
  await afterChange()
}

async function removeGroup(n: MenuAdminNode) {
  try {
    await ElMessageBox.confirm(t('sys.menu.confirmDeleteGroup', { name: titleOf(n) }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  await menuApi.removeGroup(n.name)
  await afterChange()
}

const kindLabel = (k: string) => (k === 'group' ? t('sys.menu.kindGroup') : k === 'dir' ? t('sys.menu.kindDir') : t('sys.menu.kindPage'))
const kindType = (k: string) => (k === 'group' ? 'warning' : k === 'dir' ? 'info' : 'primary')
// 代码菜单每种语言的默认显示名，作为输入框的占位提示
function defaultTitle(code: LocaleCode): string {
  const key = dialog.node?.titleKey
  return key ? t('sys.menu.titlePlaceholder', { name: t(key, {}, { locale: code }) }) : ''
}
</script>

<template>
  <div class="ga-page ga-menu">
    <el-card v-loading="loading">
      <div class="ga-menu__head">
        <div class="ga-menu__hint">{{ t('sys.menu.hint') }}</div>
        <div v-if="canUpdate" class="ga-menu__actions">
          <span v-if="dirty" class="ga-menu__dirty">{{ t('sys.menu.dirty') }}</span>
          <el-button :disabled="!dirty" @click="undo">{{ t('sys.menu.undo') }}</el-button>
          <el-button type="primary" :disabled="!dirty" :loading="saving" data-test="menu-save-layout" @click="saveLayout">{{ t('sys.menu.saveLayout') }}</el-button>
          <el-button type="primary" plain data-test="menu-create-group" @click="openGroup">{{ t('sys.menu.createGroup') }}</el-button>
        </div>
      </div>

      <el-tree
        :data="tree"
        node-key="name"
        default-expand-all
        :expand-on-click-node="false"
        :draggable="canUpdate"
        :allow-drop="allowDrop"
        :allow-drag="allowDrag"
        class="ga-menu__tree"
        data-test="menu-tree"
        @node-drop="onDrop"
      >
        <template #default="{ data }">
          <div class="ga-menu__node" :data-test="`menu-node-${(data as TreeNode).name}`">
            <span class="ga-menu__title">
              <el-icon v-if="resolveIcon((data as TreeNode).icon)"><component :is="resolveIcon((data as TreeNode).icon)" /></el-icon>
              <span>{{ titleOf(data as TreeNode) }}</span>
              <el-tag size="small" :type="kindType((data as TreeNode).kind)" effect="plain">{{ kindLabel((data as TreeNode).kind) }}</el-tag>
              <el-tag v-if="(data as TreeNode).customized" size="small" type="warning" effect="plain">{{ t('sys.menu.customized') }}</el-tag>
              <el-tag v-if="(data as TreeNode).hidden" size="small" type="info">{{ (data as TreeNode).codeHidden ? t('sys.menu.codeHidden') : t('sys.menu.hidden') }}</el-tag>
            </span>
            <span class="ga-menu__meta">
              <span class="ga-menu__sort" :title="t('sys.menu.sort')" :data-test="`menu-sort-${(data as TreeNode).name}`">#{{ (data as TreeNode).sort }}</span>
              <code v-if="(data as TreeNode).path">{{ (data as TreeNode).path }}</code>
              <code v-if="(data as TreeNode).perm" class="ga-menu__perm">{{ (data as TreeNode).perm }}</code>
            </span>
            <span v-if="canUpdate" class="ga-menu__ops" @click.stop>
              <el-button link type="primary" @click="openEdit(data as TreeNode)">{{ t('common.edit') }}</el-button>
              <el-button v-if="(data as TreeNode).customized" link type="warning" @click="reset(data as TreeNode)">{{ t('sys.menu.reset') }}</el-button>
              <el-button v-if="(data as TreeNode).kind === 'group'" link type="danger" @click="removeGroup(data as TreeNode)">{{ t('common.delete') }}</el-button>
            </span>
          </div>
        </template>
      </el-tree>
    </el-card>

    <el-dialog
      v-model="dialog.visible"
      :title="dialog.mode === 'group' ? t('sys.menu.groupTitle') : dialog.node?.kind === 'group' ? t('sys.menu.editGroupTitle') : t('sys.menu.editTitle')"
      width="600px"
      destroy-on-close
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="titles" :label="t('sys.menu.titles')">
          <GaI18nInputs
            v-model="form.titles"
            :maxlength="32"
            :placeholder="isGroupForm ? undefined : defaultTitle"
            :required="isGroupForm ? [DEFAULT_LANG] : []"
            test-id="menu-form"
          />
        </el-form-item>
        <el-form-item :label="t('sys.menu.icon')">
          <el-select v-model="form.icon" filterable clearable :placeholder="t('sys.menu.iconPlaceholder')" style="width: 100%">
            <el-option v-for="name in icons" :key="name" :value="name" :label="name">
              <span class="ga-menu__icon-opt">
                <el-icon><component :is="resolveIcon(name)" /></el-icon>
                {{ name }}
              </span>
            </el-option>
          </el-select>
        </el-form-item>
        <el-form-item v-if="dialog.mode === 'group'" :label="t('sys.menu.parent')">
          <el-select v-model="form.parent" style="width: 100%">
            <el-option value="" :label="t('sys.menu.topLevel')" />
            <el-option v-for="c in containers" :key="c.name" :value="c.name" :label="titleOf(c)" />
          </el-select>
        </el-form-item>
        <el-form-item :label="t('sys.menu.sort')">
          <el-input-number v-model="form.sort" :min="0" :max="MAX_SORT" :step="10" :disabled="dirty" controls-position="right" data-test="menu-form-sort" />
          <div class="ga-form-hint">{{ dirty ? t('sys.menu.sortDirty') : t('sys.menu.sortHint') }}</div>
        </el-form-item>
        <el-form-item :label="t('sys.menu.hideInSidebar')">
          <el-switch v-model="form.hidden" :disabled="dialog.node?.codeHidden" />
          <div v-if="dialog.node?.codeHidden" class="ga-form-hint">{{ t('sys.menu.codeHiddenHint') }}</div>
        </el-form-item>
        <template v-if="dialog.node && dialog.node.kind !== 'group'">
          <el-form-item :label="t('sys.menu.path')">
            <code>{{ dialog.node.path }}</code>
          </el-form-item>
          <el-form-item v-if="dialog.node.component" :label="t('sys.menu.component')">
            <code>{{ dialog.node.component }}</code>
          </el-form-item>
          <el-form-item v-if="dialog.node.perm" :label="t('sys.menu.perm')">
            <code>{{ dialog.node.perm }}</code>
          </el-form-item>
          <el-form-item label=" ">
            <div class="ga-form-hint">{{ t('sys.menu.readonlyHint') }}</div>
          </el-form-item>
        </template>
        <el-form-item v-else label=" ">
          <div class="ga-form-hint">{{ t('sys.menu.groupHint') }}</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="menu-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.ga-menu__head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}
.ga-menu__hint {
  flex: 1 1 360px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
  line-height: 1.6;
}
.ga-menu__actions {
  display: flex;
  align-items: center;
  gap: 8px;
}
.ga-menu__dirty {
  font-size: 12px;
  color: var(--el-color-warning);
}
.ga-menu__tree :deep(.el-tree-node__content) {
  height: auto;
  min-height: 36px;
}
.ga-menu__node {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 4px 8px 4px 0;
  min-width: 0;
}
.ga-menu__title {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-weight: 500;
}
.ga-menu__meta {
  display: inline-flex;
  gap: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ga-menu__perm {
  color: var(--el-color-primary);
}
.ga-menu__sort {
  min-width: 44px;
  padding: 0 6px;
  border-radius: 4px;
  background: var(--el-fill-color-light);
  font-variant-numeric: tabular-nums;
  text-align: center;
}
.ga-menu__ops {
  margin-left: auto;
  flex: none;
}
.ga-menu__icon-opt {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
@media (max-width: 768px) {
  .ga-menu__meta {
    display: none;
  }
}
</style>
