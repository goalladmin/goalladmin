// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': 'Créer une invitation pour un marchand', 'onboarding.revoke-invite': 'Révoquer une invitation pour un marchand' },
  menu: {
    invitations: 'Invitations aux marchands',
    'agent.merchants': 'Mes marchands',
  },
  permGroup: {
    'agent.merchant': 'Mes marchands',
  },
  perm: {
    agent: {
      merchant: { list: 'Voir mes marchands' },
    },
  },
  org: {
    overview: {
      counts: { merchants: 'Mes marchands' },
    },
  },
  merchants: {
    keywordPlaceholder: 'Code / nom / contact / téléphone',
    readonly: 'Les marchands sont ici en lecture seule : la plateforme les ouvre, les modifie, les désactive et les transfère.',
    code: 'Code',
    name: 'Nom',
    contactName: 'Contact',
    contactPhone: 'Téléphone du contact',
    createdAt: 'Ouvert le',
  },
}
