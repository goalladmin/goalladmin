package system_test

// 头像（docs/decisions.md D-040，规范 §13.2 第 61 条）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/migrations"
	"github.com/goalladmin/goalladmin/server/modules/system"
)

// doRaw 发一个原样的请求体（上传头像用）。
func (f *fixture) doRaw(token, method, path string, body []byte, contentType string) resp {
	f.t.Helper()
	req := httptest.NewRequest(method, base+path, bytes.NewReader(body))
	req.RemoteAddr = "203.0.113.10:5000"
	req.Header.Set("Content-Type", contentType)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(rec, req)
	var env httpx.Envelope
	require.NoError(f.t, json.Unmarshal(rec.Body.Bytes(), &env), "非信封响应: %s", rec.Body.String())
	return resp{rec: rec, env: env}
}

// 夹带在图片里的东西：上传后一个字节都不能留下。
const (
	smuggledScript = "<script>alert('avatar')</script>"
	smuggledExif   = "GPS-LATITUDE-31.2304-SECRET-HOME"
)

// testPNG 生成一张 w×h、带透明渐变的 PNG，文件尾再追加一段脚本（PNG 解码器会忽略 IEND 之后的内容）。
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x % 256), G: uint8(y % 256), B: 200, A: uint8((x + y) % 256)}) //nolint:gosec // 测试图案
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	buf.WriteString(smuggledScript)
	return buf.Bytes()
}

// testJPEG 生成一张 JPEG，并在文件头后面插一段带"位置信息"的 APP1（EXIF）段。
func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i % 251) //nolint:gosec // 测试图案
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	raw := buf.Bytes()
	payload := append([]byte("Exif\x00\x00"), []byte(smuggledExif)...)
	n := len(payload) + 2
	app1 := append([]byte{0xFF, 0xE1, byte(n >> 8), byte(n)}, payload...) //nolint:gosec // 测试数据，长度很小
	out := append([]byte{}, raw[:2]...)
	out = append(out, app1...)
	return append(out, raw[2:]...)
}

// manyScansJPEG 在一张正常 JPEG 的 EOI 之前插入 n 个空的扫描段（只看段结构，内容无所谓）。
func manyScansJPEG(t *testing.T, n int) []byte {
	t.Helper()
	raw := testJPEG(t, 16, 16)
	sos := []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00, 0x00}
	out := append([]byte{}, raw[:len(raw)-2]...)
	for i := 0; i < n; i++ {
		out = append(out, sos...)
	}
	return append(out, 0xFF, 0xD9)
}

// bigPNG 生成一张 side×side 的灰度 PNG（压缩后很小）。
func bigPNG(t *testing.T, side int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewGray(image.Rect(0, 0, side, side))))
	return buf.Bytes()
}

var avatarValueRe = regexp.MustCompile(`^upload:[0-9a-f]{32}$`)

// avatarBytes 读头像并解出 JPEG。
func (f *fixture) avatarBytes(token, key string, thumb bool) []byte {
	f.t.Helper()
	path := "/system/avatars/" + key
	if thumb {
		path += "?size=64"
	}
	r := f.do(token, "GET", path, nil)
	require.Equal(f.t, 0, r.env.Code, r.rec.Body.String())
	s := r.data()["image"].(string)
	require.True(f.t, strings.HasPrefix(s, "data:image/jpeg;base64,"))
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, "data:image/jpeg;base64,"))
	require.NoError(f.t, err)
	return b
}

func (f *fixture) avatarOf(id uint64) string {
	f.t.Helper()
	var v string
	require.NoError(f.t, f.gdb.Raw("SELECT avatar FROM ga_user WHERE id = ?", id).Scan(&v).Error)
	return v
}

