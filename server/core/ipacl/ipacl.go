// Package ipacl 是 IP 黑名单与白名单（D-062）。
//
// 三层检查，顺序固定：
//
//  1. 黑名单（global）：对三个程序（平台、代理商、商户）的每个请求都生效，包括登录、验证码、刷新；健康检查除外。
//  2. 端白名单（portal）：这个端的每个接口；名单为空表示不限制。
//  3. 主体黑名单先拒绝；主体白名单（org）和账号白名单（user）：登录和每个已登录的请求；两层都设了就都要满足，为空的那层不限制。
//
// 名单整份放在内存里（一个整体发布的快照），每个请求只做几次前缀比较。写名单时在同一事务里把 ga_change_seq 的
// ip_rules 序号加一：本程序提交后立即重读；别的程序在请求时每隔一段时间（默认 5 秒）最多查一次序号，变了才重读。
// 重读失败沿用旧快照并记日志。
//
// IP 取 gin 的 ClientIP()，依赖 server.trustedProxies 配对（规范 §12.2）：配错时看到的是代理的地址。
package ipacl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// Kind 是规则的种类。
type Kind string

// 规则种类。
const (
	Deny  Kind = "deny"  // 黑名单
	Allow Kind = "allow" // 白名单
)

// Scope 是规则的作用范围。
type Scope string

// 作用范围。黑名单支持 ScopeGlobal、ScopeOrg；白名单支持端、主体和账号。
const (
	ScopeGlobal Scope = "global"
	ScopePortal Scope = "portal"
	ScopeOrg    Scope = "org"
	ScopeUser   Scope = "user"
)

// 容量和边界（D-062）。
const (
	MaxAllowEntries       = 100   // 每份白名单最多这么多条
	MaxOrgDenyRules       = 100   // 每个主体生效中的黑名单上限
	MaxOrgDenyStoredRules = 1000  // 每个主体含过期项的黑名单记录上限（D-092）
	MaxDenyRules          = 10000 // 生效中的全局黑名单最多这么多条；主体的黑名单按主体各自计数，不占这个额度（D-102）
	MinDenyBitsV4         = 8     // 黑名单的网段不能宽于 IPv4 /8
	MinDenyBitsV6         = 16    // 不能宽于 IPv6 /16
	MaxRemark             = 255   // 备注最多这么多个字符
	// SeqName 是 ga_change_seq 里这份数据的名字。
	SeqName = "ip_rules"
	// DefaultRefresh 是别的程序改了名单之后，本程序最晚多久发现（每隔这么久最多查一次序号）。
	DefaultRefresh = 5 * time.Second
)

// Target 是规则的归属；白名单支持端、主体或账号，黑名单支持全局或主体。
type Target struct {
	Scope  Scope
	Portal string
	OrgID  uint64 // ScopeOrg；主体端的 ScopeUser 也带上账号所属的主体
	UserID uint64 // ScopeUser
}

// PortalTarget 是某个端的白名单。
func PortalTarget(portal string) Target { return Target{Scope: ScopePortal, Portal: portal} }

// OrgTarget 是某个主体的名单归属。
func OrgTarget(portal string, orgID uint64) Target {
	return Target{Scope: ScopeOrg, Portal: portal, OrgID: orgID}
}

// UserTarget 是某个账号的白名单；平台端账号的 orgID 为 0。
func UserTarget(portal string, orgID, userID uint64) Target {
	return Target{Scope: ScopeUser, Portal: portal, OrgID: orgID, UserID: userID}
}

func (t Target) validate() error {
	ok := t.Portal != ""
	switch t.Scope {
	case ScopePortal:
		ok = ok && t.OrgID == 0 && t.UserID == 0
	case ScopeOrg:
		ok = ok && t.OrgID != 0 && t.UserID == 0
	case ScopeUser:
		ok = ok && t.UserID != 0
	default:
		ok = false
	}
	if !ok {
		return fmt.Errorf("ipacl: 白名单归属不合法: %+v", t)
	}
	return nil
}

