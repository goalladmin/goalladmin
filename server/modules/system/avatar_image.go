package system

// 头像图片的处理（docs/decisions.md D-040）：只收 JPEG 和 PNG，先看尺寸再解码，居中裁成正方形、缩放、
// 透明部分铺白底，重新编码成 JPEG。存下来的是全新的图片，原文件里的 EXIF、注释和夹带的内容都不会留下。

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"net/http"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

// 头像的上限和输出尺寸。
const (
	avatarMaxBytes  = 1 << 20     // 请求体上限；浏览器端已经裁剪、缩到 512×512 再上传，正常只有几十 KB
	avatarMaxSide   = 2048        // 宽高上限：浏览器端只会传 512×512，留足余量也不让解码吃掉几百 MB 内存
	avatarMaxPixels = 2048 * 2048 // 像素上限：超过的不解码（防解压炸弹）
	avatarMaxScans  = 40          // 渐进式 JPEG 的扫描段上限：解码器每一段都要把整张图过一遍，段数不设限时 1 MB 的文件能算上几分钟
	avatarSize      = 256         // 大图
	avatarThumbSize = 64          // 小图（列表里用）
)

func avatarError(key, msg string, params ...any) error {
	return httpx.ErrValidation.WithFields(httpx.NewField("image", key, msg, params...))
}

// processAvatar 检查并重新生成头像，返回 256×256 和 64×64 的 JPEG。
func processAvatar(raw []byte) (big, thumb []byte, err error) {
	if len(raw) == 0 {
		return nil, nil, avatarError("system.avatar.empty", "no image")
	}
	if len(raw) > avatarMaxBytes {
		return nil, nil, avatarError("system.avatar.size", "the image is larger than 1 MB", "max", avatarMaxBytes)
	}
	// 按内容判断格式，不看文件名和声明的 Content-Type；只用 JPEG 和 PNG 的解码器，不走全局注册表
	var decodeConfig func([]byte) (image.Config, error)
	var decode func([]byte) (image.Image, error)
	switch http.DetectContentType(raw) {
	case "image/jpeg":
		if n, ok := jpegScans(raw); !ok || n > avatarMaxScans {
			return nil, nil, avatarError("system.avatar.type", "the image cannot be read")
		}
		decodeConfig = func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
	case "image/png":
		decodeConfig = func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	default:
		return nil, nil, avatarError("system.avatar.type", "only JPEG and PNG images are accepted")
	}
	cfg, err := decodeConfig(raw)
	if err != nil {
		return nil, nil, avatarError("system.avatar.type", "the image cannot be read")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > avatarMaxSide || cfg.Height > avatarMaxSide || cfg.Width*cfg.Height > avatarMaxPixels {
		return nil, nil, avatarError("system.avatar.dimensions", "the image is too large (at most 2048×2048)", "max", avatarMaxSide)
	}
	src, err := decode(raw)
	if err != nil {
		return nil, nil, avatarError("system.avatar.type", "the image cannot be read")
	}
	square := squareOnWhite(src)
	if big, err = encodeJPEG(resample(square, avatarSize), 88); err != nil {
		return nil, nil, err
	}
	if thumb, err = encodeJPEG(resample(square, avatarThumbSize), 85); err != nil {
		return nil, nil, err
	}
	return big, thumb, nil
}

// squareOnWhite 居中裁出最大的正方形，画到白底的 RGBA 上（透明部分变白）。
func squareOnWhite(src image.Image) *image.RGBA {
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	sp := image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/2)
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, sp, draw.Over)
	return dst
}

// resample 把正方形图缩放到 n×n：缩小时对每个目标像素覆盖的源区域取平均，放大时取最近的源像素。
func resample(src *image.RGBA, n int) *image.RGBA {
	side := src.Bounds().Dx()
	dst := image.NewRGBA(image.Rect(0, 0, n, n))
	span := func(d int) (int, int) {
		lo := d * side / n
		hi := (d + 1) * side / n
		if hi <= lo {
			hi = lo + 1
		}
		return lo, min(hi, side)
	}
	for dy := 0; dy < n; dy++ {
		y0, y1 := span(dy)
		for dx := 0; dx < n; dx++ {
			x0, x1 := span(dx)
			var r, g, bl, cnt int
			for y := y0; y < y1; y++ {
				row := src.Pix[y*src.Stride:]
				for x := x0; x < x1; x++ {
					i := x * 4
					r += int(row[i])
					g += int(row[i+1])
					bl += int(row[i+2])
					cnt++
				}
			}
			o := dy*dst.Stride + dx*4
			dst.Pix[o] = uint8(r / cnt)    //nolint:gosec // 平均值不超过 255
			dst.Pix[o+1] = uint8(g / cnt)  //nolint:gosec // 同上
			dst.Pix[o+2] = uint8(bl / cnt) //nolint:gosec // 同上
			dst.Pix[o+3] = 255
		}
	}
	return dst
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// jpegScans 数 JPEG 里的扫描段（SOS）有多少个，不解码。结构读不懂时 ok 为 false。
// 段的格式：FF xx；SOI、EOI、RSTn、TEM 没有长度，其余带两字节长度；SOS 的头之后是熵编码数据，
// 其中的 FF 00（转义）和 FF D0–D7（重启标记）不算段的结束。
func jpegScans(b []byte) (n int, ok bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, false
	}
	i := 2
	for i < len(b) {
		if b[i] != 0xFF {
			return n, false
		}
		for i < len(b) && b[i] == 0xFF { // 允许填充的 FF
			i++
		}
		if i >= len(b) {
			return n, false
		}
		m := b[i]
		i++
		switch {
		case m == 0xD9: // EOI
			return n, true
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			continue
		}
		if i+2 > len(b) {
			return n, false
		}
		l := int(b[i])<<8 | int(b[i+1])
		if l < 2 || i+l > len(b) {
			return n, false
		}
		i += l
		if m != 0xDA {
			continue
		}
		n++
		if n > avatarMaxScans {
			return n, true
		}
		// 跳过熵编码数据，停在下一个真正的段标记上
		for i < len(b) {
			if b[i] == 0xFF && i+1 < len(b) && b[i+1] != 0x00 && (b[i+1] < 0xD0 || b[i+1] > 0xD7) {
				break
			}
			i++
		}
	}
	return n, false
}
