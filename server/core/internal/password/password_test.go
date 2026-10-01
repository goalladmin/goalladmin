package password

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHashVerify(t *testing.T) {
	h := NewHasher(4)
	hash, err := h.Hash("correct-horse-9")
	require.NoError(t, err)
	require.True(t, h.Verify(hash, "correct-horse-9"))
	require.False(t, h.Verify(hash, "wrong"))
	require.False(t, h.Verify("", "x"))
	_, err = h.Hash(strings.Repeat("a", 73))
	require.ErrorIs(t, err, ErrTooLong)
	require.False(t, h.Verify(hash, strings.Repeat("a", 73)))
	h.VerifyDummy("anything")
}

func TestPolicy(t *testing.T) {
	h := NewHasher(4)
	p := DefaultPolicy()
	prev, _ := h.Hash("old-password-1")
	cases := map[string]error{
		"short1":                 ErrTooShort,
		"onlyletters":            ErrNoDigit,
		"1234567890":             ErrNoLetter,
		"alice12345":             ErrSameAsUsername,
		"old-password-1":         ErrSameAsPrevious,
		" padded-pass-1":         ErrContainsSpaces,
		strings.Repeat("a1", 40): ErrTooLong,
		"good-password-7":        nil,
		"中文密码也可以1234":            nil,
	}
	for pwd, want := range cases {
		err := p.Check(h, pwd, "alice12345", prev)
		if want == nil {
			require.NoError(t, err, pwd)
		} else {
			require.ErrorIs(t, err, want, pwd)
		}
	}
}

func TestGenerate(t *testing.T) {
	seen := map[string]bool{}
	strictest := Policy{MinLength: 20, RequireUpper: true, RequireLower: true, RequireSymbol: true}
	for i := 0; i < 20; i++ {
		s, err := Generate(0)
		require.NoError(t, err)
		require.Len(t, s, 20)
		require.NoError(t, DefaultPolicy().Check(nil, s, "", ""))
		require.NoError(t, strictest.Check(nil, s, "", ""), "生成的密码满足任何合法策略")
		require.False(t, seen[s])
		seen[s] = true
	}
	s, err := Generate(40)
	require.NoError(t, err)
	require.Len(t, s, 40, "策略要求更长时按策略的长度生成")
}

func TestPolicyOptionalClasses(t *testing.T) {
	p := Policy{MinLength: 10, RequireUpper: true, RequireLower: true, RequireSymbol: true}
	for pwd, want := range map[string]error{
		"abcdefgh12":  ErrNoUpper,
		"ABCDEFGH12":  ErrNoLower,
		"Abcdefgh12":  ErrNoSymbol,
		"Abcdefg!12":  nil,
		"Abcdefg 12!": nil, // 空格不算符号，但有 ! 就够
		"ABCDEFGH!!":  ErrNoDigit,
	} {
		err := p.Check(nil, pwd, "", "")
		if want == nil {
			require.NoError(t, err, pwd)
		} else {
			require.ErrorIs(t, err, want, pwd)
		}
	}
	// 默认策略不要求大写、小写、符号
	require.NoError(t, DefaultPolicy().Check(nil, "abcdefgh12", "", ""))
}

// 规范 §13.2 第 105 条（D-058）：超过 72 字节的密码（25 个汉字就是 75 字节）、没有密码的账号，校验失败前同样做一次
// bcrypt 比较，和"账号不存在"的耗时相当，不能靠响应快慢试出账号在不在。
func TestVerify_105_OverlongPasswordTakesAsLongAsUnknownAccount(t *testing.T) {
	h := NewHasher(10)
	hash, err := h.Hash("correct-horse-9")
	require.NoError(t, err)
	long := strings.Repeat("密", 25)
	require.Greater(t, len(long), MaxBytes)

	timeIt := func(fn func()) time.Duration {
		best := time.Duration(1 << 62)
		for range 3 {
			start := time.Now()
			fn()
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	unknown := timeIt(func() { h.VerifyDummy(long) })
	require.False(t, h.Verify(hash, long))
	require.False(t, h.Verify("", "correct-horse-9"))
	overlong := timeIt(func() { h.Verify(hash, long) })
	noHash := timeIt(func() { h.Verify("", "correct-horse-9") })
	require.Greater(t, overlong, unknown/3, "超长密码：%v，不存在的账号：%v", overlong, unknown)
	require.Greater(t, noHash, unknown/3, "没有密码的账号：%v，不存在的账号：%v", noHash, unknown)
}
