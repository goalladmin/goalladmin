package dict

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func platformOnly(code string) bool { return code == "platform" }

func goodDict() Dict {
	return Dict{
		Code: "demo.level", Portal: "platform", Name: "级别", ValueType: Int,
		Items: []Item{{Value: "1", Label: "一"}, {Value: "2", Label: "二", Children: []Item{{Value: "21", Label: "二一"}}}},
	}
}

func TestValidateDecls(t *testing.T) {
	ok := []Decl{{Module: "demo", Dict: goodDict()}}
	require.NoError(t, ValidateDecls(ok, platformOnly))

	cases := map[string]func(d *Dict){
		"编码没有模块前缀":  func(d *Dict) { d.Code = "other.level" },
		"编码不含点":     func(d *Dict) { d.Code = "demo" },
		"编码有大写":     func(d *Dict) { d.Code = "demo.Level" },
		"端未注册":      func(d *Dict) { d.Portal = "nowhere" },
		"端为空":       func(d *Dict) { d.Portal = "" },
		"名称为空":      func(d *Dict) { d.Name = " " },
		"值类型非法":     func(d *Dict) { d.ValueType = "float" },
		"没有项":       func(d *Dict) { d.Items = nil },
		"整数值不规范":    func(d *Dict) { d.Items[0].Value = "01" },
		"整数值不是数":    func(d *Dict) { d.Items[0].Value = "x" },
		"子项与父项值重复":  func(d *Dict) { d.Items[1].Children[0].Value = "1" },
		"显示文字为空":    func(d *Dict) { d.Items[0].Label = "" },
		"颜色非法":      func(d *Dict) { d.Items[0].Color = "red" },
		"语言代码非法":    func(d *Dict) { d.Items[0].LabelI18n = map[string]string{"english": "One"} },
		"对照表里放默认语言": func(d *Dict) { d.Items[0].LabelI18n = map[string]string{"zh-CN": "一"} },
		"值两端有空白":    func(d *Dict) { d.ValueType = String; d.Items[0].Value = " a" },
		"排序为负":      func(d *Dict) { d.Items[0].Sort = -1 },
		"字符串值重复": func(d *Dict) {
			d.ValueType = String
			d.Items[0].Value = "a"
			d.Items[1].Value = "a"
			d.Items[1].Children = nil
		},
		"字典排序为负":     func(d *Dict) { d.Sort = -1 },
		"名称的对照表语言非法": func(d *Dict) { d.NameI18n = map[string]string{"EN": "Level"} },
	}
	for name, mut := range cases {
		d := goodDict()
		mut(&d)
		require.Error(t, ValidateDecls([]Decl{{Module: "demo", Dict: d}}, platformOnly), name)
	}

	// 值按字节比较：大小写、重音不同就是不同的值（与 value 列的 utf8mb4_bin 一致）
	d := goodDict()
	d.ValueType = String
	d.Items = []Item{{Value: "a", Label: "a"}, {Value: "A", Label: "A"}, {Value: "e", Label: "e"}, {Value: "é", Label: "é"}}
	require.NoError(t, ValidateDecls([]Decl{{Module: "demo", Dict: d}}, platformOnly))
	require.True(t, ValidCode("demo.level"))
	require.True(t, ValidCode("bank"))
	require.False(t, ValidCode("DEMO.LEVEL"))
	require.False(t, ValidCode("demo..x"))

	// 所有端共用
	d = goodDict()
	d.Portal = AllPortals
	require.NoError(t, ValidateDecls([]Decl{{Module: "demo", Dict: d}}, platformOnly))

	// 两个模块声明同一编码：第二个模块的编码没有自己的前缀，先被前缀规则拦住；同一模块重复声明被查重拦住
	require.Error(t, ValidateDecls([]Decl{{Module: "demo", Dict: goodDict()}, {Module: "demo", Dict: goodDict()}}, platformOnly))
}

func TestNormalizeValue(t *testing.T) {
	for _, v := range []string{"0", "1", "-2", "9223372036854775807"} {
		got, err := NormalizeValue(Int, v)
		require.NoError(t, err, v)
		require.Equal(t, v, got)
	}
	for _, v := range []string{"", "01", "+1", "1.0", "0x1", " 1", "9223372036854775808", "-0"} {
		_, err := NormalizeValue(Int, v)
		require.Error(t, err, v)
	}
	_, err := NormalizeValue(String, "abc")
	require.NoError(t, err)
	_, err = NormalizeValue(String, "a b")
	require.NoError(t, err, "中间的空格可以")
	_, err = NormalizeValue(String, "abc ")
	require.Error(t, err)
}

func TestAdminCodeAndColor(t *testing.T) {
	require.True(t, ValidAdminCode("bank_list"))
	require.False(t, ValidAdminCode("bank.list"), "带点的编码留给代码声明")
	require.False(t, ValidAdminCode("Bank"))
	require.False(t, ValidAdminCode("1bank"))
	require.False(t, ValidAdminCode(""))
	for _, c := range []string{"", "primary", "success", "warning", "danger", "info", "#00AAff"} {
		require.True(t, ValidColor(c), c)
	}
	for _, c := range []string{"red", "#fff", "#12345g", "Primary"} {
		require.False(t, ValidColor(c), c)
	}
}

func TestPickLanguage(t *testing.T) {
	i18n, err := encodeI18n(map[string]string{"en-US": "Low"}, maxLabelLen)
	require.NoError(t, err)
	require.Equal(t, "低", pick("低", i18n, "zh-CN"))
	require.Equal(t, "Low", pick("低", i18n, "en-US"))
	// 回退链（D-026）：没有该语言时先英文，再默认语言；繁体先回退默认语言（简体）
	require.Equal(t, "Low", pick("低", i18n, "ja-JP"), "没有日语时回落英文")
	require.Equal(t, "低", pick("低", i18n, "zh-TW"), "没有繁体时回落简体（默认语言），不是英文")
	ja, err := encodeI18n(map[string]string{"ja-JP": "低い"}, maxLabelLen)
	require.NoError(t, err)
	require.Equal(t, "低い", pick("低", ja, "ja-JP"))
	require.Equal(t, "低", pick("低", ja, "de-DE"), "既没有德语也没有英文时回落默认文字")
	require.Equal(t, "低", pick("低", "", "en-US"))
	require.Equal(t, "低", pick("低", "not json", "en-US"))
	empty, err := encodeI18n(nil, maxLabelLen)
	require.NoError(t, err)
	require.Equal(t, "", empty)
}
