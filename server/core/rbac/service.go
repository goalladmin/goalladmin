package rbac

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/internal/ratelimit"
	"github.com/goalladmin/goalladmin/server/core/internal/secmark"
	"github.com/goalladmin/goalladmin/server/core/internal/textx"
	"github.com/goalladmin/goalladmin/server/core/internal/ttlcache"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/portal"
)

// Service 是授权服务：角色、角色授权、用户角色、判定、菜单。通过 app.Deps.RBAC 注入模块。
//
// 规则（规范 §6.4、§6.5）：
//   - 超管 = 拥有任一 is_super 且启用的角色；超管跳过权限码判定，但操作日志照常记录。
//   - 敏感权限码只有超管能授予；非超管授权时不能授出自己没有的权限码。
//   - 只有超管能把 super 角色分配给别人；super 角色的 is_super 不可修改。
//   - 非超管分配角色时，目标角色不能含敏感权限码，也不能含自己没有的权限码（否则可以借角色给自己提权）。
//   - 不能停用最后一个启用的超管账号（IsLastSuper 供 system 模块调用）。
type Service struct {
	reg           *Registry
	log           *slog.Logger
	base          context.Context // 带数据库句柄，供后台重载用
	st            store
	selfWrites    *ratelimit.KeyedGate
	managerWrites *ratelimit.KeyedGate
	allWrites     *ratelimit.Gate
	roleWrites    *ratelimit.Window
	now           func() time.Time

	menuMu    sync.Mutex                    // 串行化菜单管理的写操作（D-025）
	menuCache *ttlcache.Cache[[]menuCustom] // 端 → 菜单的后台调整；回填用它自己的失效代数（D-043）

	portals *portal.Registry // 可为 nil

	sessionActive  func(ctx context.Context, portal, sid string) (bool, error) // 可为 nil（D-046）
	lockSession    func(ctx context.Context, portal, sid string) (bool, error) // 可为 nil（D-059）
	onPolicyChange func()
	onMenuChange   func(portal string)

	// snap 是当前生效的授权快照：成员关系、角色状态、权限码策略来自同一个数据库快照，整体发布；一次判定只取一次
	// （D-051、D-053）。数据范围不在这里，按请求的数据库视图算（D-054）。
	snap atomic.Pointer[policySnapshot]
	// stale 为 true 表示最近一次重载失败：库里的授权已经变了（写入方都是先提交再重载），内存没跟上。
	// 这时旧快照可能比库里宽，不能拿来判定；retryAt（UnixNano）之后的判定会先重试重载（D-051）。
	stale   atomic.Bool
	retryAt atomic.Int64
	// publishing 是正在提交或已提交、还没重新发布快照的授权写入数（D-055）：大于 0 时判定先等它归零
	publishing atomic.Int32
	maxAge     time.Duration // 快照最长用多久（Options.CacheTTL），过了就在下一次判定时重读

	// reloadMu 串行化 Reload：读库和写内存是一个整体，两次并发重载不会让先读到旧数据的那次最后覆盖新数据
	//（被旧数据盖住的快照要等下次写入或过了有效期才恢复，D-043）。
	reloadMu sync.Mutex
	// invalidateMu 只保护失效代数与快照发布，远端通知不等读库；重载途中收到通知时不发布旧快照（D-075）。
	invalidateMu sync.Mutex
	policyGen    uint64

	// testHook 只给本包的测试用（生产为 nil）：在"读库之后、写缓存之前"这类窗口里插入另一次写入，验证不会把旧数据填回去。
	testHook func(stage string)
}

func (s *Service) hook(stage string) {
	if s.testHook != nil {
		s.testHook(stage)
	}
}

// Options 是构造参数。
type Options struct {
	Registry *Registry
	Base     context.Context // 必须带数据库句柄（app.Context）
	Log      *slog.Logger
	Now      func() time.Time
	CacheTTL time.Duration // 授权快照和菜单调整缓存的最长使用时间，默认 15 秒（进程外的改动靠它生效）
	// Portals 用来查账号状态（IsLastSuper 只把启用中的超管算数）。为 nil 时退化为只看角色。
	Portals *portal.Registry
	// SessionActive 报告会话此刻是否有效（未吊销、未过期、属于这个端），未配置 LockSession 时用于兼容回退。
	// 写操作的兼容回退也需核对锁屏并返回 httpx.ErrSessionLocked（D-106）；普通读无法提供事务排序保证。
	// 两者都为 nil 时不检查会话。
	SessionActive func(ctx context.Context, portal, sid string) (bool, error)
	// LockSession 在调用方的事务里给会话行加锁，并报告它是否有效（WithSelf 和管理写入用，D-059、D-089）。
	// 内置装配还核对未锁屏，锁屏返回 httpx.ErrSessionLocked（D-106）；自定义装配需提供同样的检查。
	// 先锁操作人账号、再锁会话；锁持续到事务结束，吊销成功返回后该会话已获准的写事务均已结束。
	// 为 nil 时退回 SessionActive 的普通读。
	LockSession func(ctx context.Context, portal, sid string) (bool, error)
	// OnPolicyChange 在授权数据成功提交、本地重载之后调用；本地重载失败也通知远端。
	OnPolicyChange func()
	// OnMenuChange 在菜单调整成功提交、本地清缓存之后调用。
	OnMenuChange func(portal string)
}

// NewService 创建授权服务并加载策略。
func NewService(o Options) (*Service, error) {
	if o.Registry == nil || o.Base == nil {
		return nil, errors.New("rbac: Registry 和 Base 必填")
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = 15 * time.Second
	}
	s := &Service{
		reg: o.Registry, log: o.Log.With("component", "rbac"), base: o.Base,
		now: o.Now, maxAge: o.CacheTTL, portals: o.Portals, sessionActive: o.SessionActive, lockSession: o.LockSession,
		menuCache:      ttlcache.New[[]menuCustom](o.CacheTTL, o.Now),
		onPolicyChange: o.OnPolicyChange, onMenuChange: o.OnMenuChange,
		selfWrites: ratelimit.NewKeyedGate(2), managerWrites: ratelimit.NewKeyedGate(2), allWrites: ratelimit.NewGate(32), roleWrites: ratelimit.New(120, time.Minute, 100_000, o.Now),
	}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	s.warnUnregistered()
	return s, nil
}

// policySnapshot 是一次重载得到的授权状态，发布后不再修改。权限码判定要用到的授权数据都在这里、来自同一个数据库快照
// （D-053）：谁有哪些角色、角色是否启用、角色有哪些权限码。成员关系和角色状态不再单独缓存——单独读的话，
// 一次判定可能拿"撤销前的成员关系"配上"撤销之后才扩大的授权"，算出这个人从来没有过的权限。
// 数据范围不在这里（D-054）：范围要和操作人的部门、被读的数据按请求所在的同一个数据库视图算，见 DataScopeOf。
type policySnapshot struct {
	enf     *enforcer
	roles   map[uint64]Role     // 全部角色（判定只用 ID、端、IsSuper、Status）
	members map[string][]uint64 // userKey(端, 用户) → 角色 ID（按角色的排序值、ID 排好）
	loaded  time.Time           // 读库的时间：超过 maxAge 之后下一次判定先重新读一遍
}

