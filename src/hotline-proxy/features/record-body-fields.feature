# Agreed with Peter, 2026-10-31. Detail: docs/proxy-body-fields.md.
Feature: Marked body fields and headers are copied into the record
  Bodies are never recorded. But a spec can mark a field of a call's or a
  reply's body with x-hotline-record, giving the name of a record
  attribute, and the proxy then copies that one field's value into the
  record. For example, SQS's QueueUrl is recorded as
  messaging.destination.name, OpenTelemetry's name for a queue, so a log
  tool shows which queue a call went to. The same mark on a header
  parameter of the call copies that header: SQS's X-Amz-Target is recorded
  as messaging.operation.name, such as AmazonSQS.SendMessage.

  A mark is the spec author's promise that the field holds no secret and
  no personal data. Only plain values are copied: text, numbers, and true
  or false, and never from inside a list, where a field has many values. The copied fields are signed with the rest of the record, and
  they never go into the hotline.requests counter.

  Background:
    Given the overlay marks.overlay.yaml:
      """
      overlay: 1.1.0
      info: { title: Marks, version: "1" }
      actions:
        - target: $.components.schemas.Order.properties
          update:
            item: { x-hotline-record: shop.order.item }
            quantity: { x-hotline-record: shop.order.quantity }
            gift: { type: boolean, x-hotline-record: shop.order.gift }
            weight: { type: [number, "null"], x-hotline-record: shop.order.weight }
            customer:
              type: object
              properties:
                country: { type: string, x-hotline-record: shop.customer.country }
        - target: $.components.schemas.Line.properties.sku
          update: { x-hotline-record: shop.line.sku }
        - target: $.paths['/payments'].post.requestBody.content['application/x-www-form-urlencoded'].schema.properties.currency
          update: { x-hotline-record: shop.payment.currency }
      """

  Rule: The marked fields are copied

    Background:
      Given the shop spec with the overlay marks.overlay.yaml

    Scenario: A field of the call
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then the record holds shop.order.item = A1
      And the record holds shop.order.quantity = 2

    Scenario Outline: A <kind> stays a <kind>
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2,<field>}
      Then the record holds <name> = <value> as a <kind>

      Examples:
        | kind          | field          | name              | value |
        | true or false | "gift":true    | shop.order.gift   | true  |
        | number        | "weight":1.5   | shop.order.weight | 1.5   |
        | whole number  | "weight":3     | shop.order.weight | 3     |

    Scenario: A field of the reply
      Given the vendor answers 200 with "X-Request-Id: r1" and the json body {"item":"B2","quantity":1}
      When the app sends GET /orders/0b4e7d2a-5c55-4b36-9f6e-8a0d1f2b3c4d
      Then the record holds shop.order.item = B2

    Scenario: A field inside an object
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2,"customer":{"country":"SK"}}
      Then the record holds shop.customer.country = SK

    Scenario: Each call's fields come from its own body
      The proxy borrows the buffer it keeps a body in from a shared pool
      and gives it back once the call's checks are done (Peter,
      2026-11-02). A buffer given back too early would let one call's
      check read another call's body. The calls here run eight at a time,
      their bodies differ in size, and every other one comes in chunks,
      with no Content-Length, so the calls use buffers of several sizes
      and swap them as they fill.
      Given the vendor answers 201
      When apps send 40 orders at once, each of a different size
      Then each order's record holds its own item and quantity, and its check passed

    Scenario: A field of a form body
      When the app sends POST /payments and the form body amount=100&currency=EUR
      Then the record holds shop.payment.currency = EUR

    Scenario: A field inside a list is not copied, since it has no one value
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2,"lines":[{"sku":"S1"}]}
      Then the record has no attribute shop.line.sku

    Scenario Outline: Nothing is copied when <case>
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body <body>
      Then the record has no attribute <name>

      Examples:
        | case                                   | body                                    | name              |
        | the field is missing                   | {"quantity":2}                          | shop.order.item   |
        | a number is sent for text              | {"item":5,"quantity":2}                 | shop.order.item   |
        | true is sent for text                  | {"item":true,"quantity":2}              | shop.order.item   |
        | text is sent for true or false         | {"item":"A1","quantity":2,"gift":"yes"} | shop.order.gift   |
        | the number is too large for the record | {"item":"A1","quantity":2,"weight":1e400} | shop.order.weight |
        | the body is not JSON                   | {"item":                                | shop.order.item   |

    Scenario: A long text is cut at 256 characters
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and an order whose item is 300 characters long
      Then the record holds shop.order.item with its first 256 characters

    Scenario: The fields stay out of the counter
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then hotline.requests carries no shop.order.item

  Rule: Marked headers of the call are copied

    Background:
      Given the overlay key.overlay.yaml:
        """
        overlay: 1.1.0
        info: { title: Key, version: "1" }
        actions:
          - target: $.paths['/orders'].post.parameters[?@.name == 'Idempotency-Key']
            update: { x-hotline-record: shop.idempotency_key }
        """
      And the shop spec with the overlay key.overlay.yaml
      And the integration's fingerprintKey is read from a file
      And the vendor answers 201

    Scenario: A header is copied, and signed
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then the record holds shop.idempotency_key = k1
      And the signature checks out when rebuilt from the record alone, with the key

    Scenario: A header sent twice is joined
      When the app sends POST /orders with "Idempotency-Key: k1; Idempotency-Key: k2" and the json body {"item":"A1","quantity":2}
      Then the record holds shop.idempotency_key = "k1, k2"

    Scenario: A missing header is not copied
      When the app sends POST /orders and the json body {"item":"A1","quantity":2}
      Then the record has no attribute shop.idempotency_key

    Scenario: A long header is cut at 256 characters
      When the app sends POST /orders with an Idempotency-Key 300 characters long
      Then the record holds shop.idempotency_key with its first 256 characters

  Rule: The copied fields are signed

    Background:
      Given the shop spec with the overlay marks.overlay.yaml
      And the integration's fingerprintKey is read from a file

    Scenario: A copied field is covered by the signature
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      Then the record holds shop.order.item = A1
      And the signature checks out when rebuilt from the record alone, with the key

    Scenario: A change to a copied field shows
      Given the vendor answers 201
      When the app sends POST /orders with "Idempotency-Key: k1" and the json body {"item":"A1","quantity":2}
      And shop.order.item is changed in the received record
      Then the signature no longer checks out

  Rule: A mark the proxy can't use is refused when it starts

    Scenario Outline: <case>
      Given the shop spec with x-hotline-record: <mark> on <field>
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-record", "<place>" and "<problem>"

      Examples:
        | case                         | mark                | field                                                                                                                | place                     | problem                  |
        | A mark on an object          | shop.metadata       | $.paths['/payments'].post.requestBody.content['application/x-www-form-urlencoded'].schema.properties.metadata | properties/metadata       | text, a number           |
        | A mark on a list             | shop.orders         | $.components.schemas.OrderList.properties.orders                                                                     | OrderList/properties/orders | text, a number         |
        | A mark that is not a name    | true                | $.components.schemas.Order.properties.item                                                                           | Order/properties/item     | must be a name           |
        | A name with a space          | "shop item"         | $.components.schemas.Order.properties.item                                                                           | Order/properties/item     | letters, digits          |
        | A name the proxy uses itself | http.request.method | $.components.schemas.Order.properties.item                                                                           | Order/properties/item     | the proxy's own          |
        | A hotline name               | hotline.item        | $.components.schemas.Order.properties.item                                                                           | Order/properties/item     | the proxy's own          |
        | A mark on a query parameter  | shop.limit          | $.paths['/orders'].get.parameters[?@.name == 'limit']                                                                | limit                     | only on a header of the call |
        | A mark on a reply header     | shop.request_id     | $.paths['/orders/{orderId}'].get.responses['200'].headers['X-Request-Id']                                            | X-Request-Id              | only on a header of the call |
        | A bad name on a header       | "shop key"          | $.paths['/orders'].post.parameters[?@.name == 'Idempotency-Key']                                                     | Idempotency-Key           | letters, digits          |

    Scenario: A login header can't be copied
      Given the overlay login.overlay.yaml:
        """
        overlay: 1.1.0
        info: { title: Login, version: "1" }
        actions:
          - target: $.paths['/orders'].get.parameters
            update:
              - { name: Authorization, in: header, x-hotline-record: shop.login }
        """
      And the shop spec with the overlay login.overlay.yaml
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-record", "Authorization" and "login"

    Scenario: A login header the spec names can't be copied either
      Given the overlay key-login.overlay.yaml:
        """
        overlay: 1.1.0
        info: { title: Key login, version: "1" }
        actions:
          - target: $.components.securitySchemes
            update:
              apiKey: { type: apiKey, in: header, name: X-Api-Key }
          - target: $.paths['/orders'].get.parameters
            update:
              - { name: X-Api-Key, in: header, x-hotline-record: shop.api_key }
        """
      And the shop spec with the overlay key-login.overlay.yaml
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-record", "X-Api-Key" and "login"

    Scenario: The same name on a header and in the body of one call
      Given the overlay twice.overlay.yaml:
        """
        overlay: 1.1.0
        info: { title: Twice, version: "1" }
        actions:
          - target: $.paths['/orders'].post.parameters[?@.name == 'Idempotency-Key']
            update: { x-hotline-record: shop.order.item }
          - target: $.components.schemas.Order.properties.item
            update: { x-hotline-record: shop.order.item }
        """
      And the shop spec with the overlay twice.overlay.yaml
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-record", "shop.order.item" and "both a header and the body"

    Scenario: The same name in the call and the reply of one route
      Given the overlay clash.overlay.yaml:
        """
        overlay: 1.1.0
        info: { title: Clash, version: "1" }
        actions:
          - target: $.paths['/orders'].post.responses['201']
            update:
              content:
                application/json:
                  schema: { $ref: '#/components/schemas/Order' }
          - target: $.components.schemas.Order.properties.item
            update: { x-hotline-record: shop.order.item }
        """
      And the shop spec with the overlay clash.overlay.yaml
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-record", "shop.order.item" and "both the call and the reply"

  Rule: SQS calls name their queue and their action

    Scenario Outline: <action> names the queue
      Given the SQS spec with the invoices overlay
      And SQS answers 200 with the json body {}
      When the app sends <action> with the json body {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/invoices"<more>}
      Then the record holds messaging.destination.name = https://sqs.eu-west-1.amazonaws.com/192325781350/invoices

      Examples:
        | action         | more                                                                                                                                                                                         |
        | SendMessage    | ,"MessageBody":"{\"schemaVersion\":1,\"eventId\":\"6f1c2b9e-1d3a-4c5b-8e7f-9a0b1c2d3e4f\",\"orderId\":\"A-1001\",\"templateId\":\"invoice-standard-v2\",\"language\":\"en\",\"requestedAt\":\"2026-10-30T10:00:00Z\"}" |
        | ReceiveMessage | ,"WaitTimeSeconds":1                                                                                                                                                                         |
        | DeleteMessage  | ,"ReceiptHandle":"r-1"                                                                                                                                                                       |

    Scenario Outline: <action> names its action
      Given the SQS spec
      And SQS answers 200 with the json body {}
      When the app sends <action> with the json body {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/orders"<more>}
      Then the record holds messaging.operation.name = AmazonSQS.<action>
      And the record has no request results

      Examples:
        | action         | more                   |
        | SendMessage    | ,"MessageBody":"hello" |
        | ReceiveMessage |                        |
        | DeleteMessage  | ,"ReceiptHandle":"r-1" |
