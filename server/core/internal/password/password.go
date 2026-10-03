// Package password 负责密码哈希与密码策略。新哈希用 Argon2id（默认）、bcrypt 或 PBKDF2（HMAC-SHA256、HMAC-SHA512），
// 部署时选一种；核对时这几种都认（D-070、D-072）。
package password

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// MaxBytes 是密码明文的长度上限：bcrypt 只认前 72 字节，接口上的校验一直是它；选 Argon2id 时也不放开，
// 几种算法之间来回切换不该让一部分密码失效（D-070）。
const MaxBytes = 72

// Algorithm 是生成新哈希用的算法，部署时选一种（配置 server.passwordHash，D-070、D-072）。核对不看这个配置：
// 几种算法的哈希都认，按哈希开头分辨。
type Algorithm string

const (
	Argon2id Algorithm = "argon2id" // 默认
	Bcrypt   Algorithm = "bcrypt"
	// PBKDF2 的两种：给有 FIPS 140 要求的部署，只用标准库（D-072）
	PBKDF2SHA256 Algorithm = "pbkdf2-sha256"
	PBKDF2SHA512 Algorithm = "pbkdf2-sha512"
)

// Algorithms 返回可选的算法，默认的在前。
func Algorithms() []Algorithm { return []Algorithm{Argon2id, Bcrypt, PBKDF2SHA256, PBKDF2SHA512} }

// DefaultBcryptCost 是选 bcrypt 时的代价（规范 §5.6）。
const DefaultBcryptCost = 12

// 选 PBKDF2 时的迭代次数（规范 §5.6、D-072）：两种摘要算一次的快慢不同，次数也就不同，花的时间差不多。
const (
	DefaultPBKDF2SHA256Iterations = 600000
	DefaultPBKDF2SHA512Iterations = 220000
)

// pbkdf2Variant 是 PBKDF2 的一种：用哪个摘要算法、生产用的迭代次数。算法的名字也是哈希字符串开头的标识
// （$pbkdf2-sha256$…）。
type pbkdf2Variant struct {
	algo       Algorithm
	newHash    func() hash.Hash
	size       int    // 摘要的字节数：输出不超过它（再长计算量翻倍、安全性不增加）
	iterations uint32 // 生产用的次数
	// 库里读出来的哈希允许的最大次数（D-072 第 4 条）：算一次在一秒以内。SHA-512 迭代一次比 SHA-256 慢，上限也就低
	maxIterations uint32
}

// pbkdf2For 返回某种 PBKDF2 的定义；a 不是 PBKDF2 时返回 false。加一种摘要算法只改这里和 Algorithms。
func pbkdf2For(a Algorithm) (pbkdf2Variant, bool) {
	switch a {
	case PBKDF2SHA256:
		return pbkdf2Variant{algo: a, newHash: sha256.New, size: sha256.Size,
			iterations: DefaultPBKDF2SHA256Iterations, maxIterations: 2000000}, true
	case PBKDF2SHA512:
		return pbkdf2Variant{algo: a, newHash: sha512.New, size: sha512.Size,
			iterations: DefaultPBKDF2SHA512Iterations, maxIterations: 1000000}, true
	default:
		return pbkdf2Variant{}, false
	}
}

// 自己生成的哈希的固定部分（D-070、D-072）：盐 16 字节、输出 32 字节；Argon2id 的并行度 1。
const (
	saltLen = 16
	keyLen  = 32
	threads = 1

	argonPrefix = "$argon2id$"
)

// 库里读出来的哈希允许的参数上限（D-070 第 4 条）：超出的不算，按"没有可用的密码"处理。
// 一条被改过的记录不能拖着进程去占几个 G 的内存、算上几分钟。上限比自己生成的（19 MiB、2 轮、并行度 1）宽，
// 够得着别的常见实现的默认参数（64 MiB、3～4 轮、并行度 1～4）：从别的系统导入的哈希也核对得了。
const (
	maxMemory     = 64 * 1024 // KiB
	maxTime       = 8
	maxThreads    = 4
	maxSaltLen    = 64
	maxKeyLen     = 64
	maxBcryptCost = 14
)

// PBKDF2 哈希的盐至少 16 字节（D-072 第 4 条）——批准用法的下限，所以认得出来的哈希在 FIPS 模式下都算得了。
// 迭代次数、输出长度的上限按摘要算法定，见 pbkdf2Variant。
const minPBKDF2SaltLen = 16

