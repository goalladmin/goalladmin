package org

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// 编号撞上已有的就换一个再试（同一个事务里接着用）；一直撞上时报错，不留下半个主体。
func TestCreate_CodeCollisionRetries(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	s := New(Options{Hash: password.NewHasher(password.Params{Memory: 64, Time: 1}).Hash})
	pwd, err := s.NewInitialPassword()
	require.NoError(t, err)

	seq := []string{"12345678", "12345678", "12345678", "87654321"}
	s.digits = func() (string, error) {
		d := seq[0]
		seq = seq[1:]
		return d, nil
	}
	first, err := s.Create(ctx, Merchant(), CreateInput{Name: "甲", OwnerUsername: "admin"}, pwd, 1)
	require.NoError(t, err)
	require.Equal(t, "M12345678", first.Org.Code)
	// 外层事务里建：撞了两次之后换到第三个编号，外层事务照常提交
	require.NoError(t, db.Tx(ctx, func(ctx context.Context) error {
		second, err := s.Create(ctx, Merchant(), CreateInput{Name: "乙", OwnerUsername: "admin"}, pwd, 1)
		require.NoError(t, err)
		require.Equal(t, "M87654321", second.Org.Code)
		return nil
	}))
	require.Empty(t, seq)

	// 一直撞上：试 codeAttempts 次后报错
	calls := 0
	s.digits = func() (string, error) { calls++; return "12345678", nil }
	_, err = s.Create(ctx, Merchant(), CreateInput{Name: "丙", OwnerUsername: "admin"}, pwd, 1)
	require.Error(t, err)
	require.Equal(t, codeAttempts, calls)
	var n int64
	require.NoError(t, gdb.Table("ga_merchant").Count(&n).Error)
	require.EqualValues(t, 2, n)
	require.NoError(t, gdb.Table("ga_merchant_user").Count(&n).Error)
	require.EqualValues(t, 2, n)

	// 两种主体的编号前缀不同，数字相同也不冲突
	s.digits = func() (string, error) { return "12345678", nil }
	ag, err := s.Create(ctx, Agent(), CreateInput{Name: "代理", OwnerUsername: "boss"}, pwd, 1)
	require.NoError(t, err)
	require.Equal(t, "A12345678", ag.Org.Code)
}

func TestRandomDigits(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		d, err := randomDigits()
		require.NoError(t, err)
		require.Regexp(t, `^\d{8}$`, d)
		seen[d] = true
	}
	require.Greater(t, len(seen), 45, "随机，不按顺序")
}
