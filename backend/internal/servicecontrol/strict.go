package servicecontrol

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

const FrameBytes = 16384
const FrameDepth = 8

func decodeFrame(frame []byte) (map[string]any, error) {
	if len(frame) == 0 || len(frame) > FrameBytes || frame[len(frame)-1] != '\n' || bytes.IndexByte(frame[:len(frame)-1], '\n') >= 0 || !utf8.Valid(frame) {
		return nil, ErrFrame
	}
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.UseNumber()
	value, err := readJSONValue(decoder, 1)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, ErrFrame
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, ErrSchema
	}
	return object, nil
}

// field名はunescape後に重複検査し、struct unmarshalの大小文字照合を使わない。
func readJSONValue(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, ErrFrame
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	// root objectは1。新しいobject/arrayだけが加算対象で、scalar/keyは増やさない。
	if depth > FrameDepth {
		return nil, ErrDepth
	}
	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return nil, ErrFrame
			}
			name, ok := key.(string)
			if !ok {
				return nil, ErrFrame
			}
			if _, exists := object[name]; exists {
				return nil, ErrFrame
			}
			value, err := readJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[name] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrFrame
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := readJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrFrame
		}
		return array, nil
	default:
		return nil, ErrFrame
	}
}
