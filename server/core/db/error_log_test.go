package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

func TestSQLLog_178_DriverValuesAreNotLogged(t *testing.T) {
	const value = "private-fixture-value"
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&mysqldrv.MySQLError{Number: 1062, Message: "Duplicate entry '" + value + "'"}, "mysql error 1062"},
		{fmt.Errorf("outer %s: %w", value, &mysqldrv.MySQLError{Number: 1366, Message: value}), "mysql error 1366"},
		{errors.New(value), "database error (*errors.errorString)"},
		{fmt.Errorf("%s: %w", value, context.Canceled), "database operation canceled"},
		{fmt.Errorf("%s: %w", value, context.DeadlineExceeded), "database operation timed out"},
	} {
		var buf bytes.Buffer
		logger := newSlogLogger(slog.New(slog.NewJSONHandler(&buf, nil)), time.Second)
		logger.Trace(context.Background(), time.Now(), func() (string, int64) { return sqlTemplateMark + "INSERT INTO fixture (value) VALUES (?)", 0 }, tc.err)
		text := buf.String()
		require.NotContains(t, text, value)
		require.Contains(t, text, tc.want)
		require.Contains(t, text, "VALUES (?)")
		require.Contains(t, text, "error_log_test.go")
		require.Contains(t, tc.err.Error(), value, "记录日志不能改写原错误")
	}
}
