package smithy

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// convertRestJSON writes the spec of a service on AWS's REST and JSON
// protocol, used by Lambda and SES: actions are told apart by method and
// path, values travel in the path, query and headers, and bodies are JSON.
func convertRestJSON(m model, serviceID string, service shape, options Options) ([]byte, error) {
	if options.Action != "" {
		return nil, fmt.Errorf("%w: %s names its actions by path, not by a header to record", ErrUnsupported, serviceID)
	}
	c := converter{model: m, schemas: map[string]any{}, options: options}
	var routes []restRoute
	for _, op := range m.operations(service) {
		http, _ := m.Shapes[op.Target].Traits["smithy.api#http"].(map[string]any)
		uri, _ := http["uri"].(string)
		method, _ := http["method"].(string)
		path, query, _ := strings.Cut(uri, "?")
		var words []string
		if query != "" {
			words = strings.Split(query, "&")
		}
		routes = append(routes, restRoute{id: op.Target, method: strings.ToLower(method), path: path, words: words})
	}
	paths := map[string]any{}
	for _, r := range routes {
		key := r.path
		if len(r.words) > 0 {
			key += "#?" + strings.Join(r.words, "&")
		}
		item, _ := paths[key].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[key] = item
		}
		item[r.method] = c.restJSONOperation(r.id)
	}
	c.schemas["AwsRestJsonError"] = map[string]any{
		"type":        "object",
		"description": "The body of an AWS REST and JSON error reply. The error's name is in the X-Amzn-ErrorType header.",
		"properties": map[string]any{
			"message": map[string]any{"type": "string"},
			"Message": map[string]any{"type": "string"},
			"Type":    map[string]any{"type": "string"},
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
			"description": "Made by Hotline's aws-openapi tool from AWS's Smithy model. Do not edit by hand.",
		},
		Servers:  []any{map[string]any{"url": "https://" + endpointPrefix(service, title) + ".{region}.amazonaws.com", "variables": map[string]any{"region": map[string]any{"default": "us-east-1"}}}},
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

func (c *converter) restJSONOperation(id string) map[string]any {
	op := c.model.Shapes[id]
	requestID := func() map[string]any {
		return map[string]any{"x-amzn-RequestId": map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false}}
	}
	errorHeaders := requestID()
	errorHeaders["X-Amzn-ErrorType"] = map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false}
	errorReply := map[string]any{
		"description": "An AWS error.",
		"headers":     errorHeaders,
		"content":     map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/AwsRestJsonError"}}},
	}
	out := map[string]any{"operationId": name(id)}
	var params []any
	input := c.ioShape(op.Input)
	for _, n := range sortedKeys(input.Members) {
		if p := c.parameter(n, input.Members[n]); p != nil {
			params = append(params, p)
		}
	}
	if len(params) > 0 {
		out["parameters"] = params
	}
	if body := c.restJSONBody(op.Input); body != nil {
		out["requestBody"] = body
	}
	code := "200"
	if http, ok := op.Traits["smithy.api#http"].(map[string]any); ok && http["code"] != nil {
		code = fmt.Sprint(http["code"])
	}
	headers := requestID()
	output := c.ioShape(op.Output)
	for _, n := range sortedKeys(output.Members) {
		mb := output.Members[n]
		if header, ok := mb.Traits["smithy.api#httpHeader"].(string); ok {
			headers[header] = map[string]any{"schema": c.textSchema(mb.Target)}
		}
	}
	reply := map[string]any{"description": "Done.", "headers": headers}
	if body := c.restJSONBody(op.Output); body != nil {
		reply["content"] = body["content"]
	}
	out["responses"] = map[string]any{code: reply, "4XX": errorReply, "5XX": errorReply}
	if doc := text(op.Traits); doc != "" {
		out["description"] = doc
	}
	return out
}

// restJSONBody is an action's body: the member sent as the whole body, or
// else the members bound nowhere else, as JSON. Anything but a structure
// sent as the whole body, such as Lambda's payload, is passed without
// being read; an overlay can say what it holds. Nil when there is no body.
func (c *converter) restJSONBody(r *ref) map[string]any {
	io := c.ioShape(r)
	for _, n := range sortedKeys(io.Members) {
		mb := io.Members[n]
		if !has(mb.Traits, "smithy.api#httpPayload") {
			continue
		}
		if c.kind(mb.Target) != "structure" {
			return map[string]any{"content": map[string]any{"*/*": map[string]any{}}}
		}
		body := map[string]any{"content": jsonContent(c.schemaRef(mb.Target))}
		if has(mb.Traits, "smithy.api#required") {
			body["required"] = true
		}
		return body
	}
	inBody := func(mb member) bool {
		for _, t := range httpBindings() {
			if has(mb.Traits, t) {
				return false
			}
		}
		return true
	}
	bound := false
	for _, mb := range io.Members {
		bound = bound || inBody(mb)
	}
	if !bound {
		return nil
	}
	n := name(r.Target)
	if _, done := c.schemas[n]; !done {
		c.schemas[n] = c.object(io.Members, inBody)
	}
	return map[string]any{"content": jsonContent(map[string]any{"$ref": "#/components/schemas/" + n})}
}

func jsonContent(schema any) map[string]any {
	return map[string]any{"application/json": map[string]any{"schema": schema}}
}
