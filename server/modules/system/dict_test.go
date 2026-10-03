package system_test

// 字典的反向测试（docs/decisions.md D-023）：代码声明的值不能在后台改掉、后台改过的显示不被同步覆盖、
// 读取接口以端为界、写入立即可见。用真实的 system 模块、真实 MySQL 跑完整 HTTP 链路。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/goalladmin/goalladmin/server/core/app"
	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/dict"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
	"github.com/goalladmin/goalladmin/server/core/rbac"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// demoModule 是只为测试声明字典的模块。
type demoModule struct{ dicts []dict.Dict }

func (m *demoModule) Name() string                { return "demo" }
func (m *demoModule) Init(*app.Deps) error        { return nil }
func (m *demoModule) Perms() []rbac.Perm          { return nil }
func (m *demoModule) Menus() []rbac.MenuNode      { return nil }
func (m *demoModule) Routes(*app.Router)          {}
func (m *demoModule) Start(context.Context) error { return nil }
func (m *demoModule) Stop(context.Context) error  { return nil }
func (m *demoModule) Dicts() []dict.Dict          { return m.dicts }

func en(s string) map[string]string { return map[string]string{"en-US": s} }

// demoDicts 返回测试用的声明；每次调用返回新切片，测试可以随意修改。
func demoDicts() []dict.Dict {
	return []dict.Dict{
		{
			Code: "demo.order_status", Portal: "platform", Name: "订单状态", NameI18n: en("Order status"), ValueType: dict.Int,
			Items: []dict.Item{
				{Value: "1", Label: "待支付", LabelI18n: en("Unpaid"), Color: "warning", Sort: 10},
				{Value: "2", Label: "已支付", LabelI18n: en("Paid"), Color: "success", Sort: 20},
				{Value: "3", Label: "已退款", LabelI18n: en("Refunded"), Color: "info", Sort: 30},
			},
		},
		{
			Code: "demo.region", Portal: dict.AllPortals, Name: "地区",
			Items: []dict.Item{
				{Value: "cn", Label: "中国", Children: []dict.Item{{Value: "cn-bj", Label: "北京"}, {Value: "cn-sh", Label: "上海"}}},
			},
		},
	}
}

// newDictApp 在同一个库上建一个应用实例；同一个 gdb 再调用一次就等于"改了代码之后重启"。
func newDictApp(t *testing.T, gdb *gorm.DB, dicts []dict.Dict) *app.App {
	t.Helper()
	cfg := conf.Default()
	cfg.Log.Level = "error"
	p := cfg.Portals[conf.DefaultPortalCode]
	p.JWTSecret = "platform-test-secret-0123456789abcdef0123456789"
	cfg.Portals[conf.DefaultPortalCode] = p
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)), app.WithPasswordHashParams(64, 1))
	require.NoError(t, err)
	a.Register(system.Module(), &demoModule{dicts: dicts})
	require.NoError(t, a.Setup())
	return a
}

func newDictFixture(t *testing.T) *fixture {
	t.Helper()
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	return &fixture{t: t, app: newDictApp(t, gdb, demoDicts()), gdb: gdb}
}

// restart 用新的声明在同一个库上重建应用；已有的令牌仍然有效（会话在库里、密钥不变）。
func (f *fixture) restart(dicts []dict.Dict) { f.app = newDictApp(f.t, f.gdb, dicts) }

// readDicts 以某种语言调用读取接口，返回 编码 → 字典。
func (f *fixture) readDicts(token, codes, lang string) (int, int, map[string]any) {
	f.t.Helper()
	req := httptest.NewRequest("GET", base+"/dicts?codes="+codes, nil)
	req.RemoteAddr = "203.0.113.10:5000"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	r := resp{rec: rec}
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &r.env), rec.Body.String())
	return rec.Code, r.env.Code, r.data()
}

// dictID 查字典的 ID。
func (f *fixture) dictID(code string) uint64 {
	f.t.Helper()
	var id uint64
	require.NoError(f.t, f.gdb.Raw("SELECT id FROM ga_dict WHERE code = ?", code).Scan(&id).Error)
	require.NotZero(f.t, id, code)
	return id
}

