// Package captcha 生成和校验图形验证码（D-029）：5 位数字，只用标准库绘制，不带任何字体文件。
//
// 验证码只是登录防护里的减速带，真正的防线是限流和失败锁定（规范 §5.7）。
package captcha

import (
	"bytes"
	"container/list"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"
)

const (
	// Width、Height 是图片尺寸（像素）。
	Width  = 140
	Height = 48
	// Length 是答案的位数。
	Length = 5

	defaultTTL = 5 * time.Minute
	defaultMax = 10240
	idBytes    = 20
)

// Captcha 生成验证码；可选共享存储，本地答案始终只由生成它的实例消费。
type Captcha struct {
	now    func() time.Time
	ttl    time.Duration
	max    int
	shared *sharedStore

	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List // 按生成顺序，最早的在前；有效期都一样，所以也是按过期时间排序
}

type entry struct {
	id      string
	scope   string
	answer  string
	expires time.Time
}

// New 创建生成器：5 位数字，5 分钟过期，最多缓存 10240 个待验证的答案（满了先丢最早生成的）。
func New() *Captcha {
	return newCaptcha(time.Now, defaultTTL, defaultMax)
}

func newCaptcha(now func() time.Time, ttl time.Duration, max int) *Captcha {
	return &Captcha{now: now, ttl: ttl, max: max, items: map[string]*list.Element{}, order: list.New()}
}

// Generate 返回验证码 ID 和 PNG 的 data URL。
func (c *Captcha) Generate() (id, imageDataURL string, err error) {
	return c.GenerateFor(context.Background(), "")
}

