# Requirements and decisions: docs/decisions/spec-checks.md (second sweep, 2026-11-23).
Feature: Parameters described with content, and every parameter style
  A spec may describe a parameter with content, one media type and its
  schema, instead of a schema alone, and may write a parameter in any of
  the styles OpenAPI 3.2 lists. Before 2026-11-23 the proxy skipped a
  parameter with content and read matrix, label, spaceDelimited and
  pipeDelimited values as plain text, so a spec looked checked when it
  was not. Now each is read and checked, and a spec is refused only where
  OpenAPI itself forbids what it says (Peter, 2026-11-23, Q1 and Q2).

  Rule: A parameter with content is read by its one media type

    Scenario: A JSON query value that fits has no results
      Given the things spec with the parameter { name: filter, in: query, content: { application/json: { schema: { type: object, properties: { color: { type: string } } } } } }
      When a call is GET /v1/things?filter=%7B%22color%22%3A%22red%22%7D
      Then the record has no request results

    Scenario Outline: A JSON value is checked inside, wherever the parameter lives
      Given the things spec with the parameter { name: <name>, in: <in>, content: { application/json: { schema: { type: object, properties: { color: { type: string } } } } } }
      When <call>
      Then the request results are <in> <name>.color type string

      Examples:
        | in     | name     | call                                                     |
        | query  | filter   | a call is GET /v1/things?filter=%7B%22color%22%3A5%7D    |
        | path   | id       | a call is GET /v1/things/%7B%22color%22%3A5%7D           |
        | header | X-Filter | a call carries the header X-Filter with the value {"color":5}|
        | cookie | filter   | a call carries the header "Cookie: filter=%7B%22color%22%3A5%7D" |

    Scenario: A value that is not JSON is reported like a body that is not
      Given the things spec with the parameter { name: filter, in: query, content: { application/json: { schema: { type: object } } } }
      When a call is GET /v1/things?filter=%7Bnope
      Then the request results are query filter json

    Scenario: A form value is read as a form
      Given the things spec with the parameter { name: filter, in: query, content: { application/x-www-form-urlencoded: { schema: { type: object, properties: { size: { type: integer } } } } } }
      When a call is GET /v1/things?filter=size%3Dbig
      Then the request results are query filter.size type integer

    Scenario: Any other media type is read as text
      Given the things spec with the parameter { name: filter, in: query, content: { text/plain: { schema: { type: string, maxLength: 3 } } } }
      When a call is GET /v1/things?filter=abcdef
      Then the request results are query filter maxLength 3

    Scenario: A response header with content is read by its media type too
      Given the things spec whose 200 response has the header RateLimit-Policy with { content: { application/json: { schema: { type: object, properties: { limit: { type: integer } } } } } }
      When the vendor answers with the header RateLimit-Policy holding {"limit":"many"}
      Then the response results are header RateLimit-Policy.limit type integer

  Rule: A path value is read by its style

    Scenario Outline: A path value in matrix, label or simple style
      Given the things spec with the parameter { name: id, in: path, required: true, style: <style>, explode: <explode>, schema: <schema> }
      When a call is GET /v1/things/<value>
      Then <outcome>

      Examples: matrix
        | style  | explode | schema                                            | value             | outcome                                       |
        | matrix | false   | { type: integer }                                 | ;id=5             | the record has no request results             |
        | matrix | false   | { type: integer }                                 | ;id=x             | the request results are path id type integer  |
        | matrix | false   | { type: array, items: { type: integer } }         | ;id=3,x           | the request results are path id[1] type integer |
        | matrix | true    | { type: array, items: { type: integer } }         | ;id=3;id=x        | the request results are path id[1] type integer |
        | matrix | false   | { type: object, properties: { G: { type: integer } } } | ;id=R,100,G,x | the request results are path id.G type integer |
        | matrix | true    | { type: object, properties: { G: { type: integer } } } | ;R=100;G=x    | the request results are path id.G type integer |
        | matrix | false   | { type: integer }                                 | 5                 | the request results are path id style matrix  |

      Examples: label
        | style  | explode | schema                                            | value             | outcome                                       |
        | label  | false   | { type: integer }                                 | .5                | the record has no request results             |
        | label  | false   | { type: integer }                                 | .x                | the request results are path id type integer  |
        | label  | false   | { type: array, items: { type: integer } }         | .3,x              | the request results are path id[1] type integer |
        | label  | true    | { type: array, items: { type: integer } }         | .3.x              | the request results are path id[1] type integer |
        | label  | false   | { type: object, properties: { G: { type: integer } } } | .R,100,G,x    | the request results are path id.G type integer |
        | label  | true    | { type: object, properties: { G: { type: integer } } } | .R=100.G=x    | the request results are path id.G type integer |
        | label  | false   | { type: integer }                                 | 5                 | the request results are path id style label   |

      Examples: simple, the default
        | style  | explode | schema                                            | value             | outcome                                       |
        | simple | false   | { type: array, items: { type: integer } }         | 3,x               | the request results are path id[1] type integer |
        | simple | false   | { type: object, properties: { G: { type: integer } } } | R,100,G,x     | the request results are path id.G type integer |
        | simple | true    | { type: object, properties: { G: { type: integer } } } | R=100,G=x     | the request results are path id.G type integer |

    Scenario: A label value keeps its dots when it is one value
      A number such as 3.5 is one value, so only lists and objects split on
      dots, and only when exploded.
      Given the things spec with the parameter { name: id, in: path, required: true, style: label, schema: { type: number } }
      When a call is GET /v1/things/.3.5
      Then the record has no request results

  Rule: A query list is split by its style

    Scenario Outline: A query list in form, spaceDelimited or pipeDelimited style
      Given the things spec with the parameter { name: ids, in: query, style: <style>, explode: <explode>, schema: { type: array, items: { type: integer } } }
      When a call is GET /v1/things?<query>
      Then the request results are query ids[1] type integer

      Examples:
        | style          | explode | query         |
        | form           | false   | ids=3,x       |
        | form           | true    | ids=3&ids=x   |
        | spaceDelimited | false   | ids=3%20x     |
        | pipeDelimited  | false   | ids=3%7Cx     |
        | pipeDelimited  | true    | ids=3&ids=x   |

    Scenario: A form query object not exploded is read as name,value pairs
      Given the things spec with the parameter { name: color, in: query, explode: false, schema: { type: object, properties: { G: { type: integer } } } }
      When a call is GET /v1/things?color=R,100,G,x
      Then the request results are query color.G type integer

  Rule: A header object is read like a simple path object

    Scenario: An exploded header object is read as name=value pairs
      Given the things spec with the parameter { name: X-Color, in: header, explode: true, schema: { type: object, properties: { G: { type: integer } } } }
      When a call carries the header "X-Color: R=100,G=x"
      Then the request results are header X-Color.G type integer

  Rule: A spec is refused only where OpenAPI forbids what it says

    Scenario Outline: A parameter written in a way OpenAPI forbids
      Given the things spec with the parameter <param>
      When the proxy loads the config
      Then the config is refused with a message naming <words>

      Examples:
        | param                                                                             | words                     |
        | { name: f, in: query, schema: {}, content: { application/json: {} } }             | "schema", "content"       |
        | { name: f, in: query, content: { application/json: {}, text/plain: {} } }         | "exactly one media type"  |
        | { name: f, in: query, content: {} }                                               | "exactly one media type"  |
        | { name: f, in: query, style: matrix, schema: {} }                                 | "style matrix", "query f" |
        | { name: X-F, in: header, style: label, schema: {} }                               | "style label", "header X-F" |
        | { name: id, in: path, required: true, style: spaceDelimited, schema: {} }         | "style spaceDelimited"    |
        | { name: f, in: cookie, style: pipeDelimited, schema: {} }                         | "style pipeDelimited"     |
        | { name: X-F, in: header, style: deepObject, schema: {} }                          | "style deepObject"        |
        | { name: id, in: path, required: true, style: form, schema: {} }                  | "style form"              |
        | { name: f, in: query, style: dotted, schema: {} }                                 | "style dotted"            |

    Scenario: A response header in a style other than simple is refused
      Given the things spec whose 200 response has the header RateLimit-Policy with { style: form, schema: {} }
      When the proxy loads the config
      Then the config is refused with a message naming "style form", "RateLimit-Policy"