// enabledRoles 返回用户在该端、该主体启用的角色。主体端（org 非 0）只认本主体的角色，也不认超管角色：
// 主体端的超管是主账号，不来自角色（D-063 第 2、6 条）。库里被直接写进的跨主体成员关系、主体里的超管角色都不算数。
func (p *policySnapshot) enabledRoles(portal string, org, userID uint64) []Role {
	ids := p.members[userKey(portal, userID)]
	out := make([]Role, 0, len(ids))
	for _, id := range ids {
		r, ok := p.roles[id]
		if !ok || r.Portal != portal || r.OrgID != org || r.Status != 1 || (org != 0 && r.IsSuper) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// policyRetryInterval 是重载失败后重试的最短间隔：失败期间每个判定都去重试会把出故障的数据库压得更重。
const policyRetryInterval = time.Second

// errPolicyPublishing：授权写入迟迟没有发布完（D-055）。
var errPolicyPublishing = errors.New("rbac: 授权改动还没有发布，暂停判定")

// errPolicyStale：最近一次重载失败，重试也没成功。判定一律报不可用（503），不拿旧快照判定。
var errPolicyStale = errors.New("rbac: 授权状态重载失败，暂停判定")

// Reload 从库重载授权快照。整个过程持锁：写入方都是"先提交、再 Reload"，串行之后最后一次重载读到的一定是
// 最新提交的状态，不会出现"A 读到旧状态、B 读到新状态并写入、A 再用旧状态覆盖"的交错。
//
// 策略、角色、成员关系在一个只读快照事务里读（D-051、D-053）：分几次读会拼出库里从没存在过的状态
// （例如"撤销前的成员关系 + 撤销之后才扩大的授权"）。读好之后整体换上，判定方一次拿到的要么全旧、要么全新。
// 重载失败时标记 stale：之后的判定先重试，重试不成功就报不可用，而不是继续用可能更宽的旧快照（D-051）。
func (s *Service) Reload() error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	return s.reloadLocked()
}

func (s *Service) reloadLocked(wait ...time.Duration) error {
	timeout := publishWait
	if len(wait) > 0 {
		timeout = min(timeout, wait[0])
	}
	ctx, cancel := context.WithTimeout(s.base, timeout)
	defer cancel()
	s.invalidateMu.Lock()
	gen := s.policyGen
	s.invalidateMu.Unlock()
	snap, err := s.loadPolicy(ctx)
	s.invalidateMu.Lock()
	defer s.invalidateMu.Unlock()
	if gen != s.policyGen {
		// 通知比本次读库更新，保留失效标记，由下一次判定重新读取。
		return errPolicyStale
	}
	if err != nil {
		s.retryAt.Store(s.now().Add(policyRetryInterval).UnixNano())
		s.stale.Store(true)
		s.log.Error("授权状态重载失败，判定暂停直到重载成功", "err", err)
		return err
	}
	s.snap.Store(snap)
	s.stale.Store(false)
	return nil
}

// InvalidatePolicy 让授权快照过期，下一次判定再单飞重读。远端通知只改内存、不读库、不再次通知（D-075）。
func (s *Service) InvalidatePolicy() {
	s.invalidateMu.Lock()
	defer s.invalidateMu.Unlock()
	s.policyGen++
	s.retryAt.Store(0)
	s.stale.Store(true)
}

// holdPublish 在改授权的事务里、写完之后调用（D-055）：从这一刻到新快照发布，判定先等着，不拿旧快照判定——
// 事务提交之后、快照重新发布之前，旧快照里还有刚撤掉的权限、刚被撤下的成员。事务结束时提交了就重新发布，
// 回滚了什么也不做，两种情况都解除标记。重新发布失败时 Reload 已经标记过期，之后的判定先重试、仍失败回 503。
func (s *Service) holdPublish(ctx context.Context) {
	s.publishing.Add(1)
	db.AfterEnd(ctx, func(committed bool) {
		func() {
			defer s.publishing.Add(-1)
			if committed {
				s.hook("grant.committed")
				_ = s.Reload()
			}
		}()
		// 本地快照已发布就解除判定等待；向远端发通知的时限不占本地发布窗口。
		if committed && s.onPolicyChange != nil {
			s.onPolicyChange()
		}
	})
}

// publishWait 是判定等"已提交、还没发布"的授权写入的最长时间：发布只是重读几张授权表，正常是毫秒级；
// 超过了就回 503，不拿旧快照判定。
const publishWait = 3 * time.Second

const (
	MaxRolesPerOrg  = 200
	MaxRolesPerUser = 20
	MaxGrantCodes   = 1024
)

func (s *Service) loadPolicy(ctx context.Context) (*policySnapshot, error) {
	var portals []string
	if s.portals != nil {
		for _, pt := range s.portals.All() {
			portals = append(portals, pt.Code)
		}
	}
	var (
		rules   []policyRule
		roles   []Role
		members []userRole
	)
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		var err error
		if rules, err = s.st.allPolicies(ctx, portals...); err != nil {
			return fmt.Errorf("rbac: load policy: %w", err)
		}
		s.hook("reload.policies")
		if roles, err = s.st.allRoles(ctx, portals...); err != nil {
			return fmt.Errorf("rbac: load roles: %w", err)
		}
		if members, err = s.st.allUserRoles(ctx, portals...); err != nil {
			return fmt.Errorf("rbac: load user roles: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.hook("reload.read")
	enf, err := newEnforcerContext(ctx, rules)
	if err != nil {
		return nil, err
	}
	byID := make(map[uint64]Role, len(roles))
	order := make(map[uint64]int, len(roles)) // allRoles 按排序值、ID 排好；成员关系里的角色按这个顺序
	for i, r := range roles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		byID[r.ID] = r
		order[r.ID] = i
	}
	mem := map[string][]uint64{}
	for _, m := range members {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		k := userKey(m.Portal, m.UserID)
		mem[k] = append(mem[k], m.RoleID)
	}
	for _, ids := range mem {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.Slice(ids, func(i, j int) bool { return order[ids[i]] < order[ids[j]] })
	}
	return &policySnapshot{enf: enf, roles: byID, members: mem, loaded: s.now()}, nil
}

// policy 返回判定用的授权快照。最近一次重载失败时先重试（最多每 policyRetryInterval 一次，并发调用有界等待同一次重载），
// 仍然失败就返回 503：旧快照里可能还有刚被撤掉的权限码、刚被撤下的成员（D-051）。有授权写入已提交（或即将提交）、
// 还没重新发布时，先等发布完成（D-055）。
func (s *Service) policy() (*policySnapshot, error) {
	deadline := time.Now().Add(publishWait)
	if s.publishing.Load() > 0 {
		for s.publishing.Load() > 0 {
			if time.Now().After(deadline) {
				return nil, httpx.ErrUnavailable.WithCause(errPolicyPublishing)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	// 快照太旧时顺手重读（D-053）：进程内的写入提交之后都会立即重新发布，这里兜住进程外的改动
	//（命令行建管理员、清理权限码），和以前角色缓存的有效期一样长
	if cur := s.snap.Load(); cur != nil && !s.stale.Load() && s.now().Sub(cur.loaded) >= s.maxAge && s.reloadMu.TryLock() {
		if cur == s.snap.Load() {
			_ = s.reloadLocked(time.Until(deadline))
		}
		s.reloadMu.Unlock()
	}
	if s.stale.Load() {
		for s.stale.Load() && s.now().UnixNano() >= s.retryAt.Load() && time.Now().Before(deadline) {
			if s.reloadMu.TryLock() {
				if s.stale.Load() {
					_ = s.reloadLocked(time.Until(deadline))
				}
				s.reloadMu.Unlock()
			} else {
				time.Sleep(2 * time.Millisecond)
			}
		}
		if s.stale.Load() {
			return nil, httpx.ErrUnavailable.WithCause(errPolicyStale)
		}
	}
	return s.snap.Load(), nil
}

func (s *Service) warnUnregistered() {
	rows, err := s.st.allPolicies(s.base)
	if err != nil {
		return
	}
	for _, r := range rows {
		if !s.servesPortal(r.V1) {
			continue
		}
		if !strings.HasSuffix(r.V2, "*") && !s.reg.Has(r.V1, r.V2) {
			s.log.Warn("策略里有未注册的权限码，判定时忽略，可用 `server rbac prune` 清理", "portal", r.V1, "subject", r.V0, "code", r.V2)
		}
	}
}

// servesPortal 报告这个进程是否注册了某个端（D-061）。平台、代理商、商户是三个程序，各自只注册自己的端，
// 别的端的权限码这个进程不认识：别的端的策略既不算"未注册"，也不能被这里的 prune 删掉。没有端注册表时（只在测试里）按都注册了算。
func (s *Service) servesPortal(code string) bool {
	if s.portals == nil {
		return true
	}
	_, ok := s.portals.Get(code)
	return ok
}

// ErrInTx：改动授权后要重载内存策略，重载用的是另一条连接，看不到调用方事务里未提交的写入；
// 所以 GrantRole、DeleteRole、Prune 不能在外层事务里调用。
var ErrInTx = errors.New("rbac: must not be called inside a transaction")

func userKey(portal string, userID uint64) string { return portal + "/" + fmt.Sprint(userID) }

// ---- 判定 ----

// enabledRoles 从当前授权快照取用户启用的角色，连同这份快照一起返回：同一次判定里的权限码都要用这一份（D-053）。
func (s *Service) enabledRoles(portal string, org, userID uint64) (*policySnapshot, []Role, error) {
	pol, err := s.policy()
	if err != nil {
		return nil, nil, err
	}
	return pol, pol.enabledRoles(portal, org, userID), nil
}

// IsSuper 报告用户是否超管（拥有启用的超管角色）。主体端没有超管角色，一律 false：主体端的超管是主账号，
// 由认证器按主体行算出来放在 Principal.Super 里（D-061 第 5 条）。
func (s *Service) IsSuper(_ context.Context, portal string, userID uint64) (bool, error) {
	if _, ok := s.scopedPortal(portal); ok {
		return false, nil
	}
	_, rs, err := s.enabledRoles(portal, 0, userID)
	if err != nil {
		return false, err
	}
	for _, r := range rs {
		if r.IsSuper {
			return true, nil
		}
	}
	return false, nil
}

// Allowed 报告用户在某端是否拥有权限码。未注册的权限码一律 false。
// 主体端只算 ctx 里身份所属主体的角色（D-063）：没有主体端身份时报 scope.ErrNoOrg。
func (s *Service) Allowed(ctx context.Context, portal string, userID uint64, perm string) (bool, error) {
	if !s.reg.Has(portal, perm) {
		return false, nil
	}
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return false, err
	}
	pol, rs, err := s.enabledRoles(portal, org, userID)
	if err != nil {
		return false, err
	}
	for _, r := range rs {
		if r.IsSuper || pol.enf.allow(r.ID, portal, perm) {
			return true, nil
		}
	}
	return false, nil
}

// AllowedInSnapshot 按调用方的只读事务视图核对权限，供聚合读取的字段范围判断（D-090）。
// 与数据查询放在同一个 db.Snapshot 中；不取写锁，也不使用进程内授权缓存。
func (s *Service) AllowedInSnapshot(ctx context.Context, p auth.Principal, perm string) (bool, error) {
	if !s.reg.Has(p.Portal, perm) {
		return false, nil
	}
	org, err := s.actorOrg(p)
	if err != nil {
		return false, err
	}
	if p.Super {
		return true, nil
	}
	var allowed bool
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		v, err := s.viewOf(ctx, p.Portal, org, p.UserID, false)
		if err != nil {
			return err
		}
		allowed = v.has(perm)
		return nil
	})
	return allowed, err
}

// Perms 返回用户在该端实际拥有的权限码（只含已注册的），供 /auth/me 使用。
func (s *Service) Perms(ctx context.Context, p auth.Principal) ([]string, error) {
	org, err := s.actorOrg(p)
	if err != nil {
		return nil, err
	}
	pol, rs, err := s.enabledRoles(p.Portal, org, p.UserID)
	if err != nil {
		return nil, err
	}
	all := s.reg.Perms(p.Portal)
	out := make([]string, 0, len(all))
	for _, perm := range all {
		for _, r := range rs {
			if r.IsSuper || pol.enf.allow(r.ID, p.Portal, perm.Code) {
				out = append(out, perm.Code)
				break
			}
		}
	}
	return out, nil
}

// RequireMiddleware 返回"必须拥有权限码"的中间件，排在认证中间件之后。
func (s *Service) RequireMiddleware(portal, perm string) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		p, ok := auth.FromCtx(ctx)
		if !ok || p.Portal != portal {
			if ok {
				secmark.Set(c, secmark.Mark{Kind: secmark.CrossPortal, Detail: perm})
			}
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		setRouteGuard(c, routeGuard{perm: perm})
		if p.Super {
			c.Next()
			return
		}
		ok, err := s.Allowed(ctx, portal, p.UserID, perm)
		if err != nil {
			httpx.Fail(c, httpx.ErrUnavailable.WithCause(err))
			return
		}
		if !ok {
			secmark.Set(c, secmark.Mark{Kind: secmark.Forbidden, Detail: perm}) // 越权被拒记安全事件（D-032）
			httpx.Fail(c, httpx.ErrForbidden)
			return
		}
		c.Next()
	}
}

// RequireSuperMiddleware 返回"必须是本端超管"的中间件，排在认证中间件之后（D-035）。
// 身份里的 Super 由认证器每次请求算出：平台端按授权快照（改角色提交后快照立即重新发布），降级后下一次请求就不再放行；
// 主体端按主体行上的主账号（D-061），随账号状态一起缓存，标了按库核对的路由每次读库（D-073）。
func (s *Service) RequireSuperMiddleware(portal string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := auth.FromCtx(c.Request.Context())
		if !ok || p.Portal != portal {
			if ok {
				secmark.Set(c, secmark.Mark{Kind: secmark.CrossPortal, Detail: GuardSuperDetail})
			}
			httpx.Fail(c, httpx.ErrTokenInvalid)
			return
		}
		if !p.Super {
			secmark.Set(c, secmark.Mark{Kind: secmark.Forbidden, Detail: GuardSuperDetail})
			httpx.Fail(c, httpx.ErrForbidden)
			return
		}
		setRouteGuard(c, routeGuard{super: true})
		c.Next()
	}
}

