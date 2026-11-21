// Package smithy turns an AWS service's Smithy model (AWS's own api
// description language, in its JSON form) into an OpenAPI 3.2 spec Hotline
// can read. Four of AWS's protocols are handled: its JSON one, where every
// call is POST / and the action is named in the X-Amz-Target header; its
// query one, used by SNS and STS, where every call is POST / with a form
// body naming the action in its field Action, and replies are XML; its
// REST and JSON one, used by Lambda and SES, where actions are told apart
// by path and bodies are JSON; and its REST and XML one, used by S3, where
// actions are told apart by path and query words, and bodies are XML.
package smithy

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrUnsupported marks a model this converter can't turn into a spec.
var ErrUnsupported = errors.New("smithy: unsupported model")

const actionHeader = "X-Amz-Target"

type model struct {
	Shapes map[string]shape `json:"shapes"`
}

type shape struct {
	Type       string `json:"type"`
	Version    string `json:"version"`
	Operations []ref  `json:"operations"`
	// A resource's own operations, and the resources inside a service or
	// a resource, as Lambda's model groups its actions.
	Resources            []ref             `json:"resources"`
	CollectionOperations []ref             `json:"collectionOperations"`
	Create               *ref              `json:"create"`
	Put                  *ref              `json:"put"`
	Read                 *ref              `json:"read"`
	Update               *ref              `json:"update"`
	Delete               *ref              `json:"delete"`
	List                 *ref              `json:"list"`
	Input                *ref              `json:"input"`
	Output               *ref              `json:"output"`
	Members              map[string]member `json:"members"`
	Member               *member           `json:"member"`
	Key                  *member           `json:"key"`
	Value                *member           `json:"value"`
	Traits               map[string]any    `json:"traits"`
}

type ref struct {
	Target string `json:"target"`
}

type member struct {
	Target string         `json:"target"`
	Traits map[string]any `json:"traits"`
}

// document is the spec's top level, in the order it is written.
type document struct {
	OpenAPI      string         `yaml:"openapi"`
	Info         map[string]any `yaml:"info"`
	ActionHeader string         `yaml:"x-hotline-action-header"` //nolint:tagliatelle // the name of our OpenAPI extension
	Servers      []any          `yaml:"servers"`
	Security     []any          `yaml:"security"`
	Paths        map[string]any `yaml:"paths"`
	Components   map[string]any `yaml:"components"`
}

// Options say what the proxy copies into its records, by marking it
// x-hotline-record in the spec.
type Options struct {
	// Record maps a field of an action's input, such as QueueUrl, to the
	// record attribute it is copied to, such as messaging.destination.name.
	Record map[string]string
	// Action, when set, lists the action header on every action, with the
	// action's own value, copied to this record attribute, such as
	// messaging.operation.name.
	Action string
}

