package httpx

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 除简体中文和英文外，每种语言都给全部框架错误码写了文案（D-026），文案里没有 %、{ } 这类格式残留。
func TestLangMessages_Complete(t *testing.T) {
	for _, l := range Langs() {
		if l == LangZH || l == LangEN {
			continue
		}
		m := langMessages(l)
		require.NotNil(t, m, "lang=%s", l)
		for code := range messages {
			if code >= 10000 {
				continue // 业务模块在测试里登记的码
			}
			s := m[code]
			require.NotEmpty(t, strings.TrimSpace(s), "lang=%s code=%d 没有文案", l, code)
			require.NotContains(t, s, "%", "lang=%s code=%d", l, code)
			require.NotContains(t, s, "{", "lang=%s code=%d", l, code)
		}
	}
}
