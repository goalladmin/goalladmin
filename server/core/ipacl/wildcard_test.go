package ipacl_test

import (
	"net/netip"
	"testing"

	"github.com/goalladmin/goalladmin/server/core/ipacl"
	"github.com/stretchr/testify/require"
)

func TestParseWildcard_177(t *testing.T) {
	for _, tc := range []struct{ input, want, first, last, outside string }{
		{"116.88.8.*", "116.88.8.0/24", "116.88.8.0", "116.88.8.255", "116.88.9.0"},
		{"116.88.*.*", "116.88.0.0/16", "116.88.0.0", "116.88.255.255", "116.89.0.0"},
		{" 116.*.*.* ", "116.0.0.0/8", "116.0.0.0", "116.255.255.255", "117.0.0.0"},
		{"*.*.*.*", "0.0.0.0/0", "0.0.0.0", "255.255.255.255", "::1"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			p, err := ipacl.ParseCIDR(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.want, p.String())
			require.True(t, p.Contains(netip.MustParseAddr(tc.first)))
			require.True(t, p.Contains(netip.MustParseAddr(tc.last)))
			require.False(t, p.Contains(netip.MustParseAddr(tc.outside)))
		})
	}
	for _, input := range []string{"*", "124.*", "124.55.*", "124.*.**.*", "124.*.12.*", "124.55.1*.*", "124.55.12.**", "124.55.12.*/24", "124.55.12.*.1", "124..12.*", "124. 55.12.*", "124.055.12.*", "124.-1.12.*", "124.+1.12.*", "124.256.12.*", "124.55.12.＊", "::ffff:124.55.12.*", "2001:db8::*", "99999999999999999999.*.*.*"} {
		_, err := ipacl.ParseCIDR(input)
		require.Error(t, err, input)
	}
}

func TestWildcard_177_NormalizedRules(t *testing.T) {
	ctx, gdb, clock := setup(t)
	s := newSvc(t, ctx, gdb, clock)
	target := ipacl.OrgTarget("merchant", 1)
	_, err := s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "124.55.*.*"}, 1, "124.55.12.1")
	require.Equal(t, "ipacl.selfLockout", fieldKey(t, err))
	_, err = s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "*.*.*.*"}, 1, "192.0.2.1")
	require.Equal(t, "ipacl.denyTooBroad", fieldKey(t, err))
	r, err := s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "124.55.12.*"}, 1, "192.0.2.1")
	require.NoError(t, err)
	r2, err := s.AddOrgDeny(ctx, target, ipacl.DenyInput{CIDR: "124.55.12.0/24"}, 1, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, r.ID, r2.ID)
	require.False(t, s.AccountAllows(ctx, "merchant", 1, 1, "124.55.12.255"))
	require.True(t, s.AccountAllows(ctx, "merchant", 2, 1, "124.55.12.255"))
	rules, err := s.SetAllow(ctx, target, []ipacl.Entry{{CIDR: "192.0.2.*"}, {CIDR: "192.0.2.0/24"}}, 1, "192.0.2.1")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, "192.0.2.0/24", rules[0].CIDR)
	require.True(t, s.AccountAllows(ctx, "merchant", 1, 1, "192.0.2.255"))
	require.False(t, s.AccountAllows(ctx, "merchant", 1, 1, "192.0.3.0"))
}
