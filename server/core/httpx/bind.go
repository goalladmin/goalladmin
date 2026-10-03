package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

var tagNameOnce sync.Once

// useJSONFieldNames 让校验错误里的字段名用 json tag（lowerCamel），而不是 Go 字段名。
func useJSONFieldNames() {
	tagNameOnce.Do(func() {
		v, ok := binding.Validator.Engine().(*validator.Validate)
		if !ok {
			return
		}
		v.RegisterTagNameFunc(func(f reflect.StructField) string {
			for _, tag := range []string{"json", "form", "uri"} {
				name := strings.SplitN(f.Tag.Get(tag), ",", 2)[0]
				if name != "" && name != "-" {
					return name
				}
			}
			return f.Name
		})
	})
}

// Bind 按 Content-Type 绑定并校验请求参数（JSON / form / query）。
// 校验失败返回 CodeValidation 并带字段明细；无法解析返回 CodeBadRequest。
func Bind(c *gin.Context, obj any) error {
	if jsonContentType(c) {
		return BindJSON(c, obj)
	}
	useJSONFieldNames()
	return toBindError(c.ShouldBind(obj))
}

// jsonContentType 报告请求声明的类型是不是 JSON：application/json，或以 +json 结尾的类型（D-095）。
func jsonContentType(c *gin.Context) bool {
	if c.Request == nil {
		return false
	}
	mt, _, err := mime.ParseMediaType(c.Request.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// errNotJSON：请求体没有声明成 JSON。操作日志按声明的类型记请求体，绑定也只认这一种，
// 绑定和记录使用同一份声明成 JSON 的文档（D-095、D-108）。
func errNotJSON() error {
	return ErrBadRequest.WithCause(errors.New("请求体的 Content-Type 不是 application/json"))
}

// BindJSON 只从 JSON 请求体绑定。Content-Type 不是 JSON 的请求不解析，回 CodeBadRequest（D-095）。
func BindJSON(c *gin.Context, obj any) error {
	return BindJSONStrict(c, obj)
}

// BindJSONStrict 只从 JSON 请求体绑定，并且拒绝结构体里没有的字段（回 CodeBadRequest）。
// 用在"只允许改某几项"的接口上：请求里夹带了不允许改的字段时明确报错，而不是悄悄忽略（D-025）。
// 和 BindJSON 一样只收声明成 JSON 的请求体（D-095）。
func BindJSONStrict(c *gin.Context, obj any) error {
	useJSONFieldNames()
	if c.Request == nil || c.Request.Body == nil {
		return ErrBadRequest
	}
	if !jsonContentType(c) {
		return errNotJSON()
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return toBindError(err)
	}
	if err := checkJSONDocument(raw, reflect.TypeOf(obj)); err != nil {
		return toBindError(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(obj); err != nil {
		return toBindError(err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return toBindError(err)
		}
		return ErrBadRequest.WithCause(errors.New("请求体里有多余的内容"))
	}
	if binding.Validator == nil {
		return nil
	}
	return toBindError(binding.Validator.ValidateStruct(obj))
}

// BindQuery 只从查询串绑定。
func BindQuery(c *gin.Context, obj any) error {
	useJSONFieldNames()
	return toBindError(c.ShouldBindQuery(obj))
}

// BindURI 从路径参数绑定。
func BindURI(c *gin.Context, obj any) error {
	useJSONFieldNames()
	return toBindError(c.ShouldBindUri(obj))
}

func toBindError(err error) error {
	if err == nil {
		return nil
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return ErrBodyTooLarge.WithCause(err)
	}
	var ve validator.ValidationErrors
	if errors.As(err, &ve) {
		fields := make([]FieldError, 0, len(ve))
		for _, fe := range ve {
			fields = append(fields, ruleField(fe))
		}
		return ErrValidation.WithFields(fields...).WithCause(err)
	}
	return ErrBadRequest.WithCause(err)
}

// ruleField 把 validator 的规则翻成字段错误：翻译键 validation.<规则>，参数 param（D-026）。
// 业务方需要更细的说明时自己返回带键的 FieldError。
func ruleField(fe validator.FieldError) FieldError {
	tag, param := fe.Tag(), fe.Param()
	var msg string
	switch tag {
	case "required":
		msg = "required"
	case "min":
		msg = "must be at least " + param
	case "max":
		msg = "must be at most " + param
	case "len":
		msg = "length must be " + param
	case "email":
		msg = "must be a valid email"
	case "oneof":
		msg = "must be one of: " + param
	case "gte":
		msg = "must be >= " + param
	case "lte":
		msg = "must be <= " + param
	default:
		if param != "" {
			return NewField(fe.Field(), "validation.rule", "failed rule "+tag+"="+param, "rule", tag, "param", param)
		}
		return NewField(fe.Field(), "validation.rule", "failed rule "+tag, "rule", tag, "param", "")
	}
	if param == "" {
		return NewField(fe.Field(), "validation."+tag, msg)
	}
	return NewField(fe.Field(), "validation."+tag, msg, "param", param)
}
