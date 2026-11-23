package stripe_test

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"hotline-tools/stripe-openapi/stripe"
)

// convert runs the tool on a spec and reads the result back as plain data.
func convert(spec string, options stripe.Options) map[string]any {
	out, err := stripe.Convert([]byte(spec), options)
	Expect(err).NotTo(HaveOccurred())
	var doc map[string]any
	Expect(yaml.Unmarshal(out, &doc)).To(Succeed())
	return doc
}

// at walks a converted spec by its keys.
func at(doc map[string]any, keys ...string) any {
	var here any = doc
	for _, key := range keys {
		m, ok := here.(map[string]any)
		if !ok {
			return nil
		}
		here = m[key]
	}
	return here
}

// schemaSpec is a 3.0 spec with one schema, written as given.
func schemaSpec(schema string) string {
	return "openapi: 3.0.0\ninfo: {title: Stripe API, version: 2026-09-30.endive}\npaths: {}\ncomponents:\n  schemas:\n    thing:\n" + schema
}

var _ = Describe("Converting Stripe's OpenAPI 3.0 spec to 3.2", func() {
	It("says 3.2.0 and keeps the rest of the top", func() {
		doc := convert(schemaSpec("      type: string\n"), stripe.Options{})
		Expect(doc["openapi"]).To(Equal("3.2.0"))
		Expect(at(doc, "info", "version")).To(Equal("2026-09-30.endive"))
	})

	It("refuses a spec that is not OpenAPI 3.0", func() {
		_, err := stripe.Convert([]byte("openapi: 3.1.0\ninfo: {title: x, version: '1'}\npaths: {}\n"), stripe.Options{})
		Expect(err).To(MatchError(stripe.ErrNotOpenAPI30))
	})

	It("refuses text that is not YAML", func() {
		_, err := stripe.Convert([]byte("openapi: [3.0.0"), stripe.Options{})
		Expect(err).To(HaveOccurred())
	})

	It("refuses YAML that is not a map", func() {
		_, err := stripe.Convert([]byte("- openapi\n"), stripe.Options{})
		Expect(err).To(MatchError(stripe.ErrNotOpenAPI30))
	})

	DescribeTable("writes a value that may be null as 3.2 does",
		func(schema string, want map[string]any) {
			Expect(at(convert(schemaSpec(schema), stripe.Options{}), "components", "schemas", "thing")).To(Equal(want))
		},
		Entry("a type gets null in its list of types",
			"      type: string\n      nullable: true\n",
			map[string]any{"type": []any{"string", "null"}}),
		Entry("a list of values gets null too, or null would fail it",
			"      type: string\n      enum: [a, b]\n      nullable: true\n",
			map[string]any{"type": []any{"string", "null"}, "enum": []any{"a", "b", nil}}),
		Entry("a choice gets a null branch",
			"      anyOf:\n        - $ref: '#/components/schemas/other'\n      nullable: true\n",
			map[string]any{"anyOf": []any{map[string]any{"$ref": "#/components/schemas/other"}, map[string]any{"type": "null"}}}),
		Entry("nullable false is simply dropped",
			"      type: string\n      nullable: false\n",
			map[string]any{"type": "string"}),
		Entry("a property called nullable is a property, not the keyword",
			"      type: object\n      properties:\n        nullable: {type: boolean}\n",
			map[string]any{"type": "object", "properties": map[string]any{"nullable": map[string]any{"type": "boolean"}}}),
	)

	DescribeTable("refuses a nullable it cannot rewrite, rather than guess",
		func(schema string) {
			_, err := stripe.Convert([]byte(schemaSpec(schema)), stripe.Options{})
			Expect(err).To(MatchError(stripe.ErrNullableShape))
		},
		Entry("beside a $ref", "      $ref: '#/components/schemas/other'\n      nullable: true\n"),
		Entry("beside oneOf", "      oneOf: [{type: string}]\n      nullable: true\n"),
		Entry("beside allOf", "      allOf: [{type: string}]\n      nullable: true\n"),
		Entry("with no type at all", "      description: anything\n      nullable: true\n"),
		Entry("with a list of types already", "      type: [string]\n      nullable: true\n"),
		Entry("that is not true or false", "      type: string\n      nullable: maybe\n"),
	)

	It("drops Stripe's own x- fields everywhere, so the spec is taken as written", func() {
		doc := convert(schemaSpec("      type: string\n      enum: [a]\n      x-stripeBypassValidation: true\n      x-resourceId: thing\n"), stripe.Options{})
		Expect(at(doc, "components", "schemas", "thing")).To(Equal(map[string]any{"type": "string", "enum": []any{"a"}}))
	})

	It("refuses an x- field that is not one of Stripe's known ones, rather than drop what it does not know", func() {
		_, err := stripe.Convert([]byte(schemaSpec("      type: string\n      x-stripeNewIdea: true\n")), stripe.Options{})
		Expect(err).To(MatchError(stripe.ErrUnknownExtension))
	})

	DescribeTable("keeps a name that looks like a keyword where keys are names",
		func(spec string, keys ...string) {
			Expect(at(convert(spec, stripe.Options{}), keys...)).NotTo(BeNil())
		},
		Entry("a property called x-id", schemaSpec("      type: object\n      properties:\n        x-id: {type: string}\n"), "components", "schemas", "thing", "properties", "x-id"),
		Entry("a schema called x-thing", "openapi: 3.0.0\ninfo: {title: Stripe API, version: '1'}\npaths: {}\ncomponents:\n  schemas:\n    x-thing: {type: string}\n", "components", "schemas", "x-thing"),
		Entry("an encoding for a form field called nullable", "openapi: 3.0.0\ninfo: {title: Stripe API, version: '1'}\npaths:\n  /v1/x:\n    post:\n      requestBody:\n        content:\n          application/x-www-form-urlencoded:\n            encoding:\n              nullable: {style: deepObject}\n", "paths", "/v1/x", "post", "requestBody", "content", "application/x-www-form-urlencoded", "encoding", "nullable"),
	)

	It("refuses 3.0's true or false exclusiveMinimum, which 3.2 writes as a number", func() {
		_, err := stripe.Convert([]byte(schemaSpec("      type: integer\n      minimum: 0\n      exclusiveMinimum: true\n")), stripe.Options{})
		Expect(err).To(MatchError(stripe.ErrBooleanBound))
	})

	Describe("path values", func() {
		const paths = `openapi: 3.0.0
info: {title: Stripe API, version: 2026-09-30.endive}
paths:
  /v1/webhook_endpoints/{webhook_endpoint}:
    get:
      parameters:
        - {in: path, name: webhook_endpoint, required: true, schema: {type: string}}
        - {in: query, name: webhook_endpoint, schema: {type: string}}
  /v1/ephemeral_keys/{key}:
    delete:
      parameters:
        - {in: path, name: key, required: true, schema: {type: string}}
`
		parameter := func(doc map[string]any, path, method string, i int) map[string]any {
			list := at(doc, "paths", path, method, "parameters").([]any)
			return list[i].(map[string]any)
		}

		It("marks the named path values safe, so records show them", func() {
			doc := convert(paths, stripe.Options{SafePathValues: []string{"webhook_endpoint"}})
			Expect(parameter(doc, "/v1/webhook_endpoints/{webhook_endpoint}", "get", 0)).To(HaveKeyWithValue("x-hotline-redact", false))
		})

		It("leaves a query value of the same name hidden", func() {
			doc := convert(paths, stripe.Options{SafePathValues: []string{"webhook_endpoint"}})
			Expect(parameter(doc, "/v1/webhook_endpoints/{webhook_endpoint}", "get", 1)).NotTo(HaveKey("x-hotline-redact"))
		})

		It("leaves every other path value hidden", func() {
			doc := convert(paths, stripe.Options{SafePathValues: []string{"webhook_endpoint"}})
			Expect(parameter(doc, "/v1/ephemeral_keys/{key}", "delete", 0)).NotTo(HaveKey("x-hotline-redact"))
		})

		It("refuses a safe name that no path value has, so a typo shows", func() {
			_, err := stripe.Convert([]byte(paths), stripe.Options{SafePathValues: []string{"webhook_endpont"}})
			Expect(err).To(MatchError(stripe.ErrUnknownPathValue))
		})

		It("marks the named secret path values, so the file under content full hides them too", func() {
			doc := convert(paths, stripe.Options{SecretPathValues: []string{"key"}})
			Expect(parameter(doc, "/v1/ephemeral_keys/{key}", "delete", 0)).To(HaveKeyWithValue("x-hotline-redact", true))
		})

		It("refuses a secret name that no path value has", func() {
			_, err := stripe.Convert([]byte(paths), stripe.Options{SecretPathValues: []string{"kee"}})
			Expect(err).To(MatchError(stripe.ErrUnknownPathValue))
		})
	})

	It("made the committed Stripe spec from the committed source", func() {
		source, err := os.ReadFile("../source/spec3-2026-09-30.endive.yaml")
		Expect(err).NotTo(HaveOccurred())
		want, err := os.ReadFile("../../../specs/stripe/api/2026-09-30.endive.openapi.yaml")
		Expect(err).NotTo(HaveOccurred())
		got, err := stripe.Convert(source, stripe.Options{SafePathValues: []string{"webhook_endpoint"}, SecretPathValues: []string{"key", "token", "confirmation_token"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal(string(want)), "run: cd tools/stripe-openapi && go run . -safe webhook_endpoint -secret key -secret token -secret confirmation_token source/spec3-2026-09-30.endive.yaml > ../../specs/stripe/api/2026-09-30.endive.openapi.yaml")
	})
})
