// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { "onboarding.invite": "创建商户邀请", "onboarding.revoke-invite": "撤销商户邀请" },
  menu: {
    invitations: "商户邀请",
    'agent.merchants': '名下商户',
  },
  permGroup: {
    'agent.merchant': '名下商户',
  },
  perm: {
    agent: {
      merchant: { list: '查看名下商户' },
    },
  },
  org: {
    overview: {
      counts: { merchants: '名下商户' },
    },
  },
  merchants: {
    keywordPlaceholder: '编号 / 名称 / 联系人 / 电话',
    readonly: '名下商户只能查看：开通、修改、停用和调整归属都由平台处理。',
    code: '编号',
    name: '名称',
    contactName: '联系人',
    contactPhone: '联系电话',
    createdAt: '开通时间',
  },
}
