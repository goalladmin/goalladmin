package rbac

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/db"
)

// Role 是角色（对应 ga_role，规范 §8.2）。角色是框架概念，管理接口由 system 模块提供。
//
// 它是公开类型，但故意不带 TableName：业务模块能 import 这个类型，如果它同时是 gorm 模型，
// `db.From(ctx).Model(&rbac.Role{})` 就能绕过授权服务直接改 ga_role（D-043）。
// 读写库用下面不导出的 roleRow。
type Role struct {
	ID        uint64    `gorm:"column:id;primaryKey" json:"id"`
	Portal    string    `gorm:"column:portal" json:"portal"`
	Code      string    `gorm:"column:code" json:"code"`
	Name      string    `gorm:"column:name" json:"name"`
	IsSuper   bool      `gorm:"column:is_super" json:"isSuper"`
	Status    int       `gorm:"column:status" json:"status"`
	Sort      uint      `gorm:"column:sort" json:"sort"`
	Remark    string    `gorm:"column:remark" json:"remark"`
	CreatedAt time.Time `gorm:"column:created_at" json:"createdAt"`
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updatedAt"`
	CreatedBy uint64    `gorm:"column:created_by" json:"createdBy"`
	UpdatedBy uint64    `gorm:"column:updated_by" json:"updatedBy"`
}

// roleRow 是 Role 在库里的样子：同一份字段，只多一个表名。只在本包内使用。
type roleRow Role

// TableName 固定表名。
func (roleRow) TableName() string { return "ga_role" }

func rolesOf(rows []roleRow) []Role {
	out := make([]Role, len(rows))
	for i, r := range rows {
		out[i] = Role(r)
	}
	return out
}

// userRole 对应 ga_user_role。不导出：见 Role 的说明。
type userRole struct {
	Portal string `gorm:"column:portal;primaryKey"`
	UserID uint64 `gorm:"column:user_id;primaryKey"`
	RoleID uint64 `gorm:"column:role_id;primaryKey"`
}

// TableName 固定表名。
func (userRole) TableName() string { return "ga_user_role" }

// policyRule 对应 ga_casbin_rule：ptype = "p"，v0 = "role:<id>"，v1 = 端，v2 = 权限码或通配模式。不导出：见 Role 的说明。
type policyRule struct {
	ID    uint64 `gorm:"column:id;primaryKey"`
	PType string `gorm:"column:ptype"`
	V0    string `gorm:"column:v0"`
	V1    string `gorm:"column:v1"`
	V2    string `gorm:"column:v2"`
	V3    string `gorm:"column:v3"`
	V4    string `gorm:"column:v4"`
	V5    string `gorm:"column:v5"`
}

// TableName 固定表名。
func (policyRule) TableName() string { return "ga_casbin_rule" }

// 内置超级管理员角色编码。
const SuperRoleCode = "super"

// 错误。
var (
	ErrRoleNotFound  = errors.New("rbac: 角色不存在")
	ErrRoleInUse     = errors.New("rbac: 角色仍被用户引用，不能删除")
	ErrRoleCodeTaken = errors.New("rbac: 角色编码已存在")
	ErrSuperRole     = errors.New("rbac: 内置超级管理员角色不允许此操作")
)

// roleSubject 是策略里的主体：role:<id>。
func roleSubject(roleID uint64) string { return fmt.Sprintf("role:%d", roleID) }

// store 封装三张表的读写。所有方法通过 db.From(ctx) 拿句柄，可以参与调用方的事务。
type store struct{}

func (store) roles(ctx context.Context, portal string) ([]Role, error) {
	var out []roleRow
	err := db.From(ctx).Where("portal = ?", portal).Order("sort ASC, id ASC").Find(&out).Error
	return rolesOf(out), err
}

// role 按 (portal, id) 读角色：角色 ID 全局唯一，但每个端只能看见、改动自己的角色（规范 §6.7）。
func (store) role(ctx context.Context, portal string, id uint64) (*Role, error) {
	var r roleRow
	err := db.From(ctx).Where("portal = ? AND id = ?", portal, id).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoleNotFound
	}
	out := Role(r)
	return &out, err
}

// lockSuperRole 用 SELECT ... FOR UPDATE 锁住该端的超管角色行，直到当前事务结束。
// 所有"可能让端失去最后一个可用超管"的操作（停用账号、改动用户角色）都先拿这把锁，
// 这样"先判断还有别的超管、再写入"的两步在并发下也是串行的（规范 §6.4）。
// 必须在事务里调用；端没有超管角色时什么也不锁。
func (store) lockSuperRole(ctx context.Context, portal string) error {
	var rows []roleRow
	return db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("portal = ? AND is_super = 1", portal).Find(&rows).Error
}

