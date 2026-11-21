package smithy

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// httpBindings are the traits that put a member somewhere other than the
// XML body.
func httpBindings() []string {
	return []string{
		"smithy.api#httpLabel", "smithy.api#httpQuery", "smithy.api#httpHeader", "smithy.api#httpPrefixHeaders",
		"smithy.api#httpPayload", "smithy.api#httpQueryParams", "smithy.api#httpResponseCode",
	}
}

// restDocument is the spec's top level for a REST service, which names no
// action header.
type restDocument struct {
	OpenAPI    string         `yaml:"openapi"`
	Info       map[string]any `yaml:"info"`
	Servers    []any          `yaml:"servers"`
	Security   []any          `yaml:"security"`
	Paths      map[string]any `yaml:"paths"`
	Components map[string]any `yaml:"components"`
}

// restRoute is one action's route: its method, the path without the
// bucket, and the query words AWS's model writes after ?.
type restRoute struct {
	id     string
	method string
	path   string
	words  []string
}

// convertRestXML writes the spec of a service on AWS's REST and XML
// protocol. Only actions on a bucket are written, with the bucket in the
// host name, as AWS's SDKs send them; the others go to another host.
func convertRestXML(m model, serviceID string, service shape, options Options) ([]byte, error) {
	if options.Action != "" {
		return nil, fmt.Errorf("%w: %s names its actions by path, not by a header to record", ErrUnsupported, serviceID)
	}
	c := converter{model: m, schemas: map[string]any{}, options: options, xml: true}
	var routes []restRoute
	for _, op := range m.operations(service) {
		http, _ := m.Shapes[op.Target].Traits["smithy.api#http"].(map[string]any)
		uri, _ := http["uri"].(string)
		method, _ := http["method"].(string)
		path, query, _ := strings.Cut(uri, "?")
		rest, onBucket := strings.CutPrefix(path, "/{Bucket}")
		if !onBucket {
			continue
		}
		if rest == "" {
			rest = "/"
		}
		var words []string
		if query != "" {
			words = strings.Split(query, "&")
		}
		routes = append(routes, restRoute{id: op.Target, method: strings.ToLower(method), path: rest, words: words})
	}
	paths := map[string]any{}
	for _, r := range routes {
		words, xID := r.words, ""
		// AWS's SDKs add x-id=Action to some calls. It is left out of the
		// route where the route is clear without it.
		if i := slices.IndexFunc(words, func(w string) bool { return strings.HasPrefix(w, "x-id=") }); i >= 0 && !clashes(routes, r, i) {
			xID = strings.TrimPrefix(words[i], "x-id=")
			words = slices.Delete(slices.Clone(words), i, i+1)
		}
		key := r.path
		if len(words) > 0 {
			key += "#?" + strings.Join(words, "&")
		}
		item, _ := paths[key].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[key] = item
		}
		item[r.method] = c.restOperation(r.id, xID)
	}
	c.schemas["AwsXmlError"] = map[string]any{
		"type":        "object",
		"description": "The body of every AWS XML error reply: Code names the error.",
		"xml":         map[string]any{"name": "Error"},
		"required":    []string{"Code"},
		"properties": map[string]any{
			"Code":      map[string]any{"type": "string"},
			"Message":   map[string]any{"type": "string"},
			"RequestId": map[string]any{"type": "string"},
		},
	}
	title := name(serviceID)
	if t, ok := service.Traits["smithy.api#title"].(string); ok {
		title = t
	}
	doc := restDocument{
		OpenAPI: "3.2.0",
		Info: map[string]any{
			"title":       title,
			"version":     service.Version,
			"description": "Made by Hotline's aws-openapi tool from AWS's Smithy model. Do not edit by hand. Only actions on a bucket are listed, with the bucket in the host name.",
		},
		Servers: []any{map[string]any{
			"url": "https://{bucket}." + endpointPrefix(service, title) + ".{region}.amazonaws.com",
			"variables": map[string]any{
				"bucket": map[string]any{"default": "bucket"},
				"region": map[string]any{"default": "us-east-1"},
			},
		}},
		Security: []any{map[string]any{"aws_sigv4": []any{}}},
		Paths:    paths,
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

// clashes says whether r would share its route with another action if
// its word i were left out.
func clashes(routes []restRoute, r restRoute, i int) bool {
	without := slices.Delete(slices.Clone(r.words), i, i+1)
	for _, other := range routes {
		if other.id == r.id || other.method != r.method || other.path != r.path {
			continue
		}
		otherWords := other.words
		if j := slices.IndexFunc(otherWords, func(w string) bool { return strings.HasPrefix(w, "x-id=") }); j >= 0 {
			otherWords = slices.Delete(slices.Clone(otherWords), j, j+1)
		}
		if slices.Equal(without, otherWords) {
			return true
		}
	}
	return false
}

func (c *converter) restOperation(id, xID string) map[string]any {
	op := c.model.Shapes[id]
	requestIDs := func() map[string]any {
		return map[string]any{
			"x-amz-request-id": map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false},
			"x-amz-id-2":       map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false},
		}
	}
	errorReply := map[string]any{
		"description": "An AWS error.",
		"headers":     requestIDs(),
		"content":     xmlContent(map[string]any{"$ref": "#/components/schemas/AwsXmlError"}),
	}
	out := map[string]any{"operationId": name(id)}
	var params []any
	if xID != "" {
		params = append(params, map[string]any{
			"name": "x-id", "in": "query",
			"schema":           map[string]any{"type": "string", "const": xID},
			"x-hotline-redact": false,
		})
	}
	input := c.ioShape(op.Input)
	for _, n := range sortedKeys(input.Members) {
		if p := c.parameter(n, input.Members[n]); p != nil {
			params = append(params, p)
		}
	}
	if len(params) > 0 {
		out["parameters"] = params
	}
	if body := c.restBody(op.Input); body != nil {
		out["requestBody"] = body
	}
	code := "200"
	if http, ok := op.Traits["smithy.api#http"].(map[string]any); ok && http["code"] != nil {
		code = fmt.Sprint(http["code"])
	}
	reply := map[string]any{"description": "Done.", "headers": requestIDs()}
	output := c.ioShape(op.Output)
	for _, n := range sortedKeys(output.Members) {
		mb := output.Members[n]
		if header, ok := mb.Traits["smithy.api#httpHeader"].(string); ok {
			reply["headers"].(map[string]any)[header] = map[string]any{"schema": c.textSchema(mb.Target)}
		}
	}
	if body := c.restBody(op.Output); body != nil {
		reply["content"] = body["content"]
	}
	out["responses"] = map[string]any{code: reply, "4XX": errorReply, "5XX": errorReply}
	if doc := text(op.Traits); doc != "" {
		out["description"] = doc
	}
	return out
}