func (f *fixture) itemID(code, value string) uint64 {
	f.t.Helper()
	var id uint64
	require.NoError(f.t, f.gdb.Raw("SELECT i.id FROM ga_dict_item i JOIN ga_dict d ON d.id = i.dict_id WHERE d.code = ? AND i.value = ?", code, value).Scan(&id).Error)
	require.NotZero(f.t, id, code+"/"+value)
	return id
}

func items(v any) []map[string]any {
	raw, _ := v.(map[string]any)["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		out = append(out, x.(map[string]any))
	}
	return out
}

// ============ 读取：值按类型输出、按语言选文字、层级、未知编码略过 ============

func TestDict_ReadDeclaredDicts(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")

	status, code, data := f.readDicts(admin, "demo.order_status,demo.region,nope,demo.order_status", "")
	require.Equal(t, 200, status)
	require.Equal(t, 0, code)
	require.Len(t, data, 2, "未知编码略过、重复编码只算一次")

	os := data["demo.order_status"].(map[string]any)
	require.Equal(t, "订单状态", os["name"])
	require.Equal(t, "int", os["valueType"])
	its := items(os)
	require.Len(t, its, 3)
	require.Equal(t, float64(1), its[0]["value"], "int 字典的值输出为 JSON 数字")
	require.Equal(t, "待支付", its[0]["label"])
	require.Equal(t, "warning", its[0]["color"])
	require.EqualValues(t, 1, its[0]["status"])

	_, _, data = f.readDicts(admin, "demo.order_status,demo.region", "en-US,en;q=0.9")
	require.Equal(t, "Order status", data["demo.order_status"].(map[string]any)["name"])
	require.Equal(t, "Paid", items(data["demo.order_status"])[1]["label"])
	region := items(data["demo.region"])
	require.Equal(t, "cn", region[0]["value"], "string 字典的值输出为 JSON 字符串")
	require.Equal(t, "中国", region[0]["label"], "没有英文时回落默认文字")
	children := region[0]["children"].([]any)
	require.Len(t, children, 2)
	require.Equal(t, "cn-bj", children[0].(map[string]any)["value"])

	// 编码按字节匹配：大小写变体、格式不对的编码都当作不存在（不会绕开缓存失效，也不查库）
	_, _, data = f.readDicts(admin, "DEMO.ORDER_STATUS,demo..x,"+strings.Repeat("a", 65), "")
	require.Empty(t, data)

	// 必须登录；编码个数有上限
	st, c, _ := f.readDicts("", "demo.region", "")
	require.Equal(t, 401, st)
	require.Equal(t, httpx.CodeTokenInvalid, c)
	_, c, _ = f.readDicts(admin, "", "")
	require.Equal(t, httpx.CodeValidation, c)
	many := make([]string, 51)
	for i := range many {
		many[i] = fmt.Sprintf("x%d", i)
	}
	_, c, _ = f.readDicts(admin, strings.Join(many, ","), "")
	require.Equal(t, httpx.CodeValidation, c)
}

// ============ 代码声明的字典：值归代码所有，后台改不了 ============

