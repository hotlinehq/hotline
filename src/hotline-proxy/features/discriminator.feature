# Requirements and decisions: docs/decisions/spec-checks.md (M3, 2026-11-22).
Feature: A discriminator names the branch a failed choice meant
  A discriminator is OpenAPI's note on a oneOf or anyOf saying which field
  tells the branches apart, and which branch each value of it means. OpenAPI
  3.2 says it must never change pass or fail. So when the choice fails, the
  proxy reports the problems of the branch the field picks, in place of one
  bare oneOf problem, with the usual keys and no new one (Peter,
  2026-11-22, Q1).

  Rule: A failed choice reports the picked branch's problems

    Scenario Outline: The field picks the branch
      Given the content blocks spec <mapped>
      When a call is POST /v1/blocks with the json body <body>
      Then the request results are <results>

      Examples:
        | mapped                    | body                                          | results                                     |
        | with a mapping            | {"type":"image"}                              | body $.source required                      |
        | with a mapping            | {"type":"text"}                               | body $.text required                        |
        | named by its schema names | {"type":"image"}                              | body $.source required                      |
        | with a mapping to a schema whose name has a dot | {"type":"image"}        | body $.source required                      |
        | with a mapping            | {"type":"image","source":{}}                  | body $.source.data required                 |
        | with a mapping            | {"type":"video"}                              | body $.type discriminator image, text       |
        | with a mapping            | {"source":{"data":"x"}}                       | body $.type discriminator image, text       |
        | with defaultMapping       | {}                                            | body $.text required                        |
        | with defaultMapping       | {"type":"video"}                              | body $.text required; body $.type const text |
        | without a discriminator   | {"type":"image"}                              | body $ oneOf                                |

    Scenario: A good value has no results
      Given the content blocks spec with a mapping
      When a call is POST /v1/blocks with the json body {"type":"image","source":{"data":"x"}}
      Then the record has no request results

    Scenario: A value with no branch is reported without the value
      Given the content blocks spec with a mapping
      When a call is POST /v1/blocks with the json body {"type":"secret-kind"}
      Then no result holds the value secret-kind

    Scenario: It works inside a list
      Given the content blocks spec with a mapping
      When a call is POST /v1/messages with the json body {"content":[{"type":"text","text":"hi"},{"type":"image"}]}
      Then the request results are body $.content[1].source required

    Scenario: The discriminator never turns a pass into a fail
      With anyOf, a value may pass through a branch other than the one its
      field names. It passes, as OpenAPI says.
      Given the content blocks spec with anyOf and a branch that takes any object
      When a call is POST /v1/blocks with the json body {"type":"image"}
      Then the record has no request results

    Scenario: Two branches matching a oneOf is reported as today
      Given the content blocks spec with a mapping and a branch that takes any object
      When a call is POST /v1/blocks with the json body {"type":"image","source":{"data":"x"}}
      Then the request results are body $ oneOf

  Rule: Anthropic's spec uses it

    Scenario Outline: Each kind of content block is checked as its own kind
      The four kinds an app sends most are checked in full. Any other kind
      goes to defaultMapping, which only needs a type, so a kind Anthropic
      adds later is never flagged.
      Given the Anthropic spec
      And the Anthropic vendor answers the message "Hello!" whole
      When the app asks for a message with the content <content>
      Then the <results>

      Examples:
        | content                                                             | results                                                          |
        | [{"type":"text","text":"Hi"}]                                       | record has no request results                                    |
        | [{"type":"document","source":{}}]                                   | record has no request results                                    |
        | [{"type":"image"}]                                                  | request results are body $.messages[0].content[0].source required |
        | [{"type":"text","text":"Hi"},{"type":"tool_result"}]                | request results are body $.messages[0].content[1].tool_use_id required |
        | [{"type":"tool_use","id":"t1","name":"get_weather"}]                | request results are body $.messages[0].content[0].input required  |

  Rule: A discriminator written wrongly is refused

    Scenario Outline: A broken discriminator refuses the spec
      Given the content blocks spec with a discriminator <mistake>
      When the proxy loads the config
      Then the config is refused with a message naming "Block" and "<message>"

      Examples:
        | mistake                                          | message                                   |
        | whose mapping names the schema Video             | Video, which the spec does not have       |
        | whose defaultMapping names the schema Video      | Video, which the spec does not have       |
        | whose mapping names the schema Other             | Other, which is not one of its branches   |
        | with no propertyName                             | has no propertyName                       |
        | whose mapping names a schema in another file     | other.yaml#/Image, which is not in this spec |

    Scenario Outline: A discriminator OpenAPI says checks nothing is loaded and ignored
      Given the content blocks spec with a discriminator <where>
      When a call is POST /v1/blocks with the json body {"type":"image"}
      Then the request results are <results>

      Examples:
        | where                               | results                |
        | on a parent schema used in allOf    | body $.source required |
        | with no oneOf or anyOf beside it    | body $.source required |