// ioShape is an action's input or output, empty when it has none.
func (c *converter) ioShape(r *ref) shape {
	if r == nil {
		return shape{}
	}
	return c.model.Shapes[r.Target]
}

// parameter is a member sent in the path, the query or a header, nil for
// one sent elsewhere. S3's bucket is in the host name, so it is left out.
func (c *converter) parameter(n string, mb member) map[string]any {
	var p map[string]any
	switch {
	case has(mb.Traits, "smithy.api#httpLabel") && (n != "Bucket" || !c.xml):
		p = map[string]any{"name": n, "in": "path"}
	case mb.Traits["smithy.api#httpQuery"] != nil:
		p = map[string]any{"name": mb.Traits["smithy.api#httpQuery"], "in": "query"}
	case mb.Traits["smithy.api#httpHeader"] != nil:
		p = map[string]any{"name": mb.Traits["smithy.api#httpHeader"], "in": "header"}
	default:
		return nil
	}
	if has(mb.Traits, "smithy.api#required") || p["in"] == "path" {
		p["required"] = true
	}
	p["schema"] = c.textSchema(mb.Target)
	return p
}

// textSchema is the schema of a value sent as text in a path, query or
// header. Times there are text in AWS's own forms, so only their type is
// checked.
func (c *converter) textSchema(target string) map[string]any {
	kind := strings.ToLower(strings.TrimPrefix(target, "smithy.api#"))
	if s, ok := c.model.Shapes[target]; ok {
		kind = s.Type
	}
	switch kind {
	case "timestamp", "blob", "string", "enum":
		return map[string]any{"type": "string"}
	case "list", "set":
		return map[string]any{"type": "array", "items": c.textSchema(c.model.Shapes[target].Member.Target)}
	}
	return c.simple(kind)
}

// restBody is an action's body: the member sent as the whole body, or
// else the members bound nowhere else, as XML. A file or text sent as the
// whole body is passed without being read. Nil when there is no body.
func (c *converter) restBody(r *ref) map[string]any {
	io := c.ioShape(r)
	for _, n := range sortedKeys(io.Members) {
		mb := io.Members[n]
		if !has(mb.Traits, "smithy.api#httpPayload") {
			continue
		}
		target := c.model.Shapes[mb.Target]
		if target.Type != "structure" {
			return map[string]any{"content": map[string]any{"*/*": map[string]any{}}}
		}
		root, ok := mb.Traits["smithy.api#xmlName"].(string)
		if !ok {
			root, ok = target.Traits["smithy.api#xmlName"].(string)
		}
		if !ok {
			root = name(mb.Target)
		}
		body := map[string]any{"content": xmlContent(map[string]any{"$ref": c.schemaRef(mb.Target)["$ref"], "xml": map[string]any{"name": root}})}
		if has(mb.Traits, "smithy.api#required") {
			body["required"] = true
		}
		return body
	}
	inBody := func(mb member) bool {
		return !slices.ContainsFunc(httpBindings(), func(t string) bool { return has(mb.Traits, t) })
	}
	if !slices.ContainsFunc(sortedKeys(io.Members), func(n string) bool { return inBody(io.Members[n]) }) {
		return nil
	}
	n := name(r.Target)
	if _, done := c.schemas[n]; !done {
		schema := c.object(io.Members, inBody)
		schema["xml"] = map[string]any{"name": n}
		if xn, ok := io.Traits["smithy.api#xmlName"].(string); ok {
			schema["xml"] = map[string]any{"name": xn}
		}
		c.schemas[n] = schema
	}
	return map[string]any{"content": xmlContent(map[string]any{"$ref": "#/components/schemas/" + n})}
}

func xmlContent(schema any) map[string]any {
	return map[string]any{"application/xml": map[string]any{"schema": schema, "x-hotline-body": "xml"}}
}
