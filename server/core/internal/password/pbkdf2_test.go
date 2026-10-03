package password

import (
	"crypto/fips140"
	"crypto/pbkdf2"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// PBKDF2 的两种，测试里挨个跑。
func pbkdf2Algorithms() []Algorithm { return []Algorithm{PBKDF2SHA256, PBKDF2SHA512} }

// 另一套实现（OpenSSL）对密码 Example-pass-2026 算出来的 PBKDF2 哈希，按本包的格式写出来。
// 前两条是生产用的参数；后面是迭代次数、盐和输出长度不一样的。
func pbkdf2Refs() map[Algorithm][]string {
	return map[Algorithm][]string{
		PBKDF2SHA256: {
			"$pbkdf2-sha256$i=600000,l=32$AAECAwQFBgcICQoLDA0ODw$gDMYsT+LTtVkTMFftZIFK54z7fjQenXGhbb7ikYl/rQ",
			"$pbkdf2-sha256$i=1000,l=32$AAECAwQFBgcICQoLDA0ODw$PZ6u+EYKB/bxyUYM9gdcNFEuwbZDEvF0hi58ocyqb2A",
			"$pbkdf2-sha256$i=310000,l=16$ZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXp7$aPImQdQS+Nc+Rj7cAGnJfA",
		},
		PBKDF2SHA512: {
			"$pbkdf2-sha512$i=220000,l=32$AAECAwQFBgcICQoLDA0ODw$fXnYVSIVKzjRsBtH6YXAdhnpqVWtaOmbAiISnqMCm5g",
			"$pbkdf2-sha512$i=1000,l=32$AAECAwQFBgcICQoLDA0ODw$VpJzA5/+5p5b41V77My7exilc+dBlAus7XCyhxKZTuc",
			"$pbkdf2-sha512$i=1000,l=64$MjM0NTY3ODk6Ozw9Pj9AQUJDREVGR0hJSktMTU5PUFE$+A8NIb134yWwfM0oaagdCk68quUqG7Ppb0p/qbn5PhvGrWwM1JY4JLRxpg+g2vnzn5rRY0yvJCBhckWVM8ppTg",
		},
	}
}

// 规范 §13.2 第 161 条（D-072）：选 PBKDF2 时新哈希是 PBKDF2-HMAC-SHA256（60 万次）或 PBKDF2-HMAC-SHA512（22 万次），
// 盐 16 字节、输出 32 字节，格式是 $pbkdf2-<摘要>$i=<次数>,l=<输出字节数>$<盐>$<输出>；另一套实现算出来的同参数哈希
// 核对得上；每次的盐不一样；参数写在哈希里，核对按哈希自己的参数算。
func TestPBKDF2_161_FormatAndInterop(t *testing.T) {
	prod := map[Algorithm]uint32{PBKDF2SHA256: 600000, PBKDF2SHA512: 220000}
	for _, algo := range pbkdf2Algorithms() {
		t.Run(string(algo), func(t *testing.T) {
			// 生产用的参数：只做格式和互通这几步（一次计算 0.1 秒上下，带竞争检测时更慢），别的用调低的次数
			require.Equal(t, Params{Algorithm: algo, Iterations: prod[algo]}, ParamsFor(algo))
			h := NewHasher(ParamsFor(algo))
			require.Equal(t, algo, h.Algorithm())
			hash, err := h.Hash("Example-pass-2026")
			require.NoError(t, err)
			require.Regexp(t, fmt.Sprintf(`^\$%s\$i=%d,l=32\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`, algo, prod[algo]), hash)
			require.Len(t, hash, 95)
			require.NotContains(t, hash, "Example")
			require.True(t, h.Verify(hash, "Example-pass-2026"))
			require.False(t, h.NeedsRehash(hash))

			refs := pbkdf2Refs()[algo]
			require.True(t, h.Verify(refs[0], "Example-pass-2026"), refs[0])
			require.False(t, h.Verify(refs[0], "example-pass-2026"), refs[0])
			for i, ref := range refs[1:] {
				require.True(t, h.Verify(ref, "Example-pass-2026"), ref)
				if i == 0 {
					require.False(t, h.Verify(ref, "example-pass-2026"), ref)
				}
			}
			// 第一条和自己生成的参数一样，不用重算；别的（次数、盐或输出的长度不一样）要重算
			for i, ref := range refs {
				require.Equal(t, i != 0, h.NeedsRehash(ref), ref)
			}
			// 输出被改了几位的不匹配（换成的仍是规范的 base64：被拒绝是因为比较不上，不是因为解析不了）
			last := map[bool]string{true: "Q", false: "A"}[strings.HasSuffix(refs[1], "A")]
			tampered := refs[1][:len(refs[1])-1] + last
			_, parsed := parsePBKDF2(tampered)
			require.True(t, parsed)
			require.False(t, h.Verify(tampered, "Example-pass-2026"))

			// 迭代次数不同的哈希器照样核对得上（按哈希里写的算），只是认为它该重算
			small := NewHasher(Params{Algorithm: algo, Iterations: 50})
			require.True(t, small.Verify(hash, "Example-pass-2026"))
			require.True(t, small.NeedsRehash(hash))
			mine, err := small.Hash("Example-pass-2026")
			require.NoError(t, err)
			require.Contains(t, mine, "$i=50,l=32$")
			require.True(t, small.Verify(mine, "Example-pass-2026"))
			require.False(t, small.Verify(mine, "Example-pass-2027"))
			require.True(t, h.Verify(mine, "Example-pass-2026"))
			require.True(t, h.NeedsRehash(mine))
			require.False(t, small.NeedsRehash(mine))
			// 次数一样、只是盐或输出的长度和自己生成的不一样：照常核对，也该重算
			v, _ := pbkdf2For(algo)
			for name, k := range map[string]pbkdf2Hash{
				"盐 24 字节":  {v: v, iterations: 50, salt: make([]byte, 24)},
				"输出 16 字节": {v: v, iterations: 50, salt: make([]byte, saltLen)},
			} {
				n := map[bool]int{true: keyLen, false: 16}[len(k.salt) != saltLen]
				k.key, err = k.compute("Example-pass-2026", n)
				require.NoError(t, err)
				require.True(t, small.Verify(k.String(), "Example-pass-2026"), name)
				require.True(t, small.NeedsRehash(k.String()), name)
			}
			again, err := small.Hash("Example-pass-2026")
			require.NoError(t, err)
			require.NotEqual(t, mine, again, "同一个密码每次的盐不同，哈希也不同")

			// 次数不合法的哈希器用生产用的次数
			maxIter := map[Algorithm]uint32{PBKDF2SHA256: 2000000, PBKDF2SHA512: 1000000}[algo]
			for _, n := range []uint32{0, maxIter + 1} {
				require.Equal(t, ParamsFor(algo), Params{Algorithm: algo, Iterations: n}.normalized(), "次数 %d", n)
			}
			require.Equal(t, maxIter, Params{Algorithm: algo, Iterations: maxIter}.normalized().Iterations)
			// 别的算法的参数不起作用
			require.Equal(t, Params{Algorithm: algo, Iterations: 50},
				NewHasher(Params{Algorithm: algo, Iterations: 50, Memory: 64, Time: 1, Cost: 4}).p)
			_, err = small.Hash(strings.Repeat("a", 73))
			require.ErrorIs(t, err, ErrTooLong)
		})
	}
}

// 规范 §13.2 第 105 条（D-058）在选 PBKDF2 时同样成立：超长的密码、没有密码的账号、看不懂的哈希、次数超出上限的哈希，
// 失败前都做一次假比较，花的时间和一次真实比较相当；假比较用的是配置的那种算法、当前的次数。
func TestPBKDF2_105_DummyTakesAsLongAsRealCompare(t *testing.T) {
	for _, algo := range pbkdf2Algorithms() {
		t.Run(string(algo), func(t *testing.T) {
			const iterations = 100000 // 比生产用的低，一次十几毫秒：量得稳，带竞争检测时也不太慢
			h := NewHasher(Params{Algorithm: algo, Iterations: iterations})
			hash, err := h.Hash("Example-pass-2026")
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
			genuine := timeIt(func() { h.Verify(hash, "wrong-password-9") })
			require.Greater(t, unknown, genuine/3, "假比较：%v，真实比较：%v", unknown, genuine)
			require.Less(t, unknown, genuine*3, "假比较：%v，真实比较：%v", unknown, genuine)
			for name, bad := range map[string]func() bool{
				"超长密码":    func() bool { return h.Verify(hash, long) },
				"没有密码的账号": func() bool { return h.Verify("", "Example-pass-2026") },
				"看不懂的哈希":  func() bool { return h.Verify("not-a-hash", "Example-pass-2026") },
				"次数超出上限的哈希": func() bool {
					return h.Verify(strings.Replace(hash, fmt.Sprintf("i=%d", iterations), "i=4000000000", 1), "Example-pass-2026")
				},
			} {
				require.False(t, bad(), name)
				d := timeIt(func() { bad() })
				require.Greater(t, d, unknown/3, "%s：%v，不存在的账号：%v", name, d, unknown)
				require.Less(t, d, unknown*20+time.Second, "%s：%v，没有按哈希里写的参数去算", name, d)
			}
		})
	}
}

// 规范 §13.2 第 161 条（D-072）：库里读出来的 PBKDF2 哈希，格式不规范、摘要算法不认识、参数或长度超出范围的一律不算
// （不匹配、也不重算）。范围：迭代 1～200 万次（SHA-512 是 100 万），盐 16～64 字节，输出 16 字节到摘要的长度而且和 l 一致。
func TestPBKDF2_161_MalformedAndOversizedRejected(t *testing.T) {
	for _, algo := range pbkdf2Algorithms() {
		t.Run(string(algo), func(t *testing.T) {
			h := NewHasher(Params{Algorithm: algo, Iterations: 50})
			good, err := h.Hash("Example-pass-2026")
			require.NoError(t, err)
			require.True(t, h.Verify(good, "Example-pass-2026"))
			parts := strings.Split(good, "$") // "", pbkdf2-sha256, i=50,l=32, 盐, 输出
			build := func(mut func(p []string)) string {
				p := append([]string{}, parts...)
				mut(p)
				return strings.Join(p, "$")
			}
			size := map[Algorithm]int{PBKDF2SHA256: 32, PBKDF2SHA512: 64}[algo]
			maxIter := map[Algorithm]int{PBKDF2SHA256: 2000000, PBKDF2SHA512: 1000000}[algo]
			// n 个全零字节的不带填充的 base64
			zeros := func(n int) string { return strings.Repeat("A", (n*4+2)/3) }
			cases := map[string]string{
				"SHA-1 的":        build(func(p []string) { p[1] = "pbkdf2-sha1" }),
				"没写摘要算法":         build(func(p []string) { p[1] = "pbkdf2" }),
				"SHA-384 的":      build(func(p []string) { p[1] = "pbkdf2-sha384" }),
				"大写的算法名":         build(func(p []string) { p[1] = strings.ToUpper(p[1]) }),
				"别的实现的另一种写法":     build(func(p []string) { p[2] = "50" }),
				"多一段":            good + "$x",
				"少一段":            strings.Join(parts[:4], "$"),
				"结尾多一个 $":        good + "$",
				"前面有空白":          " " + good,
				"前面多一段":          "x" + good,
				"参数顺序不对":         build(func(p []string) { p[2] = "l=32,i=50" }),
				"少一个参数":          build(func(p []string) { p[2] = "i=50" }),
				"多一个参数":          build(func(p []string) { p[2] = "i=50,l=32,x=1" }),
				"前导零":            build(func(p []string) { p[2] = "i=050,l=32" }),
				"正号":             build(func(p []string) { p[2] = "i=+50,l=32" }),
				"负数":             build(func(p []string) { p[2] = "i=-50,l=32" }),
				"十六进制":           build(func(p []string) { p[2] = "i=0x32,l=32" }),
				"带空格":            build(func(p []string) { p[2] = "i=50, l=32" }),
				"空的数字":           build(func(p []string) { p[2] = "i=,l=32" }),
				"超出 32 位":        build(func(p []string) { p[2] = "i=4294967296,l=32" }),
				"次数 0":           build(func(p []string) { p[2] = "i=0,l=32" }),
				"次数刚过上限":         build(func(p []string) { p[2] = fmt.Sprintf("i=%d,l=32", maxIter+1) }),
				"次数很大":           build(func(p []string) { p[2] = "i=4000000000,l=32" }),
				"l 比实际的输出短":      build(func(p []string) { p[2] = "i=50,l=31" }),
				"l 比实际的输出长":      build(func(p []string) { p[2] = "i=50,l=33" }),
				"l 是 0":          build(func(p []string) { p[2] = "i=50,l=0" }),
				"盐里有换行":          build(func(p []string) { p[3] = p[3][:8] + "\n" + p[3][8:] }),
				"结尾有换行":          good + "\n",
				"输出里有回车换行":       build(func(p []string) { p[4] = p[4][:20] + "\r\n" + p[4][20:] }),
				"盐 15 字节":        build(func(p []string) { p[3] = "AAECAwQFBgcICQoLDA0O" }),
				"盐 8 字节":         build(func(p []string) { p[3] = "AAECAwQFBgc" }),
				"盐 65 字节":        build(func(p []string) { p[3] = zeros(65) }),
				"盐为空":            build(func(p []string) { p[3] = "" }),
				"输出太短":           build(func(p []string) { p[2], p[4] = "i=50,l=15", "AAECAwQFBgcICQoLDA0O" }),
				"输出比摘要长":         build(func(p []string) { p[2], p[4] = fmt.Sprintf("i=50,l=%d", size+1), zeros(size+1) }),
				"输出为空":           build(func(p []string) { p[2], p[4] = "i=50,l=0", "" }),
				"带填充的 base64":    build(func(p []string) { p[4] += "=" }),
				"URL 字母表":        build(func(p []string) { p[3] = "AAECAwQFBgcICQoLDA0_-w" }),
				"base64 里有非法字符":  build(func(p []string) { p[3] = "AAECAwQFBgcICQoLDA0O!!" }),
				"base64 末尾多余的位":  build(func(p []string) { p[3] = "AAECAwQFBgcICQoLDA0ODx" }),
				"Argon2id 的参数写法": build(func(p []string) { p[2] = "v=19" }),
				"次数的名字不对":        build(func(p []string) { p[2] = "t=50,l=32" }),
				"长度的名字不对":        build(func(p []string) { p[2] = "i=50,x=32" }),
				"名字是大写":          build(func(p []string) { p[2] = "I=50,L=32" }),
			}
			// 不规范的 base64：最后一个字符里用不到的那几位不是 0。宽松的解码器会解出同样的字节（于是核对得上），
			// 这里按严格的来：同一份哈希只有一种写法
			const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
			flipLastBit := func(b64 string) string {
				i := strings.IndexByte(alphabet, b64[len(b64)-1])
				require.GreaterOrEqual(t, i, 0)
				return b64[:len(b64)-1] + string(alphabet[i^1])
			}
			cases["盐的 base64 末尾有多余的位"] = build(func(p []string) { p[3] = flipLastBit(p[3]) })
			cases["输出的 base64 末尾有多余的位"] = build(func(p []string) { p[4] = flipLastBit(p[4]) })
			for name, hash := range cases {
				start := time.Now()
				require.False(t, h.Verify(hash, "Example-pass-2026"), "%s: %s", name, hash)
				require.Less(t, time.Since(start), time.Second, "%s：不该按哈希里写的参数去算", name)
				require.False(t, h.NeedsRehash(hash), "%s: %s", name, hash)
				// 直接看解析：有的写法换掉了盐，解析放过了也核对不上、长度也对，上面三条看不出来
				_, parsed := parsePBKDF2(hash)
				require.False(t, parsed, "%s: %s", name, hash)
			}
			// 范围以内的照常解析：边界值本身是合法的
			for name, ok := range map[string]string{
				"次数 1":     build(func(p []string) { p[2] = "i=1,l=32" }),
				"次数正好是上限":  build(func(p []string) { p[2] = fmt.Sprintf("i=%d,l=32", maxIter) }),
				"盐 64 字节":  build(func(p []string) { p[3] = zeros(64) }),
				"输出 16 字节": build(func(p []string) { p[2], p[4] = "i=50,l=16", "AAECAwQFBgcICQoLDA0ODw" }),
				"输出等于摘要长度": build(func(p []string) { p[2], p[4] = fmt.Sprintf("i=50,l=%d", size), zeros(size) }),
			} {
				_, parsed := parsePBKDF2(ok)
				require.True(t, parsed, "%s: %s", name, ok)
			}
		})
	}
	// SHA-256 的输出不能超过 32 字节，SHA-512 的可以到 64：把一份 64 字节输出的 SHA-512 哈希改成 SHA-256 的名字，不认
	wide := pbkdf2Refs()[PBKDF2SHA512][2]
	_, ok := parsePBKDF2(wide)
	require.True(t, ok)
	_, ok = parsePBKDF2(strings.Replace(wide, "sha512", "sha256", 1))
	require.False(t, ok)
}

// 规范 §13.2 第 161 条（D-072）：四种算法两两之间——哪个哈希器都核对得了别的算法的哈希；"该不该重算"按选的算法
// 判断，不是自己选的那种（PBKDF2 的两种之间也算）就该重算。所以怎么换配置，已有的密码都不失效。
func TestAlgorithmChoice_161_EveryPair(t *testing.T) {
	require.Equal(t, []Algorithm{Argon2id, Bcrypt, PBKDF2SHA256, PBKDF2SHA512}, Algorithms())
	hashers := map[Algorithm]*Hasher{
		Argon2id:     NewHasher(fast),
		Bcrypt:       NewHasher(Params{Algorithm: Bcrypt, Cost: 4}),
		PBKDF2SHA256: NewHasher(Params{Algorithm: PBKDF2SHA256, Iterations: 50}),
		PBKDF2SHA512: NewHasher(Params{Algorithm: PBKDF2SHA512, Iterations: 50}),
	}
	require.Len(t, hashers, len(Algorithms()))
	prefix := map[Algorithm]string{Argon2id: "$argon2id$", Bcrypt: "$2a$04$", PBKDF2SHA256: "$pbkdf2-sha256$", PBKDF2SHA512: "$pbkdf2-sha512$"}
	hashes := map[Algorithm]string{}
	for _, a := range Algorithms() {
		require.Equal(t, a, hashers[a].Algorithm())
		hash, err := hashers[a].Hash("Example-pass-2026")
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(hash, prefix[a]), "%s: %s", a, hash)
		hashes[a] = hash
		// 假比较不 panic；空的、看不懂的不匹配、不重算
		hashers[a].VerifyDummy(strings.Repeat("密", 25))
		require.False(t, hashers[a].Verify("", "x"))
		require.False(t, hashers[a].NeedsRehash(""))
		require.False(t, hashers[a].NeedsRehash("not-a-hash"))
	}
	for _, mine := range Algorithms() {
		for _, theirs := range Algorithms() {
			h, hash := hashers[mine], hashes[theirs]
			require.True(t, h.Verify(hash, "Example-pass-2026"), "选 %s 的核对 %s 的哈希", mine, theirs)
			require.False(t, h.Verify(hash, "Example-pass-2027"), "选 %s 的核对 %s 的哈希", mine, theirs)
			require.Equal(t, mine != theirs, h.NeedsRehash(hash), "选 %s 的看 %s 的哈希", mine, theirs)
		}
	}
}

