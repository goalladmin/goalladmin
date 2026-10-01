package captcha

import (
	"bytes"
	"encoding/base64"
	"image/png"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestGenerate_ImageAndAnswer(t *testing.T) {
	c := New()
	ids := map[string]bool{}
	for range 20 {
		id, url, err := c.Generate()
		require.NoError(t, err)
		require.Len(t, id, 2*idBytes)
		require.False(t, ids[id], "ID 不能重复")
		ids[id] = true

		ans := c.Peek(id)
		require.Len(t, ans, Length)
		require.Equal(t, strings.Trim(ans, "0123456789"), "", "答案只有数字")

		require.True(t, strings.HasPrefix(url, "data:image/png;base64,"))
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
		require.NoError(t, err)
		img, err := png.Decode(bytes.NewReader(raw))
		require.NoError(t, err)
		require.Equal(t, Width, img.Bounds().Dx())
		require.Equal(t, Height, img.Bounds().Dy())
	}
}

func TestVerify_OneShot(t *testing.T) {
	c := New()
	id, _, err := c.Generate()
	require.NoError(t, err)
	ans := c.Peek(id)

	require.True(t, c.Verify(id, " "+ans+" "), "首尾空白忽略")
	require.False(t, c.Verify(id, ans), "用过一次就作废")

	id, _, err = c.Generate()
	require.NoError(t, err)
	ans = c.Peek(id)
	wrong := "00000"
	if ans == wrong {
		wrong = "11111"
	}
	require.False(t, c.Verify(id, wrong))
	require.False(t, c.Verify(id, ans), "答错也作废，不能接着猜")

	require.False(t, c.Verify("", "12345"))
	require.False(t, c.Verify("unknown", "12345"))
	id, _, err = c.Generate()
	require.NoError(t, err)
	require.False(t, c.Verify(id, ""), "空答案不通过")
}

func TestVerify_Expires(t *testing.T) {
	clk := &clock{t: time.Unix(1_000_000, 0)}
	c := newCaptcha(clk.now, 5*time.Minute, 100)
	id, _, err := c.Generate()
	require.NoError(t, err)
	ans := c.Peek(id)

	clk.t = clk.t.Add(5 * time.Minute)
	require.Equal(t, "", c.Peek(id))
	require.False(t, c.Verify(id, ans), "过期不通过")
}

func TestStore_BoundedAndSweeps(t *testing.T) {
	clk := &clock{t: time.Unix(1_000_000, 0)}
	c := newCaptcha(clk.now, time.Minute, 3)
	ids := make([]string, 0, 5)
	for range 5 {
		id, _, err := c.Generate()
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.Equal(t, 3, c.size(), "满了丢最早的")
	require.Equal(t, "", c.Peek(ids[0]))
	require.Equal(t, "", c.Peek(ids[1]))
	require.NotEqual(t, "", c.Peek(ids[4]))

	// 过期的在下次生成时清掉
	clk.t = clk.t.Add(2 * time.Minute)
	_, _, err := c.Generate()
	require.NoError(t, err)
	require.Equal(t, 1, c.size())
}

func TestConcurrentUse(t *testing.T) {
	c := New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	passed := 0
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _, err := c.Generate()
			if err != nil {
				return
			}
			ans := c.Peek(id)
			// 同一个 ID 并发校验两次，只能有一次通过
			var inner sync.WaitGroup
			for range 2 {
				inner.Add(1)
				go func() {
					defer inner.Done()
					if c.Verify(id, ans) {
						mu.Lock()
						passed++
						mu.Unlock()
					}
				}()
			}
			inner.Wait()
		}()
	}
	wg.Wait()
	require.Equal(t, 32, passed)
	require.Equal(t, 0, c.size())
}

func TestRandomDigits_Distribution(t *testing.T) {
	counts := map[rune]int{}
	for range 2000 {
		s, err := randomDigits(Length)
		require.NoError(t, err)
		for _, ch := range s {
			counts[ch]++
		}
	}
	require.Len(t, counts, 10)
	for d, n := range counts {
		// 期望每个数字 1000 次；放宽到 ±30%，只防明显的偏差
		require.InDelta(t, 1000, n, 300, "数字 %c", d)
	}
}

func TestRender_DrawsDigitsAndVaries(t *testing.T) {
	dark := func(ans string, seed uint64) int {
		img := render(ans, mrand.New(mrand.NewPCG(seed, seed)))
		n := 0
		for i := 0; i < len(img.Pix); i += 4 {
			if int(img.Pix[i])+int(img.Pix[i+1])+int(img.Pix[i+2]) < 3*170 {
				n++
			}
		}
		return n
	}
	// 数字确实画上去了：深色像素远多于只有干扰线和噪点时
	require.Greater(t, dark("88888", 1), dark("11111", 1))
	require.Greater(t, dark("12345", 2), 500)

	a := render("12345", mrand.New(mrand.NewPCG(3, 3)))
	b := render("12345", mrand.New(mrand.NewPCG(4, 4)))
	require.NotEqual(t, a.Pix, b.Pix, "同样的答案每次画得不一样")
}

func TestGlyphs_AllDefined(t *testing.T) {
	require.Len(t, glyphs, 10*glyphRows*glyphCols)
	require.Equal(t, "", strings.Trim(glyphs, ".#"))
	for d := byte('0'); d <= '9'; d++ {
		on := 0
		for row := range glyphRows {
			for col := range glyphCols {
				if glyphOn(d, row, col) {
					on++
				}
			}
		}
		require.Greater(t, on, 8, "数字 %c", d)
	}
}
