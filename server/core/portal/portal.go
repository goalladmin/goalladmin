// Package portal 定义"端"：一组独立的用户来源、登录入口、签名密钥和权限集。
//
// 框架内置 platform 端；业务方可以注册更多端（如商户端、代理商端），每个端的令牌互不通用。
package portal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Portal 是一个端的声明。模块在 Init 里把它注册进 Registry。
type Portal struct {
	Code       string         // 端代号，也是路由前缀 /api/<code>/v1 和令牌里的 aud
	Users      UserProvider   // 这个端的用户从哪来
	Dept       DeptProvider   // 可选：用户所属部门和部门树（按部门的数据权限用，D-039）；为 nil 时部门类范围按仅本人算
	AccessTTL  time.Duration  // 0 表示取配置文件里的值
	RefreshTTL time.Duration  // 0 表示取配置文件里的值
	Login      LoginPolicy    // 零值字段取 DefaultLoginPolicy()；配置文件 portals.<code>.login 可以再调严
	Password   PasswordPolicy // 零值字段取 DefaultPasswordPolicy()；配置文件 portals.<code>.password 可以再调严
	// Scoped 为 true 的端是主体端（D-061）：每个账号属于一个主体（代理商、商户……），登录要带主体编号，
	// Principal.OrgID 就是这个主体，Principal.Super 表示本主体的主账号。Users 必须实现 OrgUserProvider。
	Scoped bool
}

// LoginPolicy 是登录防护参数（规范 §5.7）。限流和锁定没有"关闭"开关，只能在底线范围内调整（D-024）。
type LoginPolicy struct {
	Window               time.Duration // 统计失败次数的窗口
	CaptchaAlways        bool          // 每次登录都要验证码
	CaptchaAfterFailures int           // 同账号+IP 在窗口内失败这么多次后要求验证码
	LockAfterFailures    int           // 同账号+IP 在窗口内失败这么多次后锁定
	AccountLockAfter     int           // 同账号在窗口内来自所有 IP 的失败累计到这么多次后锁定账号
	LockDuration         time.Duration // 锁定时长
	IPRatePerMinute      int           // 同 IP 每分钟允许的登录请求数
	AccountRatePerMinute int           // 同账号每分钟的登录请求数；超出后这个账号的登录必须带验证码（D-103）
}

// DefaultLoginPolicy 返回规范里的默认值。
func DefaultLoginPolicy() LoginPolicy {
	return LoginPolicy{
		Window:               15 * time.Minute,
		CaptchaAfterFailures: 3,
		LockAfterFailures:    10,
		AccountLockAfter:     50,
		LockDuration:         15 * time.Minute,
		IPRatePerMinute:      20,
		AccountRatePerMinute: 10,
	}
}

// Normalized 把零值字段补成默认值。负数不补，留给 Validate 拒绝：配置里写负数不能被悄悄当成默认值。
func (p LoginPolicy) Normalized() LoginPolicy {
	d := DefaultLoginPolicy()
	if p.Window == 0 {
		p.Window = d.Window
	}
	if p.CaptchaAfterFailures == 0 {
		p.CaptchaAfterFailures = d.CaptchaAfterFailures
	}
	if p.LockAfterFailures == 0 {
		p.LockAfterFailures = d.LockAfterFailures
	}
	if p.AccountLockAfter == 0 {
		p.AccountLockAfter = d.AccountLockAfter
	}
	if p.LockDuration == 0 {
		p.LockDuration = d.LockDuration
	}
	if p.IPRatePerMinute == 0 {
		p.IPRatePerMinute = d.IPRatePerMinute
	}
	if p.AccountRatePerMinute == 0 {
		p.AccountRatePerMinute = d.AccountRatePerMinute
	}
	return p
}

// 登录防护的底线：配置只能在这个范围内取值，越界拒绝启动（D-024）。
// 上限约束的是"多宽松"（失败多少次才锁、每分钟多少次），下限约束的是"多短"（锁多久、按多长的窗口计数）。
const (
	MaxCaptchaAfterFailures = 5
	MaxLockAfterFailures    = 20
	MaxAccountLockAfter     = 200
	MaxIPRatePerMinute      = 600
	MaxAccountRatePerMinute = 120
	MinLockDuration         = time.Minute
	MinWindow               = time.Minute
	MaxPolicyDuration       = 24 * time.Hour // 统计窗口和锁定时长的上限
)

