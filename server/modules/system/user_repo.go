package system

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/rbac"
)

// UserRepo 读写 ga_user。所有方法通过 db.From(ctx) 拿句柄。
type UserRepo struct{}

// NewUserRepo 创建仓库。
func NewUserRepo() *UserRepo { return &UserRepo{} }

// FindByUsername 按账号查用户；不存在返回 portal.ErrAccountNotFound。
func (r *UserRepo) FindByUsername(ctx context.Context, username string) (*User, error) {
	var u User
	err := db.From(ctx).Where("username = ?", username).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, portal.ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("system: find user: %w", err)
	}
	return &u, nil
}

// FindByID 按 ID 查用户；不存在返回 portal.ErrAccountNotFound。
func (r *UserRepo) FindByID(ctx context.Context, id uint64) (*User, error) {
	var u User
	err := db.From(ctx).First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, portal.ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("system: find user: %w", err)
	}
	return &u, nil
}

// FindByIDForShare 在事务里按 ID 查用户并加共享锁：读到的是最新提交的数据，事务结束前别人改不了这一行（D-045）。
func (r *UserRepo) FindByIDForShare(ctx context.Context, id uint64) (*User, error) {
	var u User
	err := db.From(ctx).Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", id).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, portal.ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("system: find user: %w", err)
	}
	return &u, nil
}

// FindByIDForUpdate 在事务里按 ID 查用户并加排他锁：同一用户的并发编辑排队，读到的是最新提交的数据。
func (r *UserRepo) FindByIDForUpdate(ctx context.Context, id uint64) (*User, error) {
	var u User
	err := db.From(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, portal.ErrAccountNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("system: find user: %w", err)
	}
	return &u, nil
}

// Create 插入用户。
func (r *UserRepo) Create(ctx context.Context, u *User) error {
	now := time.Now().UTC()
	u.CreatedAt, u.UpdatedAt = now, now
	if err := db.From(ctx).Create(u).Error; err != nil {
		return fmt.Errorf("system: create user: %w", err)
	}
	return nil
}

// UpdatePasswordHash 更新密码哈希与强制改密标记。
func (r *UserRepo) UpdatePasswordHash(ctx context.Context, id uint64, hash string, mustChange bool) error {
	now := time.Now().UTC()
	res := db.From(ctx).Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"password_hash":   hash,
		"must_change_pwd": mustChange,
		"pwd_changed_at":  now,
		"updated_at":      now,
	})
	if res.Error != nil {
		return fmt.Errorf("system: update password: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return portal.ErrAccountNotFound
	}
	return nil
}

// RehashPassword 把密码哈希从 oldHash 换成 newHash，只动这一列（portal.PasswordRehasher，D-070）：登录时把旧算法的哈希
// 升级成当前的，密码没变，所以"必须改密"、改密时间、更新时间都不动。哈希已经不是 oldHash 时什么都不做。
func (r *UserRepo) RehashPassword(ctx context.Context, id uint64, oldHash, newHash string) error {
	err := db.From(ctx).Model(&User{}).Where("id = ? AND password_hash = ?", id, oldHash).UpdateColumn("password_hash", newHash).Error
	if err != nil {
		return fmt.Errorf("system: rehash password: %w", err)
	}
	return nil
}

// TouchLogin 记录最后登录时间和 IP。
func (r *UserRepo) TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error {
	return db.From(ctx).Model(&User{}).Where("id = ?", id).Updates(map[string]any{
		"last_login_at": at.UTC(),
		"last_login_ip": ip,
	}).Error
}

// 用户数据资源的列（D-039）：数据所属部门是 dept_id，所属的人是用户自己。
const (
	userDeptCol  = "dept_id"
	userOwnerCol = "id"
)