// Entry 是白名单的一条：网段（或单个地址）和备注。
type Entry struct {
	CIDR   string `json:"cidr"`
	Remark string `json:"remark"`
}

// Rule 是一条规则的视图。
type Rule struct {
	ID        uint64     `json:"id"`
	Kind      Kind       `json:"kind"`
	Scope     Scope      `json:"scope"`
	Portal    string     `json:"portal"`
	OrgID     uint64     `json:"orgId"`
	UserID    uint64     `json:"userId"`
	CIDR      string     `json:"cidr"`
	ExpiresAt *time.Time `json:"expiresAt"`
	Remark    string     `json:"remark"`
	CreatedBy uint64     `json:"createdBy"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// Expired 报告黑名单规则在 now 时是否已经过期。
func (r Rule) Expired(now time.Time) bool { return r.ExpiresAt != nil && !r.ExpiresAt.After(now) }

// ParseCIDR 把输入规范化成网段：支持 IPv4 后缀通配，单个地址变成 /32 或 /128，
// IPv4 映射的 IPv6 地址按 IPv4 算，主机位清零（D-091）。
func ParseCIDR(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "*") {
		return parseWildcard(s)
	}
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		if p.Addr().Is4In6() {
			bits := p.Bits() - 96
			if bits < 0 {
				return netip.Prefix{}, errors.New("ipacl: IPv4 映射地址的前缀太短")
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), bits)
		}
		if p.Addr().Zone() != "" {
			return netip.Prefix{}, errors.New("ipacl: 不接受带 zone 的地址")
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	if a.Zone() != "" {
		return netip.Prefix{}, errors.New("ipacl: 不接受带 zone 的地址")
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// parseWildcard 只接受完整的四段 IPv4；星号出现后右边必须全部为星号。
// 规范化后仍按前缀比较，绝不把用户输入作为正则或字符串模式执行。
func parseWildcard(s string) (netip.Prefix, error) {
	if len(s) > 15 {
		return netip.Prefix{}, errors.New("ipacl: IPv4 通配网段格式无效")
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return netip.Prefix{}, errors.New("ipacl: IPv4 通配网段需要四段")
	}
	first := 4
	for i, part := range parts {
		if part == "*" {
			first = min(first, i)
			parts[i] = "0"
		} else if first != 4 {
			return netip.Prefix{}, errors.New("ipacl: 星号右侧必须全部为星号")
		}
	}
	a, err := netip.ParseAddr(strings.Join(parts, "."))
	if err != nil || !a.Is4() {
		return netip.Prefix{}, errors.New("ipacl: IPv4 通配网段格式无效")
	}
	return netip.PrefixFrom(a, first*8), nil
}

// parseIP 解析请求的来源地址；解析不了返回无效地址。
func parseIP(s string) netip.Addr {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

// ---- 表 ----

type row struct {
	ID        uint64     `gorm:"column:id;primaryKey"`
	Kind      string     `gorm:"column:kind"`
	Scope     string     `gorm:"column:scope"`
	Portal    string     `gorm:"column:portal"`
	OrgID     uint64     `gorm:"column:org_id"`
	UserID    uint64     `gorm:"column:user_id"`
	CIDR      string     `gorm:"column:cidr"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
	Status    int        `gorm:"column:status"`
	Sort      int        `gorm:"column:sort"`
	Remark    string     `gorm:"column:remark"`
	CreatedAt time.Time  `gorm:"column:created_at"`
	UpdatedAt time.Time  `gorm:"column:updated_at"`
	CreatedBy uint64     `gorm:"column:created_by"`
	UpdatedBy uint64     `gorm:"column:updated_by"`
}

func (row) TableName() string { return "ga_ip_rule" }

