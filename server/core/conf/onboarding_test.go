package conf

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOnboarding_169_DefaultAndEnv(t *testing.T) {
	cfg := Default()
	require.False(t, cfg.Onboarding.AgentEnabled)
	require.False(t, cfg.Onboarding.MerchantEnabled)
	t.Setenv("GA_ONBOARDING_AGENT_ENABLED", "true")
	t.Setenv("GA_ONBOARDING_MERCHANT_ENABLED", "true")
	t.Setenv("GA_ONBOARDING_MERCHANT_ORIGIN", "https://merchant.example.com")
	cfg, err := LoadFor("", "agent")
	require.NoError(t, err)
	require.True(t, cfg.Onboarding.AgentEnabled)
	require.True(t, cfg.Onboarding.MerchantEnabled)
	require.NoError(t, Validate(cfg))
	for _, origin := range []string{"", "https://merchant.example.com/path", "https://merchant.example.com#other", "https://user:secret@merchant.example.com"} {
		cfg.Onboarding.MerchantOrigin = origin
		require.Error(t, Validate(cfg))
	}
}
