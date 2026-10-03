package org

// 平台端的管理操作（D-065）：平台的 modules/agent、modules/merchant 只是这些方法的接口层。
//
// 写操作的加锁顺序是"主体行 → 账号行 → 会话行"（D-063）；调用方把它们放进 rbac 的 WithActor（平台超管锁）里，
// 这里的事务加入外层事务。初始密码的哈希由调用方在拿锁之前算好（NewInitialPassword），不在锁里等它。

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// ---- 读 ----

// findOrg 按条件读一个主体；lock 为 true 时加排他锁（在调用方的事务里）。不存在返回 errOrgMissing。
func (s *Service) findOrg(ctx context.Context, k Kind, lock bool, where string, args ...any) (*orgRow, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	q := db.From(ctx).Table(k.orgTable)
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var o orgRow
	err := q.Where(where, args...).Take(&o).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errOrgMissing
	}
	if err != nil {
		return nil, wrap("find org", err)
	}
	return &o, nil
}

// lockOrg 锁住一个主体行；不存在回 404。
func (s *Service) lockOrg(ctx context.Context, k Kind, id uint64) (*orgRow, error) {
	o, err := s.findOrg(ctx, k, true, "id = ?", id)
	return o, notFound(err)
}

// Filter 是主体列表的筛选条件（关键字在 httpx.PageQuery 里：匹配编号、名称、联系人、联系电话）。
type Filter struct {
	Status  *int
	AgentID *uint64 // 只对商户：只列这个代理商名下的，0 表示直属平台
}

// List 分页列出主体。
func (s *Service) List(ctx context.Context, k Kind, f Filter, q httpx.PageQuery) ([]Info, int64, error) {
	if err := k.check(); err != nil {
		return nil, 0, err
	}
	tx := db.From(ctx).Table(k.orgTable)
	if q.Keyword != "" {
		like := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("code LIKE ? OR name LIKE ? OR contact_name LIKE ? OR contact_phone LIKE ?", like, like, like, like)
	}
	if f.Status != nil {
		tx = tx.Where("status = ?", *f.Status)
	}
	if f.AgentID != nil {
		if !k.parent {
			return nil, 0, errKind
		}
		tx = tx.Where("agent_id = ?", *f.AgentID)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, wrap("count orgs", err)
	}
	order := q.Order(map[string]string{"createdAt": "created_at", "code": "code", "name": "name", "sort": "sort"}, "sort ASC, id DESC")
	var rows []orgRow
	if err := tx.Order(order).Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error; err != nil {
		return nil, 0, wrap("list orgs", err)
	}
	out := make([]Info, len(rows))
	owners := make([]uint64, 0, len(rows))
	for i := range rows {
		out[i] = rows[i].info()
		if rows[i].OwnerUserID != 0 {
			owners = append(owners, rows[i].OwnerUserID)
		}
	}
	names, err := s.usernames(ctx, k, owners)
	if err != nil {
		return nil, 0, err
	}
	for i := range out {
		out[i].OwnerUsername = names[out[i].OwnerUserID]
	}
	return out, total, nil
}

// Get 读一个主体；不存在回 404。
func (s *Service) Get(ctx context.Context, k Kind, id uint64) (*Info, error) {
	o, err := s.findOrg(ctx, k, false, "id = ?", id)
	if err != nil {
		return nil, notFound(err)
	}
	info := o.info()
	if o.OwnerUserID != 0 {
		names, err := s.usernames(ctx, k, []uint64{o.OwnerUserID})
		if err != nil {
			return nil, err
		}
		info.OwnerUsername = names[o.OwnerUserID]
	}
	return &info, nil
}

// Briefs 按 ID 批量取主体的编号和名称；不存在的 ID 不在结果里。
func (s *Service) Briefs(ctx context.Context, k Kind, ids []uint64) (map[uint64]Brief, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	out := map[uint64]Brief{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []Brief
	if err := db.From(ctx).Table(k.orgTable).Select("id", "code", "name", "status").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, wrap("org briefs", err)
	}
	for _, r := range rows {
		out[r.ID] = r
	}
	return out, nil
}

// BriefOptions 的筛选和出参只使用公开的简要字段。
func (s *Service) BriefOptions(ctx context.Context, k Kind, keyword string) ([]Brief, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	tx := db.From(ctx).Table(k.orgTable).Select("id", "code", "name", "status")
	if keyword != "" {
		like := "%" + escapeLike(keyword) + "%"
		tx = tx.Where("(code LIKE ? OR name LIKE ?)", like, like)
	}
	out := []Brief{}
	if err := tx.Order("sort ASC, id DESC").Limit(50).Find(&out).Error; err != nil {
		return nil, wrap("org brief options", err)
	}
	return out, nil
}

