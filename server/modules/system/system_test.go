package system_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

func newApp(t *testing.T) *app.App {
	t.Helper()
	gdb := db.OpenTestDB(t)
	ctx := db.WithDB(context.Background(), gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(system.Module())
	require.NoError(t, a.Setup())
	return a
}

func TestCreateAdminAndLogin(t *testing.T) {
	a := newApp(t)
	ctx := a.Context(context.Background())

	pwd, err := system.CreateAdmin(ctx, a.Deps(), "admin")
	require.NoError(t, err)
	require.Len(t, pwd, 20)

	_, err = system.CreateAdmin(ctx, a.Deps(), "admin")
	require.ErrorContains(t, err, "已存在")
	_, err = system.CreateAdmin(ctx, a.Deps(), "ab")
	require.Error(t, err)

	// 库里：一个用户、一条 super 角色关系、密码是 Argon2id 哈希（D-070）、必须改密
	var u struct {
		PasswordHash  string
		MustChangePwd bool
		Status        int
	}
	require.NoError(t, a.Deps().DB.Raw("SELECT password_hash, must_change_pwd, status FROM ga_user WHERE username = 'admin'").Scan(&u).Error)
	require.True(t, strings.HasPrefix(u.PasswordHash, "$argon2id$v=19$"), u.PasswordHash)
	require.NotContains(t, u.PasswordHash, pwd)
	require.True(t, u.MustChangePwd)
	require.Equal(t, 1, u.Status)
	var links int64
	require.NoError(t, a.Deps().DB.Raw("SELECT COUNT(*) FROM ga_user_role ur JOIN ga_role r ON r.id = ur.role_id WHERE ur.portal = 'platform' AND r.code = 'super'").Scan(&links).Error)
	require.EqualValues(t, 1, links)

	// 用打印出的密码登录平台端
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": pwd})
	req := httptest.NewRequest("POST", "/api/platform/v1/auth/login", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GA-Client", "web")
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, 0, env.Code, rec.Body.String())
	data := env.Data.(map[string]any)
	require.Equal(t, true, data["mustChangePwd"])
	require.NotEmpty(t, data["accessToken"])

	// 密码错误不能登录
	body, _ = json.Marshal(map[string]string{"username": "admin", "password": pwd + "x"})
	req = httptest.NewRequest("POST", "/api/platform/v1/auth/login", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GA-Client", "web")
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, httpx.CodeLoginFailed, env.Code)
}