// lockRole 用 SELECT ... FOR UPDATE 锁住一个角色行，直到当前事务结束（授权时串行化同一角色的并发修改），
// 并返回锁定读看到的那一行：锁定读读的是最新已提交的数据，不受事务快照影响，锁内的判断要用它而不是再做一次普通读。
// 角色不存在（例如刚被并发删除）时返回 ErrRoleNotFound，调用方不会对着一个已删除的角色继续写授权。
func (store) lockRole(ctx context.Context, portal string, id uint64) (*Role, error) {
	var rows []roleRow
	if err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("portal = ? AND id = ?", portal, id).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrRoleNotFound
	}
	out := Role(rows[0])
	return &out, nil
}

func (store) roleByCode(ctx context.Context, portal, code string) (*Role, error) {
	var r roleRow
	err := db.From(ctx).Where("portal = ? AND code = ?", portal, code).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRoleNotFound
	}
	out := Role(r)
	return &out, err
}

func (store) createRole(ctx context.Context, r *Role) error {
	now := time.Now().UTC()
	r.CreatedAt, r.UpdatedAt = now, now
	row := roleRow(*r)
	err := db.From(ctx).Create(&row).Error
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return ErrRoleCodeTaken
	}
	*r = Role(row) // 带回自增 ID
	return err
}

func (store) updateRole(ctx context.Context, portal string, id uint64, fields map[string]any) error {
	fields["updated_at"] = time.Now().UTC()
	res := db.From(ctx).Model(&roleRow{}).Where("portal = ? AND id = ?", portal, id).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrRoleNotFound
	}
	return nil
}

func (store) deleteRole(ctx context.Context, portal string, id uint64) error {
	return db.From(ctx).Where("portal = ? AND id = ?", portal, id).Delete(&roleRow{}).Error
}

// roleUserCountLocked 用锁定读（FOR SHARE）数角色被多少用户引用：数的是最新已提交的分配，不受事务快照影响；删角色前在锁内用它。
func (store) roleUserCountLocked(ctx context.Context, portal string, roleID uint64) (int64, error) {
	var n int64
	err := db.From(ctx).Model(&userRole{}).Clauses(clause.Locking{Strength: "SHARE"}).Where("portal = ? AND role_id = ?", portal, roleID).Count(&n).Error
	return n, err
}

func (store) userRoles(ctx context.Context, portal string, userID uint64) ([]Role, error) {
	return store{}.userRolesQ(ctx, portal, userID, false)
}

// userRolesLocked 是 userRoles 的锁定读（FOR SHARE）：锁内做判断用。锁定读拿到的是最新已提交的行，
// 不受事务快照影响——调用方的事务可能在拿超管锁之前就做过读、快照早已建立（D-043）。
func (store) userRolesLocked(ctx context.Context, portal string, userID uint64) ([]Role, error) {
	return store{}.userRolesQ(ctx, portal, userID, true)
}

func (store) userRolesQ(ctx context.Context, portal string, userID uint64, lock bool) ([]Role, error) {
	var out []roleRow
	q := db.From(ctx).
		Joins("JOIN ga_user_role ur ON ur.role_id = ga_role.id AND ur.portal = ga_role.portal").
		Where("ur.portal = ? AND ur.user_id = ?", portal, userID).
		Order("ga_role.sort ASC, ga_role.id ASC")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := q.Find(&out).Error
	return rolesOf(out), err
}

// userRolesBatch 一次查多个用户的角色（含停用的），供列表页显示。
func (store) userRolesBatch(ctx context.Context, portal string, userIDs []uint64) (map[uint64][]Role, error) {
	out := map[uint64][]Role{}
	if len(userIDs) == 0 {
		return out, nil
	}
	// 嵌入的必须是导出类型 Role：嵌入不导出的 roleRow 时反射填不进去，扫出来全是零值（IsSuper 会变成 false）。
	// 这里显式 Table("ga_role")，不靠 TableName
	var rows []struct {
		Role
		UserID uint64 `gorm:"column:user_id"`
	}
	err := db.From(ctx).Table("ga_role").
		Select("ga_role.*, ur.user_id").
		Joins("JOIN ga_user_role ur ON ur.role_id = ga_role.id AND ur.portal = ga_role.portal").
		Where("ur.portal = ? AND ur.user_id IN ?", portal, userIDs).
		Order("ga_role.sort ASC, ga_role.id ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.UserID] = append(out[r.UserID], r.Role)
	}
	return out, nil
}

func (store) replaceUserRoles(ctx context.Context, portal string, userID uint64, roleIDs []uint64) error {
	if err := db.From(ctx).Where("portal = ? AND user_id = ?", portal, userID).Delete(&userRole{}).Error; err != nil {
		return err
	}
	if len(roleIDs) == 0 {
		return nil
	}
	rows := make([]userRole, 0, len(roleIDs))
	for _, id := range roleIDs {
		rows = append(rows, userRole{Portal: portal, UserID: userID, RoleID: id})
	}
	return db.From(ctx).Create(&rows).Error
}

// usersWithRole 返回拥有某角色的用户 ID。
func (store) usersWithRole(ctx context.Context, portal string, roleID uint64) ([]uint64, error) {
	var ids []uint64
	err := db.From(ctx).Model(&userRole{}).Where("portal = ? AND role_id = ?", portal, roleID).Pluck("user_id", &ids).Error
	return ids, err
}

