// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': '建立商戶邀請', 'onboarding.revoke-invite': '撤銷商戶邀請' },
  menu: {
    invitations: '商戶邀請',
    'agent.merchants': '旗下商戶',
  },
  permGroup: {
    'agent.merchant': '旗下商戶',
  },
  perm: {
    agent: {
      merchant: { list: '查看旗下商戶' },
    },
  },
  org: {
    overview: {
      counts: { merchants: '旗下商戶' },
    },
  },
  merchants: {
    keywordPlaceholder: '編號 / 名稱 / 聯絡人 / 電話',
    readonly: '旗下商戶只能查看：開通、修改、停用和調整歸屬都由平台處理。',
    code: '編號',
    name: '名稱',
    contactName: '聯絡人',
    contactPhone: '聯絡電話',
    createdAt: '開通時間',
  },
}