// GenerateFor 为指定端生成验证码，ID 的存储归属在创建后不再改变。
func (c *Captcha) GenerateFor(ctx context.Context, scope string) (id, imageDataURL string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	answer, err := randomDigits(Length)
	if err != nil {
		return "", "", err
	}
	id, err = randomID()
	if err != nil {
		return "", "", err
	}

	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return "", "", fmt.Errorf("captcha: seed: %w", err)
	}
	// 只用于图形扰动；答案和 ID 都取自 crypto/rand。种子也来自 crypto/rand，扰动无法预测
	img := render(answer, mrand.New(mrand.NewChaCha8(seed))) //nolint:gosec // 见上一行
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", "", fmt.Errorf("captcha: encode: %w", err)
	}
	id, err = c.store(ctx, scope, id, answer)
	if err != nil {
		return "", "", err
	}
	return id, "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func randomID() (string, error) {
	var raw [idBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("captcha: id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Verify 校验答案；无论对错都作废该 ID。过期、不存在、空值一律不通过。
func (c *Captcha) Verify(id, answer string) bool {
	return c.VerifyFor(context.Background(), "", id, answer)
}

// VerifyFor 只消费同一个端的验证码；空答案也消耗这次机会。
func (c *Captcha) VerifyFor(ctx context.Context, scope, id, answer string) bool {
	answer = strings.TrimSpace(answer)
	if ctx.Err() != nil || id == "" {
		return false
	}
	want, ok := c.consume(ctx, scope, id)
	if !ok || answer == "" || ctx.Err() != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(answer)) == 1
}

// Peek 只查看答案（测试用），不作废；不存在或已过期时返回空串。
func (c *Captcha) Peek(id string) string {
	if c.shared != nil && validID(id, 'r') {
		answer, _ := c.shared.read(context.Background(), "peek", "", id, c.now())
		return answer
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[id]
	if !ok {
		return ""
	}
	e := el.Value.(*entry)
	if !c.now().Before(e.expires) {
		return ""
	}
	return e.answer
}

func (c *Captcha) put(scope, id, answer string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	// 先清掉已过期的（都在前面），还满就丢最早生成的
	for f := c.order.Front(); f != nil && !now.Before(f.Value.(*entry).expires); f = c.order.Front() {
		c.removeLocked(f)
	}
	for c.order.Len() >= c.max {
		c.removeLocked(c.order.Front())
	}
	c.items[id] = c.order.PushBack(&entry{id: id, scope: scope, answer: answer, expires: now.Add(c.ttl)})
}

func (c *Captcha) take(scope, id string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[id]
	if !ok || el.Value.(*entry).scope != scope {
		return "", false
	}
	c.removeLocked(el)
	e := el.Value.(*entry)
	if !c.now().Before(e.expires) {
		return "", false
	}
	return e.answer, true
}

func (c *Captcha) removeLocked(el *list.Element) {
	delete(c.items, el.Value.(*entry).id)
	c.order.Remove(el)
}

func (c *Captcha) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

// randomDigits 取 n 位随机数字（crypto/rand，拒绝采样避免取模偏差）。
func randomDigits(n int) (string, error) {
	out := make([]byte, 0, n)
	var b [1]byte
	for len(out) < n {
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("captcha: answer: %w", err)
		}
		if b[0] >= 250 {
			continue
		}
		out = append(out, '0'+b[0]%10)
	}
	return string(out), nil
}

// ---- 绘制 ----

// glyphs 是本项目自己定义的 5×7 点阵数字，每个数字 7 行、每行 5 格，'#' 表示有点。
const glyphs = "" +
	".###." + "#...#" + "#...#" + "#...#" + "#...#" + "#...#" + ".###." + // 0
	"..#.." + ".##.." + "..#.." + "..#.." + "..#.." + "..#.." + ".###." + // 1
	".###." + "#...#" + "....#" + "...#." + "..#.." + ".#..." + "#####" + // 2
	"#####" + "...#." + "..#.." + "...#." + "....#" + "#...#" + ".###." + // 3
	"...#." + "..##." + ".#.#." + "#..#." + "#####" + "...#." + "...#." + // 4
	"#####" + "#...." + "####." + "....#" + "....#" + "#...#" + ".###." + // 5
	"..##." + ".#..." + "#...." + "####." + "#...#" + "#...#" + ".###." + // 6
	"#####" + "....#" + "...#." + "..#.." + ".#..." + ".#..." + ".#..." + // 7
	".###." + "#...#" + "#...#" + ".###." + "#...#" + "#...#" + ".###." + // 8
	".###." + "#...#" + "#...#" + ".####" + "....#" + "...#." + ".##.." // 9

const glyphCols, glyphRows = 5, 7

func glyphOn(d byte, row, col int) bool {
	return glyphs[int(d-'0')*glyphCols*glyphRows+row*glyphCols+col] == '#'
}

func between(r *mrand.Rand, lo, hi float64) float64 { return lo + r.Float64()*(hi-lo) }

// render 画出答案：浅色底，每位数字随机缩放、错切、旋转、错位，每个点随机抖动；
// 整张图再做正弦波扭曲，最后加两条干扰曲线和噪点。
func render(answer string, r *mrand.Rand) *image.RGBA {
	bg := color.RGBA{channel(r, 228, 26), channel(r, 228, 26), channel(r, 228, 26), 255}
	fg := darkColor(r)

	src := image.NewRGBA(image.Rect(0, 0, Width, Height))
	fill(src, bg)
	const margin = 8.0
	cell := (Width - 2*margin) / float64(len(answer))
	for i := 0; i < len(answer); i++ {
		s := between(r, 3.8, 4.3) // 点距：字宽 5 格不超过一格位置，相邻数字不重叠
		theta := between(r, -0.28, 0.28)
		shear := between(r, -0.2, 0.2)
		cx := margin + cell*(float64(i)+0.5) + between(r, -2, 2)
		cy := Height/2 + between(r, -4, 4)
		sin, cos := math.Sincos(theta)
		col := fg
		if r.IntN(3) == 0 {
			col = darkColor(r)
		}
		for row := range glyphRows {
			for c := range glyphCols {
				if !glyphOn(answer[i], row, c) {
					continue
				}
				x := (float64(c) - 2) * s
				y := (float64(row) - 3) * s
				x += shear * y
				px := cx + x*cos - y*sin + between(r, -0.12, 0.12)*s
				py := cy + x*sin + y*cos + between(r, -0.12, 0.12)*s
				disc(src, px, py, s*between(r, 0.52, 0.62), col)
			}
		}
	}

	// 正弦波扭曲
	dst := image.NewRGBA(src.Bounds())
	ax, ay := between(r, 1, 2), between(r, 1.5, 3)
	lx, ly := between(r, 30, 60), between(r, 40, 80)
	px, py := between(r, 0, 2*math.Pi), between(r, 0, 2*math.Pi)
	for y := range Height {
		for x := range Width {
			sx := int(math.Round(float64(x) + ax*math.Sin(2*math.Pi*float64(y)/lx+px)))
			sy := int(math.Round(float64(y) + ay*math.Sin(2*math.Pi*float64(x)/ly+py)))
			if sx < 0 || sx >= Width || sy < 0 || sy >= Height {
				dst.SetRGBA(x, y, bg)
				continue
			}
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}

	// 干扰曲线：横穿全图，比数字的笔画细；一条与数字同色，一条随机色
	for i := range 2 {
		y0, amp := between(r, 12, Height-12), between(r, 4, 10)
		l, ph, w := between(r, 50, 120), between(r, 0, 2*math.Pi), between(r, 0.5, 0.75)
		col := fg
		if i == 1 {
			col = noiseColor(r)
		}
		for x := 0.0; x < Width; x += 0.5 {
			disc(dst, x, y0+amp*math.Sin(2*math.Pi*x/l+ph), w, col)
		}
	}
	// 噪点
	for range 45 {
		disc(dst, between(r, 0, Width), between(r, 0, Height), between(r, 0.6, 1.4), noiseColor(r))
	}
	return dst
}

// darkColor 随机取一个深色（和浅色底对比足够）。
func darkColor(r *mrand.Rand) color.RGBA {
	c := [3]uint8{channel(r, 20, 60), channel(r, 20, 60), channel(r, 20, 60)}
	c[r.IntN(3)] = channel(r, 80, 60) // 让一个通道偏亮，带点颜色
	return color.RGBA{c[0], c[1], c[2], 255}
}

func noiseColor(r *mrand.Rand) color.RGBA {
	return color.RGBA{channel(r, 80, 120), channel(r, 80, 120), channel(r, 80, 120), 255}
}

// channel 取 [lo, lo+n) 里的一个颜色分量，截到 0–255。
func channel(r *mrand.Rand, lo, n int) uint8 {
	v := lo + r.IntN(n)
	switch {
	case v < 0:
		return 0
	case v > 255:
		return 255
	}
	return uint8(v)
}

func fill(img *image.RGBA, c color.RGBA) {
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
}

// disc 画实心圆（中心在 (cx, cy)，半径 rad），超出图片的部分裁掉。
func disc(img *image.RGBA, cx, cy, rad float64, c color.RGBA) {
	minX, maxX := int(math.Floor(cx-rad)), int(math.Ceil(cx+rad))
	minY, maxY := int(math.Floor(cy-rad)), int(math.Ceil(cy+rad))
	b := img.Bounds()
	for y := max(minY, b.Min.Y); y <= min(maxY, b.Max.Y-1); y++ {
		for x := max(minX, b.Min.X); x <= min(maxX, b.Max.X-1); x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			if dx*dx+dy*dy <= rad*rad {
				img.SetRGBA(x, y, c)
			}
		}
	}
}
