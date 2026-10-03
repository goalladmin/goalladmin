<script setup lang="ts">
import { GaDataCenter } from '@ga/shell'
import { dashboardApi } from '../../api/system'
</script>

<template><GaDataCenter :fetch="dashboardApi.get" /></template>
