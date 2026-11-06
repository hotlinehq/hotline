package read

import (
	"maps"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Form reads a form body, with bracketed names nested as Stripe writes
// them: items[0][price]=p1 is {"items": [{"price": "p1"}]}. Every value is
// text.
func Form(data []byte) (map[string]any, error) {
	values, err := url.ParseQuery(string(data))
	if err != nil {
		return nil, err
	}
	return nest(values), nil
}

// nest reads bracketed names into objects and lists. A name with no
// brackets keeps its first value.
func nest(values url.Values) map[string]any {
	root := map[string]any{}
	for _, key := range slices.Sorted(maps.Keys(values)) {
		path := splitBrackets(key)
		for _, value := range values[key] {
			put(root, path, value)
		}
	}
	return lists(root).(map[string]any)
}

func splitBrackets(key string) []string {
	name, rest, found := strings.Cut(key, "[")
	path := []string{name}
	for found {
		var part string
		part, rest, _ = strings.Cut(rest, "]")
		path = append(path, part)
		_, rest, found = strings.Cut(rest, "[")
	}
	return path
}

func put(m map[string]any, path []string, value string) {
	key := path[0]
	if key == "" {
		key = strconv.Itoa(len(m))
	}
	if len(path) == 1 {
		if _, set := m[key]; !set {
			m[key] = value
		}
		return
	}
	child, ok := m[key].(map[string]any)
	if !ok {
		child = map[string]any{}
		m[key] = child
	}
	put(child, path[1:], value)
}

// lists turns objects whose names are all numbers into lists, in the
// order of the numbers.
func lists(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for key, child := range m {
		m[key] = lists(child)
	}
	indexes := make([]int, 0, len(m))
	for key := range m {
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || strconv.Itoa(i) != key {
			return m
		}
		indexes = append(indexes, i)
	}
	if len(indexes) == 0 {
		return m
	}
	sort.Ints(indexes)
	list := make([]any, len(indexes))
	for i, index := range indexes {
		list[i] = m[strconv.Itoa(index)]
	}
	return list
}
