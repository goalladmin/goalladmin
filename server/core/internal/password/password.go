// Package password 负责密码哈希与密码策略。
package password

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// DefaultCost 是 bcrypt 的默认代价（规范 §5.6）。测试可以调低。
const DefaultCost = 12

// MaxBytes 是 bcrypt 能处理的明文上限。
const MaxBytes = 72

// Hasher 用固定代价做哈希与校验。
type Hasher struct {
	cost  int
	dummy string // 用于账号不存在时也做一次比较，抹平耗时差异
}

// NewHasher 创建哈希器；cost 不在合法范围时用 DefaultCost。
func NewHasher(cost int) *Hasher {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		cost = DefaultCost
	}
	h := &Hasher{cost: cost}
	dummy, err := bcrypt.GenerateFromPassword([]byte("goalladmin-dummy-password"), cost)
	if err != nil {
		panic(err)
	}
	h.dummy = string(dummy)
	return h
}

// ErrTooLong 表示明文超过 72 字节。
var ErrTooLong = fmt.Errorf("password: 明文超过 %d 字节", MaxBytes)

// Hash 生成哈希。
func (h *Hasher) Hash(plain string) (string, error) {
	if len(plain) > MaxBytes {
		return "", ErrTooLong
	}
	out, err := bcrypt.GenerateFromPassword([]byte(plain), h.cost)
	if err != nil {
		return "", fmt.Errorf("password: hash: %w", err)
	}
	return string(out), nil
}

// Verify 比较明文与哈希。明文超过 72 字节、账号没有密码时照样花一次比较的时间再返回 false（D-058）：
// 否则"账号存在"比"账号不存在"（VerifyDummy 总会做一次比较）返回得快得多，一次请求就能试出账号在不在。
func (h *Hasher) Verify(hash, plain string) bool {
	if len(plain) > MaxBytes || hash == "" {
		h.VerifyDummy(plain)
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// VerifyDummy 对一个假哈希做一次比较，耗时与真实比较相当。账号不存在时调用。
func (h *Hasher) VerifyDummy(plain string) {
	if len(plain) > MaxBytes {
		plain = plain[:MaxBytes]
	}
	_ = bcrypt.CompareHashAndPassword([]byte(h.dummy), []byte(plain))
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
