// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { "onboarding.invite": "Create invitation", "onboarding.revoke-invite": "Revoke invitation" },
  menu: {
    invitations: "Merchant invitations",
    'agent.merchants': 'My merchants',
  },
  permGroup: {
    'agent.merchant': 'My merchants',
  },
  perm: {
    agent: {
      merchant: { list: 'View my merchants' },
    },
  },
  org: {
    overview: {
      counts: { merchants: 'My merchants' },
    },
  },
  merchants: {
    keywordPlaceholder: 'Code / name / contact / phone',
    readonly: 'Merchants here are read-only: the platform opens, edits, disables and moves them.',
    code: 'Code',
    name: 'Name',
    contactName: 'Contact',
    contactPhone: 'Contact phone',
    createdAt: 'Opened on',
  },
}
