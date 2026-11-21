package smithy_test

import (
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.yaml.in/yaml/v3"

	"hotline-tools/aws-openapi/smithy"
)

// model is a tiny AWS JSON 1.0 service with one action, one error and the
// shape kinds the converter knows.
const model = `{
  "smithy": "2.0",
  "shapes": {
    "com.example#Queues": {
      "type": "service",
      "version": "2012-11-05",
      "operations": [{"target": "com.example#Send"}, {"target": "com.example#Purge"}],
      "traits": {
        "aws.protocols#awsJson1_0": {},
        "aws.protocols#awsQueryCompatible": {},
        "aws.auth#sigv4": {"name": "queues"},
        "smithy.api#title": "Example Queues"
      }
    },
    "com.example#Send": {
      "type": "operation",
      "input": {"target": "com.example#SendRequest"},
      "output": {"target": "com.example#SendResult"},
      "errors": [{"target": "com.example#NoQueue"}]
    },
    "com.example#Purge": {
      "type": "operation",
      "input": {"target": "com.example#PurgeRequest"},
      "output": {"target": "smithy.api#Unit"}
    },
    "com.example#PurgeRequest": {
      "type": "structure",
      "members": {"QueueUrl": {"target": "com.example#String", "traits": {"smithy.api#required": {}}}}
    },
    "com.example#SendRequest": {
      "type": "structure",
      "members": {
        "QueueUrl": {"target": "com.example#String", "traits": {"smithy.api#required": {}, "smithy.api#documentation": "<p>The queue.</p>"}},
        "Body": {"target": "com.example#String", "traits": {"smithy.api#required": {}}},
        "Delay": {"target": "com.example#NullableInteger"},
        "Size": {"target": "smithy.api#Long"},
        "Fifo": {"target": "smithy.api#Boolean"},
        "Data": {"target": "com.example#Binary"},
        "At": {"target": "smithy.api#Timestamp"},
        "Kind": {"target": "com.example#Kind"},
        "Tags": {"target": "com.example#TagMap"},
        "Names": {"target": "com.example#NameList"},
        "Short": {"target": "smithy.api#String", "traits": {"smithy.api#length": {"min": 1, "max": 80}, "smithy.api#pattern": "^[a-z]+$"}},
        "Few": {"target": "com.example#NameList", "traits": {"smithy.api#length": {"max": 10}}},
        "Small": {"target": "smithy.api#Integer", "traits": {"smithy.api#range": {"min": 0, "max": 900}}},
        "Any": {"target": "smithy.api#Document"}
      }
    },
    "com.example#SendResult": {
      "type": "structure",
      "members": {"MessageId": {"target": "com.example#String"}}
    },
    "com.example#NoQueue": {
      "type": "structure",
      "members": {"message": {"target": "com.example#String"}},
      "traits": {"smithy.api#error": "client", "smithy.api#httpError": 400}
    },
    "com.example#String": {"type": "string"},
    "com.example#NullableInteger": {"type": "integer"},
    "com.example#Binary": {"type": "blob"},
    "com.example#Kind": {
      "type": "enum",
      "members": {
        "A": {"target": "smithy.api#Unit", "traits": {"smithy.api#enumValue": "a"}},
        "B": {"target": "smithy.api#Unit", "traits": {"smithy.api#enumValue": "b"}}
      }
    },
    "com.example#TagMap": {"type": "map", "key": {"target": "com.example#String"}, "value": {"target": "com.example#String"}},
    "com.example#NameList": {"type": "list", "member": {"target": "com.example#String"}}
  }
}`

// sqsRecord is what the committed SQS spec copies into records.
func sqsRecord() smithy.Options {
	return smithy.Options{
		Record: map[string]string{"QueueUrl": "messaging.destination.name"},
		Action: "messaging.operation.name",
	}
}

func convert(m string) map[string]any {
	return convertRecording(m, smithy.Options{})
}

func convertRecording(m string, options smithy.Options) map[string]any {
	out, err := smithy.Convert([]byte(m), options)
	Expect(err).NotTo(HaveOccurred())
	var doc map[string]any
	Expect(yaml.Unmarshal(out, &doc)).To(Succeed())
	return doc
}

