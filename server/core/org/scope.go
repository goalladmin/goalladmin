package org

import (
	"context"
	"fmt"
	"regexp"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/scope"
)

// colRe 是允许的列名（同 scope.ByOrg）：小写字母、数字、下划线，可以带一级表名或别名。列名会拼进 SQL，只认这种形状。
var colRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}(\.[a-z_][a-z0-9_]{0,63})?$`)

// ByChildOrgs 返回一个 GORM scope：代理商看名下商户的数据（D-065）。只留 col 是当前代理商名下某个商户的行：
//
//	WHERE col IN (SELECT id FROM ga_merchant WHERE agent_id = <当前代理商>)
//
// 代理商只从 ctx 里代理商端的身份来（会话行里的主体）。身份缺失、不是代理商端的身份（平台端、商户端）、没有主体时，
// 这次查询直接报 scope.ErrNoOrg，绝不退化成"不加条件"。归属每次查询现算：商户改了归属，原代理商下一次请求就看不到。
func ByChildOrgs(ctx context.Context, col string) func(*gorm.DB) *gorm.DB {
	return func(tx *gorm.DB) *gorm.DB {
		if !colRe.MatchString(col) {
			_ = tx.AddError(fmt.Errorf("org: 列名 %q 不合规", col))
			return tx
		}
		p, ok := auth.FromCtx(ctx)
		if !ok || p.Portal != Agent().portal || p.OrgID == 0 {
			_ = tx.AddError(scope.ErrNoOrg)
			return tx
		}
		return tx.Where(clause.Expr{
			SQL:  "? IN (SELECT id FROM " + Merchant().orgTable + " WHERE agent_id = ?)",
			Vars: []any{clause.Column{Name: col}, p.OrgID},
		})
	}
}