// Validate 检查登录防护参数是否在底线范围内（先 Normalized 再调用）。
func (p LoginPolicy) Validate() error {
	switch {
	case p.Window < MinWindow || p.Window > MaxPolicyDuration:
		return fmt.Errorf("login.window 须在 %s–%s 之间", MinWindow, MaxPolicyDuration)
	case p.CaptchaAfterFailures < 1 || p.CaptchaAfterFailures > MaxCaptchaAfterFailures:
		return fmt.Errorf("login.captchaAfterFailures 须在 1–%d 之间（每次都要验证码用 captchaAlways）", MaxCaptchaAfterFailures)
	case p.LockAfterFailures < 1 || p.LockAfterFailures > MaxLockAfterFailures:
		return fmt.Errorf("login.lockAfterFailures 须在 1–%d 之间", MaxLockAfterFailures)
	case p.AccountLockAfter < 1 || p.AccountLockAfter > MaxAccountLockAfter:
		return fmt.Errorf("login.accountLockAfter 须在 1–%d 之间", MaxAccountLockAfter)
	case p.LockDuration < MinLockDuration || p.LockDuration > MaxPolicyDuration:
		return fmt.Errorf("login.lockDuration 须在 %s–%s 之间", MinLockDuration, MaxPolicyDuration)
	case p.IPRatePerMinute < 1 || p.IPRatePerMinute > MaxIPRatePerMinute:
		return fmt.Errorf("login.ipRatePerMinute 须在 1–%d 之间", MaxIPRatePerMinute)
	case p.AccountRatePerMinute < 1 || p.AccountRatePerMinute > MaxAccountRatePerMinute:
		return fmt.Errorf("login.accountRatePerMinute 须在 1–%d 之间", MaxAccountRatePerMinute)
	}
	return nil
}

// PasswordPolicy 是密码策略（规范 §5.6）。"至少包含字母和数字、不等于用户名、不与上一次相同"始终生效，
// 这里的开关只能在此之上加要求。
type PasswordPolicy struct {
	MinLength     int           // 最短长度（字符数）
	RequireUpper  bool          // 必须含大写字母
	RequireLower  bool          // 必须含小写字母
	RequireSymbol bool          // 必须含符号（字母、数字、空白以外的字符）
	MaxAge        time.Duration // 密码有效期；0 表示不过期。过期后登录照常，但必须先改密才能做别的事
}

// 密码策略的底线（D-024）。
const (
	MinPasswordLength  = 8
	MaxPasswordLength  = 64
	DefaultPasswordLen = 10
	MinPasswordMaxAge  = 24 * time.Hour
	MaxPasswordMaxAge  = 3650 * 24 * time.Hour
)

// DefaultPasswordPolicy 返回规范里的默认值：至少 10 位，字母加数字，不过期。
func DefaultPasswordPolicy() PasswordPolicy { return PasswordPolicy{MinLength: DefaultPasswordLen} }

// Normalized 把零值字段补成默认值。
func (p PasswordPolicy) Normalized() PasswordPolicy {
	if p.MinLength == 0 {
		p.MinLength = DefaultPasswordLen
	}
	return p
}

// Validate 检查密码策略是否在底线范围内（先 Normalized 再调用）。
func (p PasswordPolicy) Validate() error {
	if p.MinLength < MinPasswordLength || p.MinLength > MaxPasswordLength {
		return fmt.Errorf("password.minLength 须在 %d–%d 之间", MinPasswordLength, MaxPasswordLength)
	}
	if p.MaxAge != 0 && (p.MaxAge < MinPasswordMaxAge || p.MaxAge > MaxPasswordMaxAge) {
		return fmt.Errorf("password.maxAgeDays 须为 0（不过期）或 1–3650")
	}
	// 配置文件按天写；代码里声明的也必须是整天，安全设置页按天展示时才不会显示得比实际更严（D-034）
	if p.MaxAge%(24*time.Hour) != 0 {
		return fmt.Errorf("password.maxAge 须为整天数")
	}
	return nil
}

// Expired 报告密码是否已过期。changedAt 为空（从没改过密码）按已过期处理。
func (p PasswordPolicy) Expired(changedAt *time.Time, now time.Time) bool {
	if p.MaxAge <= 0 {
		return false
	}
	return changedAt == nil || now.Sub(*changedAt) > p.MaxAge
}

