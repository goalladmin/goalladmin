package dict

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/migrations"
)

// 规范 §13.2 第 99 条：一本字典的项数和层数有上限（D-055）；库里的历史数据更深时，拼树到上限为止，不无限递归。
func TestDict_99_ItemCountAndDepthLimits(t *testing.T) {
	gdb := db.OpenTestDB(t)
	ctx := db.TestContext(t, gdb)
	_, err := db.MigrateUp(ctx, gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	s := NewService(Options{PortalOK: platformOnly})
	d, err := s.CreateDict(ctx, DictInput{Code: "biz_tree", Portal: "platform", Name: "tree"})
	require.NoError(t, err)

	// 层数：顶层算 1，第 MaxDepth+1 层拒绝
	parent := uint64(0)
	for i := 1; i <= MaxDepth; i++ {
		it, err := s.CreateItem(ctx, d.ID, ItemInput{ParentID: parent, Value: fmt.Sprintf("d%d", i), Label: "x"})
		require.NoError(t, err, i)
		parent = it.ID
	}
	_, err = s.CreateItem(ctx, d.ID, ItemInput{ParentID: parent, Value: "too-deep", Label: "x"})
	require.ErrorIs(t, err, httpx.ErrValidation)
	var he *httpx.Error
	require.ErrorAs(t, err, &he)
	require.Contains(t, fmt.Sprint(he.Fields), "dict.tooDeep")

	// 库里的历史数据比上限深（直接改库写进去的）：拼树到上限为止
	deepParent := parent
	for i := 0; i < 3; i++ {
		require.NoError(t, gdb.Exec("INSERT INTO ga_dict_item (dict_id, parent_id, value, label, label_i18n, status, created_at, updated_at) VALUES (?, ?, ?, 'x', '{}', 1, NOW(3), NOW(3))",
			d.ID, deepParent, fmt.Sprintf("old%d", i)).Error)
		require.NoError(t, gdb.Raw("SELECT id FROM ga_dict_item WHERE dict_id = ? AND value = ?", d.ID, fmt.Sprintf("old%d", i)).Scan(&deepParent).Error)
	}
	detail, err := s.GetDict(ctx, d.ID)
	require.NoError(t, err)
	depthOf := func(items []ItemInfo) int {
		var f func([]ItemInfo) int
		f = func(items []ItemInfo) int {
			best := 0
			for _, it := range items {
				best = max(best, 1+f(it.Children))
			}
			return best
		}
		return f(items)
	}
	require.Equal(t, MaxDepth, depthOf(detail.Items))

	// 项数：到 MaxItems 为止
	var rows []map[string]any
	for i := 0; i < MaxItems-MaxDepth; i++ {
		rows = append(rows, map[string]any{"dict_id": d.ID, "parent_id": 0, "value": fmt.Sprintf("v%d", i), "label": "x", "label_i18n": "{}", "status": 1,
			"created_at": time.Now().UTC(), "updated_at": time.Now().UTC()})
	}
	require.NoError(t, gdb.Table("ga_dict_item").CreateInBatches(rows, 200).Error)
	_, err = s.CreateItem(ctx, d.ID, ItemInput{Value: "one-more", Label: "x"})
	require.ErrorIs(t, err, httpx.ErrValidation)
	he = nil
	require.ErrorAs(t, err, &he)
	require.Contains(t, fmt.Sprint(he.Fields), "dict.tooManyItems")
}

// 代码声明的字典同样受上限约束，启动时就拒绝；正好到上限的合法（D-056）。
func TestDict_99_DeclLimits(t *testing.T) {
	levels := func(n int) Dict { // 共 n 层：顶层一项，往下每层一项
		d := goodDict()
		cur := &d.Items[0]
		for i := 1; i < n; i++ {
			cur.Children = []Item{{Value: fmt.Sprintf("%d", 100+i), Label: "x"}}
			cur = &cur.Children[0]
		}
		return d
	}
	require.NoError(t, ValidateDecls([]Decl{{Module: "demo", Dict: levels(MaxDepth)}}, platformOnly), "正好 MaxDepth 层")
	require.ErrorContains(t, ValidateDecls([]Decl{{Module: "demo", Dict: levels(MaxDepth + 1)}}, platformOnly), "层级")

	items := func(n int) Dict {
		d := goodDict()
		d.Items = nil
		for i := 0; i < n; i++ {
			d.Items = append(d.Items, Item{Value: fmt.Sprintf("%d", i), Label: "x"})
		}
		return d
	}
	require.NoError(t, ValidateDecls([]Decl{{Module: "demo", Dict: items(MaxItems)}}, platformOnly), "正好 MaxItems 项")
	require.ErrorContains(t, ValidateDecls([]Decl{{Module: "demo", Dict: items(MaxItems + 1)}}, platformOnly), "项超过")
}