// usernames 按账号 ID 批量取登录名。
func (s *Service) usernames(ctx context.Context, k Kind, ids []uint64) (map[uint64]string, error) {
	out := map[uint64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID       uint64 `gorm:"column:id"`
		Username string `gorm:"column:username"`
	}
	if err := db.From(ctx).Table(k.userTable).Select("id", "username").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, wrap("usernames", err)
	}
	for _, r := range rows {
		out[r.ID] = r.Username
	}
	return out, nil
}

// Accounts 分页列出一个主体的账号（只读）；status 非 nil 时按状态过滤，关键字匹配登录名、显示名。主体不存在回 404。
func (s *Service) Accounts(ctx context.Context, k Kind, orgID uint64, status *int, q httpx.PageQuery) ([]Account, int64, error) {
	o, err := s.findOrg(ctx, k, false, "id = ?", orgID)
	if err != nil {
		return nil, 0, notFound(err)
	}
	tx := db.From(ctx).Table(k.userTable).Where("org_id = ?", orgID)
	if q.Keyword != "" {
		like := "%" + escapeLike(q.Keyword) + "%"
		tx = tx.Where("(username LIKE ? OR display_name LIKE ?)", like, like)
	}
	if status != nil {
		tx = tx.Where("status = ?", *status)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, wrap("count accounts", err)
	}
	order := q.Order(map[string]string{"createdAt": "created_at", "username": "username", "lastLoginAt": "last_login_at"}, "sort ASC, id ASC")
	var rows []userRow
	if err := tx.Order(order).Offset(q.Offset()).Limit(q.Limit()).Find(&rows).Error; err != nil {
		return nil, 0, wrap("list accounts", err)
	}
	out := make([]Account, len(rows))
	for i := range rows {
		out[i] = rows[i].account(o.OwnerUserID)
	}
	return out, total, nil
}

// Sessions 分页列出一个主体的有效会话。主体不存在回 404。
func (s *Service) Sessions(ctx context.Context, k Kind, orgID uint64, q httpx.PageQuery) ([]Session, int64, error) {
	if _, err := s.findOrg(ctx, k, false, "id = ?", orgID); err != nil {
		return nil, 0, notFound(err)
	}
	// 账号归属和会话行归属同时核对，异常记录也不能混入列表或总数（D-089）。
	users := db.From(ctx).Table(k.userTable).Select("id").Where("org_id = ?", orgID)
	rows, total, err := s.sessions.ListActivePageInOrg(ctx, k.portal, orgID, users, q.Page, q.PageSize)
	if err != nil {
		return nil, 0, wrap("list sessions", err)
	}
	ids := make([]uint64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	names, err := s.usernames(ctx, k, ids)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Session, len(rows))
	for i, r := range rows {
		out[i] = Session{
			SID: r.SID, UserID: r.UserID, Username: names[r.UserID], IP: r.IP, UserAgent: r.UserAgent,
			Locked: r.LockedAt != nil, CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt,
		}
	}
	return out, total, nil
}

// ---- 写 ----

// InitialPassword 是一份随机初始密码和它的哈希（建主体、重置主账号密码用）。算哈希慢：调用方在拿锁（WithActor）之前
// 用 NewInitialPassword 生成好再传进来，平台的写操作不在锁里等它。零值不可用。
type InitialPassword struct {
	plain, hash string
}

// Plain 返回明文：只在建主体、重置之后的那一次响应里给出去。
func (p InitialPassword) Plain() string { return p.plain }

// errNoPassword：调用方没有传 NewInitialPassword 生成的密码。是编程错误，请求以 500 终止。
var errNoPassword = errors.New("org: 缺少初始密码（用 NewInitialPassword 生成）")

// NewInitialPassword 生成随机初始密码（满足任何合法的密码策略）并算好哈希，算法和参数与各端登录核对用的一致。
// 算哈希和核对密码占同一个并发上限（D-068），满了回 429，不排队：调用方先做完便宜的检查（CheckCreate、CheckResetOwner）再调。
func (s *Service) NewInitialPassword() (InitialPassword, error) {
	plain, err := password.Generate(0)
	if err != nil {
		return InitialPassword{}, err
	}
	hash, err := s.hash(plain)
	if err != nil {
		return InitialPassword{}, err
	}
	return InitialPassword{plain: plain, hash: hash}, nil
}

