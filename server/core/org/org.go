// Package org 是主体与主体账号（D-065）：代理商、商户两种主体的表、主体端的用户来源，以及平台端的管理操作。
//
// 两种主体共用一份实现，差别只有端代号、表名、编号前缀和归属关系（商户可以挂在一个代理商下面）：
//
//	orgs := deps.Orgs                               // app 在有数据库时创建
//	p := portal.Portal{Code: org.Merchant().Portal(), Users: orgs.Users(org.Merchant()), Scoped: true}
//
// 平台、代理商、商户是三个程序、共用一个库。平台程序里没有另外两个端的认证器，所以这里的管理操作自己按库读写
// 主体表、账号表和会话表：吊销会话、重置主账号密码都直接写库，提交后回调通知别的程序清缓存（D-075），
// 没配 Redis 或通知漏收时，状态缓存最长 15 秒后跟上；
// 写操作在锁内按库认定，立即生效（D-061、D-063）。
//
// 加锁顺序和授权一致（D-063）："主体行 → 账号行 → 会话行"。平台的写操作由调用方放进 rbac 的 WithActor（平台超管锁），
// 再在这里锁主体行；主体端从不锁平台的超管角色行，不会成环。
package org

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/password"
	"github.com/goalladmin/goalladmin/server/core/internal/session"
)

// Kind 描述一种主体：端代号、主体表、账号表、编号前缀。只能用 Agent()、Merchant() 取得，零值不可用。
type Kind struct {
	portal    string
	orgTable  string
	userTable string
	prefix    string
	parent    bool // 商户：主体表有 agent_id（所属代理商）
}

// Agent 是代理商：agent 端，编号 A 开头。
func Agent() Kind {
	return Kind{portal: "agent", orgTable: "ga_agent", userTable: "ga_agent_user", prefix: "A"}
}

// Merchant 是商户：merchant 端，编号 M 开头；可以挂在一个代理商下面（agent_id，0 表示直属平台）。
func Merchant() Kind {
	return Kind{portal: "merchant", orgTable: "ga_merchant", userTable: "ga_merchant_user", prefix: "M", parent: true}
}

// Portal 返回这种主体所在的端代号。
func (k Kind) Portal() string { return k.portal }

// String 返回端代号（日志用）。
func (k Kind) String() string { return k.portal }

// errKind：零值的 Kind，或者在只适用于商户的操作上传了代理商。是调用方的编程错误，请求以 500 终止。
var errKind = errors.New("org: 主体种类无效（只能用 org.Agent() 或 org.Merchant()）")

func (k Kind) check() error {
	if k != Agent() && k != Merchant() {
		return errKind
	}
	return nil
}

// omit 返回整行写入主体表时要去掉的列：代理商表没有 agent_id。
func (k Kind) omit() []string {
	if k.parent {
		return nil
	}
	return []string{"agent_id"}
}

// 状态。
const (
	StatusEnabled  = 1
	StatusDisabled = 0
)

// 字段长度（和迁移 00015 的列宽一致）。
const (
	MaxName         = 64
	MaxContactName  = 64
	MaxContactPhone = 32
	MaxDisplayName  = 64
	MaxRemark       = 255
	MaxSort         = 1000000
)

// Options 是 New 的参数。
type Options struct {
	Now func() time.Time
	// Hash 生成密码哈希（建主体、重置主账号密码的初始密码用）。由 core/app 传进来：进程共用的哈希器，先在密码计算的
	// 并发闸门里占一个位置，满了返回 httpx.ErrTooManyRequests（D-068、D-070）。nil 时用默认参数、不限并发。
	Hash func(plain string) (string, error)
	// 提交后的缓存失效通知（D-075）；平台程序也能通知本进程未注册的主体端。
	OnAccountChange func(portal string, userID uint64)
	OnOrgChange     func(portal string)
	OnSessionChange func(portal, sid string)
	// ClearRoles 清空一个账号在这个端的全部角色（更换主账号时用在原主账号身上，D-101）。在调用方的事务里执行，
	// 主体行已经锁住。由 core/app 接到授权服务上；nil 时不清（没有授权服务的装配）。
	ClearRoles func(ctx context.Context, portal string, userID uint64) error
}

// Service 是主体服务。所有方法通过 db.From(ctx) 拿句柄：在调用方的事务里就加入它。
type Service struct {
	now             func() time.Time
	sessions        *session.Manager
	hash            func(plain string) (string, error)
	digits          func() (string, error) // 编号里的 8 位数字；测试可以换掉，用来制造编号冲突
	onAccountChange func(portal string, userID uint64)
	onOrgChange     func(portal string)
	clearRoles      func(ctx context.Context, portal string, userID uint64) error
}

// New 创建主体服务。
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Hash == nil {
		// 哈希器建的时候要算一次哈希（抹平耗时用的假密码），用到时再建
		hasher := sync.OnceValue(func() *password.Hasher { return password.NewHasher(password.DefaultParams()) })
		o.Hash = func(plain string) (string, error) { return hasher().Hash(plain) }
	}
	return &Service{
		now:             o.Now,
		sessions:        session.NewManager(session.Config{Now: o.Now, OnChange: o.OnSessionChange}),
		hash:            o.Hash,
		digits:          randomDigits,
		onAccountChange: o.OnAccountChange,
		onOrgChange:     o.OnOrgChange,
		clearRoles:      o.ClearRoles,
	}
}

func (s *Service) accountChanged(ctx context.Context, k Kind, userID uint64) {
	if s.onAccountChange != nil {
		db.AfterCommit(ctx, func() { s.onAccountChange(k.portal, userID) })
	}
}

func (s *Service) orgChanged(ctx context.Context, k Kind) {
	if s.onOrgChange != nil {
		db.AfterCommit(ctx, func() { s.onOrgChange(k.portal) })
	}
}

