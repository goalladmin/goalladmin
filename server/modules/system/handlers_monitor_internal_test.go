package system

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 缓存过期时同时到达的请求只让一个去查库（D-043）：其余等它的结果，不会一起冲到数据库上。
func TestSecurityCache_SingleFlight(t *testing.T) {
	var cache securityCache
	var computed atomic.Int32
	now := time.Now()
	clock := func() time.Time { return now }
	compute := func() (monitorSecurityResponse, error) {
		computed.Add(1)
		time.Sleep(50 * time.Millisecond) // 模拟查库
		return monitorSecurityResponse{Sessions: 7}, nil
	}
	var wg sync.WaitGroup
	var bad atomic.Int32
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := cache.load("platform", clock, 10*time.Second, compute)
			if err != nil || resp.Sessions != 7 {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, bad.Load())
	require.EqualValues(t, 1, computed.Load(), "50 个并发未命中只能查一次库")

	// 另一个端各算各的；过期后再算一次
	_, err := cache.load("other", clock, 10*time.Second, compute)
	require.NoError(t, err)
	require.EqualValues(t, 2, computed.Load())
	now = now.Add(11 * time.Second)
	_, err = cache.load("platform", clock, 10*time.Second, compute)
	require.NoError(t, err)
	require.EqualValues(t, 3, computed.Load())
	// 缓存时长为 0 表示不缓存
	_, err = cache.load("platform", clock, 0, compute)
	require.NoError(t, err)
	require.EqualValues(t, 4, computed.Load())

	// 查库失败：同时排队的请求一起失败，只查一次；之后的新请求再查
	now = now.Add(11 * time.Second)
	var failed atomic.Int32
	release := make(chan struct{})
	boom := func() (monitorSecurityResponse, error) {
		failed.Add(1)
		<-release // 等其余请求都排到队里再失败
		return monitorSecurityResponse{}, errBoom
	}
	var errs atomic.Int32
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.load("platform", clock, 10*time.Second, boom); errors.Is(err, errBoom) {
				errs.Add(1)
			}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	require.EqualValues(t, 20, errs.Load())
	require.EqualValues(t, 1, failed.Load(), "一起排队的请求只让第一个去查，失败了一起失败")
	_, err = cache.load("platform", clock, 10*time.Second, compute)
	require.NoError(t, err, "后来的请求重新查")
	require.EqualValues(t, 5, computed.Load())
}

var errBoom = errors.New("boom")
