package oplogimpl

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMaskJSON_NestedAndCaseInsensitive(t *testing.T) {
	in := `{"username":"alice","Password":"p1","profile":{"apiKey":"k","nick":"a"},"items":[{"token":"t","n":1}],"authorization":"x","signature":"s","credentials":["a"]}`
	out := MaskJSON([]byte(in))
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, "alice", m["username"])
	require.Equal(t, Masked, m["Password"])
	require.Equal(t, Masked, m["authorization"])
	require.Equal(t, Masked, m["signature"])
	require.Equal(t, Masked, m["credentials"])
	require.Equal(t, Masked, m["profile"].(map[string]any)["apiKey"])
	require.Equal(t, "a", m["profile"].(map[string]any)["nick"])
	item := m["items"].([]any)[0].(map[string]any)
	require.Equal(t, Masked, item["token"])
	require.EqualValues(t, 1, item["n"])
	require.NotContains(t, out, "p1")
	require.NotContains(t, out, `"k"`)
}

func TestMaskJSON_InvalidIsNotStored(t *testing.T) {
	out := MaskJSON([]byte(`{"password": "leak"`))
	require.Equal(t, "<invalid json, 19 bytes>", out)
	require.NotContains(t, out, "leak")
}

func TestMaskQuery(t *testing.T) {
	require.Equal(t, "", MaskQuery(""))
	out := MaskQuery("b=2&secret=s3cr3t&a=1&Token=t&a=3")
	require.Equal(t, "Token=***&a=1&a=3&b=2&secret=***", out)
	require.NotContains(t, out, "s3cr3t")
	require.Equal(t, "<invalid query, 5 bytes>", MaskQuery("a=%zz"))
}

func TestIsSensitiveKey(t *testing.T) {
	for _, k := range []string{"password", "oldPassword", "PASSWD", "clientSecret", "accessToken", "apiKey", "x-credential", "sign", "Authorization"} {
		require.True(t, IsSensitiveKey(k), k)
	}
	for _, k := range []string{"username", "title", "status", "email"} {
		require.False(t, IsSensitiveKey(k), k)
	}
}

func TestTruncate_UTF8Boundary(t *testing.T) {
	s := strings.Repeat("中", 10) // 30 字节
	out := truncate(s, 8)
	require.Equal(t, "中中", out)
	require.Equal(t, "abc", truncate("abc", 8))
}

// capture 用给定的 Content-Type 和请求体跑一次 captureBody，返回要入库的文本。
func capture(t *testing.T, contentType, body string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/x", strings.NewReader(body))
	if contentType != "" {
		c.Request.Header.Set("Content-Type", contentType)
	}
	out, err := captureBody(c)
	require.NoError(t, err)
	return out
}

// 179（D-095）：操作日志记的是脱敏后的内容，不取决于请求把自己声明成什么类型。
func TestCaptureBody_179_FormTypedJSONIsMaskedAsJSON(t *testing.T) {
	const body = `{"oldPassword":"Old-Placeholder-1","newPassword":"New-Placeholder-2","note":"keep"}`
	for _, ct := range []string{"application/x-www-form-urlencoded", "application/x-www-form-urlencoded; charset=utf-8"} {
		out := capture(t, ct, body)
		require.NotContains(t, out, "Old-Placeholder-1", ct)
		require.NotContains(t, out, "New-Placeholder-2", ct)
		require.JSONEq(t, `{"oldPassword":"***","newPassword":"***","note":"keep"}`, out, ct)
	}
	// 前面有空白、数组开头的也算
	require.JSONEq(t, `[{"password":"***"}]`, capture(t, "application/x-www-form-urlencoded", " \r\n\t[{\"password\":\"Old-Placeholder-1\"}]"))
	// 看着像 JSON、其实不合法的：只存占位符
	bad := capture(t, "application/x-www-form-urlencoded", `{"password":"Old-Placeholder-1"`)
	require.Equal(t, "<invalid json, 31 bytes>", bad)
	// JSON 值后面还跟着别的内容：只记前半段就漏了后半段，整段存占位符
	trailing := capture(t, "application/x-www-form-urlencoded", `{"a":1}&status=2&password=Old-Placeholder-1`)
	require.Equal(t, "<invalid json, 43 bytes>", trailing)
	require.Equal(t, `{"a":1}`, capture(t, "application/x-www-form-urlencoded", "{\"a\":1} \r\n"))
	// 真正的表单照旧
	require.Equal(t, "a=1&password=***", capture(t, "application/x-www-form-urlencoded", "password=Old-Placeholder-1&a=1"))
	// 别的类型不存内容
	for _, ct := range []string{"text/plain", "", "multipart/form-data; boundary=x", "application/octet-stream"} {
		out := capture(t, ct, body)
		require.NotContains(t, out, "Placeholder", ct)
		require.True(t, strings.HasPrefix(out, "<") && strings.HasSuffix(out, " bytes>"), "%q: %s", ct, out)
	}
}

