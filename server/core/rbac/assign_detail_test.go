package rbac

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

// 规范 §13.2 第 152 条（D-069）：分配角色、重新启用角色被拒时，角色里是哪个权限码、哪个范围挡住了，只回给能看角色的
// 操作人（拥有声明了 RoleView 的权限码）；别人只得到 rbac.role.notAssignable 和角色 ID，响应里没有权限码、资源、范围。
// 拒不拒绝不变、什么都不写；完整的原因进服务端日志。
func TestOrgRBAC_152_RejectionDetailOnlyForRoleViewers(t *testing.T) {
	f := newOrgFix(t)
	s := f.s
	ctxO := f.as(f.ownerA)

	// 三种挡住的原因各一个角色，外加一个能分配的
	sens := f.role(t, f.ownerA, "sens", "shop:staff:admin")
	notOwned := f.role(t, f.ownerA, "notowned", "shop:thing:edit")
	wide := f.role(t, f.ownerA, "wide")
	require.NoError(t, s.GrantRole(ctxO, f.ownerA, shopP, wide.ID, []string{"shop:thing:list"}, map[string]DataScope{"shop:thing": ScopeAll}))
	fine := f.role(t, f.ownerA, "fine", "shop:thing:list")

	// 两个员工权限一样（能看东西，范围是默认的"仅本人"），差别只在 viewer 多一个"查看角色"
	blind := f.role(t, f.ownerA, "blind", "shop:thing:list")
	viewer := f.role(t, f.ownerA, "viewer", "shop:thing:list", "shop:role:list")
	require.NoError(t, s.AssignUserRoles(ctxO, f.ownerA, shopP, f.staffA.UserID, []uint64{blind.ID}))
	require.NoError(t, s.AssignUserRoles(ctxO, f.ownerA, shopP, f.staffA2.UserID, []uint64{viewer.ID}))
	f.exec(t, "INSERT INTO t_org_user (org_id, username, status) VALUES (?, 'target', 1)", f.orgA)
	var target uint64
	require.NoError(t, db.From(f.ctx).Raw("SELECT id FROM t_org_user WHERE org_id = ? AND username = 'target'", f.orgA).Scan(&target).Error)

	// 带细节的响应里才有的东西：权限码和资源（都以端的前缀开头）、参数里的权限码、资源、范围
	secrets := []string{"shop:", `"perm"`, `"resource"`, `"scope"`, `"yours"`}
	rolesOf := func() string {
		var ids []uint64
		require.NoError(t, db.From(f.ctx).Raw("SELECT role_id FROM ga_user_role WHERE portal = ? AND user_id = ? ORDER BY role_id", shopP, target).Scan(&ids).Error)
		b, _ := json.Marshal(ids)
		return string(b)
	}
	body := func(err error) (string, []httpx.FieldError) {
		t.Helper()
		var he *httpx.Error
		require.ErrorAs(t, err, &he)
		require.ErrorIs(t, err, httpx.ErrForbidden)
		b, e := json.Marshal(he.Fields)
		require.NoError(t, e)
		return string(b), he.Fields
	}

	cases := []struct {
		name string
		role *Role
		key  string
		has  []string // 能看角色的人看到的细节
	}{
		{"含敏感权限码", sens, "rbac.role.holdsSensitive", []string{"shop:staff:admin"}},
		{"含自己没有的权限码", notOwned, "rbac.role.holdsNotOwned", []string{"shop:thing:edit"}},
		{"范围比自己宽", wide, "rbac.role.widerScope", []string{"shop:thing", "sees all"}},
	}
	for _, c := range cases {
		before := rolesOf()
		// 没有查看角色权限：笼统的拒绝，只有角色 ID；原因在服务端日志里
		var logs bytes.Buffer
		ctxB := logx.WithLogger(f.as(f.staffA), slog.New(slog.NewTextHandler(&logs, nil)))
		js, fields := body(s.AssignUserRoles(ctxB, f.staffA, shopP, target, []uint64{c.role.ID}))
		require.Len(t, fields, 1, c.name)
		require.Equal(t, "rbac.role.notAssignable", fields[0].Key, c.name)
		require.Equal(t, "roleIds", fields[0].Field)
		require.Equal(t, map[string]any{"id": c.role.ID}, fields[0].Params, c.name)
		for _, secret := range secrets {
			require.NotContains(t, js, secret, "%s：响应里不该有 %s", c.name, secret)
		}
		require.Contains(t, logs.String(), c.key, "%s：原因进了服务端日志", c.name)
		for _, d := range c.has {
			require.Contains(t, logs.String(), d, c.name)
		}
		require.Contains(t, logs.String(), "level=WARN")
		// 有查看角色权限：和以前一样带细节
		js, fields = body(s.AssignUserRoles(f.as(f.staffA2), f.staffA2, shopP, target, []uint64{c.role.ID}))
		require.Len(t, fields, 1, c.name)
		require.Equal(t, c.key, fields[0].Key, c.name)
		for _, d := range c.has {
			require.Contains(t, js, d, c.name)
		}
		require.Equal(t, before, rolesOf(), "%s：被拒的什么都不写", c.name)
	}

	// 一次带几个：每个被拒的角色各一条，能分配的不在里面；顺序和传入的一致
	js, fields := body(s.AssignUserRoles(f.as(f.staffA), f.staffA, shopP, target, []uint64{fine.ID, sens.ID, notOwned.ID}))
	require.Len(t, fields, 2)
	require.Equal(t, []any{"rbac.role.notAssignable", sens.ID, "rbac.role.notAssignable", notOwned.ID},
		[]any{fields[0].Key, fields[0].Params["id"], fields[1].Key, fields[1].Params["id"]})
	require.NotContains(t, js, "shop:")
	require.Equal(t, "null", rolesOf(), "一个被拒，整份都不写")
	// 能分配的照常
	require.NoError(t, s.AssignUserRoles(f.as(f.staffA), f.staffA, shopP, target, []uint64{fine.ID}))

	// 重新启用角色走的是同一个检查：同样的区别
	off := RoleInput{Code: "sens", Name: "sens", Status: 0}
	on := RoleInput{Code: "sens", Name: "sens", Status: 1}
	_, err := s.UpdateRole(ctxO, f.ownerA, shopP, sens.ID, off)
	require.NoError(t, err)
	_, err = s.UpdateRole(f.as(f.staffA), f.staffA, shopP, sens.ID, on)
	js, fields = body(err)
	require.Equal(t, "rbac.role.notAssignable", fields[0].Key)
	require.NotContains(t, js, "shop:")
	_, err = s.UpdateRole(f.as(f.staffA2), f.staffA2, shopP, sens.ID, on)
	js, fields = body(err)
	require.Equal(t, "rbac.role.holdsSensitive", fields[0].Key)
	require.Contains(t, js, "shop:staff:admin")
	got, err := s.Role(ctxO, shopP, sens.ID)
	require.NoError(t, err)
	require.Equal(t, 0, got.Status, "被拒的没有启用")

	// 主账号（主体内的超管）不受这条检查约束
	require.NoError(t, s.AssignUserRoles(ctxO, f.ownerA, shopP, target, []uint64{notOwned.ID, wide.ID}))
}