const fipsChildEnv = "GA_TEST_FIPS_ONLY_CHILD"

// 规范 §13.2 第 161 条（D-072）：PBKDF2 的两种在 Go 的"只许批准的算法"模式（GODEBUG=fips140=only）下能生成、核对、
// 做假比较，另一套实现的哈希也核对得上——选它们的理由就是给有 FIPS 140 要求的部署用。这个模式只能在进程启动时打开，
// 所以另起一个进程跑同一个测试。以后有人把盐、输出改短，或者换成没批准的摘要算法，这条会挂。
func TestPBKDF2_161_WorksInFIPSOnlyMode(t *testing.T) {
	if os.Getenv(fipsChildEnv) == "1" {
		fipsOnlyChild(t)
		return
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPBKDF2_161_WorksInFIPSOnlyMode$", "-test.v") //nolint:gosec // 测试程序自己
	cmd.Env = append(os.Environ(), fipsChildEnv+"=1", "GODEBUG=fips140=only")
	out, err := cmd.CombinedOutput()
	// 有的平台、有的构建方式进不了 FIPS 模式（Go 在进程启动时就报错退出）：那里没法验，跳过
	if err != nil && (strings.Contains(string(out), "FIPS 140-3 mode is not supported") ||
		strings.Contains(string(out), "FIPS 140-3 mode is incompatible")) {
		t.Skipf("这个平台或构建方式不支持 FIPS 模式（%s/%s）", runtime.GOOS, runtime.GOARCH)
	}
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "fips-only-child: ok", "子进程没有真的跑到检查")
}

