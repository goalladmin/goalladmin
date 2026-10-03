package rbac

import (
	"context"
	"slices"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// CheckUserDepartment 检查目标账号在新部门持有的相对部门角色。调用方须持管理事务锁。
func (s *Service) CheckUserDepartment(ctx context.Context, actor auth.Principal, userID, deptID uint64) error {
	if !db.InTx(ctx) {
		return ErrNoTx
	}
	me, err := s.loadActor(ctx, actor)
	if err != nil || me.super {
		return err
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	roles, err := s.st.userRolesLocked(ctx, actor.Portal, org, userID)
	if err != nil {
		return err
	}
	for _, role := range roles {
		if err := s.checkRoleDepartment(ctx, actor, me, role.ID, deptID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkRoleDepartment(ctx context.Context, actor auth.Principal, me *actorView, roleID, deptID uint64) error {
	if deptID == 0 || s.portals == nil {
		return nil
	}
	if pt, ok := s.portals.Get(actor.Portal); !ok || pt.Dept == nil {
		return nil
	}
	codes, err := s.st.rolePermsLocked(ctx, actor.Portal, roleID)
	if err != nil {
		return err
	}
	scopes, err := s.st.roleScopesLocked(ctx, roleID)
	if err != nil {
		return err
	}
	return s.checkDepartmentCoverage(ctx, actor, me, codes, scopes, deptID)
}

// checkRoleManageable 检查会改变整个角色的操作所需资格，含现有持有人的实际集合（D-120）。
func (s *Service) checkRoleManageable(ctx context.Context, actor auth.Principal, me *actorView, roleID uint64) error {
	if err := s.checkAssignable(ctx, actor, me, actor.Portal, []uint64{roleID}); err != nil {
		return err
	}
	if s.portals == nil {
		return nil
	}
	if pt, ok := s.portals.Get(actor.Portal); !ok || pt.Dept == nil {
		return nil
	}
	codes, err := s.st.rolePermsLocked(ctx, actor.Portal, roleID)
	if err != nil {
		return err
	}
	scopes, err := s.st.roleScopesLocked(ctx, roleID)
	if err != nil {
		return err
	}
	return s.checkRoleMembersDepartment(ctx, actor, me, roleID, codes, scopes)
}

// checkRoleMembersDepartment 检查给定角色状态在全部现有持有人上的覆盖；codes 可仅列本次扩大的权限。
func (s *Service) checkRoleMembersDepartment(ctx context.Context, actor auth.Principal, me *actorView, roleID uint64, codes []string, scopes map[string]DataScope) error {
	if s.portals == nil {
		return nil
	}
	pt, ok := s.portals.Get(actor.Portal)
	if !ok || pt.Dept == nil {
		return nil
	}
	// 没有相对部门权限时无需读取持有人；普通元数据和无关权限变更不受它影响。
	relative := false
	for _, d := range s.reg.DataResources(actor.Portal) {
		sc := roleResourceScope(d, scopes)
		if sc != ScopeDept && sc != ScopeDeptTree {
			continue
		}
		for _, perm := range d.Perms {
			if slices.ContainsFunc(codes, func(rule string) bool { return policyMatch(perm, rule) }) {
				relative = true
				break
			}
		}
	}
	if !relative {
		return nil
	}
	org, err := s.actorOrg(actor)
	if err != nil {
		return err
	}
	ids, err := s.st.roleMembersLocked(ctx, actor.Portal, org, roleID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		deptID, err := pt.Dept.UserDept(ctx, id)
		if err != nil {
			return err
		}
		if err := s.checkDepartmentCoverage(ctx, actor, me, codes, scopes, deptID); err != nil {
			return err
		}
	}
	return nil
}

func roleResourceScope(d DataResource, scopes map[string]DataScope) DataScope {
	if stored, ok := scopes[d.Code]; ok && d.Allowed(stored) {
		return stored
	}
	return d.Default
}

// checkDepartmentCoverage 展开目标部门的相对范围，与操作人在对应权限上的实际集合比较。
func (s *Service) checkDepartmentCoverage(ctx context.Context, actor auth.Principal, me *actorView, codes []string, scopes map[string]DataScope, deptID uint64) error {
	if deptID == 0 || s.portals == nil {
		return nil // 相对部门范围在未分配部门时降为仅本人。
	}
	pt, ok := s.portals.Get(actor.Portal)
	if !ok || pt.Dept == nil {
		return nil
	}
	for _, d := range s.reg.DataResources(actor.Portal) {
		sc := roleResourceScope(d, scopes)
		if sc != ScopeDept && sc != ScopeDeptTree {
			continue
		}
		ids := []uint64{deptID}
		if sc == ScopeDeptTree {
			var err error
			ids, err = pt.Dept.DeptSubtree(ctx, deptID)
			if err != nil {
				return err
			}
		}
		for _, perm := range d.Perms {
			if !slices.ContainsFunc(codes, func(rule string) bool { return policyMatch(perm, rule) }) {
				continue
			}
			mine, err := s.filterFor(ctx, actor, me.scopeOf(d, perm))
			if err != nil {
				return err
			}
			for _, id := range ids {
				if !mine.AllowsDept(id) {
					return httpx.ErrForbidden.WithFields(httpx.NewField("deptId", "rbac.role.widerScope", "the department would give the account a wider data scope"))
				}
			}
		}
	}
	return nil
}
