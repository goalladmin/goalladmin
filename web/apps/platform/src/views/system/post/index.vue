<script setup lang="ts">
// 岗位管理（D-033）：岗位只是身份标签，不带权限。
import { reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'
import { formatTime, useI18n, useTable } from '@ga/shell'

import { postApi } from '../../../api/system'
import type { Post, PostInput } from '../../../api/system'

const { t } = useI18n()

const table = useTable<{ keyword: string; status: number | undefined }, Post>({
  query: { keyword: '', status: undefined },
  fetch: (p) => postApi.list(p),
})

const dialog = reactive({ visible: false, id: 0, saving: false })
const form = reactive<Required<PostInput>>({ code: '', name: '', status: 1, sort: 0, remark: '' })
const formRef = ref<FormInstance>()
const rules: FormRules = {
  code: [
    { required: true, message: () => t('common.required'), trigger: 'blur' },
    { pattern: /^[a-z][a-z0-9-]{0,63}$/, message: () => t('sys.post.codeRule'), trigger: 'blur' },
  ],
  name: [{ required: true, whitespace: true, message: () => t('common.required'), trigger: 'blur' }],
}

function openCreate() {
  dialog.id = 0
  Object.assign(form, { code: '', name: '', status: 1, sort: 0, remark: '' })
  dialog.visible = true
}

function openEdit(p: Post) {
  dialog.id = p.id
  Object.assign(form, { code: p.code, name: p.name, status: p.status, sort: p.sort, remark: p.remark })
  dialog.visible = true
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
    if (dialog.id === 0) await postApi.create({ ...form, code: form.code.trim(), name: form.name.trim() })
    else await postApi.update(dialog.id, { name: form.name.trim(), status: form.status, sort: form.sort, remark: form.remark })
    dialog.visible = false
    ElMessage.success(t('common.success'))
    await table.reload()
  } catch {
    // 已提示
  } finally {
    dialog.saving = false
  }
}

async function remove(p: Post) {
  try {
    await ElMessageBox.confirm(t('sys.post.confirmDelete', { name: p.name }), t('common.confirmTitle'), { type: 'warning' })
  } catch {
    return
  }
  try {
    await postApi.remove(p.id)
    ElMessage.success(t('common.success'))
    await table.reload()
  } catch {
    // 已提示（还有人在用）
  }
}
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('common.keyword')">
          <el-input v-model="table.query.keyword" clearable :placeholder="t('sys.post.keywordPlaceholder')" style="width: 200px" @keyup.enter="table.search()" />
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
        <el-button v-perm="'system:post:create'" type="primary" data-test="post-create" @click="openCreate">{{ t('common.create') }}</el-button>
      </el-form>
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="post-table">
        <el-table-column prop="code" :label="t('sys.post.code')" min-width="140" />
        <el-table-column prop="name" :label="t('sys.post.name')" min-width="160" />
        <el-table-column prop="userCount" :label="t('sys.dept.users')" width="100" align="right" />
        <el-table-column prop="sort" :label="t('common.sort')" width="90" />
        <el-table-column :label="t('common.status')" width="100">
          <template #default="{ row }">
            <el-tag :type="(row as Post).status === 1 ? 'success' : 'info'" size="small">{{ (row as Post).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="remark" :label="t('common.remark')" min-width="160" show-overflow-tooltip />
        <el-table-column :label="t('common.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as Post).createdAt) }}</template>
        </el-table-column>
        <el-table-column :label="t('common.actions')" width="150" fixed="right">
          <template #default="{ row }">
            <el-button v-perm="'system:post:update'" link type="primary" @click="openEdit(row as Post)">{{ t('common.edit') }}</el-button>
            <el-button v-perm="'system:post:delete'" link type="danger" @click="remove(row as Post)">{{ t('common.delete') }}</el-button>
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

    <el-dialog v-model="dialog.visible" :title="dialog.id === 0 ? t('sys.post.createTitle') : t('sys.post.editTitle')" width="480px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="auto" @submit.prevent="submit">
        <el-form-item prop="code" :label="t('sys.post.code')">
          <el-input v-model="form.code" :disabled="dialog.id !== 0" maxlength="64" data-test="post-form-code" />
          <div v-if="dialog.id === 0" class="ga-form-hint">{{ t('sys.post.codeRule') }}</div>
        </el-form-item>
        <el-form-item prop="name" :label="t('sys.post.name')">
          <el-input v-model="form.name" maxlength="64" data-test="post-form-name" />
        </el-form-item>
        <el-form-item prop="sort" :label="t('common.sort')">
          <el-input-number v-model="form.sort" :min="0" :max="1000000" />
        </el-form-item>
        <el-form-item prop="status" :label="t('common.status')">
          <el-switch v-model="form.status" :active-value="1" :inactive-value="0" />
        </el-form-item>
        <el-form-item prop="remark" :label="t('common.remark')">
          <el-input v-model="form.remark" type="textarea" maxlength="255" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialog.visible = false">{{ t('common.cancel') }}</el-button>
        <el-button type="primary" :loading="dialog.saving" data-test="post-form-submit" @click="submit">{{ t('common.save') }}</el-button>
      </template>
    </el-dialog>
  </div>
</template>
