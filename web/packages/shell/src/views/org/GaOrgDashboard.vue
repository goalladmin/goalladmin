<script setup lang="ts">
import GaDataCenter from '../../data-center/GaDataCenter.vue'
import { orgApi } from '../../org/api'
</script>

<template><GaDataCenter :fetch="orgApi.dashboard" /></template>
