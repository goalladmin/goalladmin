// 代理商端的文案（D-026、D-067）：只有代理商端特有的名下商户；子账号、角色等页面的文案在壳里。
export default {
  op: { 'onboarding.invite': 'মার্চেন্ট আমন্ত্রণ তৈরি করুন', 'onboarding.revoke-invite': 'মার্চেন্ট আমন্ত্রণ বাতিল করুন' },
  menu: {
    invitations: 'মার্চেন্ট আমন্ত্রণ',
    'agent.merchants': 'আমার মার্চেন্ট',
  },
  permGroup: {
    'agent.merchant': 'আমার মার্চেন্ট',
  },
  perm: {
    agent: {
      merchant: { list: 'আমার মার্চেন্ট দেখুন' },
    },
  },
  org: {
    overview: {
      counts: { merchants: 'আমার মার্চেন্ট' },
    },
  },
  merchants: {
    keywordPlaceholder: 'কোড / নাম / যোগাযোগকারী / ফোন',
    readonly: 'এখানকার মার্চেন্ট শুধু দেখা যায়: খোলা, সম্পাদনা, বন্ধ ও স্থানান্তর প্ল্যাটফর্মই করে।',
    code: 'কোড',
    name: 'নাম',
    contactName: 'যোগাযোগকারী',
    contactPhone: 'যোগাযোগ নম্বর',
    createdAt: 'খোলার সময়',
  },
}
