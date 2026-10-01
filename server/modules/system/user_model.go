package system

import "time"

// User 对应 ga_user。字段顺序与表一致（docs/conventions.md）。
type User struct {
	ID            uint64     `gorm:"column:id;primaryKey"`
	Username      string     `gorm:"column:username"`
	PasswordHash  string     `gorm:"column:password_hash" json:"-"` // 永远不序列化；对外只用 UserView
	DisplayName   string     `gorm:"column:display_name"`
	Email         string     `gorm:"column:email"`
	Phone         string     `gorm:"column:phone"`
	Avatar        string     `gorm:"column:avatar"`
	Bio           string     `gorm:"column:bio"`     // 个人简介，本人在个人中心维护（D-038）
	DeptID        uint64     `gorm:"column:dept_id"` // 所属部门（D-033），0 表示未分配
	MustChangePwd bool       `gorm:"column:must_change_pwd"`
	PwdChangedAt  *time.Time `gorm:"column:pwd_changed_at"`
	MFASecret     []byte     `gorm:"column:mfa_secret"`
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

// TableName 固定表名。
func (User) TableName() string { return "ga_user" }

// 状态取值（docs/conventions.md）。
const (
	StatusEnabled  = 1
	StatusDisabled = 0
)
