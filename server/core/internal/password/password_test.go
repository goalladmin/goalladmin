package password

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fast 是参数降到最低的 Argon2id，测试用。
var fast = Params{Memory: 64, Time: 1}

func TestHashVerify(t *testing.T) {
	h := NewHasher(fast)
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
	h := NewHasher(fast)
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
// 哈希比较，和"账号不存在"的耗时相当，不能靠响应快慢试出账号在不在。看不懂的哈希、参数超出上限的哈希也一样（D-070）。
func TestVerify_105_OverlongPasswordTakesAsLongAsUnknownAccount(t *testing.T) {
	h := NewHasher(DefaultParams())
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
	real := timeIt(func() { h.Verify(hash, "wrong-password-9") })
	require.Greater(t, unknown, real/3, "假比较：%v，真实比较：%v", unknown, real)
	require.False(t, h.Verify(hash, long))
	require.False(t, h.Verify("", "correct-horse-9"))
	for name, bad := range map[string]func() bool{
		"超长密码":      func() bool { return h.Verify(hash, long) },
		"没有密码的账号":   func() bool { return h.Verify("", "correct-horse-9") },
		"看不懂的哈希":    func() bool { return h.Verify("not-a-hash", "correct-horse-9") },
		"参数超出上限的哈希": func() bool { return h.Verify(strings.Replace(hash, "m=19456", "m=4194304", 1), "correct-horse-9") },
	} {
		require.False(t, bad(), name)
		d := timeIt(func() { bad() })
		require.Greater(t, d, unknown/3, "%s：%v，不存在的账号：%v", name, d, unknown)
		require.Less(t, d, unknown*20+time.Second, "%s：%v，没有按哈希里写的参数去算", name, d)
	}
}

// 规范 §13.2 第 154 条（D-070）：新哈希是 Argon2id，格式是通行的那种，别的实现算出来的同参数哈希核对得上；
// 每次的盐不一样；参数写在哈希里，核对按哈希自己的参数算。
func TestArgon2id_154_FormatAndInterop(t *testing.T) {
	h := NewHasher(DefaultParams())
	hash, err := h.Hash("Example-pass-2026")
	require.NoError(t, err)
	require.Regexp(t, `^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`, hash)
	require.Len(t, hash, 97)
	require.True(t, h.Verify(hash, "Example-pass-2026"))
	require.False(t, h.Verify(hash, "Example-pass-2027"))
	require.False(t, h.NeedsRehash(hash))
	again, err := h.Hash("Example-pass-2026")
	require.NoError(t, err)
	require.NotEqual(t, hash, again, "同一个密码每次的盐不同，哈希也不同")
	require.NotContains(t, hash, "Example")

	// 参考实现（libargon2）对同一个密码算出来的两份：默认参数的，和 64 KiB、3 轮的
	for _, ref := range []string{
		"$argon2id$v=19$m=19456,t=2,p=1$AAECAwQFBgcICQoLDA0ODw$m67JqK1PiLiwDtOTYH3r5dllwZxDlMPhel4bObuXNk4",
		"$argon2id$v=19$m=64,t=3,p=1$EBESExQVFhcYGRobHB0eHw$d0pUYvs0RG35zGTuzj3U9EEV0+SX0JPtcKBYsW2dLAY",
	} {
		require.True(t, h.Verify(ref, "Example-pass-2026"), ref)
		require.False(t, h.Verify(ref, "example-pass-2026"), ref)
	}
	// 参数不同的哈希器照样核对得上（按哈希里写的算），只是认为它该重算
	small := NewHasher(fast)
	require.True(t, small.Verify(hash, "Example-pass-2026"))
	require.True(t, small.NeedsRehash(hash))
	mine, err := small.Hash("Example-pass-2026")
	require.NoError(t, err)
	require.Contains(t, mine, "$m=64,t=1,p=1$")
	require.True(t, h.Verify(mine, "Example-pass-2026"))
	require.True(t, h.NeedsRehash(mine))
	require.False(t, small.NeedsRehash(mine))

	// 参数不合法的哈希器用默认参数
	def, err := NewHasher(Params{}).Hash("Example-pass-2026")
	require.NoError(t, err)
	require.Contains(t, def, "$m=19456,t=2,p=1$")
	tooBig, err := NewHasher(Params{Memory: maxMemory + 1, Time: 1}).Hash("Example-pass-2026")
	require.NoError(t, err)
	require.Contains(t, tooBig, "$m=19456,t=2,p=1$")
}

// 规范 §13.2 第 154 条（D-070）：升级前留下的 bcrypt 哈希照常核对，并且被认为该重算；cost 超出上限的不算。
func TestLegacyBcrypt_154_VerifiedAndFlaggedForRehash(t *testing.T) {
	h := NewHasher(fast)
	for _, old := range []string{
		"$2a$12$Nva5w2RIjIpehq7kK63Y4.w6GcOP7tku6vis2v3MOeUwSWBYO6NKq", // cost 12：升级前的默认值
		"$2a$10$q9D3cXe0foyGiLzpOJY0s..bwxT2ZY.nSmCygqcVhi3BNv5L7P5i6",
	} {
		require.True(t, h.Verify(old, "Example-pass-2026"), old)
		require.False(t, h.Verify(old, "Example-pass-2027"), old)
		require.True(t, h.NeedsRehash(old), old)
		for _, minor := range []string{"$2b$", "$2y$"} { // 别的实现写的小版本号
			v := minor + old[4:]
			require.True(t, h.Verify(v, "Example-pass-2026"), v)
			require.True(t, h.NeedsRehash(v), v)
		}
	}
	// cost 15 以上的不核对（算一次要几秒到几分钟），也不重算
	costly := "$2a$15$Nva5w2RIjIpehq7kK63Y4.w6GcOP7tku6vis2v3MOeUwSWBYO6NKq"
	start := time.Now()
	require.False(t, h.Verify(costly, "Example-pass-2026"))
	require.Less(t, time.Since(start), time.Second, "没有按 cost 15 去算")
	require.False(t, h.NeedsRehash(costly))
	for _, bad := range []string{"", "x", "$2a$", "$2a$12$short", "$1$abc$def", "$2x$12$Nva5w2RIjIpehq7kK63Y4.w6GcOP7tku6vis2v3MOeUwSWBYO6NKq"} {
		require.False(t, h.Verify(bad, "Example-pass-2026"), bad)
		require.False(t, h.NeedsRehash(bad), bad)
	}
}

// 规范 §13.2 第 154 条（D-070）：库里读出来的 Argon2id 哈希，格式不规范、版本不认识、参数或长度超出上限的一律不算
// （不匹配、也不重算）；尤其不能按哈希里写的几个 G 内存去算。上限是内存 64 MiB、8 轮、并行度 4。
func TestArgon2id_154_MalformedAndOversizedRejected(t *testing.T) {
	h := NewHasher(fast)
	good, err := h.Hash("Example-pass-2026")
	require.NoError(t, err)
	require.True(t, h.Verify(good, "Example-pass-2026"))
	parts := strings.Split(good, "$") // "", argon2id, v=19, m=64,t=1,p=1, 盐, 输出
	build := func(mut func(p []string)) string {
		p := append([]string{}, parts...)
		mut(p)
		return strings.Join(p, "$")
	}
	cases := map[string]string{
		"argon2i":       build(func(p []string) { p[1] = "argon2i" }),
		"argon2d":       build(func(p []string) { p[1] = "argon2d" }),
		"大写的算法名":        build(func(p []string) { p[1] = "ARGON2ID" }),
		"旧版本 v=16":      build(func(p []string) { p[2] = "v=16" }),
		"没有版本":          strings.Replace(good, "$v=19", "", 1),
		"多一段":           good + "$x",
		"少一段":           strings.Join(parts[:5], "$"),
		"结尾多一个 $":       good + "$",
		"前面有空白":         " " + good,
		"参数顺序不对":        build(func(p []string) { p[3] = "t=1,m=64,p=1" }),
		"少一个参数":         build(func(p []string) { p[3] = "m=64,t=1" }),
		"多一个参数":         build(func(p []string) { p[3] = "m=64,t=1,p=1,x=1" }),
		"前导零":           build(func(p []string) { p[3] = "m=064,t=1,p=1" }),
		"正号":            build(func(p []string) { p[3] = "m=+64,t=1,p=1" }),
		"负数":            build(func(p []string) { p[3] = "m=-64,t=1,p=1" }),
		"十六进制":          build(func(p []string) { p[3] = "m=0x40,t=1,p=1" }),
		"带空格":           build(func(p []string) { p[3] = "m=64, t=1,p=1" }),
		"空的数字":          build(func(p []string) { p[3] = "m=,t=1,p=1" }),
		"超出 32 位":       build(func(p []string) { p[3] = "m=4294967296,t=1,p=1" }),
		"内存 4 GiB":      build(func(p []string) { p[3] = "m=4194304,t=1,p=1" }),
		"内存刚过上限":        build(func(p []string) { p[3] = "m=65537,t=1,p=1" }),
		"内存小于 8×并行度":    build(func(p []string) { p[3] = "m=7,t=1,p=1" }),
		"轮数 0":          build(func(p []string) { p[3] = "m=64,t=0,p=1" }),
		"轮数刚过上限":        build(func(p []string) { p[3] = "m=64,t=9,p=1" }),
		"轮数很大":          build(func(p []string) { p[3] = "m=64,t=4000000000,p=1" }),
		"并行度 0":         build(func(p []string) { p[3] = "m=64,t=1,p=0" }),
		"并行度刚过上限":       build(func(p []string) { p[3] = "m=72,t=1,p=5" }),
		"盐里有换行":         build(func(p []string) { p[4] = p[4][:8] + "\n" + p[4][8:] }),
		"结尾有换行":         good + "\n",
		"输出里有回车换行":      build(func(p []string) { p[5] = p[5][:20] + "\r\n" + p[5][20:] }),
		"盐太短":           build(func(p []string) { p[4] = "AAECAwQFBg" }),
		"盐太长":           build(func(p []string) { p[4] = strings.Repeat("A", 88) }),
		"盐为空":           build(func(p []string) { p[4] = "" }),
		"输出太短":          build(func(p []string) { p[5] = "AAECAwQFBgcICQoLDA0O" }),
		"输出太长":          build(func(p []string) { p[5] = strings.Repeat("A", 88) }),
		"输出为空":          build(func(p []string) { p[5] = "" }),
		"带填充的 base64":   build(func(p []string) { p[5] += "=" }),
		"URL 字母表":       build(func(p []string) { p[4] = "AAECAwQFBgcICQoLDA0_-w" }),
		"base64 里有非法字符": build(func(p []string) { p[4] = "AAECAwQFBgcICQoLDA0O!!" }),
		"base64 末尾多余的位": build(func(p []string) { p[4] = "AAECAwQFBgcICQoLDA0ODx" }),
	}
	// 不规范的 base64：最后一个字符里用不到的那几位不是 0。宽松的解码器会解出同样的字节（于是核对得上），
	// 这里按严格的来：同一份哈希只有一种写法
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	flipLastBit := func(b64 string) string {
		i := strings.IndexByte(alphabet, b64[len(b64)-1])
		require.GreaterOrEqual(t, i, 0)
		return b64[:len(b64)-1] + string(alphabet[i^1])
	}
	cases["盐的 base64 末尾有多余的位"] = build(func(p []string) { p[4] = flipLastBit(p[4]) })
	cases["输出的 base64 末尾有多余的位"] = build(func(p []string) { p[5] = flipLastBit(p[5]) })
	for name, hash := range cases {
		start := time.Now()
		require.False(t, h.Verify(hash, "Example-pass-2026"), "%s: %s", name, hash)
		require.Less(t, time.Since(start), time.Second, "%s：不该按哈希里写的参数去算", name)
		require.False(t, h.NeedsRehash(hash), "%s: %s", name, hash)
		// 直接看解析：有的写法换掉了盐，解析放过了也核对不上、长度也对，上面三条看不出来
		_, parsed := parseArgon(hash)
		require.False(t, parsed, "%s: %s", name, hash)
	}
	// 上限以内的照常核对：边界值本身是合法的
	_, ok := parseArgon(build(func(p []string) { p[3] = "m=65536,t=8,p=4" }))
	require.True(t, ok)
	_, ok = parseArgon(build(func(p []string) { p[3] = "m=8,t=1,p=1" }))
	require.True(t, ok)
}

// 上限以内、和自己生成的参数不一样的哈希（别的实现常见的默认值：64 MiB、3 轮、并行度 4）照常核对，并被标记为该重算。
func TestArgon2id_154_OtherImplementationsDefaultsVerify(t *testing.T) {
	h := NewHasher(fast)
	// 参考实现（libargon2）算的：m=65536,t=3,p=4，密码 Example-pass-2026
	const ref = "$argon2id$v=19$m=65536,t=3,p=4$AAECAwQFBgcICQoLDA0ODw$JDg4OA1bvpWWuczdQ/iCPIe/7l1TdMEmCu4BPbid55U"
	require.True(t, h.Verify(ref, "Example-pass-2026"))
	require.False(t, h.Verify(ref, "Example-pass-2027"))
	require.True(t, h.NeedsRehash(ref))
}

// 规范 §13.2 第 160 条（D-070）：部署时可以选 bcrypt——新哈希是 bcrypt（cost 12），核对照样两种都认；
// "该不该重算"按选的算法判断：选了 bcrypt，Argon2id 的哈希和 cost 不一样的 bcrypt 哈希都该重算，反过来也一样。
// 所以两种算法之间来回切换，已有的密码都不失效。
func TestAlgorithmChoice_160_BcryptAndBack(t *testing.T) {
	argon := NewHasher(fast)
	bc := NewHasher(Params{Algorithm: Bcrypt, Cost: 4})
	require.Equal(t, Argon2id, argon.Algorithm())
	require.Equal(t, Bcrypt, bc.Algorithm())

	bh, err := bc.Hash("Example-pass-2026")
	require.NoError(t, err)
	require.Regexp(t, `^\$2a\$04\$.{53}$`, bh)
	ah, err := argon.Hash("Example-pass-2026")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(ah, "$argon2id$"))

	// 两个哈希器都核对得了两种哈希
	for name, h := range map[string]*Hasher{"选 Argon2id 的": argon, "选 bcrypt 的": bc} {
		for _, hash := range []string{ah, bh} {
			require.True(t, h.Verify(hash, "Example-pass-2026"), "%s 核对 %s", name, hash)
			require.False(t, h.Verify(hash, "Example-pass-2027"), "%s 核对 %s", name, hash)
		}
	}
	// 该不该重算：不是自己选的算法、或参数不一样的才重算
	require.False(t, bc.NeedsRehash(bh))
	require.True(t, bc.NeedsRehash(ah), "选了 bcrypt：Argon2id 的哈希要换回来")
	require.True(t, bc.NeedsRehash("$2a$12$Nva5w2RIjIpehq7kK63Y4.w6GcOP7tku6vis2v3MOeUwSWBYO6NKq"), "cost 和当前的不一样")
	require.False(t, argon.NeedsRehash(ah))
	require.True(t, argon.NeedsRehash(bh))
	require.False(t, bc.NeedsRehash(""), "空的、看不懂的不重算")
	require.False(t, bc.NeedsRehash("not-a-hash"))
	// 超长密码两种都拒绝；选 bcrypt 时假比较也是 bcrypt（不 panic、花的是一次 bcrypt 的时间）
	_, err = bc.Hash(strings.Repeat("a", 73))
	require.ErrorIs(t, err, ErrTooLong)
	bc.VerifyDummy(strings.Repeat("密", 25))
	require.False(t, bc.Verify("", "x"))

	// 生产用的参数：bcrypt 是 cost 12；cost 不合法、算法不认识时用默认值
	require.Equal(t, Params{Algorithm: Bcrypt, Cost: 12}, ParamsFor(Bcrypt))
	require.Equal(t, Params{Algorithm: Argon2id, Memory: 19456, Time: 2}, ParamsFor(Argon2id))
	require.Equal(t, DefaultParams(), ParamsFor("scrypt"))
	require.Subset(t, Algorithms(), []Algorithm{Argon2id, Bcrypt})
	for _, cost := range []int{0, 3, 15, -1} {
		require.Equal(t, 12, NewHasher(Params{Algorithm: Bcrypt, Cost: cost}).p.Cost, "cost %d", cost)
	}
	require.Equal(t, Argon2id, NewHasher(Params{Algorithm: "scrypt", Cost: 4}).Algorithm())
	require.Equal(t, 10, NewHasher(Params{Algorithm: Bcrypt, Cost: 10, Memory: 64, Time: 1}).p.Cost)

	// 选 bcrypt 时"账号不存在"的假比较和一次真实的 bcrypt 比较耗时相当
	slow := NewHasher(Params{Algorithm: Bcrypt, Cost: 10})
	real, err := slow.Hash("correct-horse-9")
	require.NoError(t, err)
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
	one := timeIt(func() { slow.Verify(real, "wrong-password-9") })
	dummy := timeIt(func() { slow.VerifyDummy("wrong-password-9") })
	require.Greater(t, dummy, one/3, "假比较：%v，真实比较：%v", dummy, one)
	require.Less(t, dummy, one*3, "假比较：%v，真实比较：%v", dummy, one)
}
