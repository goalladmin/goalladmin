package oplogimpl

import (
	"encoding/json"
	"strings"
	"testing"

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