// CreateInput 是建主体的入参。主账号随主体一起建，初始密码由 NewInitialPassword 生成。
type CreateInput struct {
	Name             string
	ContactName      string
	ContactPhone     string
	AgentID          uint64 // 只对商户：所属代理商，0 表示直属平台
	Sort             uint
	Remark           string
	OwnerUsername    string // 主账号的登录名（主体内唯一）
	OwnerDisplayName string
}

// Created 是建主体的结果。Password 是主账号的初始密码：只在这里返回一次，库里只有哈希；主账号首次登录必须改密。
type Created struct {
	Org      Info   `json:"org"`
	OwnerID  uint64 `json:"ownerId"`
	Password string `json:"password"`
}

// codeAttempts 是编号撞上已有编号时最多试几次（8 位随机数字，撞上的概率很小）。
const codeAttempts = 5

// cleanCreate 校验、整理建主体的入参：主体的字段、主账号的登录名和显示名。不读库。
func cleanCreate(k Kind, in CreateInput) (fields orgFields, username, display string, err error) {
	if in.AgentID != 0 && !k.parent {
		return fields, "", "", errKind
	}
	if fields, err = cleanFields(in.Name, in.ContactName, in.ContactPhone, in.Sort, in.Remark); err != nil {
		return fields, "", "", err
	}
	username = portal.NormalizeUsername(in.OwnerUsername)
	if !usernameRe.MatchString(username) {
		return fields, "", "", fieldErr("ownerUsername", "org.username", "3-64 characters: a lowercase letter first, then lowercase letters, digits, '_', '.' or '-'")
	}
	if display, err = text("ownerDisplayName", in.OwnerDisplayName, MaxDisplayName, false); err != nil {
		return fields, "", "", err
	}
	if display == "" {
		display = username // 和平台建用户、主体内建账号一样：没填显示名就用登录名
	}
	return fields, username, display, nil
}

// CheckCreate 只做 Create 的字段校验，不读库。调用方在 NewInitialPassword 之前调（D-068）：字段不合法的请求不去算哈希。
// 只是为了不白算：Create 自己照样校验。
func (s *Service) CheckCreate(k Kind, in CreateInput) error {
	if err := k.check(); err != nil {
		return err
	}
	_, _, _, err := cleanCreate(k, in)
	return err
}

// CheckResetOwner 是重置主账号密码之前的便宜检查（D-068）：主体存在（否则 404）、有主账号。普通读、不加锁，
// 调用方在 NewInitialPassword 之前调；只是为了不白算，ResetOwnerPassword 在锁里照样认定。
func (s *Service) CheckResetOwner(ctx context.Context, k Kind, id uint64) error {
	o, err := s.findOrg(ctx, k, false, "id = ?", id)
	if err != nil {
		return notFound(err)
	}
	if o.OwnerUserID == 0 {
		return fieldErr("id", "org.noOwner", "this org has no owner account")
	}
	return nil
}

