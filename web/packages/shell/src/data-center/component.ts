import { defineAsyncComponent } from 'vue'

// 图表只在进入数据中心时加载。
export const GaDataCenter = defineAsyncComponent(() => import('./GaDataCenter.vue'))
