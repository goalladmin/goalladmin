// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': '가맹점 초대 생성', 'onboarding.revoke-invite': '가맹점 초대 취소' },
  menu: {
    invitations: '가맹점 초대',
    'agent.merchants': '소속 가맹점',
  },
  permGroup: {
    'agent.merchant': '소속 가맹점',
  },
  perm: {
    agent: {
      merchant: { list: '소속 가맹점 보기' },
    },
  },
  org: {
    overview: {
      counts: { merchants: '소속 가맹점' },
    },
  },
  merchants: {
    keywordPlaceholder: '번호 / 이름 / 담당자 / 전화',
    readonly: '소속 가맹점은 보기만 할 수 있습니다. 개설, 수정, 중지, 소속 변경은 플랫폼이 처리합니다.',
    code: '번호',
    name: '이름',
    contactName: '담당자',
    contactPhone: '연락처',
    createdAt: '개설 시각',
  },
}