func (r row) view() Rule {
	return Rule{
		ID: r.ID, Kind: Kind(r.Kind), Scope: Scope(r.Scope), Portal: r.Portal, OrgID: r.OrgID, UserID: r.UserID,
		CIDR: r.CIDR, ExpiresAt: r.ExpiresAt, Remark: r.Remark, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// ---- 快照 ----

type orgKey struct {
	portal string
	org    uint64
}

type userKey struct {
	portal string
	user   uint64
}

type denyRange struct {
	p     netip.Prefix
	until *time.Time
}

type snapshot struct {
	orgDeny map[orgKey][]denyRange // 主体黑名单只在识别账号所属主体后匹配
	seq     uint64
	hosts   map[netip.Addr]*time.Time // 黑名单里的单个地址 → 到期时间（nil 永久）
	ranges  []denyRange               // 黑名单里的网段
	portal  map[string][]netip.Prefix
	org     map[orgKey][]netip.Prefix
	user    map[userKey][]netip.Prefix
}

func contains(list []netip.Prefix, a netip.Addr) bool {
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func alive(until *time.Time, now time.Time) bool { return until == nil || until.After(now) }

// ---- 服务 ----

// Options 是构造参数。
type Options struct {
	DB      *gorm.DB // 读名单用的句柄（不在调用方的事务里）；写名单走 db.From(ctx)
	Log     *slog.Logger
	Now     func() time.Time
	Refresh time.Duration // 0 用 DefaultRefresh
	// OnChange 在名单成功提交、本地重读之后调用；重读失败也通知远端。
	OnChange func()
}

// Service 是名单服务：匹配（每个请求）和维护（后台、命令行）。
type Service struct {
	base     *gorm.DB
	log      *slog.Logger
	now      func() time.Time
	refresh  time.Duration
	onChange func()

	snap         atomic.Pointer[snapshot]
	checked      atomic.Int64 // 上次查序号的时间（UnixNano）
	reloadMu     sync.Mutex
	invalidateMu sync.Mutex // 只保护失效代数和快照发布，通知不等读库（D-075）
	gen          uint64
	testHook     func(stage string) // 只给本包测试用：在读库之后、发布之前插入通知
}

// New 创建服务。用之前要 Load 一次。
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Refresh <= 0 {
		o.Refresh = DefaultRefresh
	}
	return &Service{base: o.DB, log: o.Log.With("component", "ipacl"), now: o.Now, refresh: o.Refresh, onChange: o.OnChange}
}

// Invalidate 标记名单待核对，由下一次请求按变更序号重读；不读库、不再次通知（D-075）。
// 保留旧快照，让数据库暂时不可用时仍可按原有名单匹配。
func (s *Service) Invalidate() {
	s.invalidateMu.Lock()
	defer s.invalidateMu.Unlock()
	s.gen++
	s.checked.Store(0)
}

// Load 从库里读一份完整的名单并发布。启动时调用；失败时调用方拒绝启动。
func (s *Service) Load(ctx context.Context) error {
	s.checked.Store(s.now().UnixNano())
	return s.reload(ctx)
}

// reload 在一个只读快照事务里读序号和全部生效的规则，读到的序号不比当前的旧才发布。
func (s *Service) reload(ctx context.Context) error {
	if err := s.lockReload(ctx); err != nil {
		return err
	}
	defer s.reloadMu.Unlock()
	s.invalidateMu.Lock()
	gen := s.gen
	s.invalidateMu.Unlock()
	var (
		seq  uint64
		rows []row
	)
	err := s.base.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT seq FROM ga_change_seq WHERE name = ?", SeqName).Scan(&seq).Error; err != nil {
			return err
		}
		return tx.Where("status = 1 AND (kind <> ? OR expires_at IS NULL OR expires_at > ?)", Deny, s.now().UTC()).Find(&rows).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return fmt.Errorf("ipacl: 读名单: %w", err)
	}
	if s.testHook != nil {
		s.testHook("reload.read")
	}
	if cur := s.snap.Load(); cur != nil && cur.seq > seq {
		return nil
	}
	next := &snapshot{
		seq: seq, hosts: map[netip.Addr]*time.Time{}, orgDeny: map[orgKey][]denyRange{},
		portal: map[string][]netip.Prefix{}, org: map[orgKey][]netip.Prefix{}, user: map[userKey][]netip.Prefix{},
	}
	now := s.now().UTC()
	for _, r := range rows {
		p, err := ParseCIDR(r.CIDR)
		if err != nil {
			s.log.Warn("名单里有无法解析的网段，忽略", "id", r.ID, "cidr", r.CIDR)
			continue
		}
		switch {
		case Kind(r.Kind) == Deny && Scope(r.Scope) == ScopeGlobal:
			if !alive(r.ExpiresAt, now) {
				continue
			}
			if p.IsSingleIP() {
				next.hosts[p.Addr()] = r.ExpiresAt
			} else {
				next.ranges = append(next.ranges, denyRange{p: p, until: r.ExpiresAt})
			}
		case Kind(r.Kind) == Deny && Scope(r.Scope) == ScopeOrg && r.OrgID != 0:
			if alive(r.ExpiresAt, now) {
				k := orgKey{r.Portal, r.OrgID}
				next.orgDeny[k] = append(next.orgDeny[k], denyRange{p: p, until: r.ExpiresAt})
			}
		case Kind(r.Kind) == Allow && Scope(r.Scope) == ScopePortal:
			next.portal[r.Portal] = append(next.portal[r.Portal], p)
		case Kind(r.Kind) == Allow && Scope(r.Scope) == ScopeOrg:
			k := orgKey{r.Portal, r.OrgID}
			next.org[k] = append(next.org[k], p)
		case Kind(r.Kind) == Allow && Scope(r.Scope) == ScopeUser:
			k := userKey{r.Portal, r.UserID}
			next.user[k] = append(next.user[k], p)
		}
	}
	s.invalidateMu.Lock()
	defer s.invalidateMu.Unlock()
	if gen == s.gen {
		s.snap.Store(next)
	}
	return nil
}

