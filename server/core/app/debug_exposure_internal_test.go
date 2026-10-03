package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
)

// 182（D-098）：只有"debug 模式 + 会接受本机之外的连接"才提醒。
func TestDebugExposure_182(t *testing.T) {
	for _, c := range []struct {
		mode, addr string
		want       bool
	}{
		{conf.ModeDebug, "0.0.0.0:8080", true},
		{conf.ModeDebug, ":8080", true},
		{conf.ModeDebug, "192.168.1.5:8080", true},
		{conf.ModeDebug, "127.0.0.1:8080", false},
		{conf.ModeDebug, "localhost:8080", false},
		{conf.ModeRelease, "0.0.0.0:8080", false},
		{conf.ModeRelease, "127.0.0.1:8080", false},
	} {
		msg, ok := debugExposure(conf.Server{Mode: c.mode, Addr: c.addr})
		require.Equal(t, c.want, ok, "%s %s", c.mode, c.addr)
		require.Equal(t, c.want, msg != "", "%s %s", c.mode, c.addr)
	}
}
