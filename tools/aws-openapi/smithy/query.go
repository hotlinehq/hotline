package smithy

import (
	"maps"
	"strings"

	"go.yaml.in/yaml/v3"
)

// actionField is the form field AWS's query protocol names the action in.
const actionField = "Action"

// queryDocument is the spec's top level for a service on AWS's query
// protocol, which names the action in a body field.
type queryDocument struct {
	OpenAPI     string         `yaml:"openapi"`
	Info        map[string]any `yaml:"info"`
	ActionField string         `yaml:"x-hotline-action-field"` //nolint:tagliatelle // the name of our OpenAPI extension
	Servers     []any          `yaml:"servers"`
	Security    []any          `yaml:"security"`
	Paths       map[string]any `yaml:"paths"`
	Components  map[string]any `yaml:"components"`
}

// convertQuery writes the spec of a service on AWS's query protocol, used
// by SNS and STS: every call is POST / with a form body whose field Action
// names the action, and every reply is XML wrapped in the action's name.
func convertQuery(m model, serviceID string, service shape, options Options) ([]byte, error) {
	c := converter{model: m, schemas: map[string]any{}, options: options, xml: true, xmlMaps: true}
	paths := map[string]any{}
	for _, op := range m.operations(service) {
		action := name(op.Target)
		paths["/#"+actionField+"="+action] = map[string]any{"post": c.queryOperation(op.Target, action, service.Version)}
	}
	c.schemas["AwsQueryError"] = map[string]any{
		"type":        "object",
		"description": "The body of every AWS query error reply: Error.Code names the error.",
		"xml":         map[string]any{"name": "ErrorResponse"},
		"properties": map[string]any{
			"Error": map[string]any{
				"type":     "object",
				"required": []string{"Code"},
				"properties": map[string]any{
					"Type":    map[string]any{"type": "string"},
					"Code":    map[string]any{"type": "string"},
					"Message": map[string]any{"type": "string"},
				},
			},
			"RequestId": map[string]any{"type": "string"},
		},
	}
	title := name(serviceID)
	if t, ok := service.Traits["smithy.api#title"].(string); ok {
		title = t
	}
	doc := queryDocument{
		OpenAPI: "3.2.0",
		Info: map[string]any{
			"title":   title,
			"version": service.Version,
			"description": "Made by Hotline's aws-openapi tool from AWS's Smithy model. Do not edit by hand. " +
				"Lists, maps and structures in a call are written by AWS with dots (Tags.member.1) and are not checked.",
		},
		ActionField: actionField,
		Servers:     []any{map[string]any{"url": "https://" + endpointPrefix(service, title) + ".{region}.amazonaws.com", "variables": map[string]any{"region": map[string]any{"default": "us-east-1"}}}},
		Security:    []any{map[string]any{"aws_sigv4": []any{}}},
		Paths:       paths,
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

func (c *converter) queryOperation(id, action, version string) map[string]any {
	op := c.model.Shapes[id]
	requestID := func() map[string]any {
		return map[string]any{"x-amzn-RequestId": map[string]any{"schema": map[string]any{"type": "string"}, "x-hotline-redact": false}}
	}
	errorReply := map[string]any{
		"description": "An AWS error.",
		"headers":     requestID(),
		"content":     map[string]any{"text/xml": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/AwsQueryError"}}},
	}
	out := map[string]any{
		"operationId": action,
		"requestBody": map[string]any{
			"required": true,
			"content":  map[string]any{"application/x-www-form-urlencoded": map[string]any{"schema": c.queryForm(op.Input, action, version)}},
		},
		"responses": map[string]any{
			"200": map[string]any{
				"description": "Done.",
				"headers":     requestID(),
				"content":     map[string]any{"text/xml": map[string]any{"schema": c.queryReply(op.Output, action)}},
			},
			"4XX": errorReply,
			"5XX": errorReply,
		},
	}
	if doc := text(op.Traits); doc != "" {
		out["description"] = doc
	}
	return out
}

// queryForm is the schema of an action's form body: Action and Version
// fixed, and each plain field with its type. Lists, maps and structures
// are sent with dots in their names (Tags.member.1=a), which the proxy
// reads as plain names, so they are left out and pass unchecked (Peter,
// 2026-11-01: no mark for dotted names until a real example needs one).
func (c *converter) queryForm(input *ref, action, version string) map[string]any {
	actionSchema := map[string]any{"type": "string", "const": action}
	if c.options.Action != "" {
		actionSchema["x-hotline-record"] = c.options.Action
	}
	props := map[string]any{
		"Action":  actionSchema,
		"Version": map[string]any{"type": "string", "const": version},
	}
	required := []string{"Action", "Version"}
	io := c.ioShape(input)
	for _, n := range sortedKeys(io.Members) {
		mb := io.Members[n]
		kind := c.kind(mb.Target)
		if kind == "structure" || kind == "union" || kind == "list" || kind == "set" || kind == "map" {
			continue
		}
		field := c.elementName(n, mb)
		schema := maps.Clone(c.schemaRef(mb.Target))
		constrain(schema, mb.Traits, kind)
		if doc := text(mb.Traits); doc != "" {
			schema["description"] = doc
		}
		if attribute, ok := c.options.Record[field]; ok {
			schema["x-hotline-record"] = attribute
		}
		props[field] = schema
		if has(mb.Traits, "smithy.api#required") {
			required = append(required, field)
		}
	}
	return map[string]any{"type": "object", "required": required, "properties": props}
}

// queryReply is the schema of an action's XML reply: an element named
// after the action holding its result, if it has one, and AWS's request ID.
func (c *converter) queryReply(output *ref, action string) map[string]any {
	props := map[string]any{
		"ResponseMetadata": map[string]any{
			"type":       "object",
			"properties": map[string]any{"RequestId": map[string]any{"type": "string"}},
		},
	}
	if output != nil && output.Target != "smithy.api#Unit" {
		props[action+"Result"] = c.schemaRef(output.Target)
	}
	return map[string]any{"type": "object", "xml": map[string]any{"name": action + "Response"}, "properties": props}
}

// kind is the Smithy type of the shape target points to.
func (c *converter) kind(target string) string {
	if s, ok := c.model.Shapes[target]; ok {
		return s.Type
	}
	return strings.ToLower(name(target))
}