// lockReload 把等待已有重载的时间也计入调用方时限（D-123）。
func (s *Service) lockReload(ctx context.Context) error {
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		if err := reloadContextError(ctx); err != nil {
			return err
		}
		if s.reloadMu.TryLock() {
			if err := reloadContextError(ctx); err != nil {
				s.reloadMu.Unlock()
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func reloadContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

// current 返回当前快照；距上次查序号超过刷新间隔时，先查一次序号（只有一个请求去查），变了就重读。
// 还没 Load 过时是空名单（不限制）：app 在 Setup 里 Load，失败就拒绝启动，正式服务里走不到这一步。
func (s *Service) current(ctx context.Context) *snapshot {
	if s.snap.Load() == nil {
		return &snapshot{}
	}
	now := s.now().UnixNano()
	last := s.checked.Load()
	if now-last >= int64(s.refresh) && s.checked.CompareAndSwap(last, now) {
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		failed := false
		var seq uint64
		err := s.base.WithContext(refreshCtx).Raw("SELECT seq FROM ga_change_seq WHERE name = ?", SeqName).Scan(&seq).Error
		if s.testHook != nil {
			s.testHook("seq.read")
		}
		switch {
		case err != nil:
			failed = true
			s.log.WarnContext(ctx, "查名单变更序号失败，沿用旧名单", "err", err)
		case s.snap.Load().seq != seq:
			if err := s.reload(refreshCtx); err != nil {
				failed = true
				s.log.WarnContext(ctx, "重读名单失败，沿用旧名单", "err", err)
			}
		}
		if failed {
			// 失败后短暂退避；不能盖掉重载期间的新失效或较新的成功检查。
			s.checked.CompareAndSwap(now, now-int64(s.refresh)+int64(min(s.refresh, time.Second)))
		}
	}
	return s.snap.Load()
}

// Blocked 报告来源地址是否在黑名单里（未到期）。
func (s *Service) Blocked(ctx context.Context, ip string) bool {
	a := parseIP(ip)
	if !a.IsValid() {
		return false
	}
	sn := s.current(ctx)
	now := s.now().UTC()
	if until, ok := sn.hosts[a]; ok && alive(until, now) {
		return true
	}
	for _, r := range sn.ranges {
		if alive(r.until, now) && r.p.Contains(a) {
			return true
		}
	}
	return false
}

// PortalAllows 报告来源地址能否访问这个端：端白名单为空表示不限制。地址解析不了时，有名单就不放行。
func (s *Service) PortalAllows(ctx context.Context, portal, ip string) bool {
	list := s.current(ctx).portal[portal]
	return len(list) == 0 || contains(list, parseIP(ip))
}

// AccountAllows 先拒绝命中的主体黑名单，再要求主体和账号白名单都满足；空白名单不限制。
func (s *Service) AccountAllows(ctx context.Context, portal string, orgID, userID uint64, ip string) bool {
	sn := s.current(ctx)
	a := parseIP(ip)
	if orgID != 0 {
		for _, r := range sn.orgDeny[orgKey{portal, orgID}] {
			if alive(r.until, s.now().UTC()) && r.p.Contains(a) {
				return false
			}
		}
		if list := sn.org[orgKey{portal, orgID}]; len(list) > 0 && !contains(list, a) {
			return false
		}
	}
	if list := sn.user[userKey{portal, userID}]; len(list) > 0 && !contains(list, a) {
		return false
	}
	return true
}

// ---- 维护 ----

func fieldErr(field, key, msg string, kv ...any) error {
	return httpx.ErrValidation.WithFields(httpx.NewField(field, key, msg, kv...))
}

func checkRemark(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > MaxRemark {
		return "", fieldErr(field, "common.maxLength", fmt.Sprintf("at most %d characters", MaxRemark), "max", MaxRemark)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", fieldErr(field, "ipacl.remarkChars", "must not contain control or invisible characters")
		}
	}
	return s, nil
}

// bump 在当前事务里给变更序号加一。更新这一行同时串行化了所有写名单的事务；提交之后本程序立即重读。
func (s *Service) bump(ctx context.Context) error {
	res := db.From(ctx).Exec("UPDATE ga_change_seq SET seq = seq + 1, updated_at = ? WHERE name = ?", s.now().UTC(), SeqName)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return errors.New("ipacl: ga_change_seq 里缺少 ip_rules 这一行（迁移 00014 没有执行？）")
	}
	db.AfterCommit(ctx, func() {
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.reload(rctx); err != nil {
			s.log.Error("名单改动后重读失败；最晚一个刷新间隔后再试", "err", err)
			s.checked.Store(0)
		}
		if s.onChange != nil {
			s.onChange()
		}
	})
	return nil
}

// DenyInput 是加一条黑名单的入参。
type DenyInput struct {
	CIDR      string
	ExpiresIn time.Duration // 从现在起多久后到期；0 表示永久
	Remark    string
}

// AddDeny 加一条黑名单（已有同一网段时更新到期时间和备注）。actorIP 是操作人当前的地址：网段包含它时拒绝，
// 免得把自己挡在外面（命令行等没有来源地址的调用传空）。
func (s *Service) AddDeny(ctx context.Context, in DenyInput, actorID uint64, actorIP string) (Rule, error) {
	return s.addDeny(ctx, Target{Scope: ScopeGlobal}, in, actorID, actorIP)
}

// AddOrgDeny 设置本主体的黑名单；调用方在管理事务内从身份取得 target。
func (s *Service) AddOrgDeny(ctx context.Context, target Target, in DenyInput, actorID uint64, actorIP string) (Rule, error) {
	if err := target.validateOrgDeny(); err != nil {
		return Rule{}, err
	}
	return s.addDeny(ctx, target, in, actorID, actorIP)
}

func (t Target) validateOrgDeny() error {
	if err := t.validate(); err != nil {
		return err
	}
	if t.Scope != ScopeOrg || (t.Portal != "agent" && t.Portal != "merchant") {
		return errors.New("ipacl: 主体黑名单归属不合法")
	}
	return nil
}

func (s *Service) addDeny(ctx context.Context, target Target, in DenyInput, actorID uint64, actorIP string) (Rule, error) {
	p, err := ParseCIDR(in.CIDR)
	if err != nil {
		return Rule{}, fieldErr("cidr", "ipacl.cidrInvalid", "not an IP address or CIDR")
	}
	if (p.Addr().Is4() && p.Bits() < MinDenyBitsV4) || (p.Addr().Is6() && p.Bits() < MinDenyBitsV6) {
		return Rule{}, fieldErr("cidr", "ipacl.denyTooBroad", fmt.Sprintf("at most /%d for IPv4 and /%d for IPv6", MinDenyBitsV4, MinDenyBitsV6),
			"v4", MinDenyBitsV4, "v6", MinDenyBitsV6)
	}
	if a := parseIP(actorIP); a.IsValid() && p.Contains(a) {
		return Rule{}, fieldErr("cidr", "ipacl.selfLockout", "this would block your own address")
	}
	now := s.now().UTC()
	if in.ExpiresIn < 0 {
		return Rule{}, fieldErr("expiresIn", "ipacl.expiresIn", "must not be negative")
	}
	var expiresAt *time.Time
	if in.ExpiresIn > 0 {
		t := now.Add(in.ExpiresIn)
		expiresAt = &t
	}
	remark, err := checkRemark("remark", in.Remark)
	if err != nil {
		return Rule{}, err
	}
	var out row
	err = db.Tx(ctx, func(ctx context.Context) error {
		if err := s.bump(ctx); err != nil {
			return err
		}
		// 容量按归属各自计数（D-102）：全局名单数全局的，主体名单数这个主体的，互不占用——
		// 主体写满自己的名单，平台照样能加全局规则。已有的同一条不算（续期不受容量限制）。
		limit := MaxDenyRules
		if target.Scope == ScopeOrg {
			limit = MaxOrgDenyRules
		}
		var active int64
		if err := target.denyWhere(db.From(ctx)).Where("status = 1 AND (expires_at IS NULL OR expires_at > ?) AND cidr <> ?", now, p.String()).Count(&active).Error; err != nil {
			return err
		}
		if active >= int64(limit) {
			return fieldErr("cidr", "ipacl.denyFull", "active rule limit reached", "max", limit)
		}
		var existing row
		err := target.denyWhere(db.From(ctx)).Where("cidr = ?", p.String()).
			Take(&existing).Error
		switch {
		case err == nil:
			existing.ExpiresAt, existing.Remark, existing.Status, existing.UpdatedAt, existing.UpdatedBy = expiresAt, remark, 1, now, actorID
			if err := db.From(ctx).Model(&existing).Select("expires_at", "remark", "status", "updated_at", "updated_by").Updates(&existing).Error; err != nil {
				return err
			}
			out = existing
			return nil
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		if target.Scope == ScopeOrg {
			var stored int64
			if err := target.denyWhere(db.From(ctx)).Count(&stored).Error; err != nil {
				return err
			}
			if stored >= MaxOrgDenyStoredRules {
				return fieldErr("cidr", "ipacl.denyStoredFull", "stored rule limit reached; remove old rules first", "max", MaxOrgDenyStoredRules)
			}
		}
		out = row{
			Kind: string(Deny), Scope: string(target.Scope), Portal: target.Portal, OrgID: target.OrgID, UserID: target.UserID, CIDR: p.String(), ExpiresAt: expiresAt, Status: 1, Remark: remark,
			CreatedAt: now, UpdatedAt: now, CreatedBy: actorID, UpdatedBy: actorID,
		}
		return db.From(ctx).Create(&out).Error
	})
	if err != nil {
		return Rule{}, err
	}
	return out.view(), nil
}

// RemoveDeny 删除一条全局黑名单；其他归属或类型按不存在处理。
func (s *Service) RemoveDeny(ctx context.Context, id uint64) (Rule, error) {
	return s.remove(ctx, id, Deny, Target{Scope: ScopeGlobal})
}

// Remove 删除任意一条规则（命令行用）；不存在时返回 httpx.ErrNotFound。
func (s *Service) Remove(ctx context.Context, id uint64) (Rule, error) {
	return s.remove(ctx, id, "", Target{})
}

// RemoveOrgDeny 删除指定主体的黑名单，跨主体或其他类型记录按不存在处理。
func (s *Service) RemoveOrgDeny(ctx context.Context, target Target, id uint64) (Rule, error) {
	if err := target.validateOrgDeny(); err != nil {
		return Rule{}, err
	}
	return s.remove(ctx, id, Deny, target)
}

func (t Target) denyWhere(q *gorm.DB) *gorm.DB {
	return q.Model(&row{}).Where("kind = ? AND scope = ? AND portal = ? AND org_id = ? AND user_id = ?", Deny, t.Scope, t.Portal, t.OrgID, t.UserID)
}

func (s *Service) remove(ctx context.Context, id uint64, kind Kind, target Target) (Rule, error) {
	var r row
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := s.bump(ctx); err != nil {
			return err
		}
		q := db.From(ctx).Where("id = ?", id)
		if kind != "" {
			q = target.denyWhere(q)
		}
		if err := q.Take(&r).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return httpx.ErrNotFound
			}
			return err
		}
		return db.From(ctx).Delete(&row{}, r.ID).Error
	})
	if err != nil {
		return Rule{}, err
	}
	return r.view(), nil
}

