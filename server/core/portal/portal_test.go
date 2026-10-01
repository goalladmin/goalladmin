package portal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 密码有效期必须是整天：配置文件按天写，代码里声明的也一样，安全设置页按天展示时才准确（D-034）。
func TestPasswordPolicy_MaxAgeWholeDays(t *testing.T) {
	ok := PasswordPolicy{MinLength: DefaultPasswordLen, MaxAge: 30 * 24 * time.Hour}
	require.NoError(t, ok.Validate())
	require.NoError(t, PasswordPolicy{MinLength: DefaultPasswordLen}.Validate(), "0 表示不过期")
	bad := PasswordPolicy{MinLength: DefaultPasswordLen, MaxAge: 47 * time.Hour}
	require.ErrorContains(t, bad.Validate(), "整天")
}
