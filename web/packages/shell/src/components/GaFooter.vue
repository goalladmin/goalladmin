<script setup lang="ts">
// 页脚的版权说明（D-119）：三个端共用。登录后在内容区下面居中一行，登录、注册、改密页在舞台底部。
import { useI18n } from 'vue-i18n'

// 版权人的注册名称，与仓库根目录的 NOTICE 一致；各语言的文案只决定它前后的说法。
const owner = 'STARDATA INTERNATIONAL PTE. LTD.'

const { t } = useI18n()
</script>

<template>
  <footer class="ga-footer" data-test="footer-copyright">{{ t('shell.footer.copyright', { owner }) }}</footer>
</template>
