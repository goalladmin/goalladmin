package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/conf"
	"github.com/goalladmin/goalladmin/server/core/db"
	"github.com/goalladmin/goalladmin/server/core/httpx"
	"github.com/goalladmin/goalladmin/server/core/logx"
)

func readinessResponse(t *testing.T, a *App, path, id, lang string) (*httptest.ResponseRecorder, httpx.Envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set(httpx.HeaderRequestID, id)
	req.Header.Set("Accept-Language", lang)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, req)
	var env httpx.Envelope
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, id, w.Header().Get(httpx.HeaderRequestID))
	require.Equal(t, id, env.RequestID)
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	return w, env
}

func TestReadinessHTTP_173_ResponseMetadata(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		name := "failure"
		if healthy {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			a, err := New(conf.Default(), WithDB(nil), WithLogger(logx.New("error", "text", io.Discard)))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
			now := time.Now()
			a.readiness.now = func() time.Time { return now }
			var calls atomic.Int32
			a.readiness.ping = func(context.Context) error {
				calls.Add(1)
				if healthy {
					return nil
				}
				return errors.New("private database endpoint")
			}
			code := httpx.CodeUnavailable
			if healthy {
				code = httpx.CodeOK
			}
			for _, lang := range []string{httpx.LangEN, httpx.LangZH} {
				w, env := readinessResponse(t, a, readyPath, "readiness-173-"+lang, lang)
				require.Equal(t, httpx.StatusOf(code), w.Code)
				require.Equal(t, code, env.Code)
				require.Equal(t, httpx.Message(code, lang), env.Msg)
				require.NotContains(t, w.Body.String(), "private database endpoint")
				if healthy {
					require.Equal(t, map[string]any{"status": "ok"}, env.Data)
				} else {
					require.Nil(t, env.Data)
				}
			}
			require.EqualValues(t, 1, calls.Load(), "只缓存结果，每个请求仍单独生成响应")
		})
	}
}

func TestReadinessHTTP_173_AppStopCancelsCheck(t *testing.T) {
	a, err := New(conf.Default(), WithDB(nil), WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
	started, finished := make(chan struct{}), make(chan struct{})
	a.readiness.ping = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, readyPath, nil))
		response <- w
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("readiness check did not start")
	}
	require.NoError(t, a.Stop(context.Background()))
	select {
	case <-finished:
	default:
		t.Fatal("App.Stop returned before the check finished")
	}
	require.Equal(t, http.StatusServiceUnavailable, (<-response).Code)
	w, _ := readinessResponse(t, a, readyPath, "stopped-ready-173", httpx.LangEN)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestReadinessHTTP_173_MySQLPoolRecovery(t *testing.T) {
	gdb := db.OpenTestDB(t)
	a, err := New(conf.Default(), WithDB(gdb), WithLogger(logx.New("error", "text", io.Discard)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, a.Stop(context.Background())) })
	var tick atomic.Int64
	tick.Store(time.Now().UnixNano())
	a.readiness.now = func() time.Time { return time.Unix(0, tick.Load()) }
	w, env := readinessResponse(t, a, readyPath, "mysql-ready-173", httpx.LangEN)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, httpx.CodeOK, env.Code)

	pool, err := gdb.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	held, err := pool.Conn(ctx)
	require.NoError(t, err)
	defer func() { _ = held.Close() }()
	waits := pool.Stats().WaitCount
	for range 10 {
		w, _ = readinessResponse(t, a, readyPath, "mysql-cached-173", httpx.LangEN)
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.Equal(t, waits, pool.Stats().WaitCount, "缓存命中不再向连接池申请连接")

	tick.Add(int64(readinessTTL))
	w, env = readinessResponse(t, a, readyPath, "mysql-busy-173", httpx.LangEN)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, httpx.CodeUnavailable, env.Code)
	require.Equal(t, waits+1, pool.Stats().WaitCount)
	for range 10 {
		w, _ = readinessResponse(t, a, readyPath, "mysql-failed-173", httpx.LangEN)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
	}
	w, env = readinessResponse(t, a, healthPath, "mysql-live-173", httpx.LangEN)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, httpx.CodeOK, env.Code)
	require.Equal(t, waits+1, pool.Stats().WaitCount, "失败缓存和存活探针都不再次查库")

	require.NoError(t, held.Close())
	w, _ = readinessResponse(t, a, readyPath, "mysql-wait-173", httpx.LangEN)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "恢复后先保留剩余缓存期")
	tick.Add(int64(readinessTTL))
	w, env = readinessResponse(t, a, readyPath, "mysql-restored-173", httpx.LangEN)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, httpx.CodeOK, env.Code)
}
