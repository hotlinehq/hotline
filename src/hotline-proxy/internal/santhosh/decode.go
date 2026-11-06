package santhosh

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
)

// decodeJSON reads JSON data into the Go data santhosh checks, with its
// numbers kept as json.Number, as written. It reads the data where it is:
// a json.Decoder would first copy it whole into a buffer of its own, more
// memory than the value it gives. Like json.Decoder, it lets a name come
// twice, the last one winning, and takes text that is not UTF-8.
func decodeJSON(data []byte) (any, error) {
	d := jsontext.NewDecoder(bytes.NewBuffer(data), jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	v, err := readValue(d)
	if err != nil {
		return nil, err
	}
	if _, err := d.ReadToken(); !errors.Is(err, io.EOF) {
		return nil, errTrailing
	}
	return v, nil
}

// errTrailing marks JSON data with more after its value.
var errTrailing = errors.New("more data after the JSON value")

// readValue reads the next JSON value from d as plain Go data.
func readValue(d *jsontext.Decoder) (any, error) {
	tok, err := d.ReadToken()
	if err != nil {
		return nil, err
	}
	switch tok.Kind() {
	case jsontext.KindNull:
		return nil, nil //nolint:nilnil // JSON null is a nil value, not a missing one
	case jsontext.KindFalse, jsontext.KindTrue:
		return tok.Bool(), nil
	case jsontext.KindString:
		return tok.String(), nil
	case jsontext.KindNumber:
		return json.Number(tok.String()), nil
	case jsontext.KindBeginObject:
		object := map[string]any{}
		for d.PeekKind() != jsontext.KindEndObject {
			name, err := d.ReadToken()
			if err != nil {
				return nil, err
			}
			key := name.String() // before the next read, which reuses its memory
			if object[key], err = readValue(d); err != nil {
				return nil, err
			}
		}
		_, err = d.ReadToken()
		return object, err
	default: // jsontext.KindBeginArray, as the decoder gives no other kind first
		list := []any{}
		for d.PeekKind() != jsontext.KindEndArray {
			item, err := readValue(d)
			if err != nil {
				return nil, err
			}
			list = append(list, item)
		}
		_, err = d.ReadToken()
		return list, err
	}
}
