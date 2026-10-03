package auditimpl

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	mysqldrv "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"

	"github.com/goalladmin/goalladmin/server/core/httpx"
)

func TestErrorText_178_DatabaseValuesNotPersisted(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	err := &mysqldrv.MySQLError{Number: 1366, Message: "private-fixture-value"}
	c.Set(httpx.KeyResponseErr, httpx.ErrInternal.WithCause(err))
	require.Equal(t, "mysql error 1366", errorText(c, httpx.CodeInternal))
	require.Equal(t, "private-fixture-value", err.Message)
}
