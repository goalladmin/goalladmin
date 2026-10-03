package system

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 180（D-096）：数扫描段和解码器按同一种方式分段。段标记位置上的 FF 00 解码器会当多余字节丢掉，
// 数段的代码不能把它当成带长度的段跳过去——按读不懂处理，这样的图片不解码。
func TestJPEGScans_180_ExtraneousMarkerIsRejected(t *testing.T) {
	soi, eoi := []byte{0xFF, 0xD8}, []byte{0xFF, 0xD9}
	app0 := []byte{0xFF, 0xE0, 0x00, 0x04, 0x01, 0x02}
	sos := []byte{0xFF, 0xDA, 0x00, 0x04, 0x01, 0x02}
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

	n, ok := jpegScans(join(soi, eoi))
	require.True(t, ok)
	require.Zero(t, n)

	// 熵编码数据里的 FF 00（转义）、重启标记、填充的 FF 照旧
	n, ok = jpegScans(join(soi, app0, sos, []byte{0x11, 0xFF, 0x00, 0x22, 0xFF, 0xD3, 0x33}, []byte{0xFF}, sos, []byte{0x44}, eoi))
	require.True(t, ok)
	require.Equal(t, 2, n)

	// 段标记位置上的 FF 00：开头、两个段之间、填充的 FF 之后，都读不懂
	pseudo := []byte{0xFF, 0x00, 0x00, 0x04, 0x01, 0x02}
	for name, raw := range map[string][]byte{
		"紧跟 SOI":     join(soi, pseudo, eoi),
		"两个段之间":      join(soi, app0, pseudo, sos, []byte{0x11}, eoi),
		"填充的 FF 之后":  join(soi, app0, []byte{0xFF}, pseudo, eoi),
		"长度盖住后面的扫描段": join(soi, []byte{0xFF, 0x00, 0x00, 0x08}, sos, eoi),
	} {
		_, ok := jpegScans(raw)
		require.False(t, ok, name)
	}
	// 段之间夹着别的字节同样读不懂（原有行为）
	_, ok = jpegScans(join(soi, app0, []byte{0x00}, eoi))
	require.False(t, ok)

	// 接到上传流程上：一张正常的图片在 SOI 之后多出 FF 00 就不解码，回"读不懂"
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.White)
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	good := buf.Bytes()
	_, _, err := processAvatar(good)
	require.NoError(t, err)
	bad := join(good[:2], pseudo, good[2:])
	_, _, err = processAvatar(bad)
	require.ErrorIs(t, err, httpx.ErrValidation)
	require.Equal(t, "system.avatar.type", httpx.AsError(err).Fields[0].Key)
}
