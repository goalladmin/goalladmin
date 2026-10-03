package oplogimpl

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuery_195_PartialAndTruncation(t *testing.T) {
	text := MaskQuery("userId=7&broken=%zz&password=private-value")
	require.Contains(t, text, "userId=7")
	require.Contains(t, text, "partial query")
	require.NotContains(t, text, "private-value")
	text = clipQuery(strings.Repeat("a", maxQuery+10))
	require.LessOrEqual(t, len(text), maxQuery)
	require.Contains(t, text, "truncated")
}
