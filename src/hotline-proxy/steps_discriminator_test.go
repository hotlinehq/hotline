package proxy_test

import (
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// blocksSpec is a vendor spec whose bodies are content blocks, a text
// block or an image block told apart by their type field, the way
// Anthropic writes them. block is the Block schema, and extra holds more
// shared schemas.
func blocksSpec(block, extra string) string {
	return fmt.Sprintf(`openapi: 3.2.0
paths:
  /v1/blocks:
    post:
      requestBody:
        content:
          application/json:
            schema: { $ref: "#/components/schemas/Block" }
  /v1/messages:
    post:
      requestBody:
        content:
          application/json:
            schema:
              type: object
              properties:
                content:
                  type: array
                  items: { $ref: "#/components/schemas/Block" }
components:
  schemas:
    Block:
%s
    TextBlock:
      type: object
      required: [text]
      properties:
        type: { const: text }
        text: { type: string }
    ImageBlock:
      type: object
      required: [type, source]
      properties:
        type: { const: image }
        source:
          type: object
          required: [data]
          properties:
            data: { type: string }
%s`, block, extra)
}

const blocksChoice = `      oneOf:
        - $ref: "#/components/schemas/TextBlock"
        - $ref: "#/components/schemas/ImageBlock"
`

const blocksMapping = `      discriminator:
        propertyName: type
        mapping:
          text: "#/components/schemas/TextBlock"
          image: ImageBlock
`

func discriminatorSteps(sc *godog.ScenarioContext, w *world) {
	specs := map[string]string{
		"with a mapping": blocksSpec(blocksChoice+blocksMapping, ""),
		// Without a mapping, the value is the schema's name (OpenAPI's
		// implicit mapping).
		"named by its schema names": blocksSpec(`      oneOf:
        - $ref: "#/components/schemas/text"
        - $ref: "#/components/schemas/image"
      discriminator: { propertyName: type }
`, `    text: { $ref: "#/components/schemas/TextBlock" }
    image: { $ref: "#/components/schemas/ImageBlock" }
`),
		// A name with a dot is still a schema name, as specs made from
		// .NET or Java write them.
		"with a mapping to a schema whose name has a dot": blocksSpec(`      oneOf:
        - $ref: "#/components/schemas/TextBlock"
        - $ref: "#/components/schemas/Image.v1"
      discriminator: { propertyName: type, mapping: { text: TextBlock, image: Image.v1 } }
`, "    Image.v1: { $ref: \"#/components/schemas/ImageBlock\" }\n"),
		"with defaultMapping": blocksSpec(blocksChoice+`      discriminator:
        propertyName: type
        mapping: { image: ImageBlock }
        defaultMapping: TextBlock
`, ""),
		"without a discriminator": blocksSpec(blocksChoice, ""),
		"with anyOf and a branch that takes any object": blocksSpec(`      anyOf:
        - $ref: "#/components/schemas/ImageBlock"
        - $ref: "#/components/schemas/Anything"
      discriminator: { propertyName: type, mapping: { image: ImageBlock } }
`, "    Anything: { type: object }\n"),
		"with a mapping and a branch that takes any object": blocksSpec(blocksChoice+`        - $ref: "#/components/schemas/Anything"
`+blocksMapping, "    Anything: { type: object }\n"),
		"with a discriminator whose mapping names the schema Video":         blocksSpec(blocksChoice+blocksMapping+"          video: Video\n", ""),
		"with a discriminator whose defaultMapping names the schema Video":  blocksSpec(blocksChoice+blocksMapping+"        defaultMapping: Video\n", ""),
		"with a discriminator whose mapping names the schema Other":         blocksSpec(blocksChoice+blocksMapping+"          other: Other\n", "    Other: { type: object }\n"),
		"with a discriminator with no propertyName":                         blocksSpec(blocksChoice+"      discriminator: { mapping: { image: ImageBlock } }\n", ""),
		"with a discriminator whose mapping names a schema in another file": blocksSpec(blocksChoice+blocksMapping+"          remote: other.yaml#/Image\n", ""),
		// OpenAPI's allOf form: the parent carries the discriminator and
		// checks nothing more, as OpenAPI says.
		"with a discriminator on a parent schema used in allOf": blocksSpec(`      allOf:
        - $ref: "#/components/schemas/Base"
        - $ref: "#/components/schemas/ImageBlock"
`, `    Base:
      type: object
      discriminator: { propertyName: type, mapping: { image: ImageBlock, text: TextBlock } }
`),
		"with a discriminator with no oneOf or anyOf beside it": blocksSpec(`      $ref: "#/components/schemas/ImageBlock"
      discriminator: { propertyName: type, mapping: { image: ImageBlock } }
`, ""),
	}
	sc.Step(`^the content blocks spec (.+)$`, func(variant string) error {
		spec, ok := specs[variant]
		if !ok {
			return failf("no content blocks spec %q", variant)
		}
		w.useSpec(spec)
		return nil
	})
	sc.Step(`^the app asks for a message with the content (.+)$`, func(content string) error {
		return w.send(anthropicMessage(false, content))
	})
	sc.Step(`^no result holds the value (\S+)$`, func(value string) error {
		results, err := w.results("request")
		if err != nil {
			return err
		}
		if len(results) == 0 || strings.Contains(strings.Join(results, "; "), value) {
			return failf("the results %v are empty or hold %s", results, value)
		}
		return nil
	})
}
