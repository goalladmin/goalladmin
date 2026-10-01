// Package token 签发和校验访问令牌（JWT，HS256）。
package token

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Issuer 是令牌的 iss。
const Issuer = "goalladmin"

// Claims 是访问令牌的内容。角色和权限不进令牌（规范 §5.1）。
type Claims struct {
	UserID    uint64
	SessionID string
	Portal    string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// Signer 为某个端签发和校验令牌。
type Signer struct {
	portal string
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// New 创建签发器。secret 长度由配置校验保证；now 为 nil 时用 time.Now。
func New(portal string, secret []byte, ttl time.Duration, now func() time.Time) *Signer {
	if now == nil {
		now = time.Now
	}
	return &Signer{portal: portal, secret: secret, ttl: ttl, now: now}
}

// TTL 返回访问令牌有效期。
func (s *Signer) TTL() time.Duration { return s.ttl }

type jwtClaims struct {
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// Sign 签发令牌。
func (s *Signer) Sign(userID uint64, sid string) (string, Claims, error) {
	now := s.now().UTC()
	c := Claims{UserID: userID, SessionID: sid, Portal: s.portal, IssuedAt: now, ExpiresAt: now.Add(s.ttl)}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, jwtClaims{
		SessionID: sid,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Audience:  jwt.ClaimStrings{s.portal},
			Subject:   strconv.FormatUint(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(c.ExpiresAt),
		},
	})
	str, err := t.SignedString(s.secret)
	if err != nil {
		return "", Claims{}, fmt.Errorf("token: sign: %w", err)
	}
	return str, c, nil
}

// ErrInvalid 是所有校验失败的统一错误；细节只进日志。
var ErrInvalid = errors.New("token: invalid")

// ErrExpired 表示令牌只是过期了（签名、签发方、受众都对）。它同时也是 ErrInvalid；
// 审计用它区分"正常过期"和"伪造或格式不对"（D-032）。
var ErrExpired = errors.New("token: expired")

// ErrBadClaims 表示签名有效、但主体或会话字段不合规。签名有效意味着签发密钥在别人手里，
// 审计按严重事件记录（D-032）。它同时也是 ErrInvalid。
var ErrBadClaims = errors.New("token: bad claims with a valid signature")

// onlyExpired 报告校验失败的原因是否只有过期：签名先于声明校验，所以声明上的错误都意味着签名有效。
func onlyExpired(err error) bool {
	if !errors.Is(err, jwt.ErrTokenExpired) {
		return false
	}
	for _, other := range []error{
		jwt.ErrTokenMalformed, jwt.ErrTokenUnverifiable, jwt.ErrTokenSignatureInvalid, jwt.ErrTokenRequiredClaimMissing,
		jwt.ErrTokenInvalidAudience, jwt.ErrTokenInvalidIssuer, jwt.ErrTokenInvalidSubject, jwt.ErrTokenUsedBeforeIssued,
		jwt.ErrTokenNotValidYet, jwt.ErrTokenInvalidId,
	} {
		if errors.Is(err, other) {
			return false
		}
	}
	return true
}

// Verify 校验令牌：只接受 HS256，iss、aud、exp 必须匹配。
func (s *Signer) Verify(raw string) (Claims, error) {
	var c jwtClaims
	_, err := jwt.ParseWithClaims(raw, &c, func(t *jwt.Token) (any, error) { return s.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(Issuer),
		jwt.WithAudience(s.portal),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(s.now),
	)
	if err != nil && onlyExpired(err) {
		return Claims{}, fmt.Errorf("%w: %w: %w", ErrInvalid, ErrExpired, err)
	}
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	uid, err := strconv.ParseUint(c.Subject, 10, 64)
	if err != nil || uid == 0 || c.SessionID == "" {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalid, ErrBadClaims)
	}
	return Claims{
		UserID:    uid,
		SessionID: c.SessionID,
		Portal:    s.portal,
		IssuedAt:  c.IssuedAt.Time,
		ExpiresAt: c.ExpiresAt.Time,
	}, nil
}
