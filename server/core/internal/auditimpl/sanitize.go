package auditimpl

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/goalladmin/goalladmin/server/core/internal/textx"
)

// 错误文本和调用栈的清理规则（D-032 第 3 条）。

const (
	maxMessage = 1024
	maxStack   = 8 << 10
)

var (
	// 引号里的内容往往是具体的值（SQL 里的参数、用户输入），一律换成 ?
	quotedRe = regexp.MustCompile("'(?:[^'\\\\]|\\\\.)*'|\"(?:[^\"\\\\]|\\\\.)*\"|`[^`]*`")
	// 敏感键后面的值：password=xxx、token: xxx
	secretKVRe = regexp.MustCompile(`(?i)(password|passwd|secret|token|key|credential|sign|authorization)(\s*[=:]\s*)[^\s,;&)]+`)
	// 连接串里的账号密码：user:pass@
	userinfoRe = regexp.MustCompile(`[^\s/:@]+:[^\s/@]+@`)
	// 指纹里去掉会变的部分：数字、长十六进制串
	digitsRe = regexp.MustCompile(`\d+`)
	hexRe    = regexp.MustCompile(`\b[0-9a-fA-F]{8,}\b`)
	// 调用栈里的偏移
	offsetRe = regexp.MustCompile(` \+0x[0-9a-f]+$`)
)

// cleanMessage 去掉错误文本里的具体值和凭据，截断到 1 KB。
func cleanMessage(s string) string {
	s = quotedRe.ReplaceAllString(s, "?")
	s = secretKVRe.ReplaceAllString(s, "${1}${2}***")
	s = userinfoRe.ReplaceAllString(s, "***@")
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, maxMessage)
}

// normalizeForFingerprint 在 cleanMessage 的基础上再去掉数字和十六进制串，让"同一类错误"得到同一个指纹。
func normalizeForFingerprint(s string) string {
	s = hexRe.ReplaceAllString(s, "H")
	return digitsRe.ReplaceAllString(s, "N")
}

// fingerprint 把若干段拼起来取 SHA-256 的前 32 个十六进制字符。
func fingerprint(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:16])
}

// cleanStack 整理 runtime/debug.Stack 的输出：
// 去掉第一行（goroutine 编号）、函数参数和偏移，文件路径只留最后三级，截断到 8 KB。
// 返回整理后的栈和 panic 发生处（panic 调用之后的第一帧，"函数 文件:行"），后者用来算指纹。
func cleanStack(raw []byte) (stack, origin string) {
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var b strings.Builder
	afterPanic := false
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "goroutine ") {
			continue
		}
		if strings.HasPrefix(l, "\t") {
			b.WriteString("\t" + shortPath(offsetRe.ReplaceAllString(strings.TrimSpace(l), "")) + "\n")
			continue
		}
		fn := stripArgs(l)
		b.WriteString(fn + "\n")
		// 运行时错误（空指针、越界）在 panic 之后还有几帧 runtime.*，跳过它们找到出错的业务代码
		if afterPanic && origin == "" && !strings.HasPrefix(fn, "runtime.") && i+1 < len(lines) {
			origin = fn + " " + shortPath(offsetRe.ReplaceAllString(strings.TrimSpace(lines[i+1]), ""))
		}
		if fn == "panic" {
			afterPanic = true
		}
	}
	return truncate(b.String(), maxStack), origin
}

// stripArgs 去掉函数行末尾的参数列表（最后一对括号），保留方法接收者里的括号。
func stripArgs(l string) string {
	if !strings.HasSuffix(l, ")") {
		return l
	}
	depth := 0
	for i := len(l) - 1; i >= 0; i-- {
		switch l[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return l[:i]
			}
		}
	}
	return l
}

// shortPath 只保留路径的最后三级：构建机上的目录结构不出现在页面上。
func shortPath(p string) string {
	parts := strings.Split(p, "/")
	if len(parts) <= 3 {
		return p
	}
	return strings.Join(parts[len(parts)-3:], "/")
}

// truncate 截到最多 n 字节，保证是合法的 UTF-8（D-058）。
func truncate(s string, n int) string { return textx.Clip(s, n) }