// Params 是生成新哈希的算法和参数。Memory、Time 是 Argon2id 的（并行度固定为 1），Cost 是 bcrypt 的，
// Iterations 是 PBKDF2 的；只有 Algorithm 选中的那一组起作用。
type Params struct {
	Algorithm  Algorithm // 空表示 Argon2id
	Memory     uint32    // Argon2id：内存，单位 KiB
	Time       uint32    // Argon2id：轮数
	Cost       int       // bcrypt：代价
	Iterations uint32    // PBKDF2：迭代次数
}

// DefaultParams 是生产用的默认参数：Argon2id，19 MiB、2 轮（规范 §5.6）。
func DefaultParams() Params { return ParamsFor(Argon2id) }

// ParamsFor 返回某种算法生产用的参数：Argon2id 是 19 MiB、2 轮，bcrypt 是 cost 12，PBKDF2 是 60 万次（SHA-256）
// 或 22 万次（SHA-512）。不认识的算法按 Argon2id。
func ParamsFor(a Algorithm) Params {
	if v, ok := pbkdf2For(a); ok {
		return Params{Algorithm: a, Iterations: v.iterations}
	}
	if a == Bcrypt {
		return Params{Algorithm: Bcrypt, Cost: DefaultBcryptCost}
	}
	return Params{Algorithm: Argon2id, Memory: 19456, Time: 2}
}

// normalized 把不合法的部分换成默认值：不认识的算法按 Argon2id；选中的那组参数不在合法范围时用生产用的参数。
func (p Params) normalized() Params {
	if v, ok := pbkdf2For(p.Algorithm); ok {
		if p.Iterations < 1 || p.Iterations > v.maxIterations {
			return ParamsFor(p.Algorithm)
		}
		return Params{Algorithm: p.Algorithm, Iterations: p.Iterations}
	}
	if p.Algorithm == Bcrypt {
		if p.Cost < bcrypt.MinCost || p.Cost > maxBcryptCost {
			return ParamsFor(Bcrypt)
		}
		return Params{Algorithm: Bcrypt, Cost: p.Cost}
	}
	if p.Memory < 8*threads || p.Memory > maxMemory || p.Time < 1 || p.Time > maxTime {
		return ParamsFor(Argon2id)
	}
	return Params{Algorithm: Argon2id, Memory: p.Memory, Time: p.Time}
}

// Hasher 按配置的算法（Argon2id、bcrypt 或 PBKDF2）生成哈希；核对时按哈希开头认算法，哪种都认（D-070、D-072）。
type Hasher struct {
	p Params
	// 用于账号不存在时也做一次比较，抹平耗时差异：和新哈希同一种算法、同样的参数
	dummyArgon  argonHash
	dummyBcrypt []byte
	dummyPBKDF2 pbkdf2Hash
}

// 假哈希的盐固定就行：它不保护任何密码，只用来花掉和真实比较一样的时间。16 字节：PBKDF2 在 FIPS 模式下的下限。
const (
	dummySalt  = "goalladmin-dummy"
	dummyPlain = "goalladmin-dummy-password"
)

// NewHasher 创建哈希器；不合法的参数按默认值处理（见 Params.normalized）。
func NewHasher(p Params) *Hasher {
	h := &Hasher{p: p.normalized()}
	if v, ok := pbkdf2For(h.p.Algorithm); ok {
		h.dummyPBKDF2 = pbkdf2Hash{v: v, iterations: h.p.Iterations, salt: []byte(dummySalt)}
		key, err := h.dummyPBKDF2.compute(dummyPlain, keyLen)
		if err != nil {
			panic(err) // 盐 16 字节、输出 32 字节、SHA-2：FIPS 模式下也在允许的范围内，不会出错
		}
		h.dummyPBKDF2.key = key
		return h
	}
	if h.p.Algorithm == Bcrypt {
		dummy, err := bcrypt.GenerateFromPassword([]byte(dummyPlain), h.p.Cost)
		if err != nil {
			panic(err) // cost 已经在合法范围内、明文不超长：不会出错
		}
		h.dummyBcrypt = dummy
		return h
	}
	h.dummyArgon = argonHash{memory: h.p.Memory, time: h.p.Time, threads: threads, salt: []byte(dummySalt)}
	h.dummyArgon.key = h.dummyArgon.compute(dummyPlain, keyLen)
	return h
}

// Algorithm 返回生成新哈希用的算法。
func (h *Hasher) Algorithm() Algorithm { return h.p.Algorithm }

