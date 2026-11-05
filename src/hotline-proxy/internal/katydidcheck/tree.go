package katydidcheck

import (
	"errors"
	"math"
	"math/big"

	"go.katydid.org.za/parser-go/cast"
	"go.katydid.org.za/parser-go/parse"
)

// buildTree reads the body into plain values (maps, lists, strings, numbers), so the JSON held in its strings can be found.
func buildTree(p parse.Parser) (any, error) {
	h, err := p.Next()
	if err != nil {
		return nil, err
	}
	return buildFrom(p, h)
}

func buildFrom(p parse.Parser, h parse.Hint) (any, error) {
	switch h {
	case parse.ValueHint:
		kind, bs, err := p.Token()
		if err != nil {
			return nil, err
		}
		return scalarOf(kind, bs), nil
	case parse.EnterHint:
		return buildContainer(p)
	}
	return nil, errUnexpectedToken
}

func buildContainer(p parse.Parser) (any, error) {
	var obj map[string]any
	var list []any
	var tagged any
	hasTag := false
	for {
		h, err := p.Next()
		if err != nil {
			return nil, err
		}
		switch h {
		case parse.LeaveHint:
			switch {
			case hasTag:
				return tagged, nil
			case list != nil:
				return list, nil
			case obj != nil:
				return obj, nil
			}
			return map[string]any{}, nil
		case parse.FieldHint:
			kind, bs, err := p.Token()
			if err != nil {
				return nil, err
			}
			name := ""
			index := -1
			switch kind {
			case parse.Int64Kind:
				var i int64
				cast.ToInt64Ptr(bs, &i)
				index = int(i)
			default:
				name = string(bs)
			}
			ch, err := p.Next()
			if err != nil {
				return nil, err
			}
			child, err := buildFrom(p, ch)
			if err != nil {
				return nil, err
			}
			switch {
			case kind == parse.TagKind:
				if m, empty := child.(map[string]any); empty && len(m) == 0 && name == "array" {
					child = []any{}
				}
				tagged, hasTag = child, true
			case index >= 0:
				for len(list) <= index {
					list = append(list, nil)
				}
				list[index] = child
			default:
				if obj == nil {
					obj = map[string]any{}
				}
				obj[name] = child
			}
		default:
			return nil, errUnexpectedToken
		}
	}
}

func scalarOf(kind parse.Kind, bs []byte) any {
	switch kind {
	case parse.NullKind:
		return nil
	case parse.TrueKind:
		return true
	case parse.FalseKind:
		return false
	case parse.Int64Kind:
		var i int64
		cast.ToInt64Ptr(bs, &i)
		return new(big.Rat).SetInt64(i)
	case parse.Float64Kind:
		var u uint64
		cast.ToFloat64BitsPtr(bs, &u)
		return new(big.Rat).SetFloat64(math.Float64frombits(u))
	case parse.DecimalKind:
		if n, ok := new(big.Rat).SetString(string(bs)); ok {
			return n
		}
	}
	return string(bs)
}

var errUnexpectedToken = errors.New("unexpected token while reading the body") //nolint:gochecknoglobals // one fixed message
