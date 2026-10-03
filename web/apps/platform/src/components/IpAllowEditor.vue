<script setup lang="ts">
// IP 白名单编辑（D-062）：平台端白名单和账号白名单共用。整份编辑、整份保存；空表示不限制。
// 是否会把自己挡在外面由服务端判断（名单对自己生效时必须包含当前 IP），这里只给提示和"加上我的 IP"的快捷按钮。
import { computed } from 'vue'
import { useI18n } from '@ga/shell'

import type { IPEntry } from '../api/system'

const props = defineProps<{ modelValue: IPEntry[]; yourIp: string; readonly?: boolean; max?: number }>()
const emit = defineEmits<{ 'update:modelValue': [IPEntry[]] }>()
const { t } = useI18n()

const rows = computed({
  get: () => props.modelValue,
  set: (v: IPEntry[]) => emit('update:modelValue', v),
})
const limit = computed(() => props.max ?? 100)
const hasMine = computed(() => props.modelValue.some((e) => e.cidr.trim() === props.yourIp))

function add(cidr = '', remark = '') {
  if (rows.value.length >= limit.value) return
  rows.value = [...rows.value, { cidr, remark }]
}
function remove(i: number) {
  rows.value = rows.value.filter((_, j) => j !== i)
}
function update(i: number, patch: Partial<IPEntry>) {
  rows.value = rows.value.map((e, j) => (j === i ? { ...e, ...patch } : e))
}
</script>

<template>
  <div class="ga-ipallow" data-test="ip-allow-editor">
    <div class="ga-ipallow__bar">
      <span>
        {{ t('ipacl.yourIp') }}<b class="ga-mono" data-test="ip-your">{{ yourIp || '-' }}</b>
      </span>
      <span v-if="!readonly" class="ga-ipallow__actions">
        <el-button size="small" :disabled="!yourIp || hasMine || rows.length >= limit" data-test="ip-add-mine" @click="add(yourIp, t('ipacl.mine'))">{{ t('ipacl.addMine') }}</el-button>
        <el-button size="small" :disabled="rows.length >= limit" data-test="ip-add-row" @click="add()">{{ t('ipacl.addRow') }}</el-button>
      </span>
    </div>
    <p class="ga-ipallow__hint">{{ t('ipacl.wildcardHint') }}</p>
    <el-table :data="rows" size="small" :empty-text="t('ipacl.emptyAllow')">
      <el-table-column :label="t('ipacl.cidr')" min-width="200">
        <template #default="{ row, $index }">
          <span v-if="readonly" class="ga-mono">{{ (row as IPEntry).cidr }}</span>
          <el-input
            v-else
            :model-value="(row as IPEntry).cidr"
            :placeholder="t('ipacl.cidrPlaceholder')"
            maxlength="64"
            :data-test="`ip-cidr-${$index}`"
            @update:model-value="(v: string) => update($index, { cidr: v })"
          />
        </template>
      </el-table-column>
      <el-table-column :label="t('common.remark')" min-width="200">
        <template #default="{ row, $index }">
          <span v-if="readonly">{{ (row as IPEntry).remark }}</span>
          <el-input v-else :model-value="(row as IPEntry).remark" maxlength="255" @update:model-value="(v: string) => update($index, { remark: v })" />
        </template>
      </el-table-column>
      <el-table-column v-if="!readonly" width="70">
        <template #default="{ $index }">
          <el-button link type="danger" @click="remove($index)">{{ t('common.delete') }}</el-button>
        </template>
      </el-table-column>
    </el-table>
    <p class="ga-ipallow__hint">{{ t('ipacl.allowHint', { max: limit }) }}</p>
  </div>
</template>

<style scoped>
.ga-ipallow__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
}
.ga-ipallow__bar b {
  margin-left: 6px;
}
.ga-ipallow__hint {
  margin: 8px 0 0;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
</style>
