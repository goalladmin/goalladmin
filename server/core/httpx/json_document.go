package httpx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
)

// 文档检查不写入请求结构体；重复字段或尾随内容被拒时不留下部分绑定值。
func checkJSONDocument(raw []byte, typ reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := checkJSONValue(dec, typ, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("JSON document has trailing content")
	}
	return nil
}

func jsonType(typ reflect.Type) reflect.Type {
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ
}

// jsonField 匹配结构体的字段名与大小写别名；动态 map 不调用它，保留业务键的大小写语义。
func jsonField(typ reflect.Type, key string) (string, reflect.Type) {
	return findJSONField(typ, key, map[reflect.Type]bool{})
}

func findJSONField(typ reflect.Type, key string, visited map[reflect.Type]bool) (string, reflect.Type) {
	typ = jsonType(typ)
	if typ == nil || typ.Kind() != reflect.Struct || visited[typ] {
		return key, nil
	}
	visited[typ] = true
	var foldedName string
	var foldedType reflect.Type
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" || (!f.IsExported() && !f.Anonymous) {
			continue
		}
		if f.Anonymous && name == "" && jsonType(f.Type).Kind() == reflect.Struct {
			n, t := findJSONField(f.Type, key, visited)
			if t != nil {
				return n, t
			}
			continue
		}
		if name == "" {
			name = f.Name
		}
		if key == name {
			return name, f.Type
		}
		if foldedType == nil && strings.EqualFold(key, name) {
			foldedName, foldedType = name, f.Type
		}
	}
	if foldedType != nil {
		return foldedName, foldedType
	}
	return key, nil
}

func checkJSONValue(dec *json.Decoder, typ reflect.Type, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting exceeds 64 levels")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	typ = jsonType(typ)
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			token, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok {
				return fmt.Errorf("JSON object key must be a string")
			}
			name, child := jsonField(typ, key)
			if typ != nil && typ.Kind() == reflect.Map {
				child = typ.Elem()
			}
			if seen[name] {
				return fmt.Errorf("JSON object contains a repeated field")
			}
			seen[name] = true
			if err := checkJSONValue(dec, child, depth+1); err != nil {
				return err
			}
		}
	case '[':
		var child reflect.Type
		if typ != nil && (typ.Kind() == reflect.Array || typ.Kind() == reflect.Slice) {
			child = typ.Elem()
		}
		for dec.More() {
			if err := checkJSONValue(dec, child, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = dec.Token()
	return err
}
