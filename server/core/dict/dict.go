// Package dict 是字典：把"值"翻译成"显示文字、颜色、下拉选项"的对照表（docs/decisions.md D-023）。
//
// 字典有两种来源，存在同一张表里：
//   - 模块在代码里声明（实现 app.DictSource），启动时同步进库。代码拥有值：后台不能加项、删项、改值；
//     后台能改显示文字、颜色、扩展值、排序和启用状态，改过的项同步时不覆盖，可以恢复默认。
//   - 后台新建，完全由管理员维护。
//
// 代码声明的字典编码必须以"模块名."开头（order.priority），后台新建的编码不能含点。
//
// 业务代码的用法：
//
//	// 声明（模块实现 app.DictSource）
//	func (m *module) Dicts() []dict.Dict {
//		return []dict.Dict{{
//			Code: "order.priority", Portal: PortalCode, Name: "订单优先级",
//			NameI18n: map[string]string{"en-US": "Note priority"}, ValueType: dict.Int,
//			Items: []dict.Item{
//				{Value: "1", Label: "低", LabelI18n: map[string]string{"en-US": "Low"}, Color: "info"},
//				{Value: "2", Label: "中", LabelI18n: map[string]string{"en-US": "Medium"}, Color: "warning"},
//			},
//		}}
//	}
//
//	// 校验（只认启用的项）
//	ok, err := deps.Dict.Has(ctx, "order.priority", "2")
package dict

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// ValueType 是字典值的类型。库里统一存字符串，读取接口按类型输出 JSON 字符串或数字。
type ValueType string

const (
	String ValueType = "string"
	Int    ValueType = "int"
)

// AllPortals 表示字典对所有端可见。
const AllPortals = "*"

// Source 是字典的来源。
const (
	SourceCode  = "code"  // 代码声明
	SourceAdmin = "admin" // 后台新建
)

// DefaultLang 是 Label、Name 字段对应的语言；其他语言放在 LabelI18n、NameI18n 里。
const DefaultLang = "zh-CN"

// Dict 是模块在代码里声明的一本字典。
type Dict struct {
	Code      string            // 模块名.名字，全小写，如 order.priority
	Portal    string            // 所属端；AllPortals 表示所有端共用
	Name      string            // 名称（默认语言）
	NameI18n  map[string]string // 其他语言的名称，键如 "en-US"
	ValueType ValueType         // String 或 Int；空表示 String
	Sort      int
	Remark    string
	Items     []Item
}

// 一本字典的规模上限（D-055）：项数和层数。后台加项时检查，代码声明在启动时检查；读出来拼树时也不超过这个层数，
// 库里的历史数据即使更深也不会无限递归。
const (
	MaxItems = 1000
	MaxDepth = 5
)

// Item 是字典的一项。Children 用于层级字典（如地区、分类）。
type Item struct {
	Value     string            // 值；Int 字典必须是十进制整数的规范写法（如 "1"、"-2"，不能写 "01"）
	Label     string            // 显示文字（默认语言）
	LabelI18n map[string]string // 其他语言的显示文字
	Color     string            // primary / success / warning / danger / info，或 #RRGGBB；空表示默认
	Extra     string            // 扩展值，业务自定（如图标名、费率）
	Sort      int
	Children  []Item
}

var (
	declCodeRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	adminCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	langRe      = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)
	hexColorRe  = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

const (
	maxCodeLen  = 64
	maxValueLen = 64
	maxLabelLen = 128
	maxNameLen  = 64
	maxI18nLen  = 1024
	maxExtraLen = 255
)

var namedColors = map[string]bool{"": true, "primary": true, "success": true, "warning": true, "danger": true, "info": true}

// ValidColor 报告颜色是否合法。
func ValidColor(c string) bool { return namedColors[c] || hexColorRe.MatchString(c) }

// ValidAdminCode 报告后台新建字典的编码是否合法：小写字母开头，只含小写字母、数字、下划线，不含点。
func ValidAdminCode(code string) bool {
	return len(code) <= maxCodeLen && adminCodeRe.MatchString(code)
}

// ValidCode 报告编码的格式是否可能存在：代码声明的（带点）或后台新建的（不带点）。
// 读取接口先用它过滤，格式不对的编码不查库。
func ValidCode(code string) bool {
	return len(code) <= maxCodeLen && (declCodeRe.MatchString(code) || adminCodeRe.MatchString(code))
}

// NormalizeValue 按值类型校验并规范化一个值。Int 字典要求规范的十进制整数写法。
func NormalizeValue(t ValueType, v string) (string, error) {
	if v == "" || len(v) > maxValueLen {
		return "", fmt.Errorf("值长度须为 1–%d", maxValueLen)
	}
	if strings.TrimSpace(v) != v {
		return "", fmt.Errorf("值不能以空白开头或结尾")
	}
	if t == Int {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != v {
			return "", fmt.Errorf("整数字典的值必须是十进制整数，如 1、-2")
		}
	}
	return v, nil
}

// encodeI18n 校验多语言对照表，返回 JSON 文本（空表返回 ""）。
func encodeI18n(m map[string]string, maxLen int) (string, error) {
	if len(m) == 0 {
		return "", nil
	}
	for k, v := range m {
		if !langRe.MatchString(k) {
			return "", fmt.Errorf("语言代码 %q 不合法，应形如 en-US", k)
		}
		if k == DefaultLang {
			return "", fmt.Errorf("默认语言 %s 的文字写在主字段里，不要放进对照表", DefaultLang)
		}
		if strings.TrimSpace(v) == "" || len([]rune(v)) > maxLen {
			return "", fmt.Errorf("语言 %s 的文字长度须为 1–%d", k, maxLen)
		}
	}
	b, err := json.Marshal(m) // map 按键排序输出，结果稳定
	if err != nil {
		return "", err
	}
	if len(b) > maxI18nLen {
		return "", fmt.Errorf("多语言对照表过长（上限 %d 字节）", maxI18nLen)
	}
	return string(b), nil
}

