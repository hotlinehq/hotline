Feature: Writing every call to a local file
  A file exporter writes each call as one line of flat JSON to calls.jsonl
  in its folder: the record, with OpenTelemetry's names, and the call's
  spans inside it as hotline.trace.spans. It writes no metrics. Most uses
  are local: trying Hotline on a laptop, or reading what an app sent
  (Peter, 2026-11-11). Requirements: file-exporter/requirements.md in the
  project files.

  Background:
    Given a file exporter

  Scenario: A call is one line in calls.jsonl
    When a call ends
    Then calls.jsonl has one line
    And the line has the time, a severity, event hotline.request, the trace and span ID, and the resource service.name hotline-proxy
    And the line's attributes have http.request.method PUT and http.response.status_code 200 as a number

  Scenario: The call's spans are inside its line
    When a call ends
    Then the line's hotline.trace.spans has hotline call with no parent, and vendor call under it
    And the folder holds no other file than calls.jsonl

  Scenario: Each span keeps its status
    Given the shop spec
    And the vendor answers 201
    When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
    Then each span in the line has the status the OTLP receiver got, and one is ok

  Scenario: The same record as the OTLP exporter
    When a call ends
    Then every attribute the OTLP receiver got is in the line with the same value

  Scenario: A file exporter alone
    Given no OTLP exporter
    When a call ends
    Then calls.jsonl has one line

  Scenario: A line shows up at once
    The file waits at most 100 ms, not the 15 seconds the OTLP exporters
    wait, so tail -f shows a call as soon as it ends (Peter, 2026-11-11).
    When a call ends
    Then calls.jsonl has the line within a second, while the proxy runs

  Scenario: Rotation by size
    Given the file exporter has maxFileSize 1KiB and maxFiles 2
    When 10 calls end
    Then calls.jsonl is plain text
    And there are 2 rotated files, each ending in .jsonl.gz and holding only whole lines

  Scenario: Rotated files stay plain with compression none
    Given the file exporter has maxFileSize 1KiB, maxFiles 2 and compression none
    When 10 calls end
    Then there are 2 rotated files, each ending in .jsonl and holding only whole lines

  Scenario: A folder that cannot be written refuses the config
    Given the file exporter's path is a file, not a folder
    When the proxy loads the config
    Then the proxy refuses the config, naming the file exporter

  Scenario: Two file exporters on one folder refuse the config
    Given a second file exporter on the same folder
    When the proxy loads the config
    Then the proxy refuses the config, saying two file exporters write to the folder

  Scenario: Two file exporters in different folders
    Given a second file exporter in its own folder
    When a call ends
    Then both folders hold the call's line

  Scenario: A second proxy on the same folder refuses to start
    Given the proxy runs
    When a second proxy starts with a file exporter on the same folder
    Then the second proxy refuses to start, naming the folder

  Scenario: A deleted folder is made again
    Given the proxy runs and a call ended
    When the folder is deleted and another call ends
    Then the call succeeds
    And calls.jsonl is back in the folder with the new call's line

  Scenario: A write that fails is counted
    Given the proxy runs and a call ended
    When the folder is replaced by a file and another call ends
    Then the call succeeds
    And the proxy warns that the export failed

  Scenario: The queue is limited by memory
    Given the integration has content full
    And the file exporter has queueMemory 1KiB
    When a call with a 4KiB request body ends
    Then the call succeeds
    And calls.jsonl has no line
    And the proxy warns that the queue was full

  Scenario: Full content keeps the bodies and every header
    Given the integration has content full
    When a call with a JSON body and an Authorization header ends
    Then the line's hotline.http.request.body decodes to the exact body sent
    And the line's hotline.http.response.body decodes to the vendor's reply
    And the line has http.request.header.authorization as sent
    And the line has http.response.header.x-request-id req-5521
    And the line's url.full is the address as sent

  Scenario: Proxy-Authorization is kept but redacted
    Given the integration has content full
    When a call with a Proxy-Trace header ends
    Then the line has http.request.header.proxy-authorization REDACTED
    And the line has http.request.header.proxy-trace as sent

  Scenario: A long body is cut and flagged
    Given the integration has content full with maxBodySize 1KiB
    And the vendor replies with a 5KiB body
    When a call ends
    Then the app gets all 5KiB
    And the line's hotline.http.response.body holds the first 1KiB
    And the line has hotline.http.response.body.truncated true and http.response.body.size 5120
    And the line has no hotline.http.request.body.truncated

  Scenario: A long request body is cut and flagged
    Given the integration has content full with maxBodySize 1KiB
    When a call with a 4KiB request body ends
    Then the call succeeds
    And the line's hotline.http.request.body holds the first 1KiB of the body sent
    And the line has hotline.http.request.body.truncated true

  Scenario: The OTLP exporter never gets the full content
    Given the integration has content full
    When a call with a JSON body and an Authorization header ends
    Then the OTLP record has no hotline.http.request.body and no http.request.header.authorization

  Scenario: A saved body can be checked against its signature
    The body signature covers the whole body as it passed, as today.
    Given the integration has content full
    When a call with a JSON body and an Authorization header ends
    Then the line's hotline.request.body.signature is the HMAC-SHA256 of the saved body with the integration's key

  Scenario: A call without a spec under full content
    Given the integration has content full
    When a call to a host without an api ends
    Then the line's url.full is the address as sent
    And the line has the request and reply headers and the reply body
    And the line has no hotline.signature

  Scenario: A failed call under full content
    Given the integration has content full
    When a call with a JSON body to a vendor that cannot be reached ends
    Then the line has http.response.status_code 502
    And the line has http.request.header.content-type as sent
    And the line has no hotline.http.response.body

  Scenario: Full content without a file exporter is skipped
    Given no file exporter
    And the integration has content full
    When the proxy loads the config
    Then the proxy warns that integration talon-shop-a has content full but there is no file exporter

  Scenario Outline: Wrong content settings
    Given the integration has <setting>
    When the proxy loads the config
    Then the proxy refuses the config, saying <problem>

    Examples:
      | setting             | problem                       |
      | content everything  | content must be records or full |
      | maxBodySize lots    | maxBodySize must be a size      |

  Scenario Outline: Wrong file exporter settings
    Given the file exporter is set with <setting>
    When the proxy loads the config
    Then the proxy refuses the config, saying <problem>

    Examples:
      | setting                 | problem                                   |
      | maxFiles 0              | maxFiles must be at least 1               |
      | compression zip         | compression must be gzip or none          |
      | no path                 | a file exporter needs a path              |
      | an otlp endpoint as well | an exporter is only one of otlp, file or console |
      | nothing in it           | an exporter must be otlp, file or console |