// GuardSuperDetail 是超管守卫拒绝时安全事件里的说明（权限码的位置）。
const GuardSuperDetail = "super"

// HoldsSuperRole 报告用户是否拥有超管角色（D-035）：直接读库、不走缓存，也不看角色和账号当前是否启用——
// 用来判断"这个账号受不受超管保护"，宁可多保护。
//
// 主体端没有超管角色，受保护的是主体的主账号（D-063 第 2 条）：按账号所属主体的 owner_user_id 判断。
func (s *Service) HoldsSuperRole(ctx context.Context, portal string, userID uint64) (bool, error) {
	if ou, ok := s.scopedPortal(portal); ok {
		return isOwner(ctx, ou, userID)
	}
	rs, err := s.st.userRoles(ctx, portal, 0, userID)
	if err != nil {
		return false, err
	}
	for _, r := range rs {
		if r.IsSuper {
			return true, nil
		}
	}
	return false, nil
}

// ---- 菜单 ----

// MenuTree 是下发给前端的菜单节点。
type MenuTree struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Component string `json:"component,omitempty"`
	TitleKey  string `json:"titleKey"`
	// Titles 是后台改过的显示名（按语言），前端优先用它，没有当前语言时用 TitleKey 翻译（D-025）。
	Titles    map[string]string `json:"titles,omitempty"`
	Icon      string            `json:"icon,omitempty"`
	KeepAlive bool              `json:"keepAlive"`
	Hidden    bool              `json:"hidden"`
	Sort      int               `json:"sort"`
	Children  []*MenuTree       `json:"children,omitempty"`
}

// Menus 返回按用户权限过滤后的菜单树（规范 §6.6），已叠加菜单管理里的调整（D-025）。
func (s *Service) Menus(ctx context.Context, p auth.Principal) (any, error) {
	perms, err := s.Perms(ctx, p)
	if err != nil {
		return nil, err
	}
	has := make(map[string]struct{}, len(perms))
	for _, c := range perms {
		has[c] = struct{}{}
	}
	nodes, err := s.layout(ctx, p.Portal)
	if err != nil {
		return nil, err
	}
	return buildTree(nodes, func(perm string) bool {
		if perm == "" || p.Super {
			return true
		}
		_, ok := has[perm]
		return ok
	}, p.Super), nil
}

// BuildMenuTree 按普通用户的可见性组装代码菜单：叶子按 Perm 判定，目录在任一子节点可见时可见。
// 没有身份信息时排除 SuperOnly；带身份的完整菜单由 Service.Menus 返回。
func BuildMenuTree(nodes []MenuNode, visible func(perm string) bool) []*MenuTree {
	return buildTree(resolveLayout(nodes, nil), visible, false)
}

// ---- 角色 ----

// 下面的只读方法在主体端只看 ctx 里身份所属主体的角色（viewOrg，D-063），平台端看主体为 0 的角色。

// Roles 列出某端（主体端：本主体）的角色。
func (s *Service) Roles(ctx context.Context, portal string) ([]Role, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	return s.st.roles(ctx, portal, org)
}

// Role 按端和 ID 取角色；别的端、别的主体的角色视为不存在。
func (s *Service) Role(ctx context.Context, portal string, id uint64) (*Role, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	r, err := s.st.role(ctx, portal, org, id)
	return r, notFound(err)
}

// RoleByCode 按端和编码取角色（主体端：本主体的）。
func (s *Service) RoleByCode(ctx context.Context, portal, code string) (*Role, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	return s.st.roleByCode(ctx, portal, org, code)
}

// RoleInput 是创建、更新角色的入参。
type RoleInput struct {
	Code       string
	Name       string
	Status     int
	Sort       uint
	Remark     string
	KeepStatus bool // 更新省略状态时在角色锁内保留原值；创建忽略。
}

// CreateRole 创建角色（永远不能创建 is_super 角色，超管角色只有内置那一个）。
func (s *Service) CreateRole(ctx context.Context, actor auth.Principal, portal string, in RoleInput) (*Role, error) {
	if err := samePortal(actor, portal); err != nil {
		return nil, err
	}
	if err := validateRoleInput(in); err != nil {
		return nil, err
	}
	if in.Code == SuperRoleCode {
		return nil, httpx.ErrConflict.WithCause(ErrRoleCodeTaken)
	}
	if err := s.reserveRoleWrite(ctx, actor); err != nil {
		return nil, err
	}
	org, err := s.actorOrg(actor) // 主体端的角色建在操作人的主体里（D-063）
	if err != nil {
		return nil, err
	}
	r := &Role{Portal: portal, OrgID: org, Code: in.Code, Name: in.Name, Status: in.Status, Sort: in.Sort, Remark: in.Remark, CreatedBy: actor.UserID, UpdatedBy: actor.UserID}
	// 操作人在锁内重新认定（D-047）：账号已停用、会话已吊销的在途请求不写
	err = s.WithActor(ctx, actor, func(ctx context.Context, _ auth.Principal) error {
		var count int64
		if err := db.From(ctx).Model(&roleRow{}).Where("portal = ? AND org_id = ?", portal, org).Count(&count).Error; err != nil {
			return err
		}
		if count >= MaxRolesPerOrg {
			return httpx.ErrValidation.WithFields(httpx.NewField("roles", "common.maxLength", "role limit reached", "max", MaxRolesPerOrg))
		}
		if err := s.st.createRole(ctx, r); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrRoleCodeTaken) {
			return nil, httpx.NewKey(httpx.CodeConflict, "rbac.role.codeTaken", "role code already exists").WithCause(err)
		}
		return nil, err
	}
	return r, nil
}