// UserProvider 是端的用户来源。框架只通过它读写账号，不关心底层是哪张表。
//
// FindByUsername 收到的 username 已经过 NormalizeUsername；返回的账号必须满足
// NormalizeUsername(Account.Username) == username，否则框架把它当作账号不存在（D-043）：
// 登录防护按输入的登录名计数，底层查询若把别的写法（重音变体、邮箱、手机号）也当成同一个账号，
// 每种写法就各有一份尝试配额。要支持邮箱登录，请先把邮箱解析成账号名再调用登录。
type UserProvider interface {
	FindByUsername(ctx context.Context, username string) (*Account, error)
	FindByID(ctx context.Context, id uint64) (*Account, error)
	UpdatePasswordHash(ctx context.Context, id uint64, hash string, mustChange bool) error
	TouchLogin(ctx context.Context, id uint64, ip string, at time.Time) error
}

// UserLocker 是 UserProvider 可以选择实现的接口（D-047）：在当前事务里按 ID 读账号并加排他锁，
// 读到的是最新提交的数据，事务结束前别人改不了这一行。
//
// 登录、改密据此确认密码和账号状态；本人及管理写操作先锁账号再锁会话（D-089），与下线按同一顺序排队。
// 没实现它的用户来源退回事务里的普通读，不能提供同样的并发状态与会话下线保证。
type UserLocker interface {
	LockByID(ctx context.Context, id uint64) (*Account, error)
}

// PasswordRehasher 是 UserProvider 可以选择实现的接口（D-070）：把账号的密码哈希从 oldHash 换成 newHash，只动这一列——
// "必须改密"、改密时间、更新时间都不变，因为密码本身没变，换的只是存法。库里的哈希已经不是 oldHash（同时有人改了密、
// 重置了密码）时什么都不做、返回 nil，不能把别人刚写的密码盖掉。
//
// 框架在登录成功、发现存的不是"选的算法 + 当前参数"的哈希时，用这次输入的密码按当前的重算一份，在建会话的事务里调它。
// 没实现它的用户来源不升级，旧哈希照常核对。
type PasswordRehasher interface {
	RehashPassword(ctx context.Context, id uint64, oldHash, newHash string) error
}

// DeptProvider 提供端的部门结构（D-039）：按部门的数据权限靠它把"本部门""本部门及下级"解析成具体部门。
// 每次判断都现查，不缓存：调部门立即生效。v0.1 叫 OrgProvider，v0.2 改名，Org 留给"主体"（D-061）。
type DeptProvider interface {
	// UserDept 返回用户所属部门，0 表示未分配。
	UserDept(ctx context.Context, userID uint64) (uint64, error)
	// DeptSubtree 返回某部门和它的全部下级部门（含自己）。
	DeptSubtree(ctx context.Context, deptID uint64) ([]uint64, error)
}

// Org 是主体（代理商、商户……）的最小视图（D-061）。
type Org struct {
	ID          uint64
	Code        string // 主体编号，登录时输入；创建后不可改
	Name        string
	Status      int    // 1 启用，0 停用；停用的主体下所有账号都按停用处理
	OwnerUserID uint64 // 本主体的主账号（端内的用户 ID）；0 表示还没有
}

// Enabled 报告主体是否启用。
func (o *Org) Enabled() bool { return o != nil && o.Status == 1 }

// OrgUserProvider 是主体端（Portal.Scoped）的用户来源必须实现的接口（D-061）。账号名只在主体内唯一，
// 所以登录走 FindByOrgUsername；UserProvider.FindByUsername 在主体端不会被框架调用。
//
// FindOrgByCode 收到的编号已经过 NormalizeOrgCode，返回的主体必须满足 NormalizeOrgCode(Org.Code) == code，
// 否则框架按主体不存在处理；FindByOrgUsername 返回的账号必须属于 orgID（Account.OrgID == orgID），
// 规则同 UserProvider.FindByUsername。FindByID 返回的账号必须带上 OrgID：每次请求都要核对它和会话里的主体一致。
type OrgUserProvider interface {
	UserProvider
	FindOrgByCode(ctx context.Context, code string) (*Org, error)
	FindOrgByID(ctx context.Context, id uint64) (*Org, error)
	FindByOrgUsername(ctx context.Context, orgID uint64, username string) (*Account, error)
}

