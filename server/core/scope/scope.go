// Package scope 给主体端（代理商端、商户端……）的查询加归属条件（规范 §7.1，D-061）。
//
// 主体 ID 只从当前身份（auth.Principal.OrgID）来：它来自会话行，不来自令牌和请求参数。主体端 repo 的每个方法——
// 包括按 ID 读、改、删——都要显式限制归属；ByOrg 提供一种通用写法：
//
//	err := db.From(ctx).Scopes(scope.ByOrg(ctx, "merchant_id")).Where("id = ?", id).First(&o).Error
//
// 别的主体的行查不到，调用方按"不存在"回 404（不回 403，不暴露这个 ID 存在）。
//
// 失败即拒绝：身份缺失、或者身份没有主体（平台端的身份永远没有主体）时，ByOrg 让这次查询直接报错，
// 绝不退化成"不加条件"。平台端要查全量数据用不带隔离的独立 repo 方法，不要复用主体端的 repo。
package scope

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/auth"
)

// ErrNoOrg 表示当前请求没有主体：身份缺失，或者不是主体端的身份。这是调用方的编程错误（在平台端用了主体端的 repo、
// 在 Public 路由里查主体数据），请求以 500 终止，进错误日志。
var ErrNoOrg = errors.New("scope: 当前请求没有主体（不是主体端的已登录身份），拒绝查询")

// MustOrg 返回当前身份所属的主体 ID；没有主体时返回 ErrNoOrg，绝不返回 0。
func MustOrg(ctx context.Context) (uint64, error) {
	p, ok := auth.FromCtx(ctx)
	if !ok || p.OrgID == 0 {
		return 0, ErrNoOrg
	}
	return p.OrgID, nil
}

// colRe 是允许的列名：小写字母、数字、下划线，可以带一级表名或别名（t.merchant_id）。列名会拼进 SQL，只认这种形状。
var colRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}(\.[a-z_][a-z0-9_]{0,63})?$`)

// ByOrg 返回一个 GORM scope：只留 col 等于当前主体的行（WHERE col = <主体 ID>）。
// 没有主体或列名不合规时，这次查询直接报错（ErrNoOrg 或列名错误），不会执行。
func ByOrg(ctx context.Context, col string) func(*gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		if !colRe.MatchString(col) {
			_ = tx.AddError(fmt.Errorf("scope: 列名 %q 不合规", col))
			return tx
		}
		id, err := MustOrg(ctx)
		if err != nil {
			_ = tx.AddError(err)
			return tx
		}
		return tx.Where(clause.Eq{Column: clause.Column{Name: col}, Value: id})
	}
}