// Info 是主体的完整资料（平台管理用）。
type Info struct {
	ID            uint64    `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	ContactName   string    `json:"contactName"`
	ContactPhone  string    `json:"contactPhone"`
	AgentID       uint64    `json:"agentId"` // 商户：所属代理商，0 表示直属平台；代理商恒为 0
	OwnerUserID   uint64    `json:"ownerUserId"`
	OwnerUsername string    `json:"ownerUsername"` // 主账号的登录名；没有主账号时为空
	Status        int       `json:"status"`
	Sort          uint      `json:"sort"`
	Remark        string    `json:"remark"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	CreatedBy     uint64    `json:"createdBy"` // 平台端的用户 ID
	UpdatedBy     uint64    `json:"updatedBy"`
}

// Brief 是主体的编号和名称（列表里显示所属代理商、日志里显示主体用）。
type Brief struct {
	ID     uint64 `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Status int    `json:"status"`
}

// Account 是主体账号的只读视图（不含密码哈希）。
type Account struct {
	ID            uint64     `json:"id"`
	OrgID         uint64     `json:"orgId"`
	Username      string     `json:"username"`
	DisplayName   string     `json:"displayName"`
	Email         string     `json:"email"`
	Phone         string     `json:"phone"`
	Avatar        string     `json:"avatar"`
	Owner         bool       `json:"owner"` // 是不是这个主体的主账号
	MustChangePwd bool       `json:"mustChangePwd"`
	LastLoginAt   *time.Time `json:"lastLoginAt"`
	LastLoginIP   string     `json:"lastLoginIp"`
	Status        int        `json:"status"`
	Sort          uint       `json:"sort"`
	Remark        string     `json:"remark"` // 主体自己写的备注
	CreatedAt     time.Time  `json:"createdAt"`
}

// Session 是主体账号的一个有效会话。
type Session struct {
	SID        string    `json:"sid"`
	UserID     uint64    `json:"userId"`
	Username   string    `json:"username"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	Locked     bool      `json:"locked"` // 锁屏中（D-027）
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// ---- 表的行（不导出：内核公开包不暴露带表的 gorm 模型，D-044）----

type orgRow struct {
	ID           uint64    `gorm:"column:id;primaryKey"`
	Code         string    `gorm:"column:code"`
	Name         string    `gorm:"column:name"`
	ContactName  string    `gorm:"column:contact_name"`
	ContactPhone string    `gorm:"column:contact_phone"`
	AgentID      uint64    `gorm:"column:agent_id"` // 只有商户表有这一列：代理商表写入时 Omit 掉（Kind.omit）
	OwnerUserID  uint64    `gorm:"column:owner_user_id"`
	Status       int       `gorm:"column:status"`
	Sort         uint      `gorm:"column:sort"`
	Remark       string    `gorm:"column:remark"`
	CreatedAt    time.Time `gorm:"column:created_at"`
	UpdatedAt    time.Time `gorm:"column:updated_at"`
	CreatedBy    uint64    `gorm:"column:created_by"`
	UpdatedBy    uint64    `gorm:"column:updated_by"`
}

func (r *orgRow) info() Info {
	return Info{
		ID: r.ID, Code: r.Code, Name: r.Name, ContactName: r.ContactName, ContactPhone: r.ContactPhone,
		AgentID: r.AgentID, OwnerUserID: r.OwnerUserID, Status: r.Status, Sort: r.Sort, Remark: r.Remark,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, CreatedBy: r.CreatedBy, UpdatedBy: r.UpdatedBy,
	}
}

type userRow struct {
	ID            uint64     `gorm:"column:id;primaryKey"`
	OrgID         uint64     `gorm:"column:org_id"`
	Username      string     `gorm:"column:username"`
	PasswordHash  string     `gorm:"column:password_hash"`
	DisplayName   string     `gorm:"column:display_name"`
	Email         string     `gorm:"column:email"`
	Phone         string     `gorm:"column:phone"`
	Avatar        string     `gorm:"column:avatar"`
	MustChangePwd bool       `gorm:"column:must_change_pwd"`
	PwdChangedAt  *time.Time `gorm:"column:pwd_changed_at"`
	LastLoginAt   *time.Time `gorm:"column:last_login_at"`
	LastLoginIP   string     `gorm:"column:last_login_ip"`
	Status        int        `gorm:"column:status"`
	Sort          uint       `gorm:"column:sort"`
	Remark        string     `gorm:"column:remark"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
	UpdatedAt     time.Time  `gorm:"column:updated_at"`
	CreatedBy     uint64     `gorm:"column:created_by"`
	UpdatedBy     uint64     `gorm:"column:updated_by"`
}

func (u *userRow) account(ownerID uint64) Account {
	return Account{
		ID: u.ID, OrgID: u.OrgID, Username: u.Username, DisplayName: u.DisplayName, Email: u.Email, Phone: u.Phone,
		Avatar: u.Avatar, Owner: ownerID != 0 && u.ID == ownerID, MustChangePwd: u.MustChangePwd,
		LastLoginAt: u.LastLoginAt, LastLoginIP: u.LastLoginIP, Status: u.Status, Sort: u.Sort, Remark: u.Remark, CreatedAt: u.CreatedAt,
	}
}

// notFound 把"主体不存在"翻成 404。
func notFound(err error) error {
	if errors.Is(err, errOrgMissing) {
		return httpx.ErrNotFound.WithCause(err)
	}
	return err
}

// errOrgMissing 是内部的"主体不存在"；对外的接口层翻成 404，主体端的用户来源翻成 portal.ErrOrgNotFound。
var errOrgMissing = errors.New("org: 主体不存在")

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("org: %s: %w", op, err)
}
