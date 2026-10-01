package session

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseRefreshToken(t *testing.T) {
	sid := strings.Repeat("a", 32)
	secret := strings.Repeat("b", 64)
	tok, ok := ParseRefreshToken(sid + "." + secret)
	require.True(t, ok)
	require.Equal(t, sid, tok.SID)
	require.Equal(t, secret, tok.Secret)
	require.Equal(t, sid+"."+secret, tok.String())

	for _, bad := range []string{"", "nodot", sid + "." + "short", strings.Repeat("Z", 32) + "." + secret, sid + "." + strings.Repeat("g", 64)} {
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