// Create 建主体和它的主账号，主账号的初始密码是 pwd（NewInitialPassword 生成）。by 是平台端的操作人。
func (s *Service) Create(ctx context.Context, k Kind, in CreateInput, pwd InitialPassword, by uint64) (*Created, error) {
	if err := k.check(); err != nil {
		return nil, err
	}
	fields, username, display, err := cleanCreate(k, in)
	if err != nil {
		return nil, err
	}
	if pwd.hash == "" {
		return nil, errNoPassword
	}

	var out *Created
	err = db.Tx(ctx, func(ctx context.Context) error {
		if k.parent {
			if err := s.checkAgent(ctx, in.AgentID); err != nil {
				return err
			}
		}
		now := s.now().UTC()
		row := orgRow{
			Name: fields.name, ContactName: fields.contactName, ContactPhone: fields.contactPhone, AgentID: in.AgentID,
			Status: StatusEnabled, Sort: fields.sort, Remark: fields.remark,
			CreatedAt: now, UpdatedAt: now, CreatedBy: by, UpdatedBy: by,
		}
		if err := s.insertOrg(ctx, k, &row); err != nil {
			return err
		}
		u := userRow{
			OrgID: row.ID, Username: username, PasswordHash: pwd.hash, DisplayName: display,
			MustChangePwd: true, Status: StatusEnabled, CreatedAt: now, UpdatedAt: now,
			// 账号行的 created_by、updated_by 是本端的用户 ID：平台随主体一起建的主账号为 0
		}
		if err := db.From(ctx).Table(k.userTable).Create(&u).Error; err != nil {
			return wrap("create owner", err)
		}
		if err := db.From(ctx).Table(k.orgTable).Where("id = ?", row.ID).Update("owner_user_id", u.ID).Error; err != nil {
			return wrap("set owner", err)
		}
		row.OwnerUserID = u.ID
		info := row.info()
		info.OwnerUsername = u.Username
		out = &Created{Org: info, OwnerID: u.ID, Password: pwd.plain}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// insertOrg 插入主体行，编号随机；编号撞上已有的就换一个再试。
func (s *Service) insertOrg(ctx context.Context, k Kind, row *orgRow) error {
	for i := 0; ; i++ {
		digits, err := s.digits()
		if err != nil {
			return err
		}
		row.Code = k.prefix + digits
		err = db.From(ctx).Table(k.orgTable).Omit(k.omit()...).Create(row).Error
		if err == nil {
			return nil
		}
		// 唯一键只有编号一个；MySQL 的唯一键冲突只回滚这一条语句，事务可以接着用
		if !errors.Is(err, gorm.ErrDuplicatedKey) || i+1 >= codeAttempts {
			return wrap("create org", err)
		}
		row.ID = 0
	}
}

// randomDigits 返回 8 位随机数字（不按顺序，不暴露主体数量）。
func randomDigits() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", n.Int64()), nil
}

// UpdateInput 是改主体资料的入参（整份替换）。编号、主账号、状态、所属代理商各有单独的方法。
type UpdateInput struct {
	Name         string
	ContactName  string
	ContactPhone string
	Sort         uint
	Remark       string
}

// Update 改主体资料。主体不存在回 404。
func (s *Service) Update(ctx context.Context, k Kind, id uint64, in UpdateInput, by uint64) error {
	fields, err := cleanFields(in.Name, in.ContactName, in.ContactPhone, in.Sort, in.Remark)
	if err != nil {
		return err
	}
	return db.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.lockOrg(ctx, k, id); err != nil {
			return err
		}
		if err := db.From(ctx).Table(k.orgTable).Where("id = ?", id).Updates(map[string]any{
			"name": fields.name, "contact_name": fields.contactName, "contact_phone": fields.contactPhone,
			"sort": fields.sort, "remark": fields.remark, "updated_at": s.now().UTC(), "updated_by": by,
		}).Error; err != nil {
			return wrap("update org", err)
		}
		s.orgChanged(ctx, k)
		return nil
	})
}

// SetStatus 启用或停用主体。停用时在同一个事务里吊销这个主体的全部会话；它的账号此后按停用处理（D-061）。
// 代理商停用不影响名下商户。主体不存在回 404。
//
// 吊销之前先锁住这个主体的全部账号行（按 ID 顺序）：主体端的本人改密、下线其他设备是"账号行 → 自己的会话行 → 其他会话行"，
// 这里如果跳过账号行直接按主体批量改会话行，两边锁会话行的顺序不同，会死锁。先锁账号行就回到"主体行 → 账号行 → 会话行"，
// 和对方排队。
func (s *Service) SetStatus(ctx context.Context, k Kind, id uint64, status int, by uint64) error {
	if status != StatusEnabled && status != StatusDisabled {
		return fieldErr("status", "org.status", "must be 0 or 1")
	}
	return db.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.lockOrg(ctx, k, id); err != nil {
			return err
		}
		if err := db.From(ctx).Table(k.orgTable).Where("id = ?", id).Updates(map[string]any{
			"status": status, "updated_at": s.now().UTC(), "updated_by": by,
		}).Error; err != nil {
			return wrap("set status", err)
		}
		if status == StatusDisabled {
			var ids []uint64
			if err := db.From(ctx).Table(k.userTable).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("org_id = ?", id).Order("id").Pluck("id", &ids).Error; err != nil {
				return wrap("lock accounts", err)
			}
			// 已经停用的再停用一次也吊销：停用期间建不出新会话，这里只是幂等
			if _, err := s.sessions.RevokeOrg(ctx, k.portal, id, auth.RevokeDisabled); err != nil {
				return wrap("revoke org sessions", err)
			}
		}
		s.orgChanged(ctx, k)
		return nil
	})
}