func TestDict_DeclaredValuesCannotBeChangedByAdmin(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")
	did := f.dictID("demo.order_status")
	paid := f.itemID("demo.order_status", "2")

	r := f.do(admin, "PUT", fmt.Sprintf("/system/dicts/%d", did), gin.H{"name": "改名", "portal": "platform"})
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "改代码字典本身: %s", r.rec.Body.String())
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/dicts/%d", did), nil)
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "删代码字典")
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", did), gin.H{"value": "9", "label": "冒充"})
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "往代码字典里加项")
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/dicts/%d/items/%d", did, paid), nil)
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "删代码声明的项")
	r = f.do(admin, "PUT", fmt.Sprintf("/system/dicts/%d/items/%d", did, paid), gin.H{"value": "20", "label": "已支付"})
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "改代码声明的项的值")
	// 用别的字典的 ID 访问这一项：视为不存在
	region := f.dictID("demo.region")
	r = f.do(admin, "PUT", fmt.Sprintf("/system/dicts/%d/items/%d", region, paid), gin.H{"value": "2", "label": "x"})
	require.Equal(t, httpx.CodeNotFound, r.env.Code, "项不属于这本字典")

	// 库里原封不动
	var row struct {
		Name   string
		Source string
	}
	require.NoError(t, f.gdb.Raw("SELECT name, source FROM ga_dict WHERE id = ?", did).Scan(&row).Error)
	require.Equal(t, "订单状态", row.Name)
	require.Equal(t, "code", row.Source)
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_dict_item WHERE dict_id = ?", did).Scan(&n).Error)
	require.EqualValues(t, 3, n)
	var val string
	require.NoError(t, f.gdb.Raw("SELECT value FROM ga_dict_item WHERE id = ?", paid).Scan(&val).Error)
	require.Equal(t, "2", val)
}

// ============ 显示可以改；改过的不被同步覆盖，可以恢复默认 ============

func TestDict_OverrideSurvivesRestartAndCanBeReset(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")
	did := f.dictID("demo.order_status")
	paid := f.itemID("demo.order_status", "2")

	// 改显示文字、英文、颜色，并停用
	r := f.do(admin, "PUT", fmt.Sprintf("/system/dicts/%d/items/%d", did, paid),
		gin.H{"value": "2", "label": "付款成功", "labelI18n": en("Payment received"), "color": "#00aa00", "sort": 20, "status": 0})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, true, r.data()["overridden"])
	require.Equal(t, true, r.data()["locked"])

	// 立即可见（缓存在提交后清掉），停用的项仍返回、状态为 0
	_, _, data := f.readDicts(admin, "demo.order_status", "")
	its := items(data["demo.order_status"])
	require.Equal(t, "付款成功", its[1]["label"])
	require.EqualValues(t, 0, its[1]["status"])
	ok, err := f.app.Deps().Dict.Has(f.app.Context(context.Background()), "demo.order_status", "2")
	require.NoError(t, err)
	require.False(t, ok, "停用的值不再通过服务端校验")

	// 改了代码里的文字后重启：改过的项保持后台的文字，没改过的项跟着代码走；启用状态不被同步改动
	next := demoDicts()
	next[0].Items[1].Label = "已付款"
	next[0].Items[0].Label = "等待支付"
	f.restart(next)
	_, _, data = f.readDicts(admin, "demo.order_status", "")
	its = items(data["demo.order_status"])
	require.Equal(t, "等待支付", its[0]["label"])
	require.Equal(t, "付款成功", its[1]["label"])
	require.EqualValues(t, 0, its[1]["status"])

	// 恢复默认：回到代码里的当前文字
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items/%d/reset", did, paid), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, false, r.data()["overridden"])
	require.Equal(t, "已付款", r.data()["label"])
	require.Equal(t, "success", r.data()["color"])

	// 只改启用状态不算"改过显示"
	r = f.do(admin, "PUT", fmt.Sprintf("/system/dicts/%d/items/%d", did, paid),
		gin.H{"value": "2", "label": "已付款", "labelI18n": en("Paid"), "color": "success", "sort": 20, "status": 1})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, false, r.data()["overridden"])

	// 后台字典的项不能"恢复默认"
	r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "bank", "name": "银行", "portal": "platform"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	bank := uint64(r.data()["id"].(float64))
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", bank), gin.H{"value": "icbc", "label": "工商银行"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	icbc := uint64(r.data()["id"].(float64))
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items/%d/reset", bank, icbc), nil)
	require.Equal(t, httpx.CodeConflict, r.env.Code)
}

// ============ 代码里删掉的项、不再声明的字典：保留、解锁、可删；再声明回来重新锁定 ============

