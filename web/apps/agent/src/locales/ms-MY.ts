// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': 'Cipta jemputan pedagang', 'onboarding.revoke-invite': 'Batalkan jemputan pedagang' },
  menu: {
    invitations: 'Jemputan pedagang',
    'agent.merchants': 'Pedagang saya',
  },
  permGroup: {
    'agent.merchant': 'Pedagang saya',
  },
  perm: {
    agent: {
      merchant: { list: 'Lihat pedagang saya' },
    },
  },
  org: {
    overview: {
      counts: { merchants: 'Pedagang saya' },
    },
  },
  merchants: {
    keywordPlaceholder: 'Kod / nama / orang hubungan / telefon',
    readonly: 'Pedagang di sini baca sahaja: platform yang membuka, menyunting, menyahdayakan dan memindahkan mereka.',
    code: 'Kod',
    name: 'Nama',
    contactName: 'Orang hubungan',
    contactPhone: 'Telefon hubungan',
    createdAt: 'Dibuka pada',
  },
}
