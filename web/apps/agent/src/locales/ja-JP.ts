// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': '加盟店の招待を作成', 'onboarding.revoke-invite': '加盟店の招待を取り消す' },
  menu: {
    invitations: '加盟店の招待',
    'agent.merchants': '配下の加盟店',
  },
  permGroup: {
    'agent.merchant': '配下の加盟店',
  },
  perm: {
    agent: {
      merchant: { list: '配下の加盟店の閲覧' },
    },
  },
  org: {
    overview: {
      counts: { merchants: '配下の加盟店' },
    },
  },
  merchants: {
    keywordPlaceholder: '番号 / 名称 / 担当者 / 電話',
    readonly: '配下の加盟店は閲覧のみです。開設・変更・停止・所属の変更はプラットフォームが行います。',
    code: '番号',
    name: '名称',
    contactName: '担当者',
    contactPhone: '連絡先電話',
    createdAt: '開設日時',
  },
}
