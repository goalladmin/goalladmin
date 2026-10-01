package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFrom_FallsBackToDefault(t *testing.T) {
	require.NotNil(t, From(context.Background()))
	require.NotNil(t, From(nil)) //nolint:staticcheck // 故意验证 nil ctx 不会 panic
}

func TestWith_AppendsAttrsAndWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	l := New("debug", "json", &buf)
	ctx := WithLogger(context.Background(), l)
	ctx = With(ctx, "request_id", "abc123")

	From(ctx).Info("hello", "k", "v")

	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	require.Equal(t, "hello", rec["msg"])
	require.Equal(t, "abc123", rec["request_id"])
	require.Equal(t, "v", rec["k"])
}

func TestNew_LevelFiltersAndTextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New("warn", "text", &buf)
	l.Info("dropped")
	l.Warn("kept")
	require.NotContains(t, buf.String(), "dropped")
	require.Contains(t, buf.String(), "kept")
}

func TestParseLevel(t *testing.T) {
	require.Equal(t, slog.LevelDebug, ParseLevel("DEBUG"))
	require.Equal(t, slog.LevelWarn, ParseLevel("warning"))
	require.Equal(t, slog.LevelError, ParseLevel("error"))
	require.Equal(t, slog.LevelInfo, ParseLevel("whatever"))
}
