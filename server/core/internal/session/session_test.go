package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRefreshToken(t *testing.T) {
	sid := strings.Repeat("a", 32)
	secret := strings.Repeat("b", 64)
	family := strings.Repeat("c", 64)
	// 三段：会话号、家族密钥、凭证（D-104）
	tok, ok := ParseRefreshToken(sid + "." + family + "." + secret)
	require.True(t, ok)
	require.Equal(t, RefreshToken{SID: sid, Family: family, Secret: secret}, tok)
	require.Equal(t, sid+"."+family+"."+secret, tok.String())
	// 两段：迁移之前下发的写法，没有家族密钥
	tok, ok = ParseRefreshToken(sid + "." + secret)
	require.True(t, ok)
	require.Equal(t, RefreshToken{SID: sid, Secret: secret}, tok)
	require.Equal(t, sid+"."+secret, tok.String())

	for _, bad := range []string{
		"", "nodot", sid + "." + "short", strings.Repeat("Z", 32) + "." + secret, sid + "." + strings.Repeat("g", 64),
		sid + "." + family + "." + "short", sid + ".short." + secret, sid + "." + strings.Repeat("G", 64) + "." + secret,
		sid + ".." + secret, sid + "." + family + "." + secret + ".", sid + "." + family + "." + secret + "." + secret,
		strings.Repeat("A", 32) + "." + family + "." + secret,
	} {
		_, ok := ParseRefreshToken(bad)
		require.False(t, ok, bad)
	}
}

func TestHashAndCompare(t *testing.T) {
	a := hashSecret("x")
	require.Len(t, a, 64)
	require.True(t, constantEq(a, hashSecret("x")))
	require.False(t, constantEq(a, hashSecret("y")))
	require.False(t, constantEq(a, a[:10]))
}

// 181（D-097）：会话号只认 32 位小写十六进制。
func TestValidSID_181(t *testing.T) {
	require.True(t, ValidSID(strings.Repeat("a", 32)))
	require.True(t, ValidSID("0123456789abcdef0123456789abcdef"))
	for _, bad := range []string{
		"", strings.Repeat("a", 31), strings.Repeat("a", 33),
		strings.Repeat("A", 32), "0123456789ABCDEF0123456789abcdef",
		strings.Repeat("g", 32), strings.Repeat("a", 31) + " ", strings.Repeat("ａ", 32),
	} {
		require.False(t, ValidSID(bad), bad)
	}
}

// 181（D-097）、188（D-104）：HoldsToken 认当前值（会话有家族密钥时要家族密钥对得上）和上一个值。
func TestHoldsToken_181_188(t *testing.T) {
	prev := hashSecret("old")
	// 迁移之前建的会话：没有家族密钥，只看两代凭证
	s := &Session{RefreshHash: hashSecret("cur"), PrevRefreshHash: &prev}
	require.True(t, s.HoldsToken(RefreshToken{Secret: "cur"}))
	require.True(t, s.HoldsToken(RefreshToken{Secret: "old"}))
	require.False(t, s.HoldsToken(RefreshToken{Secret: "older"}))
	require.False(t, s.HoldsToken(RefreshToken{}))
	s.PrevRefreshHash = nil
	require.False(t, s.HoldsToken(RefreshToken{Secret: "old"}))
	require.True(t, s.HoldsToken(RefreshToken{Secret: "cur"}))

	// 有家族密钥的会话：当前值要连家族密钥一起对上
	fam := hashSecret("fam")
	s = &Session{RefreshHash: hashSecret("cur"), PrevRefreshHash: &prev, FamilyHash: &fam}
	require.True(t, s.HoldsToken(RefreshToken{Family: "fam", Secret: "cur"}))
	require.False(t, s.HoldsToken(RefreshToken{Secret: "cur"}), "当前值只随三段的凭证下发")
	require.False(t, s.HoldsToken(RefreshToken{Family: "other", Secret: "cur"}))
	require.True(t, s.HoldsToken(RefreshToken{Family: "fam", Secret: "old"}))
	require.True(t, s.HoldsToken(RefreshToken{Secret: "old"}), "上一个值：会话升级时在途的两段凭证")
	require.True(t, s.HoldsToken(RefreshToken{Family: "fam", Secret: "older"}), "家族密钥对得上：持有过这个会话的凭证")
	require.True(t, s.HoldsToken(RefreshToken{Family: "fam"}))
	require.False(t, s.HoldsToken(RefreshToken{Family: "other", Secret: "older"}))
	require.False(t, s.HoldsToken(RefreshToken{Secret: "older"}))
}