// OrgLocker 是主体端（Portal.Scoped）的用户来源必须实现的接口（D-063）：在当前事务里按 ID 读主体的最新提交状态，
// 并加排他锁（SELECT ... FOR UPDATE），事务结束前别人改不了这一行，也拿不到它的锁。
//
// 框架用它做两件事：登录在建会话的事务里确认主体仍然启用（同 UserLocker，D-047）；授权的写操作（建角色、授权、
// 分配角色、WithActor）用它把同一主体内的写操作串行起来——主体端没有超管角色行可锁。必须是排他锁：共享锁之间不互斥，
// 两个写操作会同时通过锁内的判断。加锁顺序是"主体行 → 账号行 → 会话行"。主体不存在时返回 ErrOrgNotFound。
type OrgLocker interface {
	LockOrgByID(ctx context.Context, id uint64) (*Org, error)
}

// ErrOrgNotFound 由 OrgUserProvider 在主体不存在时返回。
var ErrOrgNotFound = errors.New("portal: org not found")

// NormalizeOrgCode 把主体编号归一化：去首尾空白、转大写（编号形如 M12345678）。
// 登录、登录防护计数、登录日志都用归一化后的值，大小写变体不能各算一份配额。
func NormalizeOrgCode(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// NormalizeUsername 把登录名归一化：去首尾空白、转小写。
//
// 登录、创建账号、登录防护计数都必须用归一化后的值：用户表的排序规则不区分大小写（admin、Admin、ADMIN
// 查到的是同一个账号），如果限流和锁定按原始输入计数，大小写变体就各有一份配额，密码尝试次数会成倍放大。
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ErrAccountNotFound 由 UserProvider 在账号不存在时返回。
var ErrAccountNotFound = errors.New("portal: account not found")

// Account 是框架认证所需的最小账号视图。
type Account struct {
	ID            uint64
	OrgID         uint64 // 所属主体；只有主体端（Portal.Scoped）用，平台端为 0
	Username      string
	DisplayName   string
	Avatar        string
	PasswordHash  string
	Status        int // 1 启用，0 停用
	MustChangePwd bool
	PwdChangedAt  *time.Time // 上次改密时间；密码有效期（PasswordPolicy.MaxAge）据此判断，为空视为从未改过
}

// Enabled 报告账号是否可登录。
func (a *Account) Enabled() bool { return a != nil && a.Status == 1 }

var codeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidCode 报告端代号是否合法。
func ValidCode(code string) bool { return codeRe.MatchString(code) }

// Validate 检查声明的完整性。
func (p Portal) Validate() error {
	if !ValidCode(p.Code) {
		return fmt.Errorf("portal: 端代号 %q 无效（小写字母开头，只能含小写字母、数字、连字符）", p.Code)
	}
	if p.Users == nil {
		return fmt.Errorf("portal: 端 %s 没有用户来源（Users）", p.Code)
	}
	if _, ok := p.Users.(OrgUserProvider); p.Scoped && !ok {
		return fmt.Errorf("portal: 端 %s 是主体端（Scoped），用户来源必须实现 portal.OrgUserProvider", p.Code)
	}
	if _, ok := p.Users.(OrgLocker); p.Scoped && !ok {
		return fmt.Errorf("portal: 端 %s 是主体端（Scoped），用户来源必须实现 portal.OrgLocker（授权写操作靠主体行锁串行，D-063）", p.Code)
	}
	return nil
}

// Registry 是进程内的端注册表。模块在 Init 里注册，app 在 Init 阶段结束后据此装配。
type Registry struct {
	mu      sync.RWMutex
	portals map[string]Portal
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry { return &Registry{portals: map[string]Portal{}} }

// Register 登记一个端；重复代号是错误。
func (r *Registry) Register(p Portal) error {
	if err := p.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.portals[p.Code]; dup {
		return fmt.Errorf("portal: 端 %s 重复注册", p.Code)
	}
	r.portals[p.Code] = p
	return nil
}

// Get 返回某个端。
func (r *Registry) Get(code string) (Portal, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.portals[code]
	return p, ok
}

// All 返回全部端，按代号排序。
func (r *Registry) All() []Portal {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Portal, 0, len(r.portals))
	for _, p := range r.portals {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}
