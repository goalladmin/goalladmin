package rbac

// 主体端（D-061、D-063）：操作落在哪个主体、主体行锁、主账号。
//
// 平台端的主体恒为 0，下面这些函数在平台端都退化成原来的行为：角色按 (端, 0) 查，锁超管角色行，超管来自超管角色。

import (
	"context"
	"errors"
	"fmt"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/portal"
	"github.com/goalladmin/goalladmin/server/core/scope"
)

// scopedPortal 报告 code 是不是主体端，是的话一并返回它的用户来源。没有端注册表（只在测试里）、
// 或者这个进程没注册这个端时按平台端算：那样的端上主体恒为 0，看不到任何主体的角色。
func (s *Service) scopedPortal(code string) (portal.OrgUserProvider, bool) {
	if s.portals == nil {
		return nil, false
	}
	p, ok := s.portals.Get(code)
	if !ok || !p.Scoped {
		return nil, false
	}
	ou, ok := p.Users.(portal.OrgUserProvider) // 注册时已经校验过
	return ou, ok
}

// actorOrg 返回操作人所在的主体：主体端取 actor.OrgID（来自会话行），必须非 0；平台端恒为 0，
// 平台端的身份带了主体说明身份是拼出来的，拒绝。不接受调用方另外传入主体（D-063 第 1 条）。
func (s *Service) actorOrg(actor auth.Principal) (uint64, error) {
	if _, ok := s.scopedPortal(actor.Portal); ok {
		if actor.OrgID == 0 {
			return 0, scope.ErrNoOrg
		}
		return actor.OrgID, nil
	}
	if actor.OrgID != 0 {
		return 0, httpx.ErrForbidden.WithCause(fmt.Errorf("rbac: 端 %s 不是主体端，身份不应带主体", actor.Portal))
	}
	return 0, nil
}

// viewOrg 是只读方法（没有操作人参数）的主体：平台端恒为 0；主体端取 ctx 里的身份，规则同 scope.MustOrg——
// 身份缺失、属于别的端、没有主体时报 scope.ErrNoOrg，绝不退化成"不加条件"。
func (s *Service) viewOrg(ctx context.Context, portalCode string) (uint64, error) {
	if _, ok := s.scopedPortal(portalCode); !ok {
		return 0, nil
	}
	p, ok := auth.FromCtx(ctx)
	if !ok || p.Portal != portalCode || p.OrgID == 0 {
		return 0, scope.ErrNoOrg
	}
	return p.OrgID, nil
}

