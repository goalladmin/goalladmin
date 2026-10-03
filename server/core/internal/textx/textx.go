// Package textx 整理要写进数据库文本列的字符串（D-058）。
package textx

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clip 返回能写进 utf8mb4 文本列的字符串：非法的 UTF-8 字节换成 U+FFFD，再在字符边界上截断到最多 n 字节。
//
// 请求头（User-Agent 等）、请求路径里的原始字节不一定是合法的 UTF-8，按字节截断也会把一个汉字切成两半；
// 严格模式的 MySQL 遇到这样的值整行拒绝写入（1366），审计记录就会整条丢掉。列宽按字符算，n 字节一定放得下。
func Clip(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// HasInvisible 报告 s 里有没有控制字符或不可见的格式字符（零宽空格、从右到左覆盖符、字节顺序标记等），
// 或者不是合法的 UTF-8。名称类的字段不接受它们（D-099）：后者能做出看起来一样、实际不同的名字。
// 零宽连接符和零宽非连接符（U+200D、U+200C）除外：孟加拉语、泰米尔语等文字的正常拼写要用到它们。
func HasInvisible(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if r == '\u200c' || r == '\u200d' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}
