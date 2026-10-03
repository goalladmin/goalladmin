package ratelimit

import (
	"container/heap"
	"time"
)

// 单次过期清理有界；积压时允许暂时拒绝新键，不逐出仍在使用的窗口。
const windowGC = 128

// gcLocked 只查看最早的窗口；未到期时不遍历其余记录（D-082）。
func (w *Window) gcLocked(now time.Time) {
	for i := 0; i < windowGC && len(w.expiry) > 0; i++ {
		old := w.expiry[0]
		if now.Sub(old.e.start) < w.window {
			break
		}
		w.removeLocal(old.key, old.e)
	}
}

func (w *Window) trackLocal(key string, e *entry) {
	heap.Push(&w.expiry, windowItem{key: key, e: e})
}

func (w *Window) removeLocal(key string, e *entry) {
	delete(w.m, key)
	if e.index >= 0 {
		heap.Remove(&w.expiry, e.index)
	}
}

type windowItem struct {
	key string
	e   *entry
}

type windowExpiry []windowItem

func (h windowExpiry) Len() int           { return len(h) }
func (h windowExpiry) Less(i, j int) bool { return h[i].e.start.Before(h[j].e.start) }
func (h windowExpiry) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].e.index, h[j].e.index = i, j
}
func (h *windowExpiry) Push(v interface{}) {
	item := v.(windowItem)
	item.e.index = len(*h)
	*h = append(*h, item)
}
func (h *windowExpiry) Pop() interface{} {
	n := len(*h) - 1
	item := (*h)[n]
	(*h)[n] = windowItem{}
	*h = (*h)[:n]
	item.e.index = -1
	return item
}
