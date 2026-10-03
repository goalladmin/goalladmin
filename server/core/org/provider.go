package org

// 主体端的用户来源（D-065）：代理商、商户程序注册端时用它。实现 portal.OrgUserProvider、portal.UserLocker（账号行排他锁，
// D-047）和 portal.OrgLocker（主体行排他锁，D-063）。

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// Users 返回某种主体所在端的用户来源，交给 portal.Portal.Users（Scoped 为 true）。kind 无效时 panic：这是装配错误。
func (s *Service) Users(k Kind) portal.OrgUserProvider {
	if err := k.check(); err != nil {
		panic(err)
	}
	return &provider{s: s, k: k}
}

type provider struct {
	s *Service
	k Kind
}

// 编译期确认三个接口都实现了。
var (
	_ portal.OrgUserProvider = (*provider)(nil)
	_ portal.UserLocker      = (*provider)(nil)
	_ portal.OrgLocker       = (*provider)(nil)
)

func toAccount(u *userRow) *portal.Account {
	return &portal.Account{
		ID: u.ID, OrgID: u.OrgID, Username: u.Username, DisplayName: u.DisplayName, Avatar: u.Avatar,
		PasswordHash: u.PasswordHash, Status: u.Status, MustChangePwd: u.MustChangePwd, PwdChangedAt: u.PwdChangedAt,
	}
}

func toOrg(o *orgRow) *portal.Org {
	return &portal.Org{ID: o.ID, Code: o.Code, Name: o.Name, Status: o.Status, OwnerUserID: o.OwnerUserID}
}

// FindByUsername 在主体端不会被框架调用：账号名只在主体内唯一，登录走 FindByOrgUsername。一律按不存在处理。
func (p *provider) FindByUsername(context.Context, string) (*portal.Account, error) {
	return nil, portal.ErrAccountNotFound
}

func (p *provider) FindByID(ctx context.Context, id uint64) (*portal.Account, error) {
	return p.user(ctx, false, "id = ?", id)
}

// LockByID 实现 portal.UserLocker：读最新提交的账号行并加排他锁。
func (p *provider) LockByID(ctx context.Context, id uint64) (*portal.Account, error) {
	return p.user(ctx, true, "id = ?", id)
}

func (p *provider) FindByOrgUsername(ctx context.Context, orgID uint64, username string) (*portal.Account, error) {
	return p.user(ctx, false, "org_id = ? AND username = ?", orgID, username)
}

func (p *provider) user(ctx context.Context, lock bool, where string, args ...any) (*portal.Account, error) {
	q := db.From(ctx).Table(p.k.userTable)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var u userRow
	err := q.Where(where, args...).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, portal.ErrAccountNotFound
	}
	if err != nil {
		return nil, wrap("find account", err)
	}
	return toAccount(&u), nil
}

func (p *provider) UpdatePasswordHash(ctx context.Context, id uint64, hash string, mustChange bool) error {
	now := p.s.now().UTC()
	res := db.From(ctx).Table(p.k.userTable).Where("id = ?", id).Updates(map[string]any{
		"password_hash":   hash,
		"must_change_pwd": mustChange,
		"pwd_changed_at":  now,
		"updated_at":      now,
	})
	if res.Error != nil {
		return wrap("update password", res.Error)
	}
	if res.RowsAffected == 0 {
		return portal.ErrAccountNotFound
	}
	p.s.accountChanged(ctx, p.k, id)
	return nil
}

// RehashPassword 把密码哈希从 oldHash 换成 newHash，只动这一列（portal.PasswordRehasher，D-070）。
// 哈希已经不是 oldHash（同时有人改了密、重置了密码）时什么都不做。
func (p *provider) RehashPassword(ctx context.Context, id uint64, oldHash, newHash string) error {
	return wrap("rehash password", db.From(ctx).Table(p.k.userTable).Where("id = ? AND password_hash = ?", id, oldHash).
		UpdateColumn("password_hash", newHash).Error)
}

func (p *provider) TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error {
	return wrap("touch login", db.From(ctx).Table(p.k.userTable).Where("id = ?", id).Updates(map[string]any{
		"last_login_at": at.UTC(),
		"last_login_ip": ip,
	}).Error)
}

func (p *provider) FindOrgByCode(ctx context.Context, code string) (*portal.Org, error) {
	return p.org(ctx, false, "code = ?", code)
}

func (p *provider) FindOrgByID(ctx context.Context, id uint64) (*portal.Org, error) {
	return p.org(ctx, false, "id = ?", id)
}

// LockOrgByID 实现 portal.OrgLocker：读最新提交的主体行并加排他锁（SELECT ... FOR UPDATE）。
func (p *provider) LockOrgByID(ctx context.Context, id uint64) (*portal.Org, error) {
	return p.org(ctx, true, "id = ?", id)
}

func (p *provider) org(ctx context.Context, lock bool, where string, args ...any) (*portal.Org, error) {
	o, err := p.s.findOrg(ctx, p.k, lock, where, args...)
	if errors.Is(err, errOrgMissing) {
		return nil, portal.ErrOrgNotFound
	}
	if err != nil {
		return nil, err
	}
	return toOrg(o), nil
}
