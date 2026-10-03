// Package scopetest 是主体隔离的测试模板（规范 §7.1，D-061）：每个主体端资源都要有隔离测试。
//
// 用法：在 repo 或 service 的测试里描述资源的读写操作，Run 造两个主体各一条数据，以主体 A 的身份
// 对主体 B 的那条逐一试列表、详情、修改、删除，断言全部看不到或"不存在"，并且 B 的数据原样不动；
// 再用没有主体的身份（平台端）调用，断言查询被拒绝、而不是退化成不加条件。
//
//	scopetest.Run(t, ctx, scopetest.Resource{
//		Portal: "merchant",
//		Seed:   func(ctx context.Context, orgID uint64) (uint64, error) { … },
//		List:   func(ctx context.Context) ([]uint64, error) { … },
//		Get:    func(ctx context.Context, id uint64) error { … },
//		Update: func(ctx context.Context, id uint64) error { … },
//		Delete: func(ctx context.Context, id uint64) error { … },
//		Snapshot: func(ctx context.Context, id uint64) (string, error) { … }, // 不带隔离地读出这一行，用来比对没被改动
//	})
//
// HTTP 层的隔离测试（拿 A 的令牌打 B 的 ID）由各端模块按路由表补齐；这里管的是 repo 和 service 这一层。
package scopetest

import (
	"context"
	"errors"
	"slices"

	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/auth"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/scope"
)

// 两个测试主体的 ID 和各自的操作人。
const (
	OrgA  uint64 = 900001
	OrgB  uint64 = 900002
	UserA uint64 = 910001
	UserB uint64 = 910002
)

// Ctx 返回带某个主体端身份的 ctx（在 parent 之上，parent 里的数据库句柄等保留）。orgID 为 0 表示没有主体的身份。
func Ctx(parent context.Context, portal string, orgID, userID uint64) context.Context {
	return auth.WithPrincipal(parent, auth.Principal{Portal: portal, OrgID: orgID, UserID: userID, Username: "scopetest"})
}

// Resource 描述一个主体端资源的操作。除 Portal、Seed、Snapshot 外，没有的操作留 nil。
type Resource struct {
	Portal string
	// Seed 以 ctx 里的身份（主体）建一条数据，返回它的 ID。
	Seed func(ctx context.Context, orgID uint64) (uint64, error)
	// List 返回 ctx 里的主体能看到的全部 ID。
	List func(ctx context.Context) ([]uint64, error)
	// Get、Update、Delete 对某个 ID 做一次读、改、删；别的主体的 ID 必须返回"不存在"的错误。
	Get    func(ctx context.Context, id uint64) error
	Update func(ctx context.Context, id uint64) error
	Delete func(ctx context.Context, id uint64) error
	// Snapshot 不带隔离地读出一行（例如整行序列化成字符串），用来确认别的主体的操作没有改动它；行不存在时返回错误。
	Snapshot func(ctx context.Context, id uint64) (string, error)
}

// IsNotFound 报告 err 是否表示"不存在"：gorm.ErrRecordNotFound 或 httpx.ErrNotFound。
func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, httpx.ErrNotFound)
}

// TB 是 Run 用到的 testing.TB 的子集（*testing.T 满足它）。Fatalf 之后不能再往下执行。
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Run 跑一遍隔离检查。ctx 里要有数据库句柄（db.TestContext）。
func Run(t TB, ctx context.Context, r Resource) {
	t.Helper()
	if r.Seed == nil || r.Snapshot == nil {
		t.Fatalf("scopetest: Seed 和 Snapshot 必填")
	}
	ctxA := Ctx(ctx, r.Portal, OrgA, UserA)
	ctxB := Ctx(ctx, r.Portal, OrgB, UserB)

	idA, err := r.Seed(ctxA, OrgA)
	if err != nil {
		t.Fatalf("scopetest: 给主体 A 造数据失败: %v", err)
	}
	idB, err := r.Seed(ctxB, OrgB)
	if err != nil {
		t.Fatalf("scopetest: 给主体 B 造数据失败: %v", err)
	}
	before, err := r.Snapshot(ctx, idB)
	if err != nil {
		t.Fatalf("scopetest: 读主体 B 的数据失败: %v", err)
	}

	if r.List != nil {
		ids, err := r.List(ctxA)
		if err != nil {
			t.Fatalf("scopetest: 主体 A 列表失败: %v", err)
		}
		if slices.Contains(ids, idB) {
			t.Errorf("scopetest: 主体 A 的列表里出现了主体 B 的数据（ID %d）", idB)
		}
		if !slices.Contains(ids, idA) {
			t.Errorf("scopetest: 主体 A 的列表里没有自己的数据（ID %d）：隔离条件可能写反了", idA)
		}
	}
	for _, op := range []struct {
		name string
		fn   func(context.Context, uint64) error
	}{{"详情", r.Get}, {"修改", r.Update}, {"删除", r.Delete}} {
		if op.fn == nil {
			continue
		}
		if err := op.fn(ctxA, idB); !IsNotFound(err) {
			t.Errorf("scopetest: 主体 A 对主体 B 的数据做%s，应得到不存在，实际: %v", op.name, err)
		}
		after, err := r.Snapshot(ctx, idB)
		if err != nil {
			t.Fatalf("scopetest: 主体 A %s之后，主体 B 的数据读不出来了: %v", op.name, err)
		}
		if after != before {
			t.Errorf("scopetest: 主体 A %s之后，主体 B 的数据被改动了\n之前: %s\n之后: %s", op.name, before, after)
		}
	}
	// 自己的数据照常能读：隔离不是"什么都查不到"
	if r.Get != nil {
		if err := r.Get(ctxA, idA); err != nil {
			t.Errorf("scopetest: 主体 A 读不到自己的数据: %v", err)
		}
	}

	// 没有主体的身份（平台端）和没有身份：查询被拒绝，而不是不加条件
	for name, c := range map[string]context.Context{"平台端身份": Ctx(ctx, "platform", 0, UserA), "没有身份": ctx} {
		if r.List != nil {
			if _, err := r.List(c); !errors.Is(err, scope.ErrNoOrg) {
				t.Errorf("scopetest: %s调用列表应被拒绝（scope.ErrNoOrg），实际: %v", name, err)
			}
		}
		if r.Get != nil {
			if err := r.Get(c, idB); !errors.Is(err, scope.ErrNoOrg) {
				t.Errorf("scopetest: %s调用详情应被拒绝（scope.ErrNoOrg），实际: %v", name, err)
			}
		}
	}
}
