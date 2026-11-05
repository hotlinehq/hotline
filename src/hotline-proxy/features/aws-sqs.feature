# Requirements and decisions: docs/proxy-aws.md and docs/proxy-sqs-example.md.
Feature: AWS SQS through the proxy
  Every SQS call is POST / on AWS's JSON protocol. The action is named only
  in the X-Amz-Target header, so our spec writes each action as its own
  route, /#X-Amz-Target=AmazonSQS.SendMessage, and names the header with
  x-hotline-action-header (Peter, 2026-10-29).

  The spec is the stable one in specs/aws/sqs, made from AWS's Smithy model.
  The invoices integration adds an overlay with its queue URL and the
  invoice job that each message holds, as JSON text inside MessageBody
  (Peter, 2026-10-30).

  Rule: The action header picks the route

    Background:
      Given the SQS spec

    Scenario Outline: Each action is its own route
      Given SQS answers 200 with the json body {}
      When the app sends <action> with the json body <body>
      Then the record has url.template /#X-Amz-Target=AmazonSQS.<action>
      And the record has no request results

      Examples:
        | action         | body                                                                                         |
        | SendMessage    | {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/orders","MessageBody":"hello"} |
        | ReceiveMessage | {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/orders","WaitTimeSeconds":20}  |

    Scenario Outline: A call without a known action has no route
      When the app sends POST / <header> with the json body {}
      Then the record says hotline.schema.skipped=no_spec_route
      And url.full and url.template are left out

      Examples:
        | header                                 |
        | without X-Amz-Target                   |
        | with "X-Amz-Target: AmazonSQS.Teleport" |
        | with "X-Amz-Target: amazonsqs.sendmessage" |

    Scenario: AWS's JSON content type is read as JSON, as the spec marks it
      When the app sends SendMessage with the json body {"MessageBody":"hello","DelaySeconds":"soon"}
      Then the request results are body $.DelaySeconds type integer

    Scenario: AWS's error reply is checked, and its headers are recorded
      Given SQS answers 400 with "x-amzn-RequestId: req-1; x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender" and the json body {"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"no queue"}
      When the app sends SendMessage with the json body {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/orders","MessageBody":"hello"}
      Then the record has no reply results
      And the record keeps the reply headers x-amzn-requestid and x-amzn-query-error

  Rule: Signed calls stay valid

    AWS signs the method, the path, Host, a hash of the body and the headers
    named in SignedHeaders. The proxy holds no AWS keys and cannot sign
    again, so every signed part must reach AWS unchanged (Peter,
    2026-10-29). The fixture was signed once by the AWS SDK; Hotline never
    imports the SDK.

    Scenario Outline: A signed call reaches the vendor with every signed part unchanged
      Given the SQS spec
      And the recorded signed SQS call from the fixture, with Proxy-Authorization added after signing
      When the app sends it to the proxy over <ingress> with <protocol>
      Then every signed part reaches the vendor exactly as in the fixture
      And SQS never sees Proxy-Authorization
      And the vendor receives Content-Length with the body's length, not a body in chunks

      Examples:
        | ingress           | protocol     |
        | the forward proxy | HTTP/1.1     |
        | the Host header   | HTTP/1.1     |
        | the Host header   | plain HTTP/2 |

  Rule: A spec with header routes must name the header

    Scenario Outline: A spec that doesn't name the routes' header is refused
      Given the SQS api with a spec whose route is /#X-Amz-Target=AmazonSQS.SendMessage and <mark>
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-action-header" and "/#X-Amz-Target=AmazonSQS.SendMessage"

      Examples:
        | mark                                  |
        | no x-hotline-action-header            |
        | x-hotline-action-header: X-Amz-Action |

  Rule: A body type is read as JSON where the spec marks it

    The proxy itself reads application/json and types ending in +json as
    JSON, besides form and XML bodies. AWS names its own type,
    application/x-amz-json-1.0, so the spec marks it with x-hotline-body:
    json, next to its schema. The proxy has nothing about AWS in it (Peter,
    2026-10-30). The mark takes xml too, as xml-bodies.feature shows.

    Scenario Outline: The mark decides whether AWS's content type is read
      Given the SQS api with a spec whose SendMessage body is application/x-amz-json-1.0 with <mark>
      When the app sends SendMessage with the json body {"DelaySeconds":"soon"}
      Then <outcome>

      Examples:
        | mark                 | outcome                                                         |
        | x-hotline-body: json | the request results are body $.DelaySeconds type integer        |
        | no x-hotline-body    | the record says hotline.schema.skipped=unsupported_content_type |

    Scenario Outline: A mark the proxy doesn't know is refused
      Given the SQS api with a spec whose SendMessage body is application/x-amz-json-1.0 with <mark>
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-body" and "application/x-amz-json-1.0"

      Examples:
        | mark                 |
        | x-hotline-body: yaml |
        | x-hotline-body: true |

  Rule: The invoice job inside each message is checked

    Background:
      Given the SQS spec with the invoices overlay

    Scenario: A valid job has no results
      Given SQS answers 200 with the json body {"MessageId":"m-1","MD5OfMessageBody":"x"}
      When the app sends SendMessage to the invoices queue with the message {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z","sendTo":"customer@example.com"}
      Then the record has url.template /#X-Amz-Target=AmazonSQS.SendMessage
      And the record has no request results
      And the record has no reply results

    Scenario Outline: A mistake in the job is reported at its place inside MessageBody
      Given SQS answers 200 with the json body {}
      When the app sends SendMessage to the invoices queue with the message <message>
      Then the request results are <results>

      Examples:
        | message | results |
        | {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z"} | body $.MessageBody.orderId required |
        | {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":1001,"templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z"} | body $.MessageBody.orderId type string |
        | {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"a-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z"} | body $.MessageBody.orderId pattern ^[A-Z]-[0-9]+$ |
        | {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","language":"en","requestedAt":"2026-10-30T19:30:00Z"} | body $.MessageBody.templateId required |
        | {"schemaVersion":1,"eventId":"42","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z"} | body $.MessageBody.eventId format uuid |
        | {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"fr","requestedAt":"2026-10-30T19:30:00Z"} | body $.MessageBody.language enum en, de, sk, cs |
        | {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z","priority":"high"} | body $.MessageBody.priority notAllowed |
        | order 42 paid | body $.MessageBody json |

    Scenario: A message sent to another queue is reported
      When the app sends SendMessage to https://sqs.eu-west-1.amazonaws.com/192325781350/refunds with the message {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z"}
      Then the request results are body $.QueueUrl const https://sqs.eu-west-1.amazonaws.com/192325781350/invoices

    Scenario: A bad message is also reported when the worker receives it
      Given SQS answers 200 with one received message {"schemaVersion":1,"orderId":"A-1001"}
      When the app sends ReceiveMessage with the json body {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/invoices"}
      Then the reply results are body $.Messages[0].Body.eventId required

    Scenario: Results never hold values from the message
      When the app sends SendMessage to the invoices queue with the message {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-10-30T19:30:00Z","sendTo":"secret-at-example"}
      Then the request results are body $.MessageBody.sendTo format email
      And no result holds secret-at-example

    Scenario: The plain SQS spec doesn't check the message
      Given the integration orders with the plain SQS spec
      When orders sends SendMessage to the invoices queue with the message order 42 paid
      Then the record has no request results