func TestDict_RemovedDeclarationsAreReleased(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")
	ctx := f.app.Context(context.Background())

	next := demoDicts()
	next[0].Items = next[0].Items[:2] // 删掉"已退款"
	next = next[:1]                   // 不再声明地区
	f.restart(next)

	did := f.dictID("demo.order_status")
	refunded := f.itemID("demo.order_status", "3")
	var it struct {
		Locked bool
		Status int
	}
	require.NoError(t, f.gdb.Raw("SELECT locked, status FROM ga_dict_item WHERE id = ?", refunded).Scan(&it).Error)
	require.False(t, it.Locked)
	require.Equal(t, 0, it.Status, "解除锁定并停用")
	_, _, data := f.readDicts(admin, "demo.order_status", "")
	require.Len(t, items(data["demo.order_status"]), 3, "历史数据还要靠它显示文字")
	ok, err := f.app.Deps().Dict.Has(ctx, "demo.order_status", "3")
	require.NoError(t, err)
	require.False(t, ok)

	var src string
	require.NoError(t, f.gdb.Raw("SELECT source FROM ga_dict WHERE code = 'demo.region'").Scan(&src).Error)
	require.Equal(t, "admin", src, "不再声明的字典转为后台字典")

	// 值只差大小写或重音也按不同的值处理，重启不会因唯一键冲突而失败
	accent := demoDicts()
	accent[1].Items[0].Children = append(accent[1].Items[0].Children, dict.Item{Value: "cn-BJ", Label: "北京（大写）"}, dict.Item{Value: "cn-é", Label: "重音"})
	f.restart(accent)
	f.itemID("demo.region", "cn-BJ")
	f.itemID("demo.region", "cn-bj")

	// 再声明回来：重新锁定、恢复启用
	f.restart(demoDicts())
	require.NoError(t, f.gdb.Raw("SELECT locked, status FROM ga_dict_item WHERE id = ?", refunded).Scan(&it).Error)
	require.True(t, it.Locked)
	require.Equal(t, 1, it.Status)
	require.NoError(t, f.gdb.Raw("SELECT source FROM ga_dict WHERE code = 'demo.region'").Scan(&src).Error)
	require.Equal(t, "code", src)

	// 解锁后的遗留项可以删；删掉整本转成后台字典的字典也可以
	f.restart(next)
	r := f.do(admin, "DELETE", fmt.Sprintf("/system/dicts/%d/items/%d", did, refunded), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	region := f.dictID("demo.region")
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/dicts/%d/items/%d", region, f.itemID("demo.region", "cn")), nil)
	require.Equal(t, httpx.CodeConflict, r.env.Code, "有子项的项不能删")
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/dicts/%d", region), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_dict_item WHERE dict_id = ?", region).Scan(&n).Error)
	require.Zero(t, n, "删字典连项一起删")
}

// 规范 §13.2 第 107 条（D-058）：代码字典里已不在声明中的遗留项，后台不能改值、不能重新启用；
// 库里即使是启用状态，也不算有效值（Has 为假、读接口里按停用返回）。只能改显示文字、保持停用或删除。
func TestDict_107_LegacyItemsStayRetired(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")
	ctx := f.app.Context(context.Background())
	next := demoDicts()
	next[0].Items = next[0].Items[:2] // 删掉"已退款"（值 3）
	f.restart(next)
	did, refunded := f.dictID("demo.order_status"), f.itemID("demo.order_status", "3")
	path := fmt.Sprintf("/system/dicts/%d/items/%d", did, refunded)
	has := func(v string) bool {
		ok, err := f.app.Deps().Dict.Has(ctx, "demo.order_status", v)
		require.NoError(t, err)
		return ok
	}

	r := f.do(admin, "PUT", path, gin.H{"value": "99", "label": "新值", "status": 0})
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "遗留项不能改值")
	require.Contains(t, r.rec.Body.String(), "dict.valueLocked")
	r = f.do(admin, "PUT", path, gin.H{"value": "3", "label": "已退款", "status": 1})
	require.Equal(t, httpx.CodeDeclaredInCode, r.env.Code, "遗留项不能重新启用")
	require.Contains(t, r.rec.Body.String(), "dict.legacyItem")
	r = f.do(admin, "PUT", path, gin.H{"value": "3", "label": "已退款（旧）"})
	require.Equal(t, 0, r.env.Code, "改显示文字可以，不给状态也保持停用："+r.rec.Body.String())
	var st int
	require.NoError(t, f.gdb.Raw("SELECT status FROM ga_dict_item WHERE id = ?", refunded).Scan(&st).Error)
	require.Zero(t, st)
	require.False(t, has("3"))
	require.False(t, has("99"))

	// 库里被改成了启用（例如升级前留下的数据，同步之后又被改过）：照样不算有效值，读接口按停用返回。
	// 重建应用清掉缓存（同步会把它再停用一次），之后再直接改库
	f.restart(next)
	ctx = f.app.Context(context.Background())
	require.NoError(t, f.gdb.Exec("UPDATE ga_dict_item SET status = 1 WHERE id = ?", refunded).Error)
	require.False(t, has("3"))
	_, _, data := f.readDicts(admin, "demo.order_status", "")
	for _, it := range items(data["demo.order_status"]) {
		if fmt.Sprint(it["value"]) == "3" {
			require.EqualValues(t, 0, it["status"])
		}
	}
}