// lockOrg 在当前事务里给主体行加排他锁，返回它的最新提交状态（portal.OrgLocker，D-063 第 4 条）；主体不存在时返回 nil。
// 只在 withLock 里调用：主体行必须是事务里的第一把锁。
func (s *Service) lockOrg(ctx context.Context, portalCode string, org uint64) (*portal.Org, error) {
	ou, ok := s.scopedPortal(portalCode)
	if !ok {
		return nil, fmt.Errorf("rbac: 端 %s 不是主体端", portalCode)
	}
	l, ok := ou.(portal.OrgLocker)
	if !ok {
		// 注册时已经要求主体端实现 OrgLocker；走到这里是装配错了，不能在没有锁的情况下继续
		return nil, fmt.Errorf("rbac: 主体端 %s 的用户来源没有实现 portal.OrgLocker", portalCode)
	}
	o, err := l.LockOrgByID(ctx, org)
	if errors.Is(err, portal.ErrOrgNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if o.ID != org {
		return nil, nil
	}
	return o, nil
}

// withLock 在一个事务里先拿写操作的串行化锁，再执行 fn（D-063 第 4 条）：
//   - 平台端：锁超管角色行（规范 §6.4），事务是默认的可重复读；
//   - 主体端：锁主体行（排他），同一主体内的写操作排队、别的主体不受影响。事务用 READ COMMITTED：
//     主体行锁已经把同一主体的写操作串起来了，可重复读下的间隙锁只会让不同主体在角色、授权、成员关系这几张共用的表里
//     互相等待甚至死锁（删一段再插入、锁定读一段，都会锁住行之间的间隙）。锁住的主体行放进 ctx（heldOrg），
//     锁内的认定直接用它，不再补拿主体锁；操作人账号和会话锁随后取得（D-089）。
//
// 平台端 ctx 已在事务里时加入该事务。主体端只有一种嵌套是允许的：已经在 WithActor（或 rbac 的写方法）里、
// 拿着同一个主体的行锁——这时直接执行 fn。别的事务里调用一律报 errNotInOrgLock：外层事务是可重复读（间隙锁又回来了），
// 而且它可能已经锁了账号行、会话行，再去锁主体行就把"主体行 → 账号行 → 会话行"的顺序倒过来了。
// 要在一个事务里做几步（建账号再分配角色），把它们都放进 WithActor。
func (s *Service) withLock(ctx context.Context, portalCode string, org uint64, fn func(ctx context.Context) error) error {
	return s.withWriteAdmission(ctx, fmt.Sprintf("manager:%s/%d", portalCode, org), s.managerWrites, func(ctx context.Context) error {
		return s.withLockAdmitted(ctx, portalCode, org, fn)
	})
}

func (s *Service) withLockAdmitted(ctx context.Context, portalCode string, org uint64, fn func(ctx context.Context) error) error {
	if _, ok := s.scopedPortal(portalCode); ok {
		if org == 0 {
			return scope.ErrNoOrg
		}
		if db.InTx(ctx) {
			if _, err := heldOrg(ctx, portalCode, org); err != nil {
				return err
			}
			return fn(ctx)
		}
		return db.TxReadCommitted(ctx, func(ctx context.Context) error {
			row, err := s.lockOrg(ctx, portalCode, org)
			if err != nil {
				return err
			}
			return fn(context.WithValue(ctx, orgLockKey{}, &orgLock{portal: portalCode, org: org, row: row}))
		})
	}
	return db.Tx(ctx, func(ctx context.Context) error {
		if err := s.st.lockSuperRole(ctx, portalCode); err != nil {
			return err
		}
		return fn(ctx)
	})
}

// orgLockKey 是 withLock 在主体端放进 ctx 的"已经锁住的主体行"。
type orgLockKey struct{}

type orgLock struct {
	portal string
	org    uint64
	row    *portal.Org // 加锁时读到的最新提交状态；锁在手里，事务结束前别人改不了；主体不存在时为 nil
}

// errNotInOrgLock：主体端的锁内操作（loadActor、AllowedLocked、DataFilterLocked、CheckEnableUser、IsLastSuper）
// 没在 WithActor（或 rbac 的写方法）里调用。它们不补拿主体锁：在别的锁里（比如 WithSelf 已经锁了账号行）自己去锁主体行，
// 就把"主体行 → 账号行"的加锁顺序倒过来了。这是调用方的编程错误，请求以 500 终止。
var errNotInOrgLock = errors.New("rbac: 主体端的锁内操作必须在 WithActor 里调用（没有拿着这个主体的行锁）")

// heldOrg 返回 ctx 里已经锁住的主体行；没有、或者锁的不是这个端的这个主体时报 errNotInOrgLock。
func heldOrg(ctx context.Context, portalCode string, org uint64) (*orgLock, error) {
	l, ok := ctx.Value(orgLockKey{}).(*orgLock)
	if !ok || l == nil || l.portal != portalCode || (org != 0 && l.org != org) {
		return nil, errNotInOrgLock
	}
	return l, nil
}

// accountOrg 读账号所属主体的当前状态（普通读）：账号不存在、主体不存在时返回 nil。
func accountOrg(ctx context.Context, ou portal.OrgUserProvider, userID uint64) (*portal.Account, *portal.Org, error) {
	acc, err := ou.FindByID(ctx, userID)
	if errors.Is(err, portal.ErrAccountNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if acc.OrgID == 0 {
		return acc, nil, nil
	}
	o, err := ou.FindOrgByID(ctx, acc.OrgID)
	if errors.Is(err, portal.ErrOrgNotFound) {
		return acc, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if o.ID != acc.OrgID {
		return acc, nil, nil
	}
	return acc, o, nil
}

// isOwner 报告账号是不是它所属主体的主账号（普通读，HoldsSuperRole 用：判断受不受保护，宁可多保护）。
func isOwner(ctx context.Context, ou portal.OrgUserProvider, userID uint64) (bool, error) {
	_, o, err := accountOrg(ctx, ou, userID)
	if err != nil {
		return false, err
	}
	return o != nil && o.OwnerUserID == userID, nil
}

// checkInOrg 确认账号属于主体 org：账号不存在或属于别的主体都回 404（不暴露这个 ID 在别的主体里存在，规范 §7.1）。
func (s *Service) checkInOrg(ctx context.Context, portalCode string, org, userID uint64) error {
	ou, ok := s.scopedPortal(portalCode)
	if !ok {
		return fmt.Errorf("rbac: 端 %s 不是主体端", portalCode)
	}
	acc, err := ou.FindByID(ctx, userID)
	if errors.Is(err, portal.ErrAccountNotFound) {
		return httpx.ErrNotFound.WithCause(err)
	}
	if err != nil {
		return err
	}
	if acc.OrgID != org {
		return httpx.ErrNotFound
	}
	return nil
}

// ignoreOrgNotFound 把"主体不存在"当成没有结果。
func ignoreOrgNotFound(err error) error {
	if errors.Is(err, portal.ErrOrgNotFound) {
		return nil
	}
	return err
}
