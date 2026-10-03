package httpx

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestJSONDocument_190_SingleMeaning(t *testing.T) {
	type item struct {
		Label string `json:"label"`
	}
	type request struct {
		Label string          `json:"label"`
		Items []item          `json:"items"`
		Tags  map[string]item `json:"tags"`
	}
	typ := reflect.TypeFor[request]()
	for _, raw := range []string{
		`{"label":"one","label":"two"}`,
		`{"label":"one","LABEL":"two"}`,
		`{"items":[{"label":"one","Label":"two"}]}`,
		`{"tags":{"a":{"label":"one","label":"two"}}}`,
		`{"tags":{"a":{},"a":{}}}`,
		`{} {}`,
		`{"items":`,
		strings.Repeat("[", 66) + "0" + strings.Repeat("]", 66),
	} {
		require.Error(t, checkJSONDocument([]byte(raw), typ), raw)
	}
	for _, raw := range []string{
		`{"label":"one","items":[{"label":"two"}],"tags":{"a":{"label":"three"},"A":{"label":"four"}}}`,
		`{"LABEL":"one"}`,
		`{"tags":{"a":{},"A":{}}}`,
	} {
		require.NoError(t, checkJSONDocument([]byte(raw), typ), raw)
	}
}

func TestBindJSON_190_StrictDocument(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, bind := range []func(*gin.Context, any) error{BindJSON, BindJSONStrict, Bind} {
		for _, raw := range []string{`{"label":"one","label":"two"}`, `{"label":"one","LABEL":"two"}`, `{"label":"one","other":true}`, `{} {}`} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("PUT", "/", strings.NewReader(raw))
			c.Request.Header.Set("Content-Type", "application/json")
			var req struct {
				Label string `json:"label"`
			}
			require.ErrorIs(t, bind(c, &req), ErrBadRequest)
		}
	}
}
