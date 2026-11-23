# Requirements and decisions: docs/decisions/spec-checks.md (M3 part 2, 2026-11-23).
Feature: The whole query string, and cookie styles
  OpenAPI 3.2 lets a spec describe the whole query string as one value, a
  parameter with in: querystring, read by the one media type its content
  names. A finding inside it is placed like a normal query parameter, with
  part query, and the whole value is placed at $ (Peter, 2026-11-23, Q1).
  It also adds a cookie style: form, the default, percent-decodes a cookie's
  value, and cookie takes it as written (Peter, 2026-11-23, Q3).

  Rule: A form query string is checked against its schema

    Scenario: A query string that fits has no results
      Given the querystring spec with form content
      When a call is GET /v1/things?q=cats&limit=5
      Then the record has no request results

    Scenario: A missing name is reported as a normal query parameter would be
      Given the querystring spec with form content
      When a call is GET /v1/things?limit=5
      Then the request results are query q required

    Scenario: A name the schema does not allow is reported by the schema, not as not_in_spec
      Given the querystring spec with form content
      When a call is GET /v1/things?q=cats&size=9
      Then the request results are query size additionalProperties

    Scenario: A login key sent in the query is not checked as part of the app's query
      Given the querystring spec with form content and a login key named key in the query
      When a call is GET /v1/things?q=cats&key=s3cr3t
      Then the record has no request results

    Scenario: A required query string that is missing is reported at $
      Given the querystring spec with form content
      When a call is GET /v1/things
      Then the request results are query $ required

  Rule: A JSON or text query string is percent-decoded and read whole

    Scenario: A JSON query string is checked inside
      Given the querystring spec with json content
      When a call is GET /v1/things?%7B%22limit%22%3A%22five%22%7D
      Then the request results are query limit type integer

    Scenario: A query string that is not JSON is reported at $
      Given the querystring spec with json content
      When a call is GET /v1/things?%7Bnope
      Then the request results are query $ querystring

    Scenario: A text query string is checked as one string
      Given the querystring spec with text content
      When a call is GET /v1/things?abcdef
      Then the request results are query $ maxLength 3

    Scenario: The record hides a JSON query string whole
      Its names are no names, so it cannot be redacted name by name.
      Given the querystring spec with json content
      When a call is GET /v1/things?%7B%22limit%22%3A5%7D
      Then the query in url.full is "REDACTED"

  Rule: A spec that writes a querystring wrongly is refused

    Scenario Outline: A wrongly shaped querystring parameter
      Given a querystring spec with <shape>
      When the proxy loads the config
      Then the config is refused with a message naming "querystring"

      Examples:
        | shape                       |
        | two querystring parameters  |
        | a query parameter beside it |
        | no content                  |
        | two media types             |

  Rule: A cookie is read by its style

    Scenario: A form cookie is percent-decoded before it is checked
      Given the cookie spec with theme of style form
      When a call carries the header "Cookie: theme=dark%20mode"
      Then the record has no request results

    Scenario: A cookie with no style is a form cookie
      Given the cookie spec with theme of no style
      When a call carries the header "Cookie: theme=dark%20mode"
      Then the record has no request results

    Scenario: A cookie-style cookie is checked as written
      Given the cookie spec with theme of style cookie
      When a call carries the header "Cookie: theme=dark%20mode"
      Then the request results are cookie theme enum dark mode

    Scenario: A list cookie sent with its name repeated keeps every value
      Given the cookie spec with ids as an exploded list
      When a call carries the header "Cookie: ids=1; ids=x"
      Then the request results are cookie ids[1] type integer

    Scenario: A list cookie not exploded is written with commas
      Given the cookie spec with ids as a list not exploded
      When a call carries the header "Cookie: ids=1,x"
      Then the request results are cookie ids[1] type integer

    Scenario: An exploded object cookie reads one cookie per property
      Given the cookie spec with prefs as an exploded object
      When a call carries the header "Cookie: size=big; session=abc"
      Then the request results are cookie prefs.size type integer

    Scenario: Other cookies are not extra properties of an exploded object
      Given the cookie spec with prefs as an exploded object
      When a call carries the header "Cookie: size=3; session=abc"
      Then the record has no request results

    Scenario: An object cookie not exploded is written as name and value pairs
      Given the cookie spec with prefs as an object not exploded
      When a call carries the header "Cookie: prefs=size,big"
      Then the request results are cookie prefs.size type integer