func TestAvatar_61_UploadIsRegenerated(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, bob := f.createUser(root, "bob", nil)

	for name, tc := range map[string]struct {
		body []byte
		ct   string
	}{
		"PNG（带透明、文件尾夹带脚本）":      {testPNG(t, 600, 400), "image/png"},
		"JPEG（带 EXIF 位置信息）":     {testJPEG(t, 300, 500), "image/jpeg"},
		"声明 JPEG、实际是 PNG：按内容判断": {testPNG(t, 40, 40), "image/jpeg"},
	} {
		t.Run(name, func(t *testing.T) {
			r := f.doRaw(bob, "POST", "/system/avatar", tc.body, tc.ct)
			require.Equal(t, 0, r.env.Code, r.rec.Body.String())
			v := r.data()["avatar"].(string)
			require.Regexp(t, avatarValueRe, v)
			require.Equal(t, v, f.avatarOf(bobID))
			me := f.do(bob, "GET", "/auth/me", nil)
			require.Equal(t, v, me.data()["user"].(map[string]any)["avatar"], "/auth/me 立即是新头像")

			key := strings.TrimPrefix(v, "upload:")
			for _, c := range []struct {
				thumb bool
				size  int
			}{{false, 256}, {true, 64}} {
				b := f.avatarBytes(root, key, c.thumb) // 别人（看得到这个人的管理员）也能按键读
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
				require.NoError(t, err, "存下来的是 JPEG")
				require.Equal(t, c.size, cfg.Width)
				require.Equal(t, c.size, cfg.Height)
				require.NotContains(t, string(b), smuggledScript, "夹带的脚本没留下")
				require.NotContains(t, string(b), smuggledExif, "EXIF 没留下")
				require.NotContains(t, string(b), "Exif", "没有 EXIF 段")
			}
		})
	}

	t.Run("透明部分铺白底", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 32, 32)) // 全透明
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, img))
		r := f.doRaw(bob, "POST", "/system/avatar", buf.Bytes(), "image/png")
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		b := f.avatarBytes(bob, strings.TrimPrefix(r.data()["avatar"].(string), "upload:"), true)
		out, err := jpeg.Decode(bytes.NewReader(b))
		require.NoError(t, err)
		rr, gg, bb, _ := out.At(32, 32).RGBA()
		require.Greater(t, rr>>8, uint32(240))
		require.Greater(t, gg>>8, uint32(240))
		require.Greater(t, bb>>8, uint32(240))
	})
}