// List 分页列出用户：keyword 匹配账号、显示名、邮箱；status 非 nil 时按状态过滤；deptIDs 非空时只列这些部门的用户；
// 只列数据范围 scope 内的用户（D-039）。
func (r *UserRepo) List(ctx context.Context, q httpx.PageQuery, status *int, deptIDs []uint64, scope rbac.DataFilter) ([]User, int64, error) {
	tx := scope.Apply(db.From(ctx).Model(&User{}), userDeptCol, userOwnerCol)
	if q.Keyword != "" {
		like := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("(username LIKE ? OR display_name LIKE ? OR email LIKE ?)", like, like, like)
	}
	if status != nil {
		tx = tx.Where("status = ?", *status)
	}
	if len(deptIDs) > 0 {
		tx = tx.Where("dept_id IN ?", deptIDs)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	order := q.Order(map[string]string{"createdAt": "created_at", "username": "username", "sort": "sort", "lastLoginAt": "last_login_at"}, "sort ASC, id ASC")
	var out []User
	err := tx.Order(order).Offset(q.Offset()).Limit(q.Limit()).Find(&out).Error
	return out, total, err
}

// ListEnabled 列出数据范围内的启用用户（下拉选项用）。
func (r *UserRepo) ListEnabled(ctx context.Context, limit int, scope rbac.DataFilter) ([]User, error) {
	var out []User
	err := scope.Apply(db.From(ctx), userDeptCol, userOwnerCol).Where("status = ?", StatusEnabled).Order("sort ASC, id ASC").Limit(limit).Find(&out).Error
	return out, err
}

// IDsInScopeQuery 返回"数据范围内的用户 ID"子查询（会话列表、活跃用户排名用，D-039）：交给内核放进 IN (...)，
// 过滤在数据库里做，不把范围内的全部 ID 先取到内存里（D-055）。
func (r *UserRepo) IDsInScopeQuery(ctx context.Context, scope rbac.DataFilter) *gorm.DB {
	return scope.Apply(db.From(ctx).Model(&User{}), userDeptCol, userOwnerCol).Select("id")
}

// CountEnabled 统计给定 ID 里启用的用户数。
func (r *UserRepo) CountEnabled(ctx context.Context, ids []uint64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	var n int64
	err := db.From(ctx).Model(&User{}).Where("id IN ? AND status = ?", ids, StatusEnabled).Count(&n).Error
	return n, err
}

// Update 按列白名单更新（docs/conventions.md：不用整结构体 Save）。
func (r *UserRepo) Update(ctx context.Context, id uint64, fields map[string]any) error {
	fields["updated_at"] = time.Now().UTC()
	res := db.From(ctx).Model(&User{}).Where("id = ?", id).Updates(fields)
	if res.Error != nil {
		return fmt.Errorf("system: update user: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return portal.ErrAccountNotFound
	}
	return nil
}

// FindByIDs 按 ID 批量取用户。
func (r *UserRepo) FindByIDs(ctx context.Context, ids []uint64) (map[uint64]*User, error) {
	out := map[uint64]*User{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []User
	if err := db.From(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].ID] = &rows[i]
	}
	return out, nil
}

// CountAll 返回用户总数（admin create 用来判断是否首个账号）。
func (r *UserRepo) CountAll(ctx context.Context) (int64, error) {
	var n int64
	err := db.From(ctx).Model(&User{}).Count(&n).Error
	return n, err
}

// ---- portal.UserProvider 适配 ----

// userProvider 把 UserRepo 适配成端的用户来源。
type userProvider struct{ repo *UserRepo }

// NewUserProvider 返回 platform 端的用户来源。
func NewUserProvider(repo *UserRepo) portal.UserProvider { return &userProvider{repo: repo} }

func toAccount(u *User) *portal.Account {
	return &portal.Account{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Avatar: u.Avatar,
		PasswordHash: u.PasswordHash, Status: u.Status, MustChangePwd: u.MustChangePwd, PwdChangedAt: u.PwdChangedAt,
	}
}

func (p *userProvider) FindByUsername(ctx context.Context, username string) (*portal.Account, error) {
	u, err := p.repo.FindByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return toAccount(u), nil
}

func (p *userProvider) FindByID(ctx context.Context, id uint64) (*portal.Account, error) {
	u, err := p.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return toAccount(u), nil
}

// LockByID 实现 portal.UserLocker（D-047）。
func (p *userProvider) LockByID(ctx context.Context, id uint64) (*portal.Account, error) {
	u, err := p.repo.FindByIDForUpdate(ctx, id)
	if err != nil {
		return nil, err
	}
	return toAccount(u), nil
}

func (p *userProvider) UpdatePasswordHash(ctx context.Context, id uint64, hash string, mustChange bool) error {
	return p.repo.UpdatePasswordHash(ctx, id, hash, mustChange)
}

func (p *userProvider) RehashPassword(ctx context.Context, id uint64, oldHash, newHash string) error {
	return p.repo.RehashPassword(ctx, id, oldHash, newHash)
}

func (p *userProvider) TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error {
	return p.repo.TouchLogin(ctx, id, ip, at)
}

// escapeLike 转义 LIKE 通配符，让关键字里的 % 和 _ 按字面匹配。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// DayCount 是某一本地日期的数量。
type DayCount struct {
	Day   string `gorm:"column:day"`
	Count int64  `gorm:"column:n"`
}

// DailyNew 按本地日期统计 since 之后每天新建的用户数（D-030）；没有新用户的日子不出现。
func (r *UserRepo) DailyNew(ctx context.Context, since time.Time, tzOffsetMinutes int) ([]DayCount, error) {
	var out []DayCount
	err := db.From(ctx).Raw(`SELECT DATE_FORMAT(DATE_ADD(created_at, INTERVAL ? MINUTE), '%Y-%m-%d') AS day, COUNT(*) AS n
		FROM ga_user WHERE created_at >= ? GROUP BY day ORDER BY day`, tzOffsetMinutes, since.UTC()).Scan(&out).Error
	return out, err
}

// UserCounts 是首页概览用的用户统计。
type UserCounts struct {
	Total   int64 `json:"total"`
	Enabled int64 `json:"enabled"`
	New     int64 `json:"new"` // since 之后创建的
}

// Counts 统计用户总数、启用数和 since 之后新建的数量。
func (r *UserRepo) Counts(ctx context.Context, since time.Time) (UserCounts, error) {
	var out UserCounts
	err := db.From(ctx).Raw(`SELECT COUNT(*) AS total, COALESCE(SUM(status = 1), 0) AS enabled,
		COALESCE(SUM(created_at >= ?), 0) AS new FROM ga_user`, since.UTC()).Scan(&out).Error
	return out, err
}
