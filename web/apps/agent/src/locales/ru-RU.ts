// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': 'Создание приглашения продавцу', 'onboarding.revoke-invite': 'Отзыв приглашения продавцу' },
  menu: {
    invitations: 'Приглашения продавцам',
    'agent.merchants': 'Мои продавцы',
  },
  permGroup: {
    'agent.merchant': 'Мои продавцы',
  },
  perm: {
    agent: {
      merchant: { list: 'Просмотр своих продавцов' },
    },
  },
  org: {
    overview: {
      counts: { merchants: 'Мои продавцы' },
    },
  },
  merchants: {
    keywordPlaceholder: 'Код / название / контакт / телефон',
    readonly: 'Продавцы здесь только для просмотра: открывает, изменяет, отключает и переводит их платформа.',
    code: 'Код',
    name: 'Название',
    contactName: 'Контактное лицо',
    contactPhone: 'Контактный телефон',
    createdAt: 'Открыт',
  },
}