// UpdateRole 更新角色的名字、状态、排序、备注；编码不可改，super 角色不可停用。
// 角色只能在它所属的端里被改动：portal 与角色不匹配时视为不存在（规范 §6.7）。
func (s *Service) UpdateRole(ctx context.Context, actor auth.Principal, portal string, id uint64, in RoleInput) (*Role, error) {
	if err := samePortal(actor, portal); err != nil {
		return nil, err
	}
	if err := s.reserveRoleWrite(ctx, actor); err != nil {
		return nil, err
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return nil, err
	}
	if _, err := s.st.role(ctx, portal, org, id); err != nil {
		return nil, notFound(err)
	}
	// 判断和写入放在同一个事务里，并先锁住超管角色行和这个角色行：角色的"当前状态"要在锁内重读，
	// 否则非超管读到"启用"之后、超管把角色停用之前的交错，会让非超管把角色写回启用、跳过重新启用的检查（D-043）。
	// 分配角色（AssignUserRoles）也在超管锁里，所以这里的检查看到的是已经提交的分配结果。
	// 主体端锁的是主体行（D-063），下同
	err = s.withLock(ctx, portal, org, func(ctx context.Context) error {
		r, err := s.st.lockRole(ctx, portal, org, id) // 锁定读：拿到的是最新已提交的状态
		if err != nil {
			return notFound(err)
		}
		if in.KeepStatus {
			in.Status = r.Status
		}
		if err := validateRoleInput(RoleInput{Code: r.Code, Name: in.Name, Status: in.Status, Sort: in.Sort, Remark: in.Remark}); err != nil {
			return err
		}
		me, err := s.loadActor(ctx, actor) // 操作人是不是超管、有哪些权限，都按锁内已提交的状态（D-043、D-045）
		if err != nil {
			return err
		}
		// 内置超管角色是超管权限的来源：非超管连名字、排序、备注也不能改（D-035）
		if r.IsSuper && !me.super {
			return httpx.ErrForbidden.WithFields(httpx.NewField("id", "rbac.role.superOnlyEdit", "only a super administrator can change the built-in super administrator role"))
		}
		if r.IsSuper && in.Status != 1 {
			return httpx.NewKey(httpx.CodeLastSuper, "rbac.role.superNoDisable", "the built-in super administrator role cannot be disabled").WithCause(ErrSuperRole)
		}
		// 停用的角色不产生任何权限；非超管把它重新启用，等于把它的权限和数据范围交给所有拥有它的人，
		// 所以要过和分配角色一样的检查：不含敏感权限码、不含自己没有的权限码、范围不宽于自己（D-039）。
		// 停用是反方向的同一件事——把这些权限从所有拥有它的人手里收走，所以用同一条规则（D-100）：
		// 非超管只能启停"自己分配得出去"的角色，名字、排序、备注不受这条限制。
		if (r.Status == 1) != (in.Status == 1) && !me.super {
			if err := s.checkRoleManageable(ctx, actor, me, id); err != nil {
				return err
			}
		}
		fields := map[string]any{"name": in.Name, "status": in.Status, "sort": in.Sort, "remark": in.Remark, "updated_by": actor.UserID}
		if err := s.st.updateRole(ctx, portal, org, id, fields); err != nil {
			return notFound(err)
		}
		// 角色启停是授权快照的一部分：提交之后重新发布，发布之前判定先等着（D-053、D-055）
		if r.Status != in.Status {
			s.holdPublish(ctx)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.st.role(ctx, portal, org, id)
}

// DeleteRole 删除角色：super 角色不可删，仍被用户引用的不可删；同时删掉它的授权。只能删自己端的角色。
func (s *Service) DeleteRole(ctx context.Context, actor auth.Principal, portal string, id uint64) error {
	if db.InTx(ctx) {
		return ErrInTx
	}
	if err := samePortal(actor, portal); err != nil {
		return err
	}
	if err := s.reserveRoleWrite(ctx, actor); err != nil {
		return err
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	if _, err := s.st.role(ctx, portal, org, id); err != nil {
		return notFound(err)
	}
	// "是否还有人在用"的判断和删除放在同一个事务里，并先拿超管锁：分配角色也在超管锁里排队，
	// 所以不会出现"数的时候没人用、数完有人分配上、然后删掉留下悬空的用户角色行"（D-043）。
	err = s.withLock(ctx, portal, org, func(ctx context.Context) error {
		if _, err := s.loadActor(ctx, actor); err != nil { // 操作人在锁内重新认定（D-047）
			return err
		}
		r, err := s.st.lockRole(ctx, portal, org, id) // 锁定读：拿到的是最新已提交的状态
		if err != nil {
			return notFound(err)
		}
		if r.IsSuper {
			return httpx.NewKey(httpx.CodeLastSuper, "rbac.role.superNoDelete", "the built-in super administrator role cannot be deleted").WithCause(ErrSuperRole)
		}
		n, err := s.st.roleUserCountLocked(ctx, r.Portal, id)
		if err != nil {
			return err
		}
		if n > 0 {
			return httpx.NewKey(httpx.CodeConflict, "rbac.role.inUse", fmt.Sprintf("the role is still assigned to %d users; unassign it first", n), "count", n).WithCause(ErrRoleInUse)
		}
		if err := s.st.deleteRolePolicies(ctx, r.Portal, id); err != nil {
			return err
		}
		if err := s.st.deleteRoleScopes(ctx, id); err != nil {
			return err
		}
		if err := s.st.deleteRole(ctx, portal, org, id); err != nil {
			return err
		}
		s.holdPublish(ctx)
		return nil
	})
	if err != nil {
		return err
	}
	return s.publishErr()
}

// RolePerms 返回角色被授予的权限码。只能看自己端的角色。
func (s *Service) RolePerms(ctx context.Context, portal string, roleID uint64) ([]string, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	if _, err := s.st.role(ctx, portal, org, roleID); err != nil {
		return nil, notFound(err)
	}
	codes, err := s.st.rolePerms(ctx, portal, roleID)
	if err != nil {
		return nil, err
	}
	// 只返回已注册的权限码：代码里删掉的权限码判定时本来就忽略；不返回它，授权对话框整体提交时才不会被
	// "未注册的权限码"拒绝，保存一次也就把它清掉了（D-035）。通配符规则原样保留。
	out := codes[:0]
	for _, c := range codes {
		if strings.HasSuffix(c, "*") || s.reg.Has(portal, c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// GrantRolePerms 整体替换角色的权限码（规范 §6.5 的敏感权限规则），数据范围不变。只能给自己端的角色授权。
func (s *Service) GrantRolePerms(ctx context.Context, actor auth.Principal, portal string, roleID uint64, codes []string) error {
	return s.GrantRole(ctx, actor, portal, roleID, codes, nil)
}

// GrantRole 整体替换角色的权限码，并设置给定数据资源的范围（D-039；scopes 为 nil 或不含某资源时，该资源的范围不变）。
// 两者在一个事务里写入，检查按保存后的最终状态做：
//   - 敏感权限码只有超管能授予；非超管不能授出自己没有的权限码；
//   - 非超管给角色加一个受数据资源约束的权限码、或者放宽某个资源的范围时，角色在该资源上的范围
//     不能比操作人自己在同一权限码上的范围宽；收窄总是允许。
func (s *Service) GrantRole(ctx context.Context, actor auth.Principal, portal string, roleID uint64, codes []string, scopes map[string]DataScope) error {
	if len(codes) > MaxGrantCodes {
		return httpx.ErrValidation.WithFields(httpx.NewField("codes", "validation.max", "too many permission codes", "param", MaxGrantCodes))
	}
	if db.InTx(ctx) {
		return ErrInTx
	}
	if err := s.reserveRoleWrite(ctx, actor); err != nil {
		return err
	}
	if err := samePortal(actor, portal); err != nil {
		return err
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	r, err := s.st.role(ctx, portal, org, roleID)
	if err != nil {
		return notFound(err)
	}
	if r.IsSuper {
		return httpx.NewKey(httpx.CodeLastSuper, "rbac.role.superHasAll", "the super administrator role has every permission and needs no grants").WithCause(ErrSuperRole)
	}
	codes = dedupe(codes)
	var fields []httpx.FieldError
	for res, sc := range scopes {
		d, ok := s.reg.DataResource(r.Portal, res)
		switch {
		case !ok:
			fields = append(fields, httpx.NewField(res, "rbac.dataScope.unknown", "unknown data resource"))
		case !d.Allowed(sc):
			fields = append(fields, httpx.NewField(res, "rbac.dataScope.invalid", "this scope is not allowed for the resource"))
		}
	}
	if len(fields) > 0 {
		return httpx.ErrValidation.WithFields(fields...)
	}
	for _, c := range codes {
		if _, ok := s.reg.Perm(r.Portal, c); !ok {
			fields = append(fields, httpx.NewField(c, "rbac.perm.unknown", "unknown permission"))
		}
	}
	if len(fields) > 0 {
		return httpx.ErrForbidden.WithFields(fields...)
	}
	// 授权也拿超管锁：非超管"自己有没有这个权限码、自己的范围有多宽"要在锁内按已提交的状态算（actorView），
	// 而不是用内存里的授权快照——快照在别人的写入提交之后才重新发布，这个窗口里操作人还能把刚被收回的权限授出去（D-043）。
	// 改角色、分配角色、删角色都在这把锁里排队，所以锁内读到的操作人权限就是当前的。
	if err := s.withLock(ctx, portal, org, func(ctx context.Context) error {
		// 锁住角色行：拿到的是最新已提交的权限码和范围；刚被并发删除的角色不留下孤儿策略
		if _, err := s.st.lockRole(ctx, r.Portal, org, r.ID); err != nil {
			return notFound(err)
		}
		me, err := s.loadActor(ctx, actor) // 不沿用认证时的 actor.Super（D-045）
		if err != nil {
			return err
		}
		if !me.super {
			var fields []httpx.FieldError
			for _, c := range codes {
				if p, _ := s.reg.Perm(r.Portal, c); p.Sensitive {
					fields = append(fields, httpx.NewField(c, "rbac.perm.sensitive", "sensitive permission can only be granted by a super administrator"))
					continue
				}
				if !me.has(c) {
					fields = append(fields, httpx.NewField(c, "rbac.perm.notOwned", "you cannot grant a permission you do not have"))
				}
			}
			if len(fields) > 0 {
				return httpx.ErrForbidden.WithFields(fields...)
			}
			oldCodes, err := s.st.rolePermsLocked(ctx, portal, roleID)
			if err != nil {
				return err
			}
			for _, old := range oldCodes {
				if !slices.Contains(codes, old) {
					if err := s.checkRoleManageable(ctx, actor, me, roleID); err != nil {
						return err
					}
					break
				}
			}
			if err := s.checkScopeGrant(ctx, actor, me, r, codes, scopes); err != nil {
				return err
			}
		}
		if err := s.st.replaceRolePerms(ctx, r.Portal, roleID, codes); err != nil {
			return err
		}
		if err := s.st.setRoleScopes(ctx, roleID, scopes); err != nil {
			return err
		}
		// 提交之后重新发布，发布之前判定先等着（D-055）：不能在"已提交、未发布"的窗口里按旧快照放行刚撤掉的权限
		s.holdPublish(ctx)
		return nil
	}); err != nil {
		return err
	}
	return s.publishErr()
}

// publishErr 是改授权的接口在提交之后的返回值：重新发布失败时告诉调用方（改动已经保存，判定暂停到重载成功）。
func (s *Service) publishErr() error {
	if s.stale.Load() {
		return httpx.ErrUnavailable.WithCause(errPolicyStale)
	}
	return nil
}

// checkScopeGrant 是非超管授权时的数据范围检查（D-039 第 5 条），在授权事务里、按最终状态做；操作人自己的范围来自锁内的 me。
func (s *Service) checkScopeGrant(ctx context.Context, actor auth.Principal, me *actorView, r *Role, codes []string, scopes map[string]DataScope) error {
	oldCodes, err := s.st.rolePermsLocked(ctx, r.Portal, r.ID)
	if err != nil {
		return err
	}
	had := make(map[string]struct{}, len(oldCodes))
	for _, c := range oldCodes {
		had[c] = struct{}{}
	}
	stored, err := s.st.roleScopesLocked(ctx, r.ID)
	if err != nil {
		return err
	}
	var fields []httpx.FieldError
	var expanded []string
	finalScopes := make(map[string]DataScope, len(stored)+len(scopes))
	for res, sc := range stored {
		finalScopes[res] = sc
	}
	for res, sc := range scopes {
		finalScopes[res] = sc
	}
	for _, d := range s.reg.DataResources(r.Portal) {
		old := d.Default
		if sc, ok := stored[d.Code]; ok && d.Allowed(sc) {
			old = sc
		}
		next := old
		if sc, ok := scopes[d.Code]; ok {
			next = sc
		}
		widened := next.Wider(old)
		if old.Wider(next) {
			// 收窄同样需要对原角色的管理资格（D-117）。
			if err := s.checkRoleManageable(ctx, actor, me, r.ID); err != nil {
				return err
			}
		}
		if _, provided := scopes[d.Code]; provided {
			hasResourceCode := slices.ContainsFunc(codes, func(c string) bool { return s.reg.ResourceOf(r.Portal, c) == d.Code })
			if !hasResourceCode {
				for _, perm := range d.Perms {
					if next.Wider(me.scopeOf(d, perm)) {
						return httpx.ErrForbidden.WithFields(httpx.NewField(d.Code, "rbac.dataScope.wider", "the unassigned resource scope is wider than yours"))
					}
				}
			}
		}
		for _, c := range codes {
			if s.reg.ResourceOf(r.Portal, c) != d.Code {
				continue
			}
			if _, existed := had[c]; existed && !widened {
				continue
			}
			expanded = append(expanded, c)
			mine := me.scopeOf(d, c)
			if next.Wider(mine) {
				fields = append(fields, httpx.NewField(d.Code, "rbac.dataScope.wider",
					fmt.Sprintf("the role's scope on %s (%s) would be wider than yours for %s (%s)", d.Code, next, c, mine),
					"resource", d.Code, "perm", c, "scope", string(next), "yours", string(mine)))
				break
			}
		}
	}
	if len(fields) > 0 {
		return httpx.ErrForbidden.WithFields(fields...)
	}
	return s.checkRoleMembersDepartment(ctx, actor, me, r.ID, expanded, finalScopes)
}

// ---- 数据范围（D-039）----

// DataResources 返回某端声明的数据资源（授权界面用）。
func (s *Service) DataResources(portal string) []DataResource { return s.reg.DataResources(portal) }

// RoleDataScopes 返回角色在每个数据资源上的范围（没存的取默认值）；超管角色全部是 all。只能看自己端的角色。
func (s *Service) RoleDataScopes(ctx context.Context, portal string, roleID uint64) (map[string]DataScope, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	r, err := s.st.role(ctx, portal, org, roleID)
	if err != nil {
		return nil, notFound(err)
	}
	stored, err := s.st.roleScopes(ctx, roleID)
	if err != nil {
		return nil, err
	}
	out := map[string]DataScope{}
	for _, d := range s.reg.DataResources(r.Portal) {
		sc := d.Default
		if v, ok := stored[d.Code]; ok && d.Allowed(v) {
			sc = v
		}
		if r.IsSuper {
			sc = ScopeAll
		}
		out[d.Code] = sc
	}
	return out, nil
}

// DataScopeOf 返回用户对某个资源做某件事（权限码 perm）时的范围：拥有 perm 的启用角色里最宽的那个；
// 超管是 all；没有任何角色拥有 perm 时是 self（最窄）。perm 必须属于这个资源，否则是编程错误。
func (s *Service) DataScopeOf(ctx context.Context, p auth.Principal, resource, perm string) (DataScope, error) {
	d, ok := s.reg.DataResource(p.Portal, resource)
	if !ok {
		return "", fmt.Errorf("rbac: 端 %s 没有声明数据资源 %s", p.Portal, resource)
	}
	if s.reg.ResourceOf(p.Portal, perm) != resource {
		return "", fmt.Errorf("rbac: 权限码 %s 不受数据资源 %s 约束", perm, resource)
	}
	// 与其他判断一致：本次请求认证时已经是超管的，按超管算（同一请求里别的检查也看 p.Super）
	if p.Super {
		return ScopeAll, nil
	}
	// 角色、角色状态、权限码、范围都按 ctx 所在的数据库视图读（D-054），不用内存里的授权快照：范围要和操作人的部门、
	// 被读的数据是同一个时刻的。调用方在 db.Snapshot 里调用时，和它读数据是同一个视图；不在事务里时自己开一个只读快照
	org, err := s.actorOrg(p)
	if err != nil {
		return "", err
	}
	var sc DataScope
	err = db.Snapshot(ctx, func(ctx context.Context) error {
		v, err := s.viewOf(ctx, p.Portal, org, p.UserID, false)
		if err != nil {
			return err
		}
		sc = v.scopeOf(d, perm)
		return nil
	})
	return sc, err
}

// DataFilter 返回用户对某个资源做某件事时的过滤条件（D-039）：范围已解析成具体部门。
// 部门信息来自端的 portal.Portal.Dept；端没有提供、或者用户没分配部门时，部门类范围按仅本人算。
//
// 范围和部门在 ctx 所在的同一个数据库视图里读（D-054）。要让过滤条件和被过滤的数据是同一个时刻的，调用方把
// "算过滤条件"和"读数据"放进同一个 db.Snapshot：分开读的话，可能拿新的范围配上旧的部门（或反过来），
// 算出操作人在任何时刻都没有过的可见范围。
func (s *Service) DataFilter(ctx context.Context, p auth.Principal, resource, perm string) (DataFilter, error) {
	var f DataFilter
	err := db.Snapshot(ctx, func(ctx context.Context) error {
		sc, err := s.DataScopeOf(ctx, p, resource, perm)
		if err != nil {
			return err
		}
		f, err = s.filterFor(ctx, p, sc)
		return err
	})
	return f, err
}

// filterFor 把范围换成过滤条件：部门类范围要查操作人所在的部门（和下级）。
func (s *Service) filterFor(ctx context.Context, p auth.Principal, sc DataScope) (DataFilter, error) {
	f := DataFilter{Scope: sc, UserID: p.UserID}
	if sc != ScopeDept && sc != ScopeDeptTree {
		return f, nil
	}
	var depts portal.DeptProvider
	if s.portals != nil {
		if pt, ok := s.portals.Get(p.Portal); ok {
			depts = pt.Dept
		}
	}
	if depts == nil {
		f.Scope = ScopeSelf
		return f, nil
	}
	dept, err := depts.UserDept(ctx, p.UserID)
	if err != nil {
		return DataFilter{}, err
	}
	if dept == 0 {
		f.Scope = ScopeSelf
		return f, nil
	}
	if sc == ScopeDept {
		f.DeptIDs = []uint64{dept}
		return f, nil
	}
	ids, err := depts.DeptSubtree(ctx, dept)
	if err != nil {
		return DataFilter{}, err
	}
	f.DeptIDs = ids
	return f, nil
}

// ---- 用户与角色 ----

// UserRoles 返回用户的全部角色（含停用的，供管理界面显示）。
// 主体端只返回本主体的角色（viewOrg，D-063）。
func (s *Service) UserRoles(ctx context.Context, portal string, userID uint64) ([]Role, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	return s.st.userRoles(ctx, portal, org, userID)
}

// UserRolesBatch 一次取多个用户的角色，供列表页使用。主体端只返回本主体的角色。
func (s *Service) UserRolesBatch(ctx context.Context, portal string, userIDs []uint64) (map[uint64][]Role, error) {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return nil, err
	}
	return s.st.userRolesBatch(ctx, portal, org, userIDs)
}

// AssignUserRoles 整体替换用户的角色。只有超管能分配 super 角色；不能让最后一个超管失去超管身份。
// 主体端（D-063）：角色和目标账号都必须属于操作人的主体（账号不存在或属于别的主体回 404）；员工不能改主账号的角色；
// 主账号的超管身份不来自角色，所以不做"最后一个超管"检查。
func (s *Service) AssignUserRoles(ctx context.Context, actor auth.Principal, portal string, userID uint64, roleIDs []uint64) error {
	if len(roleIDs) > MaxRolesPerUser {
		return httpx.ErrValidation.WithFields(httpx.NewField("roleIds", "validation.max", "too many roles", "param", MaxRolesPerUser))
	}
	if err := samePortal(actor, portal); err != nil {
		return err
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	roleIDs = dedupeIDs(roleIDs)
	if err := s.reserveRoleWrite(ctx, actor); err != nil {
		return err
	}
	givesSuper := false
	for _, id := range roleIDs {
		// 按 (portal, org, id) 查：别的端、别的主体的角色对本端、本主体来说就是不存在
		r, err := s.st.role(ctx, portal, org, id)
		if err != nil {
			return httpx.ErrValidation.WithFields(httpx.NewField("roleIds", "rbac.role.notFound", fmt.Sprintf("role %d not found", id), "id", id))
		}
		if r.IsSuper {
			givesSuper = true
		}
	}
	// "最后一个可用超管"的判断和写入放在同一个事务里，并先锁住超管角色行：两个超管并发互相降级时，
	// 后拿到锁的那个看到的是前一个已经提交的结果，不会双双通过判断（规范 §6.4）。
	// 非超管的检查也在锁里做：超管同时把目标提升为超管时，要么先提升（这里看到目标已是超管而拒绝），
	// 要么先改完（提升在后面覆盖），不会出现"判断时还不是超管、写入时已经是"（D-035）。
	return s.withLock(ctx, portal, org, func(ctx context.Context) error {
		// 角色是否存在要在锁内再看一次：删角色也在这把锁里，锁外看到的角色可能在排队时已被删掉，
		// 不再核对就会给一个已删除的角色留下悬空的用户角色行（D-043）
		for _, id := range roleIDs {
			if _, err := s.st.lockRole(ctx, portal, org, id); err != nil {
				return httpx.ErrValidation.WithFields(httpx.NewField("roleIds", "rbac.role.notFound", fmt.Sprintf("role %d not found", id), "id", id))
			}
		}
		// 操作人是不是超管在锁内按库重新认定（D-045）：认证时是超管、之后被收回超管角色的在途请求，
		// 不能再凭旧身份给自己或别人分配超管角色
		me, err := s.loadActor(ctx, actor)
		if err != nil {
			return err
		}
		// 主体端：目标账号必须属于操作人的主体（模块已经按主体查过目标，这里是最后一道防线）
		if me.org != nil {
			if err := s.checkInOrg(ctx, portal, org, userID); err != nil {
				return err
			}
		}
		if givesSuper && !me.super {
			return httpx.ErrForbidden.WithFields(httpx.NewField("roleIds", "rbac.role.superOnlyAssign", "only a super administrator can assign the super role"))
		}
		if !me.super {
			// 非超管：不能动超管账号的角色；新加的角色必须"权限不超过自己"，规则与授权（GrantRolePerms）一致：
			// 含敏感权限码的角色不能分配，含自己没有的权限码的角色也不能分配。目标用户已有的角色不算新加，
			// 所以整体替换时保留原有角色不会被这条挡住。
			current, err := s.st.userRolesLocked(ctx, portal, org, userID) // 锁定读：看到的是已提交的分配（D-043）
			if err != nil {
				return err
			}
			superOnly := httpx.ErrForbidden.WithFields(httpx.NewField("roleIds", "rbac.role.superOnlyChange", "only a super administrator can change the roles of a super administrator"))
			if me.org != nil && me.org.OwnerUserID == userID {
				return superOnly // 主体端：员工不能改主账号的角色（锁内按主体行认定的主账号）
			}
			have := make(map[uint64]struct{}, len(current))
			for _, r := range current {
				if r.IsSuper {
					return superOnly
				}
				have[r.ID] = struct{}{}
			}
			var added []uint64
			var removed []uint64
			for _, id := range roleIDs {
				if _, ok := have[id]; !ok {
					added = append(added, id)
				}
			}
			for _, role := range current {
				if !slices.Contains(roleIDs, role.ID) {
					removed = append(removed, role.ID)
				}
			}
			if err := s.checkAssignable(ctx, actor, me, portal, removed); err != nil {
				return err
			}
			if err := s.checkAssignable(ctx, actor, me, portal, added); err != nil {
				return err
			}
			if s.portals != nil {
				if pt, ok := s.portals.Get(portal); ok && pt.Dept != nil {
					deptID, err := pt.Dept.UserDept(ctx, userID)
					if err != nil {
						return err
					}
					for _, roleID := range append(added, removed...) {
						if err := s.checkRoleDepartment(ctx, actor, me, roleID, deptID); err != nil {
							return err
						}
					}
				}
			}
		}
		if !givesSuper && me.org == nil {
			last, err := s.IsLastSuper(ctx, portal, userID)
			if err != nil {
				return err
			}
			if last {
				return httpx.New(httpx.CodeLastSuper, "")
			}
		}
		if err := s.st.replaceUserRoles(ctx, portal, userID, roleIDs); err != nil {
			return err
		}
		// 提交之后重新发布授权快照，发布之前判定先等着（D-053、D-055）：成员关系是快照的一部分
		s.holdPublish(ctx)
		return nil
	})
}

// ErrNoTx：ClearUserRoles 只能在调用方的事务里用。
var ErrNoTx = errors.New("rbac: must be called inside a transaction")

// ErrUnscopedPortal：ClearUserRoles 不用于有超管角色的端。
var ErrUnscopedPortal = errors.New("rbac: not a scoped portal")

// ClearUserRoles 清空一个主体端账号的全部角色，不经过"操作人能不能分配"的检查：给平台发起、不以主体端身份执行的变更用
// （更换主账号时用在原主账号身上，D-101）。必须在事务里调用，且调用方已经锁住这个账号所属主体的行——主体端分配角色、
// 启停角色都排在同一把锁后面（withLock），所以不会和它们交叉。提交之后重新发布授权快照并通知别的进程。
//
// 不用于平台端这类有超管角色的端：那里改角色要过"最后一个超管"的检查，走 AssignUserRoles。本进程注册了这个端
// 就看它是不是主体端；没有注册的（平台程序管理主体端的账号）看库里这个端有没有超管角色——主体端没有。
//
// 删除在调用方的事务里做（平台的管理事务是可重复读），会在这个账号的角色行附近留下间隙锁，直到事务结束；
// 更换主账号是低频的管理操作，这段时间里别的主体给相邻账号分配角色会短暂等待。
func (s *Service) ClearUserRoles(ctx context.Context, portalCode string, userID uint64) error {
	if !db.InTx(ctx) {
		return ErrNoTx
	}
	if s.portals != nil {
		if p, ok := s.portals.Get(portalCode); ok && !p.Scoped {
			return ErrUnscopedPortal
		}
	}
	hasSuper, err := s.st.hasSuperRole(ctx, portalCode)
	if err != nil {
		return err
	}
	if hasSuper {
		return ErrUnscopedPortal
	}
	if err := s.st.replaceUserRoles(ctx, portalCode, userID, nil); err != nil {
		return err
	}
	s.holdPublish(ctx)
	return nil
}

// WithSuperLock 在一个事务里先锁住该端的超管角色行，再执行 fn，用来串行化所有可能让端失去
// 最后一个可用超管的操作（改动用户角色、停用账号）。已在事务里时加入该事务。fn 里对账号和角色的
// 读取看到的是并发操作提交后的状态，所以"先判断再写入"在锁内是安全的。
// 主体端没有超管角色，锁的是 ctx 里身份所属主体的行（D-063）；没有主体端身份时报 scope.ErrNoOrg。
func (s *Service) WithSuperLock(ctx context.Context, portal string, fn func(ctx context.Context) error) error {
	org, err := s.viewOrg(ctx, portal)
	if err != nil {
		return err
	}
	return s.withLock(ctx, portal, org, fn)
}

// CurrentActor 在超管锁里按已提交的状态重新认定操作人：账号停用或会话已被吊销的回 401（D-046），
// 否则返回 Super 更新过的身份（D-045）。
// 请求认证时算出的 Principal.Super 在整个请求里不变：认证之后、拿到锁之前超管角色被收回，旧身份仍说"是超管"。
// 所有"超管可以跳过检查"的写操作，都要在 WithSuperLock 里先调它，后面的判断只用它返回的身份。
// 必须在 WithSuperLock 里调用（锁定读才看得到并发写入已提交的结果）；actor 必须属于 portal。
func (s *Service) CurrentActor(ctx context.Context, actor auth.Principal) (auth.Principal, error) {
	if !db.InTx(ctx) {
		return auth.Principal{}, ErrNoTx
	}
	me, err := s.loadActor(ctx, actor)
	if err != nil {
		return actor, err
	}
	actor.Super = me.super
	return actor, nil
}

// WithSelf 用于只改调用者本人数据的写操作（个人资料、头像、下线本人的其他设备）：不拿全端共用的超管锁，
// 在事务里先锁本人的账号行、再锁本人的会话行，确认账号仍启用、会话仍有效（否则 401）且未锁屏（否则 423），再执行 fn（D-058、D-059、D-106）。
// 这类接口只要求登录，任何账号都能调用；拿超管锁的话，一个普通账号高并发调用就能把全端的管理写操作排在后面。
// 停用账号写的是账号行，吊销会话（下线、改密、重置密码、停用）写的是会话行，所以两边都排队：这里拿到锁时看到的是
// 对方已经提交的结果，对方要等这里提交之后才能写——吊销返回之后，不会再有拿着被吊销会话的本人写操作提交。
// 加锁顺序是"账号行 → 会话行"，和改密、解锁、建会话一致；吊销单个会话只锁会话行，不会反过来等账号行。
// 用户来源没实现 portal.UserLocker、没配 LockSession 时退回普通读。
func (s *Service) WithSelf(ctx context.Context, p auth.Principal, fn func(ctx context.Context) error) error {
	return s.withWriteAdmission(ctx, "self:"+userKey(p.Portal, p.UserID), s.selfWrites, func(ctx context.Context) error {
		return db.Tx(ctx, func(ctx context.Context) error {
			if p.UserID == 0 {
				return httpx.ErrTokenInvalid
			}
			if s.portals != nil {
				if pt, ok := s.portals.Get(p.Portal); ok && pt.Users != nil {
					var acc *portal.Account
					var err error
					if l, ok := pt.Users.(portal.UserLocker); ok {
						acc, err = l.LockByID(ctx, p.UserID)
					} else {
						acc, err = pt.Users.FindByID(ctx, p.UserID)
					}
					if errors.Is(err, portal.ErrAccountNotFound) {
						return httpx.ErrTokenInvalid
					}
					if err != nil {
						return err
					}
					if acc.Status != 1 {
						return httpx.ErrTokenInvalid
					}
					// 主体端：账号仍属于身份的主体、主体仍启用（D-063 第 5 条）。普通读，不拿主体行锁：
					// 加锁顺序是"主体行 → 账号行"，这里已经锁了账号行；拿主体行锁也会让任何员工都能把本主体的管理写操作排在后面
					if pt.Scoped {
						ou, _ := pt.Users.(portal.OrgUserProvider)
						if ou == nil || p.OrgID == 0 || acc.OrgID != p.OrgID {
							return httpx.ErrTokenInvalid
						}
						o, err := ou.FindOrgByID(ctx, p.OrgID)
						if errors.Is(err, portal.ErrOrgNotFound) {
							return httpx.ErrTokenInvalid
						}
						if err != nil {
							return err
						}
						if o.ID != p.OrgID || !o.Enabled() {
							return httpx.ErrTokenInvalid
						}
					}
				}
			}
			check := s.lockSession
			if check == nil {
				check = s.sessionActive
			}
			if check != nil && p.SessionID != "" {
				ok, err := check(ctx, p.Portal, p.SessionID)
				if err != nil {
					return err
				}
				if !ok {
					return httpx.ErrTokenInvalid
				}
			}
			return fn(ctx)
		})
	})
}

// WithActor 拿超管锁，在锁内按已提交的状态重新认定操作人，再把认定后的身份交给 fn（D-047）：
// 账号已停用、会话已吊销的回 401、会话已锁屏的回 423，fn 不执行；Super 按库重算。拿锁和认定合在一步，调用方拿不到旧身份，
// 也就不会忘了认定。所有由后台操作人发起的写操作都走它（或走内部已经这样做的 rbac 方法）。
// 同一端的这类写操作因此串行执行，后台写操作量小，可以接受。
//
// 主体端拿的是操作人主体行的排他锁（D-063）：同一主体的写操作串行，别的主体不受影响。
// 认定时继续锁住操作人账号和会话，直到事务结束，和本人操作、会话下线按同一顺序排队（D-089）。
func (s *Service) WithActor(ctx context.Context, actor auth.Principal, fn func(ctx context.Context, actor auth.Principal) error) error {
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	return s.withLock(ctx, actor.Portal, org, func(ctx context.Context) error {
		cur, err := s.CurrentActor(ctx, actor)
		if err != nil {
			return err
		}
		return fn(ctx, cur)
	})
}

// AllowedLocked 报告操作人此刻是否拥有权限码，按锁内已提交的授权算（D-047）。必须在 WithActor / WithSuperLock 里调用。
func (s *Service) AllowedLocked(ctx context.Context, actor auth.Principal, perm string) (bool, error) {
	if !db.InTx(ctx) {
		return false, ErrNoTx
	}
	if !s.reg.Has(actor.Portal, perm) {
		return false, nil
	}
	me, err := s.loadActor(ctx, actor)
	if err != nil {
		return false, err
	}
	return me.has(perm), nil
}

// DataFilterLocked 与 DataFilter 相同，但操作人的超管身份、权限和范围都在锁内按已提交的状态算（D-045）：
// 用在结果会长期生效的写操作上（挪部门、选负责人），而不是请求级的读取过滤。必须在 WithSuperLock 里调用。
func (s *Service) DataFilterLocked(ctx context.Context, p auth.Principal, resource, perm string) (DataFilter, error) {
	if !db.InTx(ctx) {
		return DataFilter{}, ErrNoTx
	}
	d, ok := s.reg.DataResource(p.Portal, resource)
	if !ok {
		return DataFilter{}, fmt.Errorf("rbac: 端 %s 没有声明数据资源 %s", p.Portal, resource)
	}
	if s.reg.ResourceOf(p.Portal, perm) != resource {
		return DataFilter{}, fmt.Errorf("rbac: 权限码 %s 不受数据资源 %s 约束", perm, resource)
	}
	me, err := s.loadActor(ctx, p)
	if err != nil {
		return DataFilter{}, err
	}
	return s.filterFor(ctx, p, me.scopeOf(d, perm))
}

// CheckEnableUser 检查操作人能否重新启用一个停用的账号（D-058）：启用账号等于把它身上角色的权限交回去，
// 规则和分配角色一致——非超管只能启用"每个角色都是自己能分配的"账号（不含敏感权限码、不含自己没有的权限码、
// 范围不宽于自己）。停用中的角色也算（D-100）：它以后被重新启用时不会再回头看都有谁拥有它。
// 超管不受限。必须在 WithActor（超管锁）里调用，actor 用锁内认定过的身份。
func (s *Service) CheckEnableUser(ctx context.Context, actor auth.Principal, portal string, userID uint64) error {
	if !db.InTx(ctx) {
		return ErrNoTx
	}
	me, err := s.loadActor(ctx, actor)
	if err != nil {
		return err
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	// 主体端：目标不存在或属于别的主体回 404（主账号也一样，D-063）
	if me.org != nil {
		if err := s.checkInOrg(ctx, portal, org, userID); err != nil {
			return err
		}
	}
	if me.super {
		return nil
	}
	roles, err := s.st.userRolesLocked(ctx, portal, org, userID) // 锁定读：看到的是已提交的分配
	if err != nil {
		return err
	}
	ids := make([]uint64, 0, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID)
	}
	err = s.checkAssignable(ctx, actor, me, portal, ids)
	if err == nil && s.portals != nil {
		if pt, ok := s.portals.Get(portal); ok && pt.Dept != nil {
			deptID, deptErr := pt.Dept.UserDept(ctx, userID)
			if deptErr != nil {
				return deptErr
			}
			for _, roleID := range ids {
				if err = s.checkRoleDepartment(ctx, actor, me, roleID, deptID); err != nil {
					break
				}
			}
		}
	}
	if errors.Is(err, httpx.ErrForbidden) {
		// 不把具体是哪个角色、哪个权限码、哪种范围告诉调用者（D-059）：他能启停这个账号，不一定看得到它的角色和授权
		return httpx.ErrForbidden.WithFields(httpx.NewField("status", "rbac.user.enableNotAssignable",
			"this account holds roles you cannot assign; only someone who can assign all of them may enable it"))
	}
	return err
}

// checkAssignable 检查非超管能否新分配这些角色：每个角色的权限码都必须是操作人拥有的，且不含敏感权限码。
// 操作人的权限和范围来自锁内按已提交状态算出来的 me（D-043）。
//
// 被拒的细节（是角色里的哪个权限码、哪个范围）只回给能看角色的操作人（拥有声明了 RoleView 的权限码，D-069）；
// 别人只得到 rbac.role.notAssignable 和角色 ID，免得有分配权限、没有查看权限的人拿角色 ID 试出角色的内容。
// 完整的原因记进服务端日志。actor 只用来记日志。
func (s *Service) checkAssignable(ctx context.Context, actor auth.Principal, me *actorView, portal string, roleIDs []uint64) error {
	var fields []httpx.FieldError
	var rejected []uint64
	for _, id := range roleIDs {
		codes, err := s.st.rolePermsLocked(ctx, portal, id) // 锁定读：看到的是已提交的授权（D-043）
		if err != nil {
			return err
		}
		// 数据范围（D-039）：角色在某个资源上的范围不能比操作人在同一权限码上的范围宽，
		// 否则可以借一个"全部"范围的角色让自己或别人看到更多数据
		if f, err := s.checkRoleScopesAssignable(ctx, me, portal, id, codes); err != nil {
			return err
		} else if f != nil {
			fields, rejected = append(fields, *f), append(rejected, id)
			continue
		}
		for _, c := range codes {
			p, ok := s.reg.Perm(portal, c)
			if p.Sensitive {
				fields, rejected = append(fields, httpx.NewField("roleIds", "rbac.role.holdsSensitive", fmt.Sprintf("role %d holds sensitive permission %s; only a super administrator can assign it", id, c), "id", id, "perm", c)), append(rejected, id)
				break
			}
			// 通配规则和未注册的权限码不算操作人"拥有"的：非超管不能把带这类规则的角色分配出去
			if !ok || !me.has(c) {
				fields, rejected = append(fields, httpx.NewField("roleIds", "rbac.role.holdsNotOwned", fmt.Sprintf("role %d holds permission %s that you do not have", id, c), "id", id, "perm", c)), append(rejected, id)
				break
			}
		}
	}
	if len(fields) == 0 {
		return nil
	}
	for _, f := range fields {
		logx.From(ctx).WarnContext(ctx, "角色不能由这个操作人分配或启停", "portal", portal, "org_id", actor.OrgID, "user_id", actor.UserID,
			"reason", f.Key, "detail", f.Message)
	}
	if !s.canViewRoles(me, portal) {
		fields = make([]httpx.FieldError, 0, len(rejected))
		for _, id := range rejected {
			fields = append(fields, httpx.NewField("roleIds", "rbac.role.notAssignable",
				fmt.Sprintf("role %d grants more than you are allowed to hand out", id), "id", id))
		}
	}
	return httpx.ErrForbidden.WithFields(fields...)
}

// canViewRoles 报告操作人能不能看角色的权限码和数据范围：超管，或拥有本端声明了 RoleView 的权限码（D-069）。
// me 是锁内按已提交状态算出来的。
func (s *Service) canViewRoles(me *actorView, portal string) bool {
	if me.super {
		return true
	}
	for _, c := range s.reg.RoleViewPerms(portal) {
		if me.has(c) {
			return true
		}
	}
	return false
}

// checkRoleScopesAssignable 检查非超管能否把角色分配出去：角色在每个受约束权限码上的范围都不宽于操作人的。
func (s *Service) checkRoleScopesAssignable(ctx context.Context, me *actorView, portal string, roleID uint64, codes []string) (*httpx.FieldError, error) {
	stored, err := s.st.roleScopesLocked(ctx, roleID)
	if err != nil {
		return nil, err
	}
	for _, c := range codes {
		res := s.reg.ResourceOf(portal, c)
		if res == "" {
			continue
		}
		d, _ := s.reg.DataResource(portal, res)
		sc := d.Default
		if v, ok := stored[res]; ok && d.Allowed(v) {
			sc = v
		}
		if mine := me.scopeOf(d, c); sc.Wider(mine) {
			f := httpx.NewField("roleIds", "rbac.role.widerScope",
				fmt.Sprintf("role %d sees %s on %s, wider than yours (%s)", roleID, sc, res, mine), "id", roleID, "resource", res, "scope", string(sc), "yours", string(mine))
			return &f, nil
		}
	}
	return nil, nil
}

// actorView 是操作人在锁内按已提交状态算出来的权限和范围（D-043）：非超管授权、分配角色、重新启用角色时，
// "自己有没有这个权限码、自己的范围有多宽"都用它判断，而不是用内存里的授权快照——快照在别人的写入提交之后
// 才重新发布，窗口里操作人还能把刚被收回的权限授出去。请求级的权限码判定（Allowed）用授权快照；
// 请求级的数据范围（DataScopeOf）用同一套 viewOf，只是普通读、看调用方的快照（D-054）。
type actorView struct {
	super  bool                            // 锁内看到启用的超管角色（不看认证时的 actor.Super，D-045）；主体端是"锁内看到的主账号"
	org    *portal.Org                     // 主体端：锁内读到的操作人主体（排他锁已经在手）；平台端为 nil
	perms  map[uint64][]string             // 启用角色 → 策略里的权限码（可能带通配符）
	scopes map[uint64]map[string]DataScope // 启用角色 → 存下来的范围
}

// loadActor 在超管锁内读操作人的启用角色、每个角色的策略和范围。全部用锁定读：调用方的事务可能在拿锁之前
// 就做过读（比如建用户时先写了用户再分配角色），一致性快照早已建立，普通读看到的是拿锁之前的旧状态。
func (s *Service) loadActor(ctx context.Context, actor auth.Principal) (*actorView, error) {
	// 是不是超管也按库算，不沿用认证时的 actor.Super：请求认证之后、拿到锁之前，超管角色可能已被收回（D-045）
	v := &actorView{perms: map[uint64][]string{}, scopes: map[uint64]map[string]DataScope{}}
	if actor.UserID == 0 {
		// 主体端没有服务器命令的身份（admin 子命令只在平台程序里）：主体、主账号都要按库认定，不接受调用方直接给出
		if _, scoped := s.scopedPortal(actor.Portal); scoped {
			return nil, httpx.ErrTokenInvalid
		}
		// 不是登录用户，是服务器上的命令（admin create 等）：身份由调用方直接给出，没有可以重读的角色
		v.super = actor.Super
		return v, nil
	}
	// 账号在认证之后被停用、会话在认证之后被吊销的，在途的请求整个作废，回 401（D-045、D-046）。
	// 先锁操作人账号、再锁会话（D-089），和本人改密、下线其他设备的顺序一致。
	// 会话锁持有到业务事务结束：下线成功返回之后，这个会话已获准的管理写入也已经结束。
	org, err := s.actorOrg(actor)
	if err != nil {
		return nil, err
	}
	if s.portals != nil {
		if p, ok := s.portals.Get(actor.Portal); ok && p.Users != nil {
			var acc *portal.Account
			var err error
			if l, ok := p.Users.(portal.UserLocker); ok {
				acc, err = l.LockByID(ctx, actor.UserID)
			} else {
				acc, err = p.Users.FindByID(ctx, actor.UserID)
			}
			if errors.Is(err, portal.ErrAccountNotFound) {
				return nil, httpx.ErrTokenInvalid
			}
			if err != nil {
				return nil, err
			}
			if acc.Status != 1 {
				return nil, httpx.ErrTokenInvalid
			}
			// 主体端（D-063 第 2 条）：账号仍属于身份的主体、主体仍启用，否则在途请求整个作废。主体行用 withLock 锁住时
			// 读到的那一行（锁在手里，就是最新提交的状态：主账号是谁、是否停用）；没拿着这个主体的锁就是调用错了
			if p.Scoped {
				held, err := heldOrg(ctx, actor.Portal, org)
				if err != nil {
					return nil, err
				}
				if acc.OrgID != org {
					return nil, httpx.ErrTokenInvalid
				}
				if !held.row.Enabled() {
					return nil, httpx.ErrTokenInvalid
				}
				v.org = held.row
			}
		}
	}
	check := s.lockSession
	if check == nil {
		check = s.sessionActive
	}
	if check != nil && actor.SessionID != "" {
		ok, err := check(ctx, actor.Portal, actor.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, httpx.ErrTokenInvalid
		}
	}
	rv, err := s.viewOf(ctx, actor.Portal, org, actor.UserID, true)
	if err != nil {
		return nil, err
	}
	v.super, v.perms, v.scopes = rv.super, rv.perms, rv.scopes
	if v.org != nil {
		v.super = v.org.OwnerUserID == actor.UserID // 主体端的超管是锁内看到的主账号，不来自角色
	}
	// 路由守卫的要求也按锁内的状态再核一遍（D-048）：请求通过守卫之后、拿到锁之前权限被收回的，排队中的写操作同样作废
	if g, ok := ctx.Value(routeGuardKey{}).(routeGuard); ok {
		if (g.super && !v.super) || (g.perm != "" && !v.has(g.perm)) {
			return nil, httpx.ErrForbidden
		}
	}
	return v, nil
}

// viewOf 按 ctx 所在的数据库视图读用户启用的角色、每个角色的策略和范围。lock 为 true 时用锁定读（锁内的写操作用，
// 见 loadActor）；为 false 时是普通读，看到的是调用方事务快照里的状态（请求级的数据范围用，D-054）。
//
// org 是角色所属的主体（平台端 0）：只读这个主体的角色；主体端（org 非 0）不认超管角色，超管是主账号（D-063）。
func (s *Service) viewOf(ctx context.Context, portal string, org, userID uint64, lock bool) (*actorView, error) {
	v := &actorView{perms: map[uint64][]string{}, scopes: map[uint64]map[string]DataScope{}}
	rs, err := s.st.userRolesQ(ctx, portal, org, userID, lock)
	if err != nil {
		return nil, err
	}
	s.hook("view.roles")
	for _, r := range rs {
		if r.Status != 1 {
			continue
		}
		if r.IsSuper {
			if org == 0 {
				v.super = true
			}
			continue
		}
		codes, err := s.st.rolePermsQ(ctx, portal, r.ID, lock)
		if err != nil {
			return nil, err
		}
		v.perms[r.ID] = codes
		sc, err := s.st.roleScopesQ(ctx, r.ID, lock)
		if err != nil {
			return nil, err
		}
		v.scopes[r.ID] = sc
	}
	return v, nil
}

// has 报告操作人是否拥有权限码（与判定用的匹配规则一致：策略里的通配符按前缀匹配）。策略读的时候已经按端过滤。
func (v *actorView) has(perm string) bool {
	if v.super {
		return true
	}
	for id := range v.perms {
		if v.roleAllows(id, perm) {
			return true
		}
	}
	return false
}

func (v *actorView) roleAllows(roleID uint64, perm string) bool {
	for _, rule := range v.perms[roleID] {
		if policyMatch(perm, rule) {
			return true
		}
	}
	return false
}

// scopeOf 与 DataScopeOf 同一规则：拥有 perm 的启用角色里最宽的范围；超管是 all；没有角色拥有 perm 时是 self。
func (v *actorView) scopeOf(d DataResource, perm string) DataScope {
	if v.super {
		return ScopeAll
	}
	best, found := ScopeSelf, false
	for id := range v.perms {
		if !v.roleAllows(id, perm) {
			continue
		}
		sc := d.Default
		if stored, ok := v.scopes[id][d.Code]; ok && d.Allowed(stored) {
			sc = stored
		}
		if !found || sc.Wider(best) {
			best, found = sc, true
		}
	}
	return best
}

// policyMatch 是判定用的匹配规则（规范 §6.3 的 keyMatch）：规则不带 * 时要完全相等，带 * 时按 * 前面的部分做前缀匹配。
func policyMatch(perm, rule string) bool {
	i := strings.Index(rule, "*")
	if i < 0 {
		return perm == rule
	}
	if len(perm) > i {
		return perm[:i] == rule[:i]
	}
	return perm == rule[:i]
}

// IsLastSuper 报告该用户是否当前唯一"可用"的超管：其他拥有启用超管角色的用户里，没有一个账号处于启用状态。
// 停用或降级这样的用户必须拒绝（规范 §6.4）。配置了 Portals 时会查账号状态，停用的超管不算数；
// 没配置时退化为只看角色。调用方还需保证该用户本身处于启用状态才有意义。
//
// 主体端（D-063 第 2 条）：主账号是主体里唯一的超管，也不能在主体内被停用——对主账号返回 true，对别人返回 false。
// 必须在 WithActor 里调用：主体是锁住的那个主体，主账号按锁内读到的主体行算；目标不存在或属于别的主体回 404。
func (s *Service) IsLastSuper(ctx context.Context, portalCode string, userID uint64) (bool, error) {
	if !db.InTx(ctx) {
		return false, ErrNoTx
	}
	if _, ok := s.scopedPortal(portalCode); ok {
		held, err := heldOrg(ctx, portalCode, 0)
		if err != nil {
			return false, err
		}
		if err := s.checkInOrg(ctx, portalCode, held.org, userID); err != nil {
			return false, err
		}
		return held.row != nil && held.row.OwnerUserID == userID, nil
	}
	// 直接读库而不走授权快照，并且用锁定读：调用方在 WithSuperLock 的事务里，必须看到并发操作已提交的状态，
	// 而事务的快照可能在拿锁之前就建立了（D-043）
	own, err := s.st.userRolesLocked(ctx, portalCode, 0, userID)
	if err != nil {
		return false, err
	}
	super := false
	for _, r := range own {
		if r.IsSuper && r.Status == 1 {
			super = true
			break
		}
	}
	if !super {
		return false, nil
	}
	others, err := s.SuperUserIDs(ctx, portalCode)
	if err != nil {
		return false, err
	}
	var users portal.UserProvider
	if s.portals != nil {
		if p, ok := s.portals.Get(portalCode); ok {
			users = p.Users
		}
	}
	for _, id := range others {
		if id == userID {
			continue
		}
		if users == nil {
			return false, nil
		}
		acc, err := users.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, portal.ErrAccountNotFound) {
				continue
			}
			return false, err
		}
		if acc.Enabled() {
			return false, nil
		}
	}
	return true, nil
}

// SuperUserIDs 返回拥有启用超管角色的用户 ID（不检查用户自身状态，由 system 模块过滤）。
// 主体端返回 ctx 里身份所属主体的主账号（D-063）。
func (s *Service) SuperUserIDs(ctx context.Context, portal string) ([]uint64, error) {
	if ou, ok := s.scopedPortal(portal); ok {
		org, err := s.viewOrg(ctx, portal)
		if err != nil {
			return nil, err
		}
		o, err := ou.FindOrgByID(ctx, org)
		if err != nil || o.ID != org || o.OwnerUserID == 0 {
			return nil, ignoreOrgNotFound(err)
		}
		return []uint64{o.OwnerUserID}, nil
	}
	roles, err := s.st.roles(ctx, portal, 0)
	if err != nil {
		return nil, err
	}
	seen := map[uint64]struct{}{}
	var out []uint64
	for _, r := range roles {
		if !r.IsSuper || r.Status != 1 {
			continue
		}
		ids, err := s.st.usersWithRole(ctx, portal, r.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// ---- 权限树与清理 ----

// PermGroup 是授权界面的分组。
type PermGroup struct {
	Group string     `json:"group"`
	Perms []PermItem `json:"perms"`
}

// PermItem 是授权界面的一项。
type PermItem struct {
	Code      string `json:"code"`
	Name      string `json:"name"`
	Sensitive bool   `json:"sensitive"`
}

// PermTree 返回某端的权限码分组树。
func (s *Service) PermTree(portal string) []PermGroup {
	var out []PermGroup
	for _, p := range s.reg.Perms(portal) {
		if len(out) == 0 || out[len(out)-1].Group != p.Group {
			out = append(out, PermGroup{Group: p.Group})
		}
		g := &out[len(out)-1]
		g.Perms = append(g.Perms, PermItem{Code: p.Code, Name: p.Name, Sensitive: p.Sensitive})
	}
	return out
}

// Prune 删除策略里未注册的权限码（通配模式保留），返回删除条数。只管本进程注册了的端（D-061）。
func (s *Service) Prune(ctx context.Context) (int, error) {
	if db.InTx(ctx) {
		return 0, ErrInTx
	}
	rows, err := s.st.allPolicies(ctx)
	if err != nil {
		return 0, err
	}
	var ids []uint64
	for _, r := range rows {
		if strings.HasSuffix(r.V2, "*") || !s.servesPortal(r.V1) {
			continue
		}
		if !s.reg.Has(r.V1, r.V2) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := db.Tx(ctx, func(ctx context.Context) error {
		if err := s.st.deletePolicies(ctx, ids); err != nil {
			return err
		}
		s.holdPublish(ctx)
		return nil
	}); err != nil {
		return 0, err
	}
	return len(ids), s.publishErr()
}

// ---- 工具 ----

func validateRoleInput(in RoleInput) error {
	var fields []httpx.FieldError
	if l := len(in.Code); l < 2 || l > 64 || !validRoleCode(in.Code) {
		fields = append(fields, httpx.NewField("code", "rbac.role.code", "2-64 chars, lowercase letters, digits, '-' or '_', starting with a letter"))
	}
	if l := len([]rune(in.Name)); l < 1 || l > 64 {
		fields = append(fields, httpx.NewField("name", "common.lengthRange", "1-64 chars", "min", 1, "max", 64))
	} else if textx.HasInvisible(in.Name) {
		// 角色名拒绝 textx.HasInvisible 定义的控制和不可见字符（D-099），避免显示身份产生歧义
		fields = append(fields, httpx.NewField("name", "org.textChars", "must not contain control or invisible characters"))
	}
	if in.Status != 0 && in.Status != 1 {
		fields = append(fields, httpx.NewField("status", "common.status", "must be 0 or 1"))
	}
	if len([]rune(in.Remark)) > 255 {
		fields = append(fields, httpx.NewField("remark", "common.maxLength", "at most 255 chars", "max", 255))
	}
	if len(fields) > 0 {
		return httpx.ErrValidation.WithFields(fields...)
	}
	return nil
}

func validRoleCode(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func notFound(err error) error {
	if errors.Is(err, ErrRoleNotFound) {
		return httpx.ErrNotFound.WithCause(err)
	}
	return err
}

// samePortal 拒绝"在别的端上操作角色"：操作者的身份来自哪个端，就只能动哪个端的角色（规范 §6.7）。
// 端内路由传进来的 portal 本来就应等于 actor.Portal，这里是最后一道防线，挡的是模块代码把端传错。
func samePortal(actor auth.Principal, portal string) error {
	if actor.Portal != portal {
		return httpx.ErrForbidden.WithCause(fmt.Errorf("rbac: actor of portal %q cannot operate on portal %q", actor.Portal, portal))
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func dedupeIDs(in []uint64) []uint64 {
	seen := map[uint64]struct{}{}
	out := make([]uint64, 0, len(in))
	for _, id := range in {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
