// Package textx 整理要写进数据库文本列的字符串（D-058）。
package textx

import (
	"strings"
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
