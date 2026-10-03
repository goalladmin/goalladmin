package scope_test

// 规范 §7.1、D-061：scope.ByOrg 只留当前主体的行；没有主体时查询被拒绝而不是不加条件；
// scopetest 模板能抓出漏了隔离条件的 repo。

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/scope"
	"github.com/goalladmin/goalladmin/server/core/scope/scopetest"
)

type item struct {
	ID         uint64 `gorm:"column:id;primaryKey"`
	MerchantID uint64 `gorm:"column:merchant_id"`
	Name       string `gorm:"column:name"`
}

func (item) TableName() string { return "t_scope_item" }

func setup(t *testing.T) context.Context {
	t.Helper()
	gdb := db.OpenTestDB(t)
	require.NoError(t, gdb.Exec("CREATE TABLE t_scope_item (id bigint unsigned AUTO_INCREMENT PRIMARY KEY, merchant_id bigint unsigned NOT NULL, name varchar(64) NOT NULL)").Error)
	return db.TestContext(t, gdb)
}

// repo 是一个正确的主体端 repo：每个方法都套 ByOrg。leak 让某个方法漏掉隔离条件，用来证明模板能抓到。
type repo struct{ leak string }

func (r repo) scoped(ctx context.Context, op string) *gorm.DB {
	q := db.From(ctx).Model(&item{})
	if r.leak == op {
		return q
	}
	return q.Scopes(scope.ByOrg(ctx, "merchant_id"))
}

func (r repo) resource() scopetest.Resource {
	return scopetest.Resource{
		Portal: "merchant",
		Seed: func(ctx context.Context, orgID uint64) (uint64, error) {
			id, err := scope.MustOrg(ctx)
			if err != nil {
				return 0, err
			}
			it := item{MerchantID: id, Name: fmt.Sprintf("org-%d", orgID)}
			if err := db.From(ctx).Create(&it).Error; err != nil {
				return 0, err
			}
			return it.ID, nil
		},
		List: func(ctx context.Context) ([]uint64, error) {
			var ids []uint64
			err := r.scoped(ctx, "list").Order("id").Pluck("id", &ids).Error
			return ids, err
		},
		Get: func(ctx context.Context, id uint64) error {
			var it item
			return r.scoped(ctx, "get").First(&it, id).Error
		},
		Update: func(ctx context.Context, id uint64) error {
			res := r.scoped(ctx, "update").Where("id = ?", id).Update("name", "changed")
			if res.Error == nil && res.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return res.Error
		},
		Delete: func(ctx context.Context, id uint64) error {
			res := r.scoped(ctx, "delete").Where("id = ?", id).Delete(&item{})
			if res.Error == nil && res.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return res.Error
		},
		Snapshot: func(ctx context.Context, id uint64) (string, error) {
			var it item
			err := db.From(ctx).First(&it, id).Error
			return fmt.Sprintf("%+v", it), err
		},
	}
}

func TestScope_ByOrgIsolatesAndFailsClosed(t *testing.T) {
	ctx := setup(t)
	scopetest.Run(t, ctx, repo{}.resource())
}

// recorder 记下模板报的错；Fatalf 用 panic 停住执行，由 run 收回。
type recorder struct{ errs []string }

type fatal struct{}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
	panic(fatal{})
}

func run(ctx context.Context, res scopetest.Resource) (errs []string) {
	r := &recorder{}
	defer func() {
		if v := recover(); v != nil {
			if _, ok := v.(fatal); !ok {
				panic(v)
			}
		}
		errs = r.errs
	}()
	scopetest.Run(r, ctx, res)
	return r.errs
}

// 漏了隔离条件的每一种 repo 方法，模板都要报出来。
func TestScopeTest_CatchesMissingIsolation(t *testing.T) {
	for _, op := range []string{"list", "get", "update", "delete"} {
		t.Run(op, func(t *testing.T) {
			ctx := setup(t)
			errs := run(ctx, repo{leak: op}.resource())
			require.NotEmpty(t, errs, "%s 漏了隔离条件，模板应该报错", op)
		})
	}
	ctx := setup(t)
	require.Empty(t, run(ctx, repo{}.resource()), "正确的 repo 不应报错")
}

// 平台端身份、没有身份、列名不合规：查询直接报错，不执行。
func TestScope_RejectsWithoutOrg(t *testing.T) {
	ctx := setup(t)
	require.NoError(t, db.From(ctx).Create(&item{MerchantID: 7, Name: "x"}).Error)

	for name, c := range map[string]context.Context{
		"没有身份":  ctx,
		"平台端身份": scopetest.Ctx(ctx, "platform", 0, 1),
	} {
		var n int64
		err := db.From(c).Model(&item{}).Scopes(scope.ByOrg(c, "merchant_id")).Count(&n).Error
		require.True(t, errors.Is(err, scope.ErrNoOrg), "%s: %v", name, err)
		_, err = scope.MustOrg(c)
		require.ErrorIs(t, err, scope.ErrNoOrg, name)
	}

	c := scopetest.Ctx(ctx, "merchant", 7, 1)
	var n int64
	require.NoError(t, db.From(c).Model(&item{}).Scopes(scope.ByOrg(c, "merchant_id")).Count(&n).Error)
	require.EqualValues(t, 1, n)
	// 列名会拼进 SQL：只认小写标识符（可带一级表名）
	for _, bad := range []string{"merchant_id; DROP TABLE t_scope_item", "1=1 OR merchant_id", "Merchant_ID", "a.b.c", ""} {
		err := db.From(c).Model(&item{}).Scopes(scope.ByOrg(c, bad)).Count(&n).Error
		require.Error(t, err, bad)
	}
	require.NoError(t, db.From(c).Table("t_scope_item AS t").Scopes(scope.ByOrg(c, "t.merchant_id")).Count(&n).Error)
	require.EqualValues(t, 1, n)
}