// 179（D-095）：查询串、表单里不像字段名的键不写原文，连值一起遮掉。
func TestMaskQuery_179_UnusualKeysAreNotStored(t *testing.T) {
	// 没有等号的一整段内容会被解析成一个键
	out := MaskQuery(`{"password":"Old-Placeholder-1"}`)
	require.Equal(t, "(key)=***", out)
	out = MaskQuery("note=ok&" + strings.Repeat("k", maxKey+1) + "=v&bad key=Old-Placeholder-1&a[0].b-c_d=1")
	require.NotContains(t, out, "Old-Placeholder-1")
	require.NotContains(t, out, strings.Repeat("k", maxKey+1))
	require.Contains(t, out, "note=ok")
	require.Contains(t, out, "a%5B0%5D.b-c_d=1")
	require.Equal(t, 2, strings.Count(out, "(key)=***"))
	// 正好 maxKey 字节的键照常
	k := strings.Repeat("k", maxKey)
	require.Equal(t, k+"=v", MaskQuery(k+"=v"))
	// 很长的值只留开头并标上原长度
	out = MaskQuery("a=" + strings.Repeat("x", 3000) + "&b=2")
	require.Contains(t, out, "b=2")
	require.Less(t, len(out), 700)
	require.Contains(t, out, "%283000+bytes%29")
}

// 179（D-095）：一个很长的值不能把别的参数挤出日志；整段被截断时结尾有标记和完整长度。
func TestMaskJSON_179_LongValuesAndTruncationMark(t *testing.T) {
	long := strings.Repeat("中", 2000) // 6000 字节
	out := capture(t, "application/json", `{"aaa":"`+long+`","zzz":"kept"}`)
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	require.Equal(t, "kept", m["zzz"])
	v := m["aaa"].(string)
	require.True(t, strings.HasSuffix(v, "…(6000 bytes)"), v[len(v)-20:])
	require.NotContains(t, out, `\u003c`, "标记里没有会被 JSON 转义的字符")
	require.LessOrEqual(t, len(v), maxValue+32)
	require.True(t, utf8.ValidString(out))
	// 正好 maxValue 字节的值原样保留
	exact := strings.Repeat("x", maxValue)
	require.Equal(t, `{"a":"`+exact+`"}`, capture(t, "application/json", `{"a":"`+exact+`"}`))

	// 键很多、每个都不长：整段超过 4 KB 时截断，并标明
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"k` + strings.Repeat("0", 3-len(itoa(i))) + itoa(i) + `":"vvvvvvvv"`)
	}
	b.WriteString("}")
	out = capture(t, "application/json", b.String())
	require.LessOrEqual(t, len(out), MaxBody)
	require.True(t, utf8.ValidString(out))
	require.Regexp(t, `…\(truncated, \d+ bytes\)$`, out)
	require.Contains(t, out, `"k000":"vvvvvvvv"`)
	// 没超的不带标记
	require.NotContains(t, clipBody(strings.Repeat("a", MaxBody)), "truncated")
	require.Equal(t, MaxBody, len(clipBody(strings.Repeat("a", MaxBody))))
	require.LessOrEqual(t, len(clipBody(strings.Repeat("中", MaxBody))), MaxBody)
	require.Regexp(t, `…\(truncated, 12288 bytes\)$`, clipBody(strings.Repeat("中", MaxBody)))
	// 不是合法 UTF-8 的内容先整理再量长度：不会不带标记地丢掉后半段
	require.Equal(t, "a\uFFFD", clipBody("a\xff"))
	bad := clipBody(strings.Repeat("a\xff", MaxBody)) // 整理后每个非法字节变成 3 字节的替换符：共 16384 字节
	require.True(t, utf8.ValidString(bad))
	require.LessOrEqual(t, len(bad), MaxBody)
	require.Regexp(t, `…\(truncated, 16384 bytes\)$`, bad)

	// 过长的键不写原文，别的参数还在
	longKey := strings.Repeat("k", maxKey+1)
	out = capture(t, "application/json", `{"`+longKey+`":"Old-Placeholder-1","zzz":"kept","`+strings.Repeat("q", 5000)+`":1}`)
	require.NotContains(t, out, longKey)
	require.NotContains(t, out, "Old-Placeholder-1")
	require.JSONEq(t, `{"(key)":"2 long keys omitted","zzz":"kept"}`, out)
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