// Convert reads a Smithy model holding one AWS JSON service and writes its
// OpenAPI spec as YAML.
func Convert(data []byte, options Options) ([]byte, error) {
	var m model
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: not a Smithy JSON model: %w", ErrUnsupported, err)
	}
	serviceID, service, err := m.service()
	if err != nil {
		return nil, err
	}
	var mediaType string
	switch {
	case has(service.Traits, "aws.protocols#awsJson1_0"):
		mediaType = "application/x-amz-json-1.0"
	case has(service.Traits, "aws.protocols#awsJson1_1"):
		mediaType = "application/x-amz-json-1.1"
	case has(service.Traits, "aws.protocols#restXml"):
		return convertRestXML(m, serviceID, service, options)
	case has(service.Traits, "aws.protocols#awsQuery"):
		return convertQuery(m, serviceID, service, options)
	case has(service.Traits, "aws.protocols#restJson1"):
		return convertRestJSON(m, serviceID, service, options)
	default:
		return nil, fmt.Errorf("%w: %s is on none of AWS's JSON, query, REST and JSON, or REST and XML protocols", ErrUnsupported, serviceID)
	}
	c := converter{model: m, mediaType: mediaType, schemas: map[string]any{}, options: options}
	prefix := name(serviceID)

	paths := map[string]any{}
	for _, op := range m.operations(service) {
		action := prefix + "." + name(op.Target)
		paths["/#"+actionHeader+"="+action] = map[string]any{"post": c.operation(op.Target, action)}
	}
	c.schemas["AwsJsonError"] = map[string]any{
		"type":        "object",
		"description": "The body of every AWS JSON error reply: __type names the error.",
		"required":    []string{"__type"},
		"properties": map[string]any{
			"__type":  map[string]any{"type": "string"},
			"message": map[string]any{"type": "string"},
			"Message": map[string]any{"type": "string"},
		},
	}

	title := name(serviceID)
	if t, ok := service.Traits["smithy.api#title"].(string); ok {
		title = t
	}
	doc := document{
		OpenAPI: "3.2.0",
		Info: map[string]any{
			"title":       title,
			"version":     service.Version,
			"description": "Made by Hotline's aws-openapi tool from AWS's Smithy model. Do not edit by hand.",
		},
		ActionHeader: actionHeader,
		Servers:      []any{map[string]any{"url": "https://" + endpointPrefix(service, title) + ".{region}.amazonaws.com", "variables": map[string]any{"region": map[string]any{"default": "us-east-1"}}}},
		Security:     []any{map[string]any{"aws_sigv4": []any{}}},
		Paths:        paths,
		Components: map[string]any{
			"securitySchemes": map[string]any{
				// AWS's signature is a header the proxy never reads.
				"aws_sigv4": map[string]any{"type": "apiKey", "in": "header", "name": "Authorization"},
			},
			"schemas": c.schemas,
		},
	}
	return yaml.Marshal(doc)
}

func (m model) service() (string, shape, error) {
	var ids []string
	for id, s := range m.Shapes {
		if s.Type == "service" {
			ids = append(ids, id)
		}
	}
	if len(ids) != 1 {
		return "", shape{}, fmt.Errorf("%w: the model must hold exactly one service, it holds %d", ErrUnsupported, len(ids))
	}
	return ids[0], m.Shapes[ids[0]], nil
}

// operations lists every action of a service or resource: its own, and
// those of the resources inside it.
func (m model) operations(s shape) []ref {
	ops := append(append([]ref{}, s.Operations...), s.CollectionOperations...)
	for _, r := range []*ref{s.Create, s.Put, s.Read, s.Update, s.Delete, s.List} {
		if r != nil {
			ops = append(ops, *r)
		}
	}
	for _, r := range s.Resources {
		ops = append(ops, m.operations(m.Shapes[r.Target])...)
	}
	return ops
}

func endpointPrefix(service shape, fallback string) string {
	if s, ok := service.Traits["aws.api#service"].(map[string]any); ok {
		if p, ok := s["endpointPrefix"].(string); ok {
			return p
		}
	}
	return strings.ToLower(fallback)
}

type converter struct {
	model     model
	mediaType string
	schemas   map[string]any
	options   Options
	// xml says bodies are XML: AWS's XML names, attributes and lists are
	// written as OpenAPI xml notes, and times as text.
	xml bool
	// xmlMaps says maps are written in XML as AWS's query protocol does: an
	// entry element for each, holding its key and value.
	xmlMaps bool
}

func (c *converter) operation(id, action string) map[string]any {
	op := c.model.Shapes[id]
	requestID := map[string]any{"x-amzn-RequestId": map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false}}
	errorHeaders := map[string]any{
		"x-amzn-RequestId":   map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false},
		"x-amzn-query-error": map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false},
	}
	errorReply := map[string]any{
		"description": "An AWS error.",
		"headers":     errorHeaders,
		"content":     c.content(map[string]any{"$ref": "#/components/schemas/AwsJsonError"}),
	}
	input := c.body(op.Input)
	c.mark(op.Input)
	out := map[string]any{
		"operationId": name(id),
		"requestBody": map[string]any{"required": true, "content": c.content(input)},
		"responses": map[string]any{
			"200": map[string]any{"description": "Done.", "headers": requestID, "content": c.content(c.body(op.Output))},
			"4XX": errorReply,
			"5XX": errorReply,
		},
	}
	if c.options.Action != "" {
		out["parameters"] = []any{map[string]any{
			"name": actionHeader, "in": "header", "required": true,
			"schema":           map[string]any{"type": "string", "const": action},
			"x-hotline-record": c.options.Action,
		}}
	}
	if doc := text(op.Traits); doc != "" {
		out["description"] = doc
	}
	return out
}

