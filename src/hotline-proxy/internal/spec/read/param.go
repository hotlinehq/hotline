package read

import (
	"net/http"
	"net/url"
	"strings"
)

// PathValue is the path value in the piece of the escaped path split at
// "/" numbered segment, unescaped. With rest, it takes the rest of the
// path from there. ok is false when the path has no such piece.
func PathValue(escapedPath string, segment int, rest bool) (string, bool) {
	if segment < 0 {
		return "", false
	}
	pieces := strings.Split(strings.TrimSuffix(escapedPath, "/"), "/")
	if segment >= len(pieces) {
		return "", false
	}
	value := pieces[segment]
	if rest {
		value = strings.Join(pieces[segment:], "/")
	}
	// The proxy only forwards paths that unescape.
	unescaped, _ := url.PathUnescape(value)
	return unescaped, true
}

// HeaderValue is a header's value, a header sent more than once joined
// with ", ". ok is false when it is not sent.
func HeaderValue(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", false
	}
	return strings.Join(values, ", "), true
}

// Cookies are the cookies a request sends, by name, each value as
// written. A name sent more than once keeps every value, in order.
func Cookies(header http.Header) map[string][]string {
	cookies := map[string][]string{}
	for _, cookie := range (&http.Request{Header: header}).Cookies() {
		cookies[cookie.Name] = append(cookies[cookie.Name], cookie.Value)
	}
	return cookies
}

// DeepObject reads name[key]=value query pairs into an object, nil when
// there are none.
func DeepObject(query url.Values, name string) any {
	found := url.Values{}
	for key, values := range query {
		if rest, ok := strings.CutPrefix(key, name+"["); ok {
			found["v["+rest] = values
		}
	}
	if len(found) == 0 {
		return nil
	}
	return nest(found)["v"]
}

// Shape turns the text values of a parameter into what its schema wants:
// a list when it wants an array, or else the first value. split says the
// list is written with commas, as in a header or a query not exploded.
func Shape(values []string, list, split bool) any {
	if !list {
		return values[0]
	}
	if split {
		values = strings.Split(strings.Join(values, ","), ",")
	}
	items := make([]any, len(values))
	for i, v := range values {
		items[i] = v
	}
	return items
}

// Pairs reads name,value pairs joined with commas into an object, as an
// object cookie not exploded is written. A name with no value gets "".
func Pairs(text string) map[string]any {
	items := strings.Split(text, ",")
	object := make(map[string]any, len(items)/2+1)
	for i := 0; i < len(items); i += 2 {
		value := ""
		if i+1 < len(items) {
			value = items[i+1]
		}
		object[items[i]] = value
	}
	return object
}