// DenyFilter 是黑名单列表的条件。
type DenyFilter struct {
	Keyword        string // 网段前缀匹配
	IncludeExpired bool
}

// ListDeny 分页列出黑名单，新加的在前。
func (s *Service) ListDeny(ctx context.Context, f DenyFilter, page, pageSize int) ([]Rule, int64, error) {
	return s.listDeny(ctx, Target{Scope: ScopeGlobal}, f, page, pageSize)
}

// ListOrgDeny 分页列出一个主体的黑名单。
func (s *Service) ListOrgDeny(ctx context.Context, target Target, f DenyFilter, page, pageSize int) ([]Rule, int64, error) {
	if err := target.validateOrgDeny(); err != nil {
		return nil, 0, err
	}
	return s.listDeny(ctx, target, f, page, pageSize)
}

func (s *Service) listDeny(ctx context.Context, target Target, f DenyFilter, page, pageSize int) ([]Rule, int64, error) {
	q := target.denyWhere(db.From(ctx))
	if !f.IncludeExpired {
		q = q.Where("expires_at IS NULL OR expires_at > ?", s.now().UTC())
	}
	if k := strings.TrimSpace(f.Keyword); k != "" {
		q = q.Where("cidr LIKE ?", strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(k)+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []row
	if err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view())
	}
	return out, total, nil
}

