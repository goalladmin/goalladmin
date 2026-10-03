package rbac

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
)

// casbin 只回答一个问题："这个角色在这个端有没有这个权限码"（规范 §6.3）。
// 不用 g：用户和角色的关系只存在 ga_user_role，判定时对用户的每个角色各 Enforce 一次。
const casbinModel = `
[request_definition]
r = sub, ptl, obj

[policy_definition]
p = sub, ptl, obj

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && r.ptl == p.ptl && keyMatch(r.obj, p.obj)
`

// rowsAdapter 是只读适配器：策略行由 Reload 在一个数据库快照里读好（和数据范围同一个快照，D-051），
// 这里只把它们装进 casbin 的模型。策略的写入由 store 在事务里完成，之后整体重载。
type rowsAdapter struct {
	rows []policyRule
}

func (a *rowsAdapter) LoadPolicy(m model.Model) error {
	for _, r := range a.rows {
		if err := persist.LoadPolicyArray([]string{r.PType, r.V0, r.V1, r.V2}, m); err != nil {
			return err
		}
	}
	return nil
}

var errReadOnly = errors.New("rbac: 策略只能通过 rbac.Service 修改")

func (a *rowsAdapter) SavePolicy(model.Model) error                              { return errReadOnly }
func (a *rowsAdapter) AddPolicy(string, string, []string) error                  { return errReadOnly }
func (a *rowsAdapter) RemovePolicy(string, string, []string) error               { return errReadOnly }
func (a *rowsAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return errReadOnly }

// enforcer 包一层 casbin。每次重载新建一个，发布后不再修改，判定之间只读。
type enforcer struct {
	mu     sync.RWMutex
	byRole map[policyKey]*casbin.Enforcer
}

type policyKey struct{ subject, portal string }

// newEnforcer 用读好的策略行建一个判定器。
func newEnforcer(rows []policyRule) (*enforcer, error) {
	return newEnforcerContext(context.Background(), rows)
}

func newEnforcerContext(ctx context.Context, rows []policyRule) (*enforcer, error) {
	groups := map[policyKey][]policyRule{}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := policyKey{row.V0, row.V1}
		groups[key] = append(groups[key], row)
	}
	x := &enforcer{byRole: make(map[policyKey]*casbin.Enforcer, len(groups))}
	for key, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := model.NewModelFromString(casbinModel)
		if err != nil {
			return nil, fmt.Errorf("rbac: model: %w", err)
		}
		e, err := casbin.NewEnforcer(m, &rowsAdapter{rows: group})
		if err != nil {
			return nil, fmt.Errorf("rbac: enforcer: %w", err)
		}
		e.EnableAutoSave(false)
		x.byRole[key] = e
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return x, nil
}

// allow 判定一个角色在某端是否拥有权限码。
func (x *enforcer) allow(roleID uint64, portal, perm string) bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	e := x.byRole[policyKey{roleSubject(roleID), portal}]
	if e == nil {
		return false
	}
	ok, err := e.Enforce(roleSubject(roleID), portal, perm)
	return err == nil && ok
}
