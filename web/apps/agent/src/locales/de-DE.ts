// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': 'Händlereinladung erstellen', 'onboarding.revoke-invite': 'Händlereinladung widerrufen' },
  menu: {
    invitations: 'Händlereinladungen',
    'agent.merchants': 'Meine Händler',
  },
  permGroup: {
    'agent.merchant': 'Meine Händler',
  },
  perm: {
    agent: {
      merchant: { list: 'Eigene Händler ansehen' },
    },
  },
  org: {
    overview: {
      counts: { merchants: 'Meine Händler' },
    },
  },
  merchants: {
    keywordPlaceholder: 'Nummer / Name / Ansprechpartner / Telefon',
    readonly: 'Händler sind hier nur lesbar: Eröffnen, Ändern, Deaktivieren und Umhängen übernimmt die Plattform.',
    code: 'Nummer',
    name: 'Name',
    contactName: 'Ansprechpartner',
    contactPhone: 'Telefon',
    createdAt: 'Eröffnet am',
  },
}
