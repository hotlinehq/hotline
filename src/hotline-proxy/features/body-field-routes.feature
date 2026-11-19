# Requirements and decisions: docs/proxy-aws-query.md.
Feature: Routes picked by a body field
  Some vendors send every call to the same path and name the action in a
  field of the body. AWS's older form, used by SNS and STS, is POST / with a
  form body such as Action=Publish&TopicArn=...&Message=... Our copy of the
  spec says which field names the action, x-hotline-action-field: Action,
  and each action is its own route, written /#Action=Publish (Peter,
  2026-11-01).

  The field is read once the whole body has arrived, from the copy of up to
  2 MiB the proxy keeps for its checks, at the top level of a form or JSON
  body. That one value picks the route for the request check, the response
  check and the record.

  Rule: The action field picks the route

    Background:
      Given the SNS-like spec

    Scenario Outline: Each action is its own route
      When the app sends the <kind> body <body>
      Then the record has url.template <template>
      And the record has no request results

      Examples:
        | kind | body                                                           | template            |
        | form | Action=Publish&TopicArn=order-paid&Message=hello               | /#Action=Publish    |
        | form | Message=hello&Action=Publish&TopicArn=order-paid               | /#Action=Publish    |
        | form | Action=ListTopics                                              | /#Action=ListTopics |
        | json | {"Action":"Publish","TopicArn":"order-paid","Message":"hello"} | /#Action=Publish    |

    Scenario: The body is checked against the route the field picked
      When the app sends the form body Action=Publish&TopicArn=order-paid
      Then the request results are body $.Message required

    Scenario: The response is checked against the same route
      Given SNS answers 200 with the xml body <PublishResponse><PublishResult></PublishResult></PublishResponse>
      When the app sends the form body Action=Publish&TopicArn=order-paid&Message=hello
      Then the response results are body $.PublishResult.MessageId required

    Scenario: The body reaches the vendor byte for byte
      When the app sends the form body Action=Publish&TopicArn=order-paid&Message=hello
      Then SNS got the body Action=Publish&TopicArn=order-paid&Message=hello

    Scenario Outline: A call without a usable action has no route
      When the app sends <call>
      Then the record says hotline.schema.skipped=no_spec_route
      And url.full and url.template are left out

      Examples:
        | call                                                 |
        | the form body TopicArn=order-paid&Message=hello      |
        | the form body Action=MadeUp                          |
        | the form body Action=publish                         |
        | the json body {"Request":{"Action":"Publish"}}       |
        | the json body ["Action","Publish"]                   |
        | the form body Action=Publish&Message=100%            |
        | the text body Action=Publish                         |
        | a form body of 3 MiB that starts with Action=Publish |

  Rule: A spec names its routes' field, and only one way to name the action

    Scenario Outline: A spec that doesn't name the routes' field is refused
      Given the SNS api with a spec whose route is <route> and <marks>
      When the proxy loads the config
      Then the config is refused with a message naming "<mark>" and "<route>"

      Examples:
        | route            | marks                                                                    | mark                    |
        | /#Action=Publish | no action mark                                                           | x-hotline-action-field  |
        | /#Target=Publish | x-hotline-action-field: Action                                           | x-hotline-action-field  |
        | /#Action=Publish | x-hotline-action-field: Action and x-hotline-action-header: X-Amz-Target | x-hotline-action-header |

  Rule: The proxy keeps 2 MiB of a body for its checks

    Scenario: A full-size SQS message is still checked and named
      AWS allows SQS messages of up to 1 MiB, and the JSON around one takes
      the call just over 1 MiB (Peter, 2026-11-01).
      Given the SQS spec
      When the app sends SendMessage to the invoices queue with a message of 1 MiB
      Then the record holds messaging.destination.name = https://sqs.eu-west-1.amazonaws.com/192325781350/invoices
      And the record has no hotline.schema.skipped