// 没有声明 RoleView 的端：非超管一律按"看不了角色"算（偏保守的一边）。
func TestRegistry_RoleViewPerms(t *testing.T) {
	reg := NewRegistry()
	require.NoError(t, reg.AddPerms("m", []Perm{
		{Code: "a:role:list", Name: "x", Portal: "p1", Group: "g", RoleView: true},
		{Code: "a:role:audit", Name: "x", Portal: "p1", Group: "g", RoleView: true},
		{Code: "a:user:list", Name: "x", Portal: "p1", Group: "g"},
		{Code: "b:role:list", Name: "x", Portal: "p2", Group: "g"},
	}))
	require.Equal(t, []string{"a:role:audit", "a:role:list"}, reg.RoleViewPerms("p1"))
	require.Empty(t, reg.RoleViewPerms("p2"))
	require.Empty(t, reg.RoleViewPerms("nope"))

	s := &Service{reg: reg}
	view := &actorView{perms: map[uint64][]string{1: {"b:role:list", "a:user:list"}}}
	require.False(t, s.canViewRoles(view, "p2"), "这个端没有声明：有名字像的权限码也不算")
	require.False(t, s.canViewRoles(view, "p1"))
	view.perms[2] = []string{"a:role:audit"}
	require.True(t, s.canViewRoles(view, "p1"))
	require.True(t, s.canViewRoles(&actorView{super: true}, "p2"), "超管总是能看")
}