// mark marks the fields of an action's input that are copied into records.
// Only inputs are marked, so an output that shares a field's name is not.
func (c *converter) mark(r *ref) {
	if r == nil || r.Target == "smithy.api#Unit" {
		return
	}
	schema, _ := c.schemas[name(r.Target)].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	for field, attribute := range c.options.Record {
		if prop, ok := props[field].(map[string]any); ok {
			prop["x-hotline-record"] = attribute
		}
	}
}

func (c *converter) content(schema any) map[string]any {
	// The proxy reads AWS's own name for JSON as JSON only when marked.
	return map[string]any{c.mediaType: map[string]any{"schema": schema, "x-hotline-body": "json"}}
}

// body is the schema of an action's input or output. An action without
// one still sends or gets an empty JSON object.
func (c *converter) body(r *ref) any {
	if r == nil || r.Target == "smithy.api#Unit" {
		return map[string]any{"type": "object"}
	}
	return c.schemaRef(r.Target)
}

// schemaRef points to a named shape, writing it into the components the
// first time. Smithy's own simple shapes are written in place.
func (c *converter) schemaRef(target string) map[string]any {
	if strings.HasPrefix(target, "smithy.api#") {
		return c.prelude(target)
	}
	n := name(target)
	if _, done := c.schemas[n]; !done {
		c.schemas[n] = nil // marks it as started, for shapes that refer to themselves
		c.schemas[n] = c.shapeSchema(c.model.Shapes[target])
	}
	return map[string]any{"$ref": "#/components/schemas/" + n}
}

func (c *converter) shapeSchema(s shape) map[string]any {
	var out map[string]any
	switch s.Type {
	case "structure":
		out = c.object(s.Members, func(member) bool { return true })
	case "union":
		props := map[string]any{}
		for _, n := range sortedKeys(s.Members) {
			props[c.propName(n, s.Members[n])] = c.memberSchema(n, s.Members[n])
		}
		out = map[string]any{"type": "object", "properties": props, "minProperties": 1, "maxProperties": 1}
	case "list", "set":
		items := c.memberSchema("", *s.Member)
		if c.xml {
			// Each item's element is named member, unless AWS names it.
			itemName := "member"
			if xn, ok := s.Member.Traits["smithy.api#xmlName"].(string); ok {
				itemName = xn
			}
			items["xml"] = map[string]any{"name": itemName}
		}
		out = map[string]any{"type": "array", "items": items}
	case "map":
		if c.xmlMaps {
			out = map[string]any{"type": "array", "items": map[string]any{
				"type": "object",
				"xml":  map[string]any{"name": "entry"},
				"properties": map[string]any{
					c.elementName("key", *s.Key):     c.memberSchema("", *s.Key),
					c.elementName("value", *s.Value): c.memberSchema("", *s.Value),
				},
			}}
			break
		}
		out = map[string]any{"type": "object", "additionalProperties": c.memberSchema("", *s.Value)}
	case "enum":
		var values []string
		for _, n := range sortedKeys(s.Members) {
			v, ok := s.Members[n].Traits["smithy.api#enumValue"].(string)
			if !ok {
				v = n
			}
			values = append(values, v)
		}
		out = map[string]any{"type": "string", "enum": values}
	case "intEnum":
		var values []any
		for _, n := range sortedKeys(s.Members) {
			values = append(values, s.Members[n].Traits["smithy.api#enumValue"])
		}
		out = map[string]any{"type": "integer", "enum": values}
	default:
		out = c.simple(s.Type)
	}
	constrain(out, s.Traits, s.Type)
	if doc := text(s.Traits); doc != "" && s.Type == "structure" {
		out["description"] = doc
	}
	if xn, ok := s.Traits["smithy.api#xmlName"].(string); ok && c.xml && s.Type == "structure" {
		out["xml"] = map[string]any{"name": xn}
	}
	return out
}