func TestAvatar_61_RejectsBadUploads(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, bob := f.createUser(root, "bob", nil)

	var gif bytes.Buffer
	gif.WriteString("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	var wide bytes.Buffer
	require.NoError(t, png.Encode(&wide, image.NewGray(image.Rect(0, 0, 5000, 2))))
	for name, tc := range map[string]struct {
		body []byte
		ct   string
		key  string
	}{
		"空":             {nil, "image/png", "system.avatar.empty"},
		"文本":            {[]byte("hello, not an image"), "image/png", "system.avatar.type"},
		"SVG（能带脚本）":     {[]byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><script>alert(1)</script></svg>`), "image/svg+xml", "system.avatar.type"},
		"GIF":           {gif.Bytes(), "image/gif", "system.avatar.type"},
		"HTML 伪装成 PNG":  {[]byte("<!DOCTYPE html><script>alert(1)</script>"), "image/png", "system.avatar.type"},
		"PNG 头但内容坏了":    {append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 64)...), "image/png", "system.avatar.type"},
		"太宽（不解码就拒绝）":    {wide.Bytes(), "image/png", "system.avatar.dimensions"},
		"太大（2049×2049）": {bigPNG(t, 2049), "image/png", "system.avatar.dimensions"},
	} {
		t.Run(name, func(t *testing.T) {
			r := f.doRaw(bob, "POST", "/system/avatar", tc.body, tc.ct)
			require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
			require.Equal(t, tc.key, fieldKey(r))
			require.Empty(t, f.avatarOf(bobID))
		})
	}
	t.Run("Content-Type 不是 image/jpeg 或 image/png：在操作日志之前拒绝，图片内容不进日志", func(t *testing.T) {
		before := len(f.oplogs(system.OpProfileAvatar))
		for _, ct := range []string{"application/x-www-form-urlencoded", "application/octet-stream", "application/json", ""} {
			r := f.doRaw(bob, "POST", "/system/avatar", testJPEG(t, 16, 16), ct)
			require.Equal(t, httpx.CodeValidation, r.env.Code, ct)
			require.Equal(t, "system.avatar.type", fieldKey(r))
		}
		require.Len(t, f.oplogs(system.OpProfileAvatar), before)
		require.Empty(t, f.avatarOf(bobID))
	})
	t.Run("渐进式 JPEG 的扫描段太多（解码会算很久）：不解码就拒绝", func(t *testing.T) {
		r := f.doRaw(bob, "POST", "/system/avatar", manyScansJPEG(t, 60), "image/jpeg")
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		require.Equal(t, "system.avatar.type", fieldKey(r))
	})
	t.Run("超过请求体上限", func(t *testing.T) {
		big := append(testPNG(t, 8, 8), bytes.Repeat([]byte{0}, 2<<20)...)
		r := f.doRaw(bob, "POST", "/system/avatar", big, "image/png")
		require.Equal(t, 413, r.rec.Code, r.rec.Body.String())
		require.Empty(t, f.avatarOf(bobID))
	})
	t.Run("未登录", func(t *testing.T) {
		r := f.doRaw("", "POST", "/system/avatar", testPNG(t, 8, 8), "image/png")
		require.Equal(t, 401, r.rec.Code)
	})
}

func TestAvatar_61_PresetsAndOldKeysExpire(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	bobID, bob := f.createUser(root, "bob", nil)

	up := func() string {
		r := f.doRaw(bob, "POST", "/system/avatar", testPNG(t, 64, 64), "image/png")
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		return strings.TrimPrefix(r.data()["avatar"].(string), "upload:")
	}
	gone := func(key string) {
		t.Helper()
		r := f.do(root, "GET", "/system/avatars/"+key, nil)
		require.Equal(t, 404, r.rec.Code, "旧键失效")
	}
	k1 := up()
	k2 := up()
	require.NotEqual(t, k1, k2)
	gone(k1)

	// 选内置头像：旧的上传图一起删掉
	r := f.do(bob, "PUT", "/system/avatar", gin.H{"preset": "ocean"})
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Equal(t, "preset:ocean", r.data()["avatar"])
	require.Equal(t, "preset:ocean", f.avatarOf(bobID))
	gone(k2)
	var n int64
	require.NoError(t, f.gdb.Raw("SELECT COUNT(*) FROM ga_user_avatar WHERE user_id = ?", bobID).Scan(&n).Error)
	require.Zero(t, n)

	for _, bad := range []gin.H{{"preset": "evil"}, {"preset": "../ocean"}, {"preset": "https://evil.example/a.png"}} {
		r = f.do(bob, "PUT", "/system/avatar", bad)
		require.Equal(t, httpx.CodeValidation, r.env.Code, r.rec.Body.String())
		require.Equal(t, "system.avatar.preset", fieldKey(r))
	}
	r = f.do(bob, "PUT", "/system/avatar", gin.H{"preset": "ocean", "userId": 1})
	require.NotEqual(t, 0, r.env.Code, "严格解析：夹带别的字段被拒")
	require.Equal(t, "preset:ocean", f.avatarOf(bobID))

	// 清除
	k3 := up()
	r = f.do(bob, "DELETE", "/system/avatar", nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Empty(t, f.avatarOf(bobID))
	gone(k3)

	// 读头像：键格式不对 404，未登录 401
	require.Equal(t, 404, f.do(root, "GET", "/system/avatars/not-a-key", nil).rec.Code)
	require.Equal(t, 401, f.do("", "GET", "/system/avatars/"+strings.Repeat("a", 32), nil).rec.Code)

	// 本人资料接口仍然不接受 avatar（D-038）
	r = f.do(bob, "PUT", "/system/profile", gin.H{"displayName": "Bob", "avatar": "preset:ocean"})
	require.NotEqual(t, 0, r.env.Code)
	require.Empty(t, f.avatarOf(bobID))

	// 操作日志：上传的请求体只记成占位符，不存图片
	logs := f.oplogs(system.OpProfileAvatar)
	require.NotEmpty(t, logs)
	var sawUpload bool
	for _, e := range logs {
		if e.Method == "POST" {
			sawUpload = true
			require.Regexp(t, `^<image/png, \d+ bytes>$`, e.Body)
		}
	}
	require.True(t, sawUpload)
}

func TestAvatar_61_AdminCanOnlyClear(t *testing.T) {
	w := newOrgWorld(t)
	f := w.f
	set := func(tok string) string {
		r := f.doRaw(tok, "POST", "/system/avatar", testPNG(t, 32, 32), "image/png")
		require.Equal(t, 0, r.env.Code, r.rec.Body.String())
		return r.data()["avatar"].(string)
	}
	set(w.aliceTok)
	set(w.olgaTok)

	// 管理资料 DTO 不接受 avatar 字段（D-108），拒绝后头像不改变
	aliceAvatar := f.avatarOf(w.alice)
	r := f.do(w.root, "PUT", fmt.Sprintf("/system/users/%d", w.alice), gin.H{"displayName": "Alice", "avatar": "https://evil.example/a.png"})
	require.Equal(t, httpx.CodeBadRequest, r.env.Code, r.rec.Body.String())
	require.Equal(t, aliceAvatar, f.avatarOf(w.alice))

	// 本部门范围的管理员：范围外 404，范围内可以清除
	_, mgr := w.manager("avmgr", w.sales, "dept")
	r = f.do(mgr, "DELETE", fmt.Sprintf("/system/users/%d/avatar", w.olga), nil)
	require.Equal(t, 404, r.rec.Code, r.rec.Body.String())
	require.NotEmpty(t, f.avatarOf(w.olga))
	r = f.do(mgr, "DELETE", fmt.Sprintf("/system/users/%d/avatar", w.alice), nil)
	require.Equal(t, 0, r.env.Code, r.rec.Body.String())
	require.Empty(t, f.avatarOf(w.alice))
	require.Equal(t, 404, f.do(w.root, "GET", "/system/avatars/"+strings.TrimPrefix(aliceAvatar, "upload:"), nil).rec.Code)
	require.NotEmpty(t, f.oplogs(system.OpUserAvatarClear))

	// 没有 system:user:update：403
	_, viewer := w.manager("avviewer", w.sales, "all", system.PermUserList)
	r = f.do(viewer, "DELETE", fmt.Sprintf("/system/users/%d/avatar", w.olga), nil)
	require.Equal(t, 403, r.rec.Code)

	// 非超管不能动超管账号
	rootID := uint64(f.do(w.root, "GET", "/auth/me", nil).data()["user"].(map[string]any)["id"].(float64))
	r = f.do(w.root, "PUT", "/system/avatar", gin.H{"preset": "amber"})
	require.Equal(t, 0, r.env.Code)
	_, allMgr := w.manager("avall", w.sales, "all")
	r = f.do(allMgr, "DELETE", fmt.Sprintf("/system/users/%d/avatar", rootID), nil)
	require.Equal(t, 403, r.rec.Code, r.rec.Body.String())
	require.Equal(t, "system.user.superProtected", fieldKey(r))
	require.Equal(t, "preset:amber", f.avatarOf(rootID))
}

// 升级迁移：原来存的任意地址清空，内置和上传的保留；可以重跑。
func TestAvatar_61_MigrationClearsArbitraryURLs(t *testing.T) {
	f := newFixture(t)
	root, _ := f.admin("root")
	a, _ := f.createUser(root, "amy", nil)
	b, _ := f.createUser(root, "ben", nil)
	c, _ := f.createUser(root, "cat", nil)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET avatar = ? WHERE id = ?", "https://tracker.example/pixel.png?u=1", a).Error)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET avatar = ? WHERE id = ?", "preset:ocean", b).Error)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET avatar = ? WHERE id = ?", "upload:"+strings.Repeat("a", 32), c).Error)
	require.NoError(t, f.gdb.Exec("INSERT INTO ga_user_avatar (user_id, avatar_key, image, thumb, created_at) VALUES (?, ?, '', '', UTC_TIMESTAMP(3))", c, strings.Repeat("a", 32)).Error)
	// 这几种以前能写进去、现在都不合法：带前缀的外部地址、没有对应图片的上传键、大小写变体
	d, _ := f.createUser(root, "dan", nil)
	e, _ := f.createUser(root, "eve", nil)
	g, _ := f.createUser(root, "gus", nil)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET avatar = ? WHERE id = ?", "preset:https://tracker.example/p.png", d).Error)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET avatar = ? WHERE id = ?", "upload:"+strings.Repeat("b", 32), e).Error)
	require.NoError(t, f.gdb.Exec("UPDATE ga_user SET avatar = ? WHERE id = ?", "PRESET:ocean", g).Error)
	sql, err := fs.ReadFile(migrations.Core(), migrations.CoreDir+"/00012_user_avatar.sql")
	require.NoError(t, err)
	ctx := db.WithDB(context.Background(), f.gdb)
	for i := 0; i < 2; i++ {
		for _, stmt := range db.SplitStatements(string(sql)) {
			require.NoError(t, db.From(ctx).Exec(stmt).Error)
		}
	}
	require.Empty(t, f.avatarOf(a))
	require.Equal(t, "preset:ocean", f.avatarOf(b))
	require.Equal(t, "upload:"+strings.Repeat("a", 32), f.avatarOf(c), "有对应图片的上传头像保留（重跑也不会清掉）")
	require.Empty(t, f.avatarOf(d))
	require.Empty(t, f.avatarOf(e))
	require.Empty(t, f.avatarOf(g))
}

// 内置头像的名字表：后端校验用的和壳里渲染用的是同一份。
func TestAvatar_61_PresetsMatchShell(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "packages", "shell", "src", "avatar.ts"))
	require.NoError(t, err)
	matches := regexp.MustCompile(`name: '([a-z]+)'`).FindAllSubmatch(src, -1)
	shell := make([]string, 0, len(matches))
	for _, m := range matches {
		shell = append(shell, string(m[1]))
	}
	got := system.AvatarPresets()
	sort.Strings(shell)
	sort.Strings(got)
	require.Equal(t, got, shell)
}