// ============ 后台字典的校验 ============

func TestDict_AdminDictValidation(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")

	r := f.do(admin, "POST", "/system/dicts", gin.H{"code": "demo.sneaky", "name": "冒充代码字典"})
	require.Equal(t, httpx.CodeValidation, r.env.Code, "带点的编码留给代码声明")
	r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "bank", "name": "银行", "portal": "nowhere"})
	require.Equal(t, httpx.CodeValidation, r.env.Code, "端不存在")
	r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "level", "name": "级别", "valueType": "int"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "*", r.data()["portal"], "不填端表示所有端共用")
	level := uint64(r.data()["id"].(float64))
	r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "level", "name": "重复"})
	require.Equal(t, httpx.CodeConflict, r.env.Code)

	for _, bad := range []gin.H{
		{"value": "01", "label": "一"},
		{"value": "one", "label": "一"},
		{"value": "1", "label": ""},
		{"value": "1", "label": "一", "color": "red"},
		{"value": "1", "label": "一", "labelI18n": gin.H{"english": "One"}},
		{"value": "1", "label": "一", "parentId": 999999},
		{"value": "1", "label": "一", "status": 2},
	} {
		r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", level), bad)
		require.Equal(t, httpx.CodeValidation, r.env.Code, "%v: %s", bad, r.rec.Body.String())
	}
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", level), gin.H{"value": "1", "label": "一"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	one := uint64(r.data()["id"].(float64))
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", level), gin.H{"value": "1", "label": "又一"})
	require.Equal(t, httpx.CodeConflict, r.env.Code, "值重复")
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", level), gin.H{"value": "11", "label": "一一", "parentId": one})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "DELETE", fmt.Sprintf("/system/dicts/%d/items/%d", level, one), nil)
	require.Equal(t, httpx.CodeConflict, r.env.Code, "有子项的项不能删")

	// 别的字典的项当父项：不存在
	r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "tag", "name": "标签"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	tag := uint64(r.data()["id"].(float64))
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", tag), gin.H{"value": "x", "label": "x", "parentId": one})
	require.Equal(t, httpx.CodeValidation, r.env.Code)

	// 值按字节比较（utf8mb4_bin）：大小写、重音不同是不同的值；完全相同才冲突
	for _, v := range []string{"Hot", "hot", "e", "é"} {
		r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", tag), gin.H{"value": v, "label": v})
		require.Equal(t, 0, r.env.Code, "%s: %s", v, r.rec.Body.String())
	}
	r = f.do(admin, "POST", fmt.Sprintf("/system/dicts/%d/items", tag), gin.H{"value": "Hot", "label": "又一个"})
	require.Equal(t, httpx.CodeConflict, r.env.Code)

	// 详情是树形
	r = f.do(admin, "GET", fmt.Sprintf("/system/dicts/%d", level), nil)
	require.Equal(t, 0, r.env.Code)
	top := r.data()["items"].([]any)
	require.Len(t, top, 1)
	require.Len(t, top[0].(map[string]any)["children"].([]any), 1)

	// 列表：按关键字、来源过滤
	r = f.do(admin, "GET", "/system/dicts?source=code", nil)
	require.EqualValues(t, 2, r.data()["total"])
	r = f.do(admin, "GET", "/system/dicts?keyword=lev", nil)
	require.EqualValues(t, 1, r.data()["total"])
}

