<script setup lang="ts">
// 名下商户（D-065、D-067）：只读。列表只有挂在本代理商下的商户，平台改了归属下一次就看不到；开通、修改、停用都由平台处理。
import { formatTime, useI18n, useRequest, useTable } from '@ga/shell'
import type { PageData, PageQuery } from '@ga/shell'

interface Merchant {
  id: number
  code: string
  name: string
  contactName: string
  contactPhone: string
  status: number
  createdAt: string
}

const { t } = useI18n()

interface Q extends Record<string, unknown> {
  keyword: string
  status: number | undefined
}
const table = useTable<Q, Merchant>({
  query: { keyword: '', status: undefined },
  fetch: (params: PageQuery & Q) => useRequest().get<PageData<Merchant>>('/merchants', { params }),
})
</script>

<template>
  <div class="ga-page">
    <el-card>
      <el-form class="ga-toolbar" :inline="true" @submit.prevent="table.search()">
        <el-form-item :label="t('common.keyword')">
          <el-input v-model="table.query.keyword" clearable :placeholder="t('merchants.keywordPlaceholder')" style="width: 240px" @keyup.enter="table.search()" />
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
      </el-form>
      <el-alert :title="t('merchants.readonly')" type="info" :closable="false" show-icon />
    </el-card>
    <el-card>
      <el-table v-loading="table.loading" :data="table.list" stripe data-test="agent-merchant-table">
        <el-table-column prop="code" :label="t('merchants.code')" min-width="130">
          <template #default="{ row }"><span class="ga-mono">{{ (row as Merchant).code }}</span></template>
        </el-table-column>
        <el-table-column prop="name" :label="t('merchants.name')" min-width="160" />
        <el-table-column prop="contactName" :label="t('merchants.contactName')" min-width="120" />
        <el-table-column prop="contactPhone" :label="t('merchants.contactPhone')" min-width="140" />
        <el-table-column :label="t('common.status')" min-width="90">
          <template #default="{ row }">
            <el-tag :type="(row as Merchant).status === 1 ? 'success' : 'info'" size="small">{{ (row as Merchant).status === 1 ? t('common.enabled') : t('common.disabled') }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column :label="t('merchants.createdAt')" min-width="170">
          <template #default="{ row }">{{ formatTime((row as Merchant).createdAt) }}</template>
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
