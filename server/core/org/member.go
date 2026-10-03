package org

// 主体端自己的后台用的方法（D-067）：代理商端、商户端的端后台套件（core/orgportal）只是这些方法和 rbac 的接口层。
//
// 主体 ID 一律由调用方从锁内认定的身份（Principal.OrgID）传进来，不来自请求。写操作由调用方放进 rbac 的 WithActor
// （已经锁了主体行），这里在同一个事务里再锁账号行：加锁顺序"主体行 → 账号行 → 会话行"（D-063）。
// 吊销会话、清状态缓存由调用方经本程序的认证器做（deps.Auth），这里只改账号表。

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// MaxEmail 是账号邮箱的长度上限（和迁移 00015 的列宽一致）。
const MaxEmail = 128

// Summary 是给主体端看的主体资料：自己主体的概览、代理商名下的商户。平台写的备注、排序、操作人不在里面。
type Summary struct {
	ID           uint64    `json:"id"`
	Code         string    `json:"code"`
	Name         string    `json:"name"`
	ContactName  string    `json:"contactName"`
	ContactPhone string    `json:"contactPhone"`
	Status       int       `json:"status"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (r *orgRow) summary() Summary {
	return Summary{ID: r.ID, Code: r.Code, Name: r.Name, ContactName: r.ContactName, ContactPhone: r.ContactPhone, Status: r.Status, CreatedAt: r.CreatedAt}
}

// summaryColumns 是 Summary 要读的列。
var summaryColumns = []string{"id", "code", "name", "contact_name", "contact_phone", "status", "created_at"}

// Summary 读一个主体给主体端看的资料；不存在回 404。
func (s *Service) Summary(ctx context.Context, k Kind, orgID uint64) (*Summary, error) {
	o, err := s.findOrg(ctx, k, false, "id = ?", orgID)
	if err != nil {
		return nil, notFound(err)
	}
	v := o.summary()
	return &v, nil
}

// ---- 账号 ----

// member 读主体里的一个账号；lock 为 true 时加排他锁。不存在、属于别的主体都回 404（不暴露这个 ID 在别处存在）。
func (s *Service) member(ctx context.Context, k Kind, orgID, userID uint64, lock bool) (*userRow, uint64, error) {
	o, err := s.findOrg(ctx, k, false, "id = ?", orgID)
	if err != nil {
		return nil, 0, notFound(err)
	}
	q := db.From(ctx).Table(k.userTable)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var u userRow
	err = q.Where("id = ? AND org_id = ?", userID, orgID).Take(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, httpx.ErrNotFound
	}
	if err != nil {
		return nil, 0, wrap("find account", err)
	}
	return &u, o.OwnerUserID, nil
}

// Member 读主体里的一个账号。不存在、属于别的主体回 404。
func (s *Service) Member(ctx context.Context, k Kind, orgID, userID uint64) (*Account, error) {
	u, owner, err := s.member(ctx, k, orgID, userID, false)
	if err != nil {
		return nil, err
	}
	a := u.account(owner)
	return &a, nil
}

// LockMember 同 Member，并锁住账号行（在调用方的事务里，排在主体行锁之后）。
func (s *Service) LockMember(ctx context.Context, k Kind, orgID, userID uint64) (*Account, error) {
	u, owner, err := s.member(ctx, k, orgID, userID, true)
	if err != nil {
		return nil, err
	}
	a := u.account(owner)
	return &a, nil
}

// MemberInput 是在主体里建账号的入参。
type MemberInput struct {
	Username    string
	DisplayName string // 空则用登录名
	Email       string
	Phone       string
	Sort        uint
	Remark      string
}

// MemberUpdate 是改账号资料的入参（整份替换）。登录名、密码、状态、角色各有单独的方法。
type MemberUpdate struct {
	DisplayName string
	Email       string
	Phone       string
	Sort        uint
	Remark      string
}

type memberFields struct {
	displayName, email, phone, remark string
	sort                              uint
}

func cleanMember(displayName, email, phone string, sort uint, remark string) (memberFields, error) {
	var f memberFields
	var err error
	if f.displayName, err = text("displayName", displayName, MaxDisplayName, true); err != nil {
		return f, err
	}
	if f.email, err = text("email", email, MaxEmail, false); err != nil {
		return f, err
	}
	if f.phone, err = text("phone", phone, MaxContactPhone, false); err != nil {
		return f, err
	}
	if sort > MaxSort {
		return f, fieldErr("sort", "org.sort", "at most {max}", "max", MaxSort)
	}
	f.sort = sort
	f.remark = strings.TrimSpace(remark)
	if !utf8.ValidString(f.remark) {
		return f, fieldErr("remark", "org.textChars", "must not contain control or invisible characters")
	}
	if utf8.RuneCountInString(f.remark) > MaxRemark {
		return f, fieldErr("remark", "org.textLength", "at most {max} characters", "max", MaxRemark)
	}
	return f, nil
}

// errUsernameTaken：登录名在主体内已存在。
var errUsernameTaken = httpx.NewKey(httpx.CodeConflict, "org.usernameTaken", "username already exists")

// cleanNewMember 校验、整理建账号的入参。不读库。
func cleanNewMember(in MemberInput) (username string, f memberFields, err error) {
	username = portal.NormalizeUsername(in.Username)
	if !usernameRe.MatchString(username) {
		return "", f, fieldErr("username", "org.username", "3-64 characters: a lowercase letter first, then lowercase letters, digits, '_', '.' or '-'")
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		in.DisplayName = username
	}
	f, err = cleanMember(in.DisplayName, in.Email, in.Phone, in.Sort, in.Remark)
	return username, f, err
}

// CheckMember 是建账号之前的便宜检查（D-068）：字段合法、登录名在本主体内还没有被占用（已占用回 4001）。普通读、不加锁，
// 调用方在算密码哈希之前调：注定失败的请求不去算。只是为了不白算，CreateMember 照样校验、照样靠唯一索引认定。
func (s *Service) CheckMember(ctx context.Context, k Kind, orgID uint64, in MemberInput) error {
	if err := k.check(); err != nil {
		return err
	}
	username, _, err := cleanNewMember(in)
	if err != nil {
		return err
	}
	var n int64
	err = db.From(ctx).Table(k.userTable).Where("org_id = ? AND username = ?", orgID, username).Limit(1).Count(&n).Error
	if err != nil {
		return wrap("check username", err)
	}
	if n > 0 {
		return errUsernameTaken
	}
	return nil
}

// CreateMember 在主体里建一个账号：密码是 hash（调用方在拿锁之前算好），下次登录必须改密。by 是本端的操作人。
// 登录名在主体内已存在回 4001。主体不存在回 404。
func (s *Service) CreateMember(ctx context.Context, k Kind, orgID uint64, in MemberInput, hash string, by uint64) (*Account, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	username, f, err := cleanNewMember(in)
	if err != nil {
		return nil, err
	}
	if hash == "" {
		return nil, errNoPassword
	}
	var out *Account
	err = db.Tx(ctx, func(ctx context.Context) error {
		o, err := s.findOrg(ctx, k, false, "id = ?", orgID)
		if err != nil {
			return notFound(err)
		}
		now := s.now().UTC()
		u := userRow{
			OrgID: orgID, Username: username, PasswordHash: hash, DisplayName: f.displayName, Email: f.email, Phone: f.phone,
			MustChangePwd: true, Status: StatusEnabled, Sort: f.sort, Remark: f.remark,
			CreatedAt: now, UpdatedAt: now, CreatedBy: by, UpdatedBy: by,
		}
		if err := db.From(ctx).Table(k.userTable).Create(&u).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return errUsernameTaken.WithCause(err)
			}
			return wrap("create account", err)
		}
		a := u.account(o.OwnerUserID)
		out = &a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// updateMember 锁住账号行再改；不存在、属于别的主体回 404。
func (s *Service) updateMember(ctx context.Context, k Kind, orgID, userID uint64, values map[string]any) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		if _, _, err := s.member(ctx, k, orgID, userID, true); err != nil {
			return err
		}
		values["updated_at"] = s.now().UTC()
		if err := db.From(ctx).Table(k.userTable).Where("id = ? AND org_id = ?", userID, orgID).Updates(values).Error; err != nil {
			return wrap("update account", err)
		}
		s.accountChanged(ctx, k, userID)
		return nil
	})
}

// UpdateMember 改账号资料。
func (s *Service) UpdateMember(ctx context.Context, k Kind, orgID, userID uint64, in MemberUpdate, by uint64) error {
	f, err := cleanMember(in.DisplayName, in.Email, in.Phone, in.Sort, in.Remark)
	if err != nil {
		return err
	}
	return s.updateMember(ctx, k, orgID, userID, map[string]any{
		"display_name": f.displayName, "email": f.email, "phone": f.phone, "sort": f.sort, "remark": f.remark, "updated_by": by,
	})
}

// SetMemberStatus 启用或停用账号（只改状态；吊销会话由调用方在同一个事务里做）。
func (s *Service) SetMemberStatus(ctx context.Context, k Kind, orgID, userID uint64, status int, by uint64) error {
	if status != StatusEnabled && status != StatusDisabled {
		return fieldErr("status", "org.status", "must be 0 or 1")
	}
	return s.updateMember(ctx, k, orgID, userID, map[string]any{"status": status, "updated_by": by})
}

// SetMemberPassword 换掉账号的密码哈希（调用方在拿锁之前算好）；mustChange 为 true 时下次登录必须改密。
// 吊销会话由调用方在同一个事务里做。
func (s *Service) SetMemberPassword(ctx context.Context, k Kind, orgID, userID uint64, hash string, mustChange bool, by uint64) error {
	if hash == "" {
		return errNoPassword
	}
	return s.updateMember(ctx, k, orgID, userID, map[string]any{
		"password_hash": hash, "must_change_pwd": mustChange, "pwd_changed_at": s.now().UTC(), "updated_by": by,
	})
}

// ProfileInput 是本人在个人中心能改的几项。
type ProfileInput struct {
	DisplayName string
	Email       string
	Phone       string
}

// UpdateProfile 本人改自己的资料（调用方放在 rbac.WithSelf 里，userID 是本人）。
func (s *Service) UpdateProfile(ctx context.Context, k Kind, orgID, userID uint64, in ProfileInput) error {
	displayName, err := text("displayName", in.DisplayName, MaxDisplayName, true)
	if err != nil {
		return err
	}
	email, err := text("email", in.Email, MaxEmail, false)
	if err != nil {
		return err
	}
	phone, err := text("phone", in.Phone, MaxContactPhone, false)
	if err != nil {
		return err
	}
	return s.updateMember(ctx, k, orgID, userID, map[string]any{
		"display_name": displayName, "email": email, "phone": phone, "updated_by": userID,
	})
}

// SetAvatar 本人换头像：preset 是内置头像的名字，空表示清除（回到名字首字母）。这一版主体端不能上传（D-067）。
func (s *Service) SetAvatar(ctx context.Context, k Kind, orgID, userID uint64, preset string) (string, error) {
	value := ""
	if preset != "" {
		if !portal.ValidAvatarPreset(preset) {
			return "", fieldErr("preset", "org.avatarPreset", "unknown built-in avatar")
		}
		value = portal.AvatarPresetPrefix + preset
	}
	if err := s.updateMember(ctx, k, orgID, userID, map[string]any{"avatar": value, "updated_by": userID}); err != nil {
		return "", err
	}
	return value, nil
}

// ---- 代理商名下的商户（只读）----

// ChildMerchants 分页列出当前代理商名下的商户（只读，D-067）。代理商只从 ctx 里代理商端的身份来（ByChildOrgs），
// 别的身份报 scope.ErrNoOrg。status 非 nil 时按状态过滤，关键字匹配编号、名称、联系人、联系电话。
func (s *Service) ChildMerchants(ctx context.Context, status *int, q httpx.PageQuery) ([]Summary, int64, error) {
	tx := db.From(ctx).Table(Merchant().orgTable).Scopes(ByChildOrgs(ctx, "id"))
	if q.Keyword != "" {
		like := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("(code LIKE ? OR name LIKE ? OR contact_name LIKE ? OR contact_phone LIKE ?)", like, like, like, like)
	}
	if status != nil {
		tx = tx.Where("status = ?", *status)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, wrap("count child merchants", err)
	}
	order := q.Order(map[string]string{"createdAt": "created_at", "code": "code", "name": "name"}, "id DESC")
	var rows []orgRow
	if err := tx.Select(summaryColumns).Order(order).Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error; err != nil {
		return nil, 0, wrap("list child merchants", err)
	}
	out := make([]Summary, len(rows))
	for i := range rows {
		out[i] = rows[i].summary()
	}
	return out, total, nil
}

// ChildMerchant 读当前代理商名下的一个商户；不存在、不在名下都回 404。
func (s *Service) ChildMerchant(ctx context.Context, id uint64) (*Summary, error) {
	var o orgRow
	err := db.From(ctx).Table(Merchant().orgTable).Scopes(ByChildOrgs(ctx, "id")).Select(summaryColumns).Where("id = ?", id).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, httpx.ErrNotFound
	}
	if err != nil {
		return nil, wrap("find child merchant", err)
	}
	v := o.summary()
	return &v, nil
}