// ============ 端边界：只返回本端和共用的、启用中的字典 ============

func TestDict_ReadIsScopedToPortal(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")

	// 直接造一本属于别的端的字典
	now := time.Now().UTC()
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_dict (portal, code, name, value_type, source, status, created_at, updated_at) VALUES ('other', 'secret', '别的端', 'string', 'admin', 1, ?, ?)", now, now).Error)
	// 后台建一本共用字典、一本停用的字典
	r := f.do(admin, "POST", "/system/dicts", gin.H{"code": "shared", "name": "共用"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	r = f.do(admin, "POST", "/system/dicts", gin.H{"code": "off", "name": "停用", "portal": "platform", "status": 0})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())

	_, code, data := f.readDicts(admin, "secret,shared,off,demo.order_status,demo.region", "")
	require.Equal(t, 0, code)
	require.NotContains(t, data, "secret", "别的端的字典当作不存在")
	require.NotContains(t, data, "off", "停用的字典不返回")
	require.Contains(t, data, "shared")
	require.Contains(t, data, "demo.order_status")
	require.Contains(t, data, "demo.region")

	r = f.do(admin, "GET", "/system/options/portals", nil)
	require.Equal(t, []any{"platform"}, r.env.Data)
}

// ============ 管理接口要权限码，读取接口登录即可 ============

func TestDict_AdminRoutesRequirePerms(t *testing.T) {
	f := newDictFixture(t)
	admin, _ := f.admin("root")
	viewer := f.createRole(admin, "viewer", []string{system.PermDictList})
	_, bob := f.createUser(admin, "bob", []uint64{viewer})
	nobody := f.createRole(admin, "nobody", nil)
	_, eve := f.createUser(admin, "eve", []uint64{nobody})
	did := f.dictID("demo.order_status")

	require.Equal(t, 0, f.do(bob, "GET", "/system/dicts", nil).env.Code)
	for _, c := range []struct{ method, path string }{
		{"POST", "/system/dicts"},
		{"PUT", fmt.Sprintf("/system/dicts/%d", did)},
		{"DELETE", fmt.Sprintf("/system/dicts/%d", did)},
		{"POST", fmt.Sprintf("/system/dicts/%d/items", did)},
	} {
		r := f.do(bob, c.method, c.path, gin.H{"name": "x", "code": "x", "value": "1", "label": "x"})
		require.Equal(t, 403, r.rec.Code, "%s %s", c.method, c.path)
	}
	require.Equal(t, 403, f.do(eve, "GET", "/system/dicts", nil).rec.Code)
	_, code, data := f.readDicts(eve, "demo.order_status", "")
	require.Equal(t, 0, code, "读取字典登录即可")
	require.Contains(t, data, "demo.order_status")
}

// ============ 声明不合法时拒绝启动 ============

func TestDict_InvalidDeclarationFailsSetup(t *testing.T) {
	gdb := db.OpenTestDB(t)
	_, err := db.MigrateUp(db.WithDB(context.Background(), gdb), gdb, migrations.Core(), migrations.CoreDir, migrations.CoreTable)
	require.NoError(t, err)
	cfg := conf.Default()
	cfg.Log.Level = "error"
	bad := demoDicts()
	bad[0].Code = "other.order_status"
	a, err := app.New(cfg, app.WithDB(gdb), app.WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	a.Register(system.Module(), &demoModule{dicts: bad})
	require.ErrorContains(t, a.Setup(), "必须以模块名")
}
