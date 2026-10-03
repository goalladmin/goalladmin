package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnboarding_169_CaptchaCapability(t *testing.T) {
	f := newAuthFixture(t)
	id, _, err := f.app.captcha.GenerateFor(context.Background(), "platform")
	require.NoError(t, err)
	answer := f.app.captcha.Peek(id)
	require.False(t, f.app.Deps().VerifyCaptcha(context.Background(), "merchant", id, answer))
	require.True(t, f.app.Deps().VerifyCaptcha(context.Background(), "platform", id, answer))
	require.False(t, f.app.Deps().VerifyCaptcha(context.Background(), "platform", id, answer))
}