// ResetOwnerPassword 把主账号的密码换成 pwd（NewInitialPassword 生成，调用方只在这次响应里给出明文）、要求下次登录改密，
// 并吊销主账号的全部会话（同一个事务）。主体不存在回 404。
func (s *Service) ResetOwnerPassword(ctx context.Context, k Kind, id uint64, pwd InitialPassword) error {
	if err := k.check(); err != nil {
		return err
	}
	if pwd.hash == "" {
		return errNoPassword
	}
	return db.Tx(ctx, func(ctx context.Context) error {
		o, err := s.lockOrg(ctx, k, id)
		if err != nil {
			return err
		}
		if o.OwnerUserID == 0 {
			return fieldErr("id", "org.noOwner", "this org has no owner account")
		}
		now := s.now().UTC()
		res := db.From(ctx).Table(k.userTable).Where("id = ? AND org_id = ?", o.OwnerUserID, id).Updates(map[string]any{
			"password_hash": pwd.hash, "must_change_pwd": true, "pwd_changed_at": now, "updated_at": now,
			"updated_by": 0, // 账号行的操作人是本端的用户 ID：平台改的为 0
		})
		if res.Error != nil {
			return wrap("reset owner password", res.Error)
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("org: 主体 %s/%d 的主账号 %d 不存在", k.portal, id, o.OwnerUserID)
		}
		if _, err := s.sessions.RevokeUser(ctx, k.portal, o.OwnerUserID, auth.RevokePwdChange, ""); err != nil {
			return wrap("revoke owner sessions", err)
		}
		s.accountChanged(ctx, k, o.OwnerUserID)
		return nil
	})
}

// ChangeOwner 把主账号换成这个主体里另一个启用的账号。主体不存在回 404。
//
// 原来的主账号留下，成为一个没有角色的普通账号（D-101）：同一个事务里吊销它的全部会话、清空它的角色。
// 主账号的权力来自"是主账号"，它身上的角色平时不起作用；换人之后还留着，等于让它带着一组没人审过的权限
// 继续登录着。新主账号需要的话可以重新给它分配。新主账号自己的会话和角色不动。
func (s *Service) ChangeOwner(ctx context.Context, k Kind, id, userID uint64, by uint64) error {
	return db.Tx(ctx, func(ctx context.Context) error {
		o, err := s.lockOrg(ctx, k, id)
		if err != nil {
			return err
		}
		if o.OwnerUserID == userID {
			return nil
		}
		var u userRow
		err = db.From(ctx).Table(k.userTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", userID).Take(&u).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && u.OrgID != id) {
			// 别的主体的账号和不存在的一样：不暴露这个 ID 在别处存在
			return fieldErr("userId", "org.ownerNotInOrg", "the account does not belong to this org")
		}
		if err != nil {
			return wrap("find account", err)
		}
		if u.Status != StatusEnabled {
			return fieldErr("userId", "org.ownerDisabled", "the account is disabled")
		}
		if err := db.From(ctx).Table(k.orgTable).Where("id = ?", id).Updates(map[string]any{
			"owner_user_id": userID, "updated_at": s.now().UTC(), "updated_by": by,
		}).Error; err != nil {
			return wrap("change owner", err)
		}
		if err := s.retireOwner(ctx, k, id, o.OwnerUserID); err != nil {
			return err
		}
		s.orgChanged(ctx, k)
		return nil
	})
}

// retireOwner 处理被换下来的主账号（D-101）：吊销全部会话、清空角色。主体行已经锁住。
// 先锁账号行再动会话行，和别的管理写入同一个顺序（D-089）。账号已经不在了（或者主体原来就没有主账号）什么都不做。
func (s *Service) retireOwner(ctx context.Context, k Kind, orgID, userID uint64) error {
	if userID == 0 {
		return nil
	}
	var prev userRow
	err := db.From(ctx).Table(k.userTable).Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND org_id = ?", userID, orgID).Take(&prev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return wrap("find previous owner", err)
	}
	if _, err := s.sessions.RevokeUser(ctx, k.portal, userID, auth.RevokeAdmin, ""); err != nil {
		return wrap("revoke previous owner sessions", err)
	}
	if s.clearRoles != nil {
		if err := s.clearRoles(ctx, k.portal, userID); err != nil {
			return wrap("clear previous owner roles", err)
		}
	}
	s.accountChanged(ctx, k, userID)
	return nil
}