// ErrTooLong 表示明文超过 72 字节。
var ErrTooLong = fmt.Errorf("password: 明文超过 %d 字节", MaxBytes)

// Hash 按配置的算法生成哈希。Argon2id：$argon2id$v=19$m=<内存>,t=<轮数>,p=<并行度>$<盐>$<输出>；bcrypt：$2a$<cost>$…；
// PBKDF2：$pbkdf2-sha256$i=<迭代次数>,l=<输出字节数>$<盐>$<输出>（SHA-512 的开头是 $pbkdf2-sha512$）。
func (h *Hasher) Hash(plain string) (string, error) {
	if len(plain) > MaxBytes {
		return "", ErrTooLong
	}
	if h.p.Algorithm == Bcrypt {
		out, err := bcrypt.GenerateFromPassword([]byte(plain), h.p.Cost)
		if err != nil {
			return "", fmt.Errorf("password: hash: %w", err)
		}
		return string(out), nil
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: hash: %w", err)
	}
	if v, ok := pbkdf2For(h.p.Algorithm); ok {
		k := pbkdf2Hash{v: v, iterations: h.p.Iterations, salt: salt}
		key, err := k.compute(plain, keyLen)
		if err != nil {
			return "", fmt.Errorf("password: hash: %w", err)
		}
		k.key = key
		return k.String(), nil
	}
	a := argonHash{memory: h.p.Memory, time: h.p.Time, threads: threads, salt: salt}
	a.key = a.compute(plain, keyLen)
	return a.String(), nil
}

// Verify 比较明文与哈希，每种算法的哈希都认。明文超过 72 字节、账号没有密码、哈希看不懂或参数超出上限时，照样花一次
// 比较的时间再返回 false（D-058）：否则"账号存在"比"账号不存在"（VerifyDummy 总会做一次比较）返回得快得多，
// 一次请求就能试出账号在不在。
func (h *Hasher) Verify(hash, plain string) bool {
	if len(plain) > MaxBytes || hash == "" {
		h.VerifyDummy(plain)
		return false
	}
	if a, ok := parseArgon(hash); ok {
		return subtle.ConstantTimeCompare(a.compute(plain, len(a.key)), a.key) == 1
	}
	if _, ok := bcryptCost(hash); ok {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
	}
	if k, ok := parsePBKDF2(hash); ok {
		// 解析时限过参数，算不出来的情况不该有；万一有，当作不匹配，不把错误往外带
		got, err := k.compute(plain, len(k.key))
		return err == nil && subtle.ConstantTimeCompare(got, k.key) == 1
	}
	h.VerifyDummy(plain)
	return false
}

// VerifyDummy 对一个假哈希做一次比较，耗时与一次真实比较（配置的算法、当前参数）相当。账号不存在时调用。
func (h *Hasher) VerifyDummy(plain string) {
	if len(plain) > MaxBytes {
		plain = plain[:MaxBytes]
	}
	switch {
	case h.dummyPBKDF2.key != nil:
		got, _ := h.dummyPBKDF2.compute(plain, len(h.dummyPBKDF2.key))
		_ = subtle.ConstantTimeCompare(got, h.dummyPBKDF2.key)
	case h.p.Algorithm == Bcrypt:
		_ = bcrypt.CompareHashAndPassword(h.dummyBcrypt, []byte(plain))
	default:
		_ = subtle.ConstantTimeCompare(h.dummyArgon.compute(plain, len(h.dummyArgon.key)), h.dummyArgon.key)
	}
}

// NeedsRehash 报告一份能核对的哈希是不是该按配置的算法和当前参数重算（D-070 第 3 条、D-072）：算法不是配置的那种，或者参数、
// 长度和当前的不一样。空的、看不懂的、参数超出上限的返回 false——它们核对不过，也就没有明文可以重算。
func (h *Hasher) NeedsRehash(hash string) bool {
	if a, ok := parseArgon(hash); ok {
		return h.p.Algorithm != Argon2id ||
			a.memory != h.p.Memory || a.time != h.p.Time || a.threads != threads || len(a.salt) != saltLen || len(a.key) != keyLen
	}
	if cost, ok := bcryptCost(hash); ok {
		return h.p.Algorithm != Bcrypt || cost != h.p.Cost
	}
	if k, ok := parsePBKDF2(hash); ok {
		return h.p.Algorithm != k.v.algo || k.iterations != h.p.Iterations || len(k.salt) != saltLen || len(k.key) != keyLen
	}
	return false
}

