package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	mysqldrv "github.com/go-sql-driver/mysql"
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

func TestDatabaseError_178_RedactedInLogFormats(t *testing.T) {
	const value = "private-fixture-value"
	err := fmt.Errorf("context %s: %w", value, &mysqldrv.MySQLError{Number: 1366, Message: value})
	for _, format := range []string{"json", "text"} {
		var buf bytes.Buffer
		New("error", format, &buf).Error("failure", "err", err)
		require.NotContains(t, buf.String(), value)
		require.Contains(t, buf.String(), "mysql error 1366")
	}
	require.Equal(t, "plain diagnosis", ErrorText(errors.New("plain diagnosis")))
	require.Contains(t, err.Error(), value)
}
