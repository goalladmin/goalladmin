package system_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/goalladmin/goalladmin/server/modules/system"
)

// 规范 §13.2 第 155 条（D-070），平台用户表：升级前留下的 bcrypt 哈希照常登录，登录成功后库里换成 Argon2id，
// 只动哈希这一列（必须改密、改密时间、更新时间不变）；密码错的不换；新建用户、重置密码写进去的就是 Argon2id。
func TestUser_155_LegacyHashUpgradedOnLogin(t *testing.T) {
	f := newFixture(t)
	admin, _ := f.admin("admin")
	uid, _ := f.createUser(admin, "alice", nil)
	row := func() (r struct {
		PasswordHash  string
		MustChangePwd bool
		PwdChangedAt  string
		UpdatedAt     string
	}) {
		require.NoError(t, f.gdb.Raw("SELECT password_hash, must_change_pwd, CAST(pwd_changed_at AS CHAR) AS pwd_changed_at, CAST(updated_at AS CHAR) AS updated_at FROM ga_user WHERE id = ?", uid).Scan(&r).Error)
		return r
	}
	require.True(t, strings.HasPrefix(row().PasswordHash, "$argon2id$"), "新写进去的哈希是 Argon2id: %s", row().PasswordHash)

	// 把库里的哈希换成升级前的样子
	old, err := bcrypt.GenerateFromPassword([]byte("legacy-pass-123"), bcrypt.MinCost)
	require.NoError(t, err)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET password_hash = ? WHERE id = ?", string(old), uid).Error)
	before := row()

	r := f.do("", "POST", "/auth/login", gin.H{"username": "alice", "password": "wrong-pass-123"})
	require.NotEqual(t, 0, r.env.Code)
	require.Equal(t, before, row(), "密码错：什么都不变")

	tok := f.login("alice", "legacy-pass-123")
	after := row()
	require.True(t, strings.HasPrefix(after.PasswordHash, "$argon2id$v=19$"), after.PasswordHash)
	// 登录本身会记最后登录时间，更新时间跟着变（和升级无关）；升级只换哈希这一列，下面单独核对
	require.Equal(t, before.MustChangePwd, after.MustChangePwd)
	require.Equal(t, before.PwdChangedAt, after.PwdChangedAt, "密码没变，改密时间不动（密码有效期照旧算）")
	require.Equal(t, 0, f.do(tok, "GET", "/auth/me", nil).env.Code)
	upgraded := row().PasswordHash
	tok2 := f.login("alice", "legacy-pass-123")
	require.Equal(t, upgraded, row().PasswordHash, "已经升级过的不再重写")
	require.Equal(t, 0, f.do(tok, "GET", "/auth/me", nil).env.Code, "升级不影响已有的会话")
	require.Equal(t, 0, f.do(tok2, "GET", "/auth/me", nil).env.Code)

	// 用户来源的 RehashPassword 本身：只换哈希这一列，更新时间也不动；旧哈希对不上时什么都不写
	ctx := f.app.Context(context.Background())
	repo := system.NewUserRepo()
	before = row()
	require.NoError(t, repo.RehashPassword(ctx, uid, "not-the-current-hash", "new-hash-1"))
	require.Equal(t, before, row())
	require.NoError(t, repo.RehashPassword(ctx, uid, before.PasswordHash, "new-hash-1"))
	after = row()
	require.Equal(t, "new-hash-1", after.PasswordHash)
	after.PasswordHash = before.PasswordHash
	require.Equal(t, before, after, "只换哈希这一列")
	require.NoError(t, repo.RehashPassword(ctx, uid, "new-hash-1", upgraded))

	// 管理员重置：写进去的是 Argon2id
	r = f.do(admin, "POST", fmt.Sprintf("/system/users/%d/reset-password", uid), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.True(t, strings.HasPrefix(row().PasswordHash, "$argon2id$"))
	require.NotEqual(t, upgraded, row().PasswordHash)
}