// argonHash 是一份解析出来的 Argon2id 哈希。
type argonHash struct {
	memory, time uint32
	threads      uint8
	salt, key    []byte
}

func (a argonHash) compute(plain string, n int) []byte {
	return argon2.IDKey([]byte(plain), a.salt, a.time, a.memory, a.threads, uint32(n)) //nolint:gosec // n 是 keyLen 或解析时限过长度的值
}

func (a argonHash) String() string {
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s", argonPrefix, argon2.Version, a.memory, a.time, a.threads,
		base64.RawStdEncoding.EncodeToString(a.salt), base64.RawStdEncoding.EncodeToString(a.key))
}

// parseArgon 解析 Hash 写出来的那种字符串。格式不对、版本不认识、参数或长度超出上限都返回 false。
// 数字必须是规范写法（没有正负号、前导零），base64 不带填充：同一份哈希只有一种写法。
func parseArgon(s string) (argonHash, bool) {
	var a argonHash
	// 换行：Go 的 base64 解码会跳过 \r、\n，带着它们的哈希是同一份内容的另一种写法，不认
	if !strings.HasPrefix(s, argonPrefix) || strings.ContainsAny(s, "\r\n") {
		return a, false
	}
	parts := strings.Split(s, "$") // "", "argon2id", "v=19", "m=..,t=..,p=..", 盐, 输出
	if len(parts) != 6 || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return a, false
	}
	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return a, false
	}
	var nums [3]uint32
	for i, name := range []string{"m=", "t=", "p="} {
		v, ok := strings.CutPrefix(kv[i], name)
		if !ok {
			return a, false
		}
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != v {
			return a, false
		}
		nums[i] = uint32(n)
	}
	m, t, p := nums[0], nums[1], nums[2]
	if p < 1 || p > maxThreads || t < 1 || t > maxTime || m < 8*p || m > maxMemory {
		return a, false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > maxSaltLen {
		return a, false
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > maxKeyLen {
		return a, false
	}
	return argonHash{memory: m, time: t, threads: uint8(p), salt: salt, key: key}, true
}

// pbkdf2Hash 是一份解析出来的 PBKDF2 哈希。
type pbkdf2Hash struct {
	v          pbkdf2Variant
	iterations uint32
	salt, key  []byte
}

// compute 只用标准库：按 FIPS 模式构建、运行时走的是 Go 自带的密码模块（D-072）。
func (k pbkdf2Hash) compute(plain string, n int) ([]byte, error) {
	return pbkdf2.Key(k.v.newHash, plain, k.salt, int(k.iterations), n)
}

func (k pbkdf2Hash) String() string {
	return fmt.Sprintf("$%s$i=%d,l=%d$%s$%s", k.v.algo, k.iterations, len(k.key),
		base64.RawStdEncoding.EncodeToString(k.salt), base64.RawStdEncoding.EncodeToString(k.key))
}

// parsePBKDF2 解析 Hash 写出来的那种字符串。开头不是认识的那几种、格式不对、参数或长度超出范围、l 和输出的实际长度
// 不一致都返回 false。写法的规则和 parseArgon 一样：数字是规范写法，base64 不带填充、不带换行。
func parsePBKDF2(s string) (pbkdf2Hash, bool) {
	var k pbkdf2Hash
	if strings.ContainsAny(s, "\r\n") {
		return k, false
	}
	parts := strings.Split(s, "$") // "", "pbkdf2-sha256", "i=..,l=..", 盐, 输出
	if len(parts) != 5 || parts[0] != "" {
		return k, false
	}
	v, ok := pbkdf2For(Algorithm(parts[1]))
	if !ok {
		return k, false
	}
	kv := strings.Split(parts[2], ",")
	if len(kv) != 2 {
		return k, false
	}
	var nums [2]uint32
	for i, name := range []string{"i=", "l="} {
		val, ok := strings.CutPrefix(kv[i], name)
		if !ok {
			return k, false
		}
		n, err := strconv.ParseUint(val, 10, 32)
		if err != nil || strconv.FormatUint(n, 10) != val {
			return k, false
		}
		nums[i] = uint32(n)
	}
	iter, l := nums[0], nums[1]
	if iter < 1 || iter > v.maxIterations {
		return k, false
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[3])
	if err != nil || len(salt) < minPBKDF2SaltLen || len(salt) > maxSaltLen {
		return k, false
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(key) < 16 || len(key) > v.size || uint32(len(key)) != l { //nolint:gosec // 长度已限在摘要长度以内
		return k, false
	}
	return pbkdf2Hash{v: v, iterations: iter, salt: salt, key: key}, true
}

// bcryptCost 报告 s 是不是一份可以核对的 bcrypt 哈希，并返回它的 cost：格式对、cost 不超过上限。
func bcryptCost(s string) (int, bool) {
	if !strings.HasPrefix(s, "$2a$") && !strings.HasPrefix(s, "$2b$") && !strings.HasPrefix(s, "$2y$") {
		return 0, false
	}
	cost, err := bcrypt.Cost([]byte(s))
	if err != nil || cost > maxBcryptCost {
		return 0, false
	}
	return cost, true
}

// Policy 是密码策略。字母加数字始终要求；大写、小写、符号按需加。
type Policy struct {
	MinLength     int // 默认 10
	RequireUpper  bool
	RequireLower  bool
	RequireSymbol bool
}

// DefaultPolicy 返回规范里的默认策略。
func DefaultPolicy() Policy { return Policy{MinLength: 10} }

// 策略错误。
var (
	ErrTooShort        = errors.New("password: 长度不足")
	ErrNoLetter        = errors.New("password: 必须包含字母")
	ErrNoDigit         = errors.New("password: 必须包含数字")
	ErrNoUpper         = errors.New("password: 必须包含大写字母")
	ErrNoLower         = errors.New("password: 必须包含小写字母")
	ErrNoSymbol        = errors.New("password: 必须包含符号")
	ErrSameAsUsername  = errors.New("password: 不能与用户名相同")
	ErrSameAsPrevious  = errors.New("password: 不能与上一次相同")
	ErrContainsSpaces  = errors.New("password: 首尾不能有空白")
	ErrExceedsMaxBytes = ErrTooLong
)

// Check 校验新密码是否符合策略。prevHash 为空时跳过"与上一次相同"的检查。
func (p Policy) Check(h *Hasher, plain, username, prevHash string) error {
	if p.MinLength <= 0 {
		p.MinLength = 10
	}
	if len(plain) > MaxBytes {
		return ErrExceedsMaxBytes
	}
	if strings.TrimSpace(plain) != plain {
		return ErrContainsSpaces
	}
	if len([]rune(plain)) < p.MinLength {
		return ErrTooShort
	}
	var hasLetter, hasDigit, hasUpper, hasLower, hasSymbol bool
	for _, r := range plain {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
			hasUpper = hasUpper || unicode.IsUpper(r)
			hasLower = hasLower || unicode.IsLower(r)
		case unicode.IsDigit(r):
			hasDigit = true
		case !unicode.IsSpace(r):
			hasSymbol = true
		}
	}
	switch {
	case !hasLetter:
		return ErrNoLetter
	case !hasDigit:
		return ErrNoDigit
	case p.RequireUpper && !hasUpper:
		return ErrNoUpper
	case p.RequireLower && !hasLower:
		return ErrNoLower
	case p.RequireSymbol && !hasSymbol:
		return ErrNoSymbol
	}
	if username != "" && strings.EqualFold(plain, username) {
		return ErrSameAsUsername
	}
	if prevHash != "" && h.Verify(prevHash, plain) {
		return ErrSameAsPrevious
	}
	return nil
}

// Generate 生成随机密码：长度取 max(20, minLength)，大写、小写、数字、符号各至少一个，
// 因此满足任何合法的策略（D-024）。去掉了易混淆的字符。
func Generate(minLength int) (string, error) {
	const (
		upper  = "ABCDEFGHJKLMNPQRSTUVWXYZ"
		lower  = "abcdefghijkmnpqrstuvwxyz"
		digit  = "23456789"
		symbol = "!@#%^&*-_=+"
	)
	n := 20
	if minLength > n {
		n = minLength
	}
	all := upper + lower + digit + symbol
	strict := Policy{MinLength: n, RequireUpper: true, RequireLower: true, RequireSymbol: true}
	pick := func(set string) (byte, error) {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		if err != nil {
			return 0, err
		}
		return set[idx.Int64()], nil
	}
	for {
		b := make([]byte, n)
		for i := range b {
			c, err := pick(all)
			if err != nil {
				return "", err
			}
			b[i] = c
		}
		s := string(b)
		if strict.Check(nil, s, "", "") == nil {
			return s, nil
		}
	}
}