// SetAgent 把商户挂到另一个代理商下面（agentID 为 0 表示改为直属平台）。代理商必须存在且启用。商户不存在回 404。
// 归属是每次查询现算的（ByChildOrgs）：原代理商下一次请求就看不到这个商户了。
func (s *Service) SetAgent(ctx context.Context, merchantID, agentID uint64, by uint64) error {
	k := Merchant()
	return db.Tx(ctx, func(ctx context.Context) error {
		o, err := s.lockOrg(ctx, k, merchantID)
		if err != nil {
			return err
		}
		if o.AgentID == agentID {
			return nil
		}
		if err := s.checkAgent(ctx, agentID); err != nil {
			return err
		}
		return wrap("set agent", db.From(ctx).Table(k.orgTable).Where("id = ?", merchantID).Updates(map[string]any{
			"agent_id": agentID, "updated_at": s.now().UTC(), "updated_by": by,
		}).Error)
	})
}

// checkAgent 确认代理商存在且启用；0 表示直属平台，不用查。
func (s *Service) checkAgent(ctx context.Context, agentID uint64) error {
	if agentID == 0 {
		return nil
	}
	a, err := s.findOrg(ctx, Agent(), false, "id = ?", agentID)
	if errors.Is(err, errOrgMissing) {
		return fieldErr("agentId", "org.agent", "the agent does not exist")
	}
	if err != nil {
		return err
	}
	if a.Status != StatusEnabled {
		return fieldErr("agentId", "org.agentDisabled", "the agent is disabled")
	}
	return nil
}

// RevokeSession 让主体的一个会话下线。会话不属于这个端的这个主体时回 404（会话 ID 是全局随机的）。
func (s *Service) RevokeSession(ctx context.Context, k Kind, orgID uint64, sid string) error {
	if err := k.check(); err != nil {
		return err
	}
	ss, err := s.sessions.Get(ctx, sid)
	if err != nil {
		return wrap("get session", err)
	}
	if ss == nil || ss.Portal != k.portal || ss.OrgID != orgID {
		return httpx.ErrNotFound
	}
	err = s.sessions.Revoke(ctx, k.portal, sid, auth.RevokeAdmin)
	if errors.Is(err, session.ErrNotFound) {
		return httpx.ErrNotFound
	}
	return wrap("revoke session", err)
}

// ---- 校验 ----

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,63}$`)

type orgFields struct {
	name, contactName, contactPhone, remark string
	sort                                    uint
}

func cleanFields(name, contactName, contactPhone string, sort uint, remark string) (orgFields, error) {
	var f orgFields
	var err error
	if f.name, err = text("name", name, MaxName, true); err != nil {
		return f, err
	}
	if f.contactName, err = text("contactName", contactName, MaxContactName, false); err != nil {
		return f, err
	}
	if f.contactPhone, err = text("contactPhone", contactPhone, MaxContactPhone, false); err != nil {
		return f, err
	}
	if sort > MaxSort {
		return f, fieldErr("sort", "org.sort", "at most {max}", "max", MaxSort)
	}
	f.sort = sort
	// 备注可以多行，只查长度
	f.remark = strings.TrimSpace(remark)
	if !utf8.ValidString(f.remark) {
		return f, fieldErr("remark", "org.textChars", "must not contain control or invisible characters")
	}
	if utf8.RuneCountInString(f.remark) > MaxRemark {
		return f, fieldErr("remark", "org.textLength", "at most {max} characters", "max", MaxRemark)
	}
	return f, nil
}

// text 去掉首尾空白，检查字符（不能有控制字符、不可见的格式字符）和长度（按字符数）。
func text(field, s string, maxLen int, required bool) (string, error) {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) {
		return "", fieldErr(field, "org.textChars", "must not contain control or invisible characters")
	}
	n := 0
	for _, r := range s {
		n++
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return "", fieldErr(field, "org.textChars", "must not contain control or invisible characters")
		}
	}
	if required && n == 0 {
		return "", fieldErr(field, "org.required", "required")
	}
	if n > maxLen {
		return "", fieldErr(field, "org.textLength", "at most {max} characters", "max", maxLen)
	}
	return s, nil
}

func fieldErr(field, key, msg string, kv ...any) error {
	return httpx.ErrValidation.WithFields(httpx.NewField(field, key, msg, kv...))
}

// escapeLike 转义 LIKE 通配符，让关键字里的 % 和 _ 按字面匹配。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
