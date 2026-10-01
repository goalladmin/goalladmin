package textx

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestClip(t *testing.T) {
	require.Equal(t, "abc", Clip("abc", 10))
	require.Equal(t, "ab", Clip("abc", 2))
	require.Equal(t, "Mozilla �", Clip("Mozilla \xff", 255), "非法字节换成 U+FFFD")
	// 22 个"中"是 66 字节，截到 64 字节不能把第 22 个切成两半
	s := Clip(strings.Repeat("中", 22), 64)
	require.True(t, utf8.ValidString(s))
	require.Equal(t, strings.Repeat("中", 21), s)
	require.True(t, utf8.ValidString(Clip("a\xffb\xfe\xfd", 3)))
	require.LessOrEqual(t, len(Clip("\xff\xff\xff\xff", 4)), 4)
	require.Equal(t, "", Clip("中", 2))
}