func decodeI18n(s string) map[string]string {
	if s == "" {
		return nil
	}
	var m map[string]string
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	return m
}

// pick 按语言选文字：默认语言用主字段，其他语言查对照表，查不到回落主字段。
func pick(def, i18n, lang string) string {
	if lang == "" || lang == DefaultLang {
		return def
	}
	m := decodeI18n(i18n)
	if s := m[lang]; s != "" {
		return s
	}
	// 缺这种语言时按统一的回退链（D-026）：繁体先简体、其他先英文，默认语言的文字在主字段里
	for _, fb := range httpx.FallbackLangs(lang) {
		if fb == DefaultLang {
			return def
		}
		if s := m[fb]; s != "" {
			return s
		}
	}
	return def
}

// validateDecl 校验一本代码声明的字典。module 是声明它的模块名；portalOK 报告端是否已注册。
func validateDecl(module string, d Dict, portalOK func(string) bool) error {
	if len(d.Code) > maxCodeLen || !declCodeRe.MatchString(d.Code) {
		return fmt.Errorf("字典编码 %q 不合规范：形如 模块名.名字，全小写，只含字母、数字、下划线", d.Code)
	}
	if !strings.HasPrefix(d.Code, module+".") {
		return fmt.Errorf("字典编码 %q 必须以模块名 %q 加点开头", d.Code, module)
	}
	if d.Portal != AllPortals && (d.Portal == "" || !portalOK(d.Portal)) {
		return fmt.Errorf("字典 %s 的端 %q 未注册（所有端共用请写 dict.AllPortals）", d.Code, d.Portal)
	}
	if strings.TrimSpace(d.Name) == "" || len([]rune(d.Name)) > maxNameLen {
		return fmt.Errorf("字典 %s 的名称长度须为 1–%d", d.Code, maxNameLen)
	}
	if _, err := encodeI18n(d.NameI18n, maxNameLen); err != nil {
		return fmt.Errorf("字典 %s 的名称：%w", d.Code, err)
	}
	vt := d.ValueType
	if vt == "" {
		vt = String
	}
	if vt != String && vt != Int {
		return fmt.Errorf("字典 %s 的值类型 %q 不合法，只能是 string 或 int", d.Code, d.ValueType)
	}
	if d.Sort < 0 {
		return fmt.Errorf("字典 %s 的排序不能为负", d.Code)
	}
	if len([]rune(d.Remark)) > 255 {
		return fmt.Errorf("字典 %s 的备注过长", d.Code)
	}
	if len(d.Items) == 0 {
		return fmt.Errorf("字典 %s 没有任何项", d.Code)
	}
	seen := map[string]bool{}
	var walk func(items []Item, depth int) error
	walk = func(items []Item, depth int) error {
		// 没有下一层就不算一层：第 MaxDepth 层的叶子不能因为空的 Children 被当成第 MaxDepth+1 层
		if len(items) == 0 {
			return nil
		}
		if depth > MaxDepth {
			return fmt.Errorf("字典 %s 的层级超过 %d 层", d.Code, MaxDepth)
		}
		for _, it := range items {
			if len(seen) >= MaxItems {
				return fmt.Errorf("字典 %s 的项超过 %d 个", d.Code, MaxItems)
			}
			if _, err := NormalizeValue(vt, it.Value); err != nil {
				return fmt.Errorf("字典 %s 的值 %q：%w", d.Code, it.Value, err)
			}
			// value 列按字节比较（utf8mb4_bin），这里也精确比较
			if seen[it.Value] {
				return fmt.Errorf("字典 %s 的值 %q 重复（同一字典内不分层级都必须唯一）", d.Code, it.Value)
			}
			seen[it.Value] = true
			if strings.TrimSpace(it.Label) == "" || len([]rune(it.Label)) > maxLabelLen {
				return fmt.Errorf("字典 %s 的值 %q：显示文字长度须为 1–%d", d.Code, it.Value, maxLabelLen)
			}
			if _, err := encodeI18n(it.LabelI18n, maxLabelLen); err != nil {
				return fmt.Errorf("字典 %s 的值 %q：%w", d.Code, it.Value, err)
			}
			if !ValidColor(it.Color) {
				return fmt.Errorf("字典 %s 的值 %q：颜色 %q 不合法", d.Code, it.Value, it.Color)
			}
			if len([]rune(it.Extra)) > maxExtraLen {
				return fmt.Errorf("字典 %s 的值 %q：扩展值过长", d.Code, it.Value)
			}
			if it.Sort < 0 {
				return fmt.Errorf("字典 %s 的值 %q：排序不能为负", d.Code, it.Value)
			}
			if err := walk(it.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(d.Items, 1)
}

// Decl 是登记到服务里的一本代码字典：声明它的模块名 + 声明本身。
type Decl struct {
	Module string
	Dict   Dict
}

// ValidateDecls 校验全部代码声明：各自合法、编码全局唯一。app 在启动时调用。
func ValidateDecls(decls []Decl, portalOK func(string) bool) error {
	seen := map[string]string{}
	for _, d := range decls {
		if err := validateDecl(d.Module, d.Dict, portalOK); err != nil {
			return fmt.Errorf("模块 %s: %w", d.Module, err)
		}
		if prev, dup := seen[d.Dict.Code]; dup {
			return fmt.Errorf("字典 %s 被模块 %s 和 %s 重复声明", d.Dict.Code, prev, d.Module)
		}
		seen[d.Dict.Code] = d.Module
	}
	return nil
}