// object is the schema of a structure's members that keep says belong in
// its body.
func (c *converter) object(members map[string]member, keep func(member) bool) map[string]any {
	props := map[string]any{}
	var required []string
	for _, n := range sortedKeys(members) {
		mb := members[n]
		if !keep(mb) {
			continue
		}
		props[c.propName(n, mb)] = c.memberSchema(n, mb)
		if has(mb.Traits, "smithy.api#required") {
			required = append(required, c.propName(n, mb))
		}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// propName is a member's name in the body: AWS's JSON name for JSON, the
// member's own name for XML, whose element name goes in the xml notes.
func (c *converter) propName(n string, mb member) string {
	if c.xml {
		return n
	}
	return jsonName(n, mb)
}

// memberSchema is the schema of a structure's member n, or of a list's
// items when n is empty.
func (c *converter) memberSchema(n string, mb member) map[string]any {
	kind := ""
	if strings.HasPrefix(mb.Target, "smithy.api#") {
		kind = strings.ToLower(strings.TrimPrefix(mb.Target, "smithy.api#"))
	} else {
		kind = c.model.Shapes[mb.Target].Type
	}
	var out map[string]any
	if c.xml && n != "" && (kind == "list" || kind == "set") && has(mb.Traits, "smithy.api#xmlFlattened") {
		// A flattened list has no outer element: each item is an element
		// named like the member, so its items are written here.
		list := c.model.Shapes[mb.Target]
		items := c.memberSchema("", *list.Member)
		items["xml"] = map[string]any{"name": c.elementName(n, mb)}
		out = map[string]any{"type": "array", "items": items}
		constrain(out, list.Traits, kind)
	} else {
		out = map[string]any{}
		for k, v := range c.schemaRef(mb.Target) {
			out[k] = v
		}
		if c.xml && n != "" {
			c.xmlNotes(out, n, mb, kind)
		}
	}
	constrain(out, mb.Traits, kind)
	if doc := text(mb.Traits); doc != "" {
		out["description"] = doc
	}
	return out
}

// xmlNotes write how a member is written in XML: its element name, when
// it differs from the member's name or the shape it points to has its
// own; as an attribute; and a list's outer element.
func (c *converter) xmlNotes(out map[string]any, n string, mb member, kind string) {
	notes := map[string]any{}
	xn, named := mb.Traits["smithy.api#xmlName"].(string)
	if _, ownName := c.model.Shapes[mb.Target].Traits["smithy.api#xmlName"]; named || ownName {
		notes["name"] = c.elementName(n, mb)
	}
	if has(mb.Traits, "smithy.api#xmlAttribute") {
		notes["attribute"] = true
		// The proxy matches an attribute by its name without a prefix.
		_, local, found := strings.Cut(xn, ":")
		if found {
			notes["name"] = local
		}
	}
	if kind == "list" || kind == "set" || kind == "map" && c.xmlMaps {
		notes["wrapped"] = true
	}
	if len(notes) > 0 {
		out["xml"] = notes
	}
}

// elementName is a member's element name in XML.
func (c *converter) elementName(n string, mb member) string {
	if xn, ok := mb.Traits["smithy.api#xmlName"].(string); ok {
		return xn
	}
	return n
}

// constrain adds Smithy's length, range and pattern rules.
func constrain(out map[string]any, traits map[string]any, kind string) {
	if l, ok := traits["smithy.api#length"].(map[string]any); ok {
		minKey, maxKey := "minLength", "maxLength"
		switch kind {
		case "list", "set":
			minKey, maxKey = "minItems", "maxItems"
		case "map", "structure":
			minKey, maxKey = "minProperties", "maxProperties"
		}
		if v, ok := l["min"]; ok {
			out[minKey] = v
		}
		if v, ok := l["max"]; ok {
			out[maxKey] = v
		}
	}
	if r, ok := traits["smithy.api#range"].(map[string]any); ok {
		if v, ok := r["min"]; ok {
			out["minimum"] = v
		}
		if v, ok := r["max"]; ok {
			out["maximum"] = v
		}
	}
	if p, ok := traits["smithy.api#pattern"].(string); ok {
		if goPattern, ok := readablePattern(p); ok {
			out["pattern"] = goPattern
		} else {
			// Go can't read it (a look ahead, a repeat above 1000), so it
			// can't be checked; it stays in the spec for people to read.
			out["x-aws-pattern"] = p
		}
	}
}

// readablePattern writes an AWS pattern, written for Java, so Go's regular
// expressions read it the same way: a character by its number (\u0020)
// becomes \x{0020}, and Java's printable class \p{Print} becomes the
// printable ASCII range. It says false when Go still can't read it.
func readablePattern(p string) (string, bool) {
	var b strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			rest := p[i+1:]
			switch {
			case len(rest) >= 5 && rest[0] == 'u' && isHex(rest[1:5]):
				b.WriteString(`\x{` + rest[1:5] + `}`)
				i += 5
			case strings.HasPrefix(rest, "p{Print}"):
				if inClass {
					b.WriteString(`\x20-\x7E`)
				} else {
					b.WriteString(`[\x20-\x7E]`)
				}
				i += len("p{Print}")
			default:
				b.WriteByte(c)
				b.WriteByte(p[i+1])
				i++
			}
		case c == '[' && !inClass:
			inClass = true
			b.WriteByte(c)
		case c == ']' && inClass:
			inClass = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	out := b.String()
	if _, err := regexp.Compile(out); err != nil {
		return "", false
	}
	return out, true
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func (c *converter) prelude(target string) map[string]any {
	t := strings.ToLower(strings.TrimPrefix(target, "smithy.api#"))
	t = strings.TrimPrefix(t, "primitive")
	return c.simple(t)
}

// simple is the JSON Schema for one of Smithy's simple shape types. AWS's
// XML writes times as date-time text.
func (c *converter) simple(kind string) map[string]any {
	if kind == "timestamp" && c.xml {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	return simple(kind)
}

// simple is the JSON Schema for one of Smithy's simple shape types, as AWS's
// JSON protocol writes them.
func simple(kind string) map[string]any {
	switch kind {
	case "string":
		return map[string]any{"type": "string"}
	case "boolean":
		return map[string]any{"type": "boolean"}
	case "byte", "short", "integer":
		return map[string]any{"type": "integer", "format": "int32"}
	case "long":
		return map[string]any{"type": "integer", "format": "int64"}
	case "biginteger":
		return map[string]any{"type": "integer"}
	case "float", "double", "bigdecimal":
		return map[string]any{"type": "number"}
	case "timestamp":
		// AWS JSON writes times as seconds since 1970, with a fraction.
		return map[string]any{"type": "number"}
	case "blob":
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	default: // document, and anything new: any JSON value
		return map[string]any{}
	}
}

func jsonName(n string, mb member) string {
	if j, ok := mb.Traits["smithy.api#jsonName"].(string); ok {
		return j
	}
	return n
}

func has(traits map[string]any, trait string) bool {
	_, ok := traits[trait]
	return ok
}

// name is the part of a shape ID after the #.
func name(id string) string {
	_, n, _ := strings.Cut(id, "#")
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

var (
	tags   = regexp.MustCompile(`<[^>]*>`)
	spaces = regexp.MustCompile(`\s+`)
)

// text is a shape's documentation as plain text: AWS writes it as HTML.
func text(traits map[string]any) string {
	doc, ok := traits["smithy.api#documentation"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(spaces.ReplaceAllString(html.UnescapeString(tags.ReplaceAllString(doc, " ")), " "))
}