func (store) rolePerms(ctx context.Context, portal string, roleID uint64) ([]string, error) {
	return store{}.rolePermsQ(ctx, portal, roleID, false)
}

// rolePermsLocked 是 rolePerms 的锁定读（FOR SHARE）：锁内做判断用，见 userRolesLocked。
func (store) rolePermsLocked(ctx context.Context, portal string, roleID uint64) ([]string, error) {
	return store{}.rolePermsQ(ctx, portal, roleID, true)
}

func (store) rolePermsQ(ctx context.Context, portal string, roleID uint64, lock bool) ([]string, error) {
	var codes []string
	q := db.From(ctx).Model(&policyRule{}).
		Where("ptype = 'p' AND v0 = ? AND v1 = ?", roleSubject(roleID), portal).
		Order("v2")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	err := q.Pluck("v2", &codes).Error
	return codes, err
}

func (store) replaceRolePerms(ctx context.Context, portal string, roleID uint64, codes []string) error {
	if err := db.From(ctx).Where("ptype = 'p' AND v0 = ? AND v1 = ?", roleSubject(roleID), portal).Delete(&policyRule{}).Error; err != nil {
		return err
	}
	if len(codes) == 0 {
		return nil
	}
	rows := make([]policyRule, 0, len(codes))
	for _, c := range codes {
		rows = append(rows, policyRule{PType: "p", V0: roleSubject(roleID), V1: portal, V2: c})
	}
	return db.From(ctx).Create(&rows).Error
}

func (store) allPolicies(ctx context.Context) ([]policyRule, error) {
	var out []policyRule
	err := db.From(ctx).Where("ptype = 'p'").Find(&out).Error
	return out, err
}

func (store) deletePolicies(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}
	return db.From(ctx).Delete(&policyRule{}, ids).Error
}

func (store) deleteRolePolicies(ctx context.Context, portal string, roleID uint64) error {
	return db.From(ctx).Where("ptype = 'p' AND v0 = ? AND v1 = ?", roleSubject(roleID), portal).Delete(&policyRule{}).Error
}

// roleDataScope 对应 ga_role_data_scope（D-039）：角色在一个数据资源上的范围。不导出：见 Role 的说明。
type roleDataScope struct {
	RoleID   uint64    `gorm:"column:role_id;primaryKey"`
	Resource string    `gorm:"column:resource;primaryKey"`
	Scope    DataScope `gorm:"column:scope"`
}

// TableName 固定表名。
func (roleDataScope) TableName() string { return "ga_role_data_scope" }

// roleScopes 读一个角色存下来的范围（资源 → 范围）。
func (store) roleScopes(ctx context.Context, roleID uint64) (map[string]DataScope, error) {
	return store{}.roleScopesQ(ctx, roleID, false)
}

// roleScopesLocked 是 roleScopes 的锁定读（FOR SHARE）：锁内做判断用，见 userRolesLocked。
func (store) roleScopesLocked(ctx context.Context, roleID uint64) (map[string]DataScope, error) {
	return store{}.roleScopesQ(ctx, roleID, true)
}

func (store) roleScopesQ(ctx context.Context, roleID uint64, lock bool) (map[string]DataScope, error) {
	var rows []roleDataScope
	q := db.From(ctx).Where("role_id = ?", roleID)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "SHARE"})
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]DataScope, len(rows))
	for _, r := range rows {
		out[r.Resource] = r.Scope
	}
	return out, nil
}

// allRoles 读全部角色（按排序值、ID），授权快照用（D-053）。
func (store) allRoles(ctx context.Context) ([]Role, error) {
	var out []roleRow
	err := db.From(ctx).Order("sort ASC, id ASC").Find(&out).Error
	return rolesOf(out), err
}

// allUserRoles 读全部成员关系，授权快照用（D-053）。
func (store) allUserRoles(ctx context.Context) ([]userRole, error) {
	var out []userRole
	err := db.From(ctx).Find(&out).Error
	return out, err
}

// setRoleScopes 写入给定资源的范围（没给的资源不动）。
func (store) setRoleScopes(ctx context.Context, roleID uint64, scopes map[string]DataScope) error {
	if len(scopes) == 0 {
		return nil
	}
	keys := make([]string, 0, len(scopes))
	rows := make([]roleDataScope, 0, len(scopes))
	for res, sc := range scopes {
		keys = append(keys, res)
		rows = append(rows, roleDataScope{RoleID: roleID, Resource: res, Scope: sc})
	}
	if err := db.From(ctx).Where("role_id = ? AND resource IN ?", roleID, keys).Delete(&roleDataScope{}).Error; err != nil {
		return err
	}
	return db.From(ctx).Create(&rows).Error
}

func (store) deleteRoleScopes(ctx context.Context, roleID uint64) error {
	return db.From(ctx).Where("role_id = ?", roleID).Delete(&roleDataScope{}).Error
}