func at(doc any, keys ...string) any {
	for _, k := range keys {
		m, ok := doc.(map[string]any)
		Expect(ok).To(BeTrue(), "no object at %s", k)
		doc = m[k]
	}
	return doc
}

var _ = Describe("Converting an AWS JSON service from Smithy to OpenAPI", func() {
	var doc map[string]any
	BeforeEach(func() { doc = convert(model) })

	It("writes an OpenAPI 3.2 document named after the service and its version", func() {
		Expect(doc["openapi"]).To(Equal("3.2.0"))
		Expect(at(doc, "info", "title")).To(Equal("Example Queues"))
		Expect(at(doc, "info", "version")).To(Equal("2012-11-05"))
	})

	It("names the action header for Hotline", func() {
		Expect(doc["x-hotline-action-header"]).To(Equal("X-Amz-Target"))
	})

	It("writes one route per action, named by the header value", func() {
		Expect(at(doc, "paths")).To(HaveKey("/#X-Amz-Target=Queues.Send"))
		Expect(at(doc, "paths")).To(HaveKey("/#X-Amz-Target=Queues.Purge"))
		Expect(at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post", "operationId")).To(Equal("Send"))
	})

	It("takes and gives AWS's JSON content type", func() {
		send := at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post")
		Expect(at(send, "requestBody", "required")).To(BeTrue())
		Expect(at(send, "requestBody", "content", "application/x-amz-json-1.0", "schema", "$ref")).To(Equal("#/components/schemas/SendRequest"))
		Expect(at(send, "responses", "200", "content", "application/x-amz-json-1.0", "schema", "$ref")).To(Equal("#/components/schemas/SendResult"))
	})

	It("marks AWS's content type to be read as JSON, in requests, replies and errors", func() {
		send := at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post")
		Expect(at(send, "requestBody", "content", "application/x-amz-json-1.0", "x-hotline-body")).To(Equal("json"))
		for _, status := range []string{"200", "4XX", "5XX"} {
			Expect(at(send, "responses", status, "content", "application/x-amz-json-1.0", "x-hotline-body")).To(Equal("json"))
		}
	})

	It("gives an action with no output an empty object", func() {
		Expect(at(doc, "paths", "/#X-Amz-Target=Queues.Purge", "post", "responses", "200", "content", "application/x-amz-json-1.0", "schema", "type")).To(Equal("object"))
	})

	It("lists AWS's error replies with their body and the error headers to record", func() {
		for _, status := range []string{"4XX", "5XX"} {
			reply := at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post", "responses", status)
			Expect(at(reply, "content", "application/x-amz-json-1.0", "schema", "$ref")).To(Equal("#/components/schemas/AwsJsonError"))
			Expect(at(reply, "headers", "x-amzn-query-error", "x-hotline-redact")).To(BeFalse())
			Expect(at(reply, "headers", "x-amzn-RequestId", "x-hotline-redact")).To(BeFalse())
		}
		Expect(at(doc, "components", "schemas", "AwsJsonError", "required")).To(ConsistOf("__type"))
	})

	It("keeps AWS's request ID in the record", func() {
		Expect(at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post", "responses", "200", "headers", "x-amzn-RequestId", "x-hotline-redact")).To(BeFalse())
	})

	It("names the signed Authorization header as the login", func() {
		Expect(at(doc, "components", "securitySchemes", "aws_sigv4")).To(Equal(map[string]any{"type": "apiKey", "in": "header", "name": "Authorization"}))
		Expect(doc["security"]).To(Equal([]any{map[string]any{"aws_sigv4": []any{}}}))
	})

	It("turns structures into objects with their required members", func() {
		send := at(doc, "components", "schemas", "SendRequest")
		Expect(at(send, "type")).To(Equal("object"))
		Expect(at(send, "required")).To(ConsistOf("QueueUrl", "Body"))
		Expect(at(send, "properties", "QueueUrl", "$ref")).To(Equal("#/components/schemas/String"))
		Expect(at(send, "properties", "QueueUrl", "description")).To(Equal("The queue."))
	})

	DescribeTable("maps each member to JSON Schema",
		func(member string, want map[string]any) {
			Expect(at(doc, "components", "schemas", "SendRequest", "properties", member)).To(Equal(want))
		},
		Entry("a whole number", "Size", map[string]any{"type": "integer", "format": "int64"}),
		Entry("true or false", "Fifo", map[string]any{"type": "boolean"}),
		Entry("a time: seconds since 1970", "At", map[string]any{"type": "number"}),
		Entry("a length and a pattern", "Short", map[string]any{"type": "string", "minLength": 1, "maxLength": 80, "pattern": "^[a-z]+$"}),
		Entry("a length on a list", "Few", map[string]any{"$ref": "#/components/schemas/NameList", "maxItems": 10}),
		Entry("a range", "Small", map[string]any{"type": "integer", "format": "int32", "minimum": 0, "maximum": 900}),
		Entry("any JSON", "Any", map[string]any{}),
	)

	DescribeTable("writes AWS's patterns so Go's regular expressions can read them",
		func(pattern string, want map[string]any) {
			doc := convert(strings.Replace(model, `"smithy.api#pattern": "^[a-z]+$"`, `"smithy.api#pattern": `+pattern, 1))
			got := at(doc, "components", "schemas", "SendRequest", "properties", "Short").(map[string]any)
			delete(got, "type")
			delete(got, "minLength")
			delete(got, "maxLength")
			Expect(got).To(Equal(want))
		},
		Entry("a character by its number", `"^[\\u0020-\\u00FF]*$"`, map[string]any{"pattern": `^[\x{0020}-\x{00FF}]*$`}),
		Entry("Java's printable class, alone", `"^\\p{Print}+$"`, map[string]any{"pattern": `^[\x20-\x7E]+$`}),
		Entry("Java's printable class, in a class", `"^[\\p{Print}\\t]+$"`, map[string]any{"pattern": `^[\x20-\x7E\t]+$`}),
		Entry("a look ahead, which Go can't read, is left out and kept aside", `"^(?!\\s*$).+$"`, map[string]any{"x-aws-pattern": `^(?!\s*$).+$`}),
		Entry("a repeat above 1000, which Go can't read, is left out and kept aside", `"^.{1,100000}$"`, map[string]any{"x-aws-pattern": `^.{1,100000}$`}),
	)

	DescribeTable("writes named shapes as components",
		func(name string, want map[string]any) {
			Expect(at(doc, "components", "schemas", name)).To(Equal(want))
		},
		Entry("a string", "String", map[string]any{"type": "string"}),
		Entry("an integer", "NullableInteger", map[string]any{"type": "integer", "format": "int32"}),
		Entry("bytes, as base64 text", "Binary", map[string]any{"type": "string", "contentEncoding": "base64"}),
		Entry("an enum", "Kind", map[string]any{"type": "string", "enum": []any{"a", "b"}}),
		Entry("a map", "TagMap", map[string]any{"type": "object", "additionalProperties": map[string]any{"$ref": "#/components/schemas/String"}}),
		Entry("a list", "NameList", map[string]any{"type": "array", "items": map[string]any{"$ref": "#/components/schemas/String"}}),
	)

	It("leaves out error shapes, which share one error body", func() {
		Expect(at(doc, "components", "schemas")).NotTo(HaveKey("NoQueue"))
	})

	It("uses AWS JSON 1.1's content type for such services", func() {
		doc := convert(strings.ReplaceAll(model, "awsJson1_0", "awsJson1_1"))
		Expect(at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post", "requestBody", "content")).To(HaveKey("application/x-amz-json-1.1"))
	})

	It("refuses a model on a protocol it doesn't know", func() {
		_, err := smithy.Convert([]byte(strings.ReplaceAll(model, "aws.protocols#awsJson1_0", "aws.protocols#ec2Query")), smithy.Options{})
		Expect(err).To(MatchError(smithy.ErrUnsupported))
	})

	It("refuses text that is not a Smithy model", func() {
		_, err := smithy.Convert([]byte("nope"), smithy.Options{})
		Expect(err).To(HaveOccurred())
	})

	It("made the committed SQS spec from the committed SQS model", func() {
		m, err := os.ReadFile("../models/sqs-2012-11-05.json")
		Expect(err).NotTo(HaveOccurred())
		want, err := os.ReadFile("../../../specs/aws/sqs/2012-11-05.openapi.yaml")
		Expect(err).NotTo(HaveOccurred())
		got, err := smithy.Convert(m, sqsRecord())
		Expect(err).NotTo(HaveOccurred())
		Expect(string(got)).To(Equal(string(want)), "run: cd tools/aws-openapi && go run . -record QueueUrl=messaging.destination.name -record-action messaging.operation.name models/sqs-2012-11-05.json > ../../specs/aws/sqs/2012-11-05.openapi.yaml")
	})

	DescribeTable("made each committed JSON spec from its committed model",
		func(model, spec, flags string) {
			m, err := os.ReadFile("../models/" + model + ".json") //nolint:gosec // the test's own files
			Expect(err).NotTo(HaveOccurred())
			want, err := os.ReadFile("../../../specs/aws/" + spec + ".openapi.yaml") //nolint:gosec // the test's own files
			Expect(err).NotTo(HaveOccurred())
			got, err := smithy.Convert(m, options(flags))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(got)).To(Equal(string(want)), "run: cd tools/aws-openapi && go run . "+flags+" models/"+model+".json > ../../specs/aws/"+spec+".openapi.yaml")
		},
		Entry("DynamoDB", "dynamodb-2012-08-10", "dynamodb/2012-08-10", "-record TableName=db.collection.name -record-action db.operation.name"),
		Entry("EventBridge", "eventbridge-2015-10-07", "eventbridge/2015-10-07", "-record-action rpc.method"),
		Entry("Kinesis", "kinesis-2013-12-02", "kinesis/2013-12-02", "-record StreamName=messaging.destination.name -record-action messaging.operation.name"),
		Entry("Secrets Manager", "secrets-manager-2017-10-17", "secrets-manager/2017-10-17", "-record-action rpc.method"),
		Entry("SSM", "ssm-2014-11-06", "ssm/2014-11-06", "-record-action rpc.method"),
		Entry("KMS", "kms-2014-11-01", "kms/2014-11-01", "-record-action rpc.method"),
		Entry("CloudWatch Logs", "cloudwatch-logs-2014-03-28", "cloudwatch-logs/2014-03-28", "-record-action rpc.method"),
		Entry("Step Functions", "sfn-2016-11-23", "sfn/2016-11-23", "-record-action rpc.method"),
	)

	It("marks the named fields of every action's input to be copied into records", func() {
		doc := convertRecording(model, sqsRecord())
		for _, input := range []string{"SendRequest", "PurgeRequest"} {
			Expect(at(doc, "components", "schemas", input, "properties", "QueueUrl", "x-hotline-record")).To(Equal("messaging.destination.name"))
		}
		Expect(at(doc, "components", "schemas", "SendRequest", "properties", "Body")).NotTo(HaveKey("x-hotline-record"))
	})

	It("marks nothing in an action's output", func() {
		doc := convertRecording(model, smithy.Options{Record: map[string]string{"MessageId": "example.message.id"}})
		Expect(at(doc, "components", "schemas", "SendResult", "properties", "MessageId")).NotTo(HaveKey("x-hotline-record"))
	})

	It("marks nothing unless asked", func() {
		Expect(at(doc, "components", "schemas", "SendRequest", "properties", "QueueUrl")).NotTo(HaveKey("x-hotline-record"))
		Expect(at(doc, "paths", "/#X-Amz-Target=Queues.Send", "post")).NotTo(HaveKey("parameters"))
	})

	It("lists the action header on every action, with its own value, to be copied into records", func() {
		doc := convertRecording(model, sqsRecord())
		for _, action := range []string{"Send", "Purge"} {
			params, ok := at(doc, "paths", "/#X-Amz-Target=Queues."+action, "post", "parameters").([]any)
			Expect(ok).To(BeTrue())
			Expect(params).To(ConsistOf(map[string]any{
				"name": "X-Amz-Target", "in": "header", "required": true,
				"schema":           map[string]any{"type": "string", "const": "Queues." + action},
				"x-hotline-record": "messaging.operation.name",
			}))
		}
	})
})

// options reads the tool's -record and -record-action flags.
func options(flags string) smithy.Options {
	o := smithy.Options{Record: map[string]string{}}
	f := strings.Fields(flags)
	for i := 0; i+1 < len(f); i += 2 {
		switch f[i] {
		case "-record":
			field, name, _ := strings.Cut(f[i+1], "=")
			o.Record[field] = name
		case "-record-action":
			o.Action = f[i+1]
		}
	}
	return o
}