// ListAllow 返回一份白名单，按网段排序。
func (s *Service) ListAllow(ctx context.Context, t Target) ([]Rule, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	var rows []row
	if err := t.where(db.From(ctx)).Order("cidr").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view())
	}
	return out, nil
}

func (t Target) where(q *gorm.DB) *gorm.DB {
	return q.Model(&row{}).Where("kind = ? AND scope = ? AND portal = ? AND org_id = ? AND user_id = ?", Allow, t.Scope, t.Portal, t.OrgID, t.UserID)
}

// Covers 报告一组网段是否包含某个地址（地址解析不了时为 false）。
func Covers(entries []Entry, ip string) bool {
	a := parseIP(ip)
	if !a.IsValid() {
		return false
	}
	for _, e := range entries {
		if p, err := ParseCIDR(e.CIDR); err == nil && p.Contains(a) {
			return true
		}
	}
	return false
}

// SetAllow 整份替换一份白名单（entries 为空表示清空、不再限制）。guardIP 非空时，名单必须包含它：
// 这份名单对操作人自己生效时（平台端白名单、自己主体的、自己账号的），由调用方传操作人当前的地址，免得把自己挡在外面。
func (s *Service) SetAllow(ctx context.Context, t Target, entries []Entry, actorID uint64, guardIP string) ([]Rule, error) {
	if err := t.validate(); err != nil {
		return nil, err
	}
	if len(entries) > MaxAllowEntries {
		return nil, fieldErr("items", "ipacl.allowTooMany", fmt.Sprintf("at most %d entries", MaxAllowEntries), "max", MaxAllowEntries)
	}
	type item struct {
		p      netip.Prefix
		remark string
	}
	seen := map[netip.Prefix]bool{}
	items := make([]item, 0, len(entries))
	for i, e := range entries {
		field := fmt.Sprintf("items[%d].cidr", i)
		p, err := ParseCIDR(e.CIDR)
		if err != nil {
			return nil, fieldErr(field, "ipacl.cidrInvalid", "not an IP address or CIDR")
		}
		remark, err := checkRemark(fmt.Sprintf("items[%d].remark", i), e.Remark)
		if err != nil {
			return nil, err
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		items = append(items, item{p: p, remark: remark})
	}
	if guardIP != "" && len(items) > 0 {
		a := parseIP(guardIP)
		covered := false
		for _, it := range items {
			if a.IsValid() && it.p.Contains(a) {
				covered = true
				break
			}
		}
		if !covered {
			return nil, fieldErr("items", "ipacl.selfLockout", "the list must include your current address", "ip", a.String())
		}
	}
	now := s.now().UTC()
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := s.bump(ctx); err != nil {
			return err
		}
		if err := t.where(db.From(ctx)).Delete(&row{}).Error; err != nil {
			return err
		}
		for _, it := range items {
			r := row{
				Kind: string(Allow), Scope: string(t.Scope), Portal: t.Portal, OrgID: t.OrgID, UserID: t.UserID, CIDR: it.p.String(),
				Status: 1, Remark: it.remark, CreatedAt: now, UpdatedAt: now, CreatedBy: actorID, UpdatedBy: actorID,
			}
			if err := db.From(ctx).Create(&r).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ListAllow(ctx, t)
}

// ClearAllow 清空一份白名单（不再限制），返回删掉的条数。命令行和平台清除主体的名单用；不做防锁死检查。
func (s *Service) ClearAllow(ctx context.Context, t Target) (int64, error) {
	if err := t.validate(); err != nil {
		return 0, err
	}
	var n int64
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := s.bump(ctx); err != nil {
			return err
		}
		res := t.where(db.From(ctx)).Delete(&row{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// ClearOrgDeny 清空一个主体自己设的黑名单（含已过期的记录），返回删掉的条数。平台和命令行用（D-102）：
// 主体把接手的人挡在外面时由平台解除。别的主体、全局名单、白名单不动。
func (s *Service) ClearOrgDeny(ctx context.Context, target Target) (int64, error) {
	if err := target.validateOrgDeny(); err != nil {
		return 0, err
	}
	var n int64
	err := db.Tx(ctx, func(ctx context.Context) error {
		if err := s.bump(ctx); err != nil {
			return err
		}
		res := target.denyWhere(db.From(ctx)).Delete(&row{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// All 返回全部规则（命令行列表用），按种类、范围、ID 排序。
func (s *Service) All(ctx context.Context) ([]Rule, error) {
	var rows []row
	if err := db.From(ctx).Order("kind, scope, portal, org_id, user_id, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]Rule, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view())
	}
	return out, nil
}
