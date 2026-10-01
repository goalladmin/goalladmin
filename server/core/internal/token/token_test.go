package token

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSignVerify(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s := New("platform", []byte("secret-0123456789abcdef0123456789abcdef"), 15*time.Minute, clock)

	raw, c, err := s.Sign(42, "sid-1")
	require.NoError(t, err)
	require.Equal(t, now.Add(15*time.Minute), c.ExpiresAt)

	got, err := s.Verify(raw)
	require.NoError(t, err)
	require.Equal(t, uint64(42), got.UserID)
	require.Equal(t, "sid-1", got.SessionID)
	require.Equal(t, "platform", got.Portal)

	// 过期
	now = now.Add(16 * time.Minute)
	_, err = s.Verify(raw)
	require.ErrorIs(t, err, ErrInvalid)
	now = now.Add(-16 * time.Minute)

	// 另一个端的签发器（同密钥不同 aud）不接受
	other := New("merchant", []byte("secret-0123456789abcdef0123456789abcdef"), 15*time.Minute, clock)
	_, err = other.Verify(raw)
	require.ErrorIs(t, err, ErrInvalid)

	// 密钥不同
	bad := New("platform", []byte("other-secret-0123456789abcdef0123456789"), 15*time.Minute, clock)
	_, err = bad.Verify(raw)
	require.ErrorIs(t, err, ErrInvalid)

	// 乱码
	_, err = s.Verify("x.y.z")
	require.ErrorIs(t, err, ErrInvalid)
}
