package guestaccount

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"unicode/utf8"
)

const fixedLayoutBytes = 256 * 1024
const fixedLayoutDepth = 16

func strictFixedLayout(data []byte) bool {
	if len(data) == 0 || len(data) > fixedLayoutBytes || !utf8.Valid(data) || !fixedLayoutUnicode(data) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	value, e := fixedLayoutValue(d, 0)
	if e != nil {
		return false
	}
	if _, e = d.Token(); e != io.EOF {
		return false
	}
	return fixedLayoutShape(value, reflect.TypeOf(fixedLayoutWire{}))
}
func fixedLayoutValue(d *json.Decoder, depth int) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, ErrUnconfigured
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth >= fixedLayoutDepth {
		return nil, ErrUnconfigured
	}
	switch delim {
	case '{':
		values := make(map[string]any)
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, ErrUnconfigured
			}
			name, ok := token.(string)
			if !ok {
				return nil, ErrUnconfigured
			}
			if _, ok := values[name]; ok {
				return nil, ErrUnconfigured
			}
			value, err := fixedLayoutValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrUnconfigured
		}
		return values, nil
	case '[':
		values := make([]any, 0)
		for d.More() {
			value, err := fixedLayoutValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrUnconfigured
		}
		return values, nil
	}
	return nil, ErrUnconfigured
}

// 型付きwireの全fieldを必須・exact-caseとして照合し、jsonの曖昧なfield照合を避ける。
func fixedLayoutShape(value any, typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok || len(object) != typ.NumField() {
			return false
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			v, ok := object[field.Tag.Get("json")]
			if !ok || !fixedLayoutShape(v, field.Type) {
				return false
			}
		}
		return true
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, v := range object {
			if !fixedLayoutShape(v, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Slice:
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if !fixedLayoutShape(v, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.String:
		_, ok := value.(string)
		return ok
	case reflect.Bool:
		_, ok := value.(bool)
		return ok
	}
	return false
}

// encoding/jsonの不正surrogate置換で、固定path等を別の文字列へ変えません。
// 通常のUTF8文字と、正しく対になったUnicode escapeはそのまま許します。
func fixedLayoutUnicode(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		if i+5 >= len(data) {
			return false
		}
		value, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+11 >= len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			i += 5
		}
	}
	return true
}