func fipsOnlyChild(t *testing.T) {
	require.True(t, fips140.Enabled(), "子进程没有进入 FIPS 模式")
	// 确认"只许批准的算法"真的在起作用：盐不到 16 字节的 PBKDF2 在这个模式下被拒绝
	_, err := pbkdf2.Key(sha256.New, "x", make([]byte, 8), 1, 32)
	require.Error(t, err, "fips140=only 没有生效")

	for _, algo := range pbkdf2Algorithms() {
		// 这个模式管的是盐和输出的长度、摘要算法，和迭代次数无关：次数调低，只拿一条生产参数的哈希核对
		h := NewHasher(Params{Algorithm: algo, Iterations: 1000}) // 假哈希在这里算，出错会 panic
		hash, err := h.Hash("Example-pass-2026")
		require.NoError(t, err, algo)
		require.True(t, h.Verify(hash, "Example-pass-2026"), algo)
		require.False(t, h.Verify(hash, "Example-pass-2027"), algo)
		require.False(t, h.NeedsRehash(hash), algo)
		h.VerifyDummy("Example-pass-2026")
		for _, ref := range pbkdf2Refs()[algo] {
			require.True(t, h.Verify(ref, "Example-pass-2026"), ref)
		}
	}
	// 认得出来的 PBKDF2 哈希都算得了：范围的边界（盐 16 字节、输出 16 字节）在这个模式下不被拒绝
	edge := pbkdf2Hash{iterations: 1, salt: make([]byte, minPBKDF2SaltLen)}
	edge.v, _ = pbkdf2For(PBKDF2SHA256)
	key, err := edge.compute("Example-pass-2026", 16)
	require.NoError(t, err)
	edge.key = key
	parsed, ok := parsePBKDF2(edge.String())
	require.True(t, ok)
	got, err := parsed.compute("Example-pass-2026", len(parsed.key))
	require.NoError(t, err)
	require.Equal(t, key, got)
	t.Log("fips-only-child: ok")
}
