# Requirements and decisions: docs/proxy-shop-example.md.
Feature: The shop's AWS apis through the proxy
  The shop example (examples/aws-shop) is four apps, each its own
  integration, using AWS's JSON apis: DynamoDB, Kinesis, EventBridge, Step
  Functions, SSM, Secrets Manager, KMS and CloudWatch Logs, beside SQS and
  S3; SNS and STS on AWS's query protocol; and Lambda and SES v2 on its
  REST and JSON protocol. Each resource is an api in testdata/aws/hotline-proxy.yaml, with the
  stable spec from specs/aws and an overlay naming that resource and the
  shape of what is sent to it (Peter, 2026-11-01).

  Rule: A DynamoDB row is checked in DynamoDB's own form

    Background:
      Given the shop api aws-dynamodb-orders

    Scenario: A valid order row has no results, and the record names the table
      Given AWS answers 200 with the json body {}
      When the app calls DynamoDB_20120810.PutItem with the json body {"TableName":"orders","Item":{"orderId":{"S":"A-1001"},"total":{"N":"12.50"},"currency":{"S":"EUR"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}}}
      Then the record has url.template /#X-Amz-Target=DynamoDB_20120810.PutItem
      And the record has no request results
      And the record holds db.collection.name = orders
      And the record holds db.operation.name = DynamoDB_20120810.PutItem

    Scenario Outline: A mistake in the row is reported at its place
      Given AWS answers 200 with the json body {}
      When the app calls DynamoDB_20120810.PutItem with the json body {"TableName":"orders","Item":<item>}
      Then the request results are <results>

      Examples:
        | item | results |
        | {"orderId":{"S":"A-1001"},"currency":{"S":"EUR"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}} | body $.Item.total required |
        | {"orderId":{"S":"A-1001"},"total":{"S":"12.50"},"currency":{"S":"EUR"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}} | body $.Item.total.N required |
        | {"orderId":{"S":"A-1001"},"total":{"N":"12.5"},"currency":{"S":"EUR"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}} | body $.Item.total.N pattern ^[0-9]+\.[0-9]{2}$ |
        | {"orderId":{"S":"A-1001"},"total":{"N":"12.50"},"currency":{"S":"GBP"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}} | body $.Item.currency.S enum EUR, CZK, USD |
        | {"orderId":{"S":"A-1001"},"total":{"N":"12.50"},"currency":{"S":"EUR"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"},"note":{"S":"x"}} | body $.Item.note additionalProperties |

    Scenario: A row for another table is reported
      Given AWS answers 200 with the json body {}
      When the app calls DynamoDB_20120810.GetItem with the json body {"TableName":"invoices","Key":{"orderId":{"S":"A-1001"}}}
      Then the request results are body $.TableName const orders

    Scenario: A bad row is also reported when it is read
      Given AWS answers 200 with the json body {"Item":{"orderId":{"S":"A-1001"},"total":{"N":"12.50"},"currency":{"S":"EUR"},"paidAt":{"S":"2026-11-01T10:00:00Z"}}}
      When the app calls DynamoDB_20120810.GetItem with the json body {"TableName":"orders","Key":{"orderId":{"S":"A-1001"}}}
      Then the record has no request results
      And the reply results are body $.Item.language required

    Scenario: DynamoDB's "already there" reply is a normal error reply
      Given AWS answers 400 with the json body {"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}
      When the app calls DynamoDB_20120810.PutItem with the json body {"TableName":"orders","Item":{"orderId":{"S":"A-1001"},"total":{"N":"12.50"},"currency":{"S":"EUR"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}},"ConditionExpression":"attribute_not_exists(orderId)"}
      Then the record has no request results
      And the record has no reply results

    Scenario: Results never hold values from the row
      Given AWS answers 200 with the json body {}
      When the app calls DynamoDB_20120810.PutItem with the json body {"TableName":"orders","Item":{"orderId":{"S":"A-1001"},"total":{"N":"12.50"},"currency":{"S":"GBP"},"language":{"S":"en"},"paidAt":{"S":"2026-11-01T10:00:00Z"}}}
      Then the request results are body $.Item.currency.S enum EUR, CZK, USD
      And no result holds A-1001

  Rule: A Kinesis record's base64 data is decoded and checked

    Background:
      Given the shop api aws-kinesis-shop-events
      And AWS answers 200 with the json body {"ShardId":"shardId-000000000000","SequenceNumber":"1"}

    Scenario: A valid shop event has no results, and the record names the stream
      When the app puts a Kinesis record holding {"type":"order-paid","orderId":"A-1001","at":"2026-11-01T10:00:00Z","total":"12.50"}
      Then the record has no request results
      And the record holds messaging.destination.name = shop-events

    Scenario Outline: A mistake in the data is reported at its place
      When the app puts a Kinesis record holding <data>
      Then the request results are <results>

      Examples:
        | data | results |
        | {"type":"order-paid","orderId":"A-1001"} | body $.Data.at required |
        | {"type":"order-shipped","orderId":"A-1001","at":"2026-11-01T10:00:00Z"} | body $.Data.type enum order-paid, invoice-made |
        | order A-1001 paid | body $.Data json |

    Scenario: Data that is not base64 is reported
      When the app calls Kinesis_20131202.PutRecord with the json body {"StreamName":"shop-events","PartitionKey":"A-1001","Data":"not base64!"}
      Then the request results are body $.Data base64

  Rule: The other resources are pinned, and what they carry is checked

    Scenario: An EventBridge event's Detail is checked
      Given the shop api aws-eventbridge-shop
      And AWS answers 200 with the json body {"FailedEntryCount":0,"Entries":[{"EventId":"e-1"}]}
      When the app calls AWSEvents.PutEvents with the json body {"Entries":[{"EventBusName":"shop","Source":"shop.invoices","DetailType":"InvoiceReady","Detail":"{\"orderId\":\"A-1001\",\"eventId\":\"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10\",\"language\":\"en\",\"createdAt\":\"2026-11-01T10:00:00Z\"}"}]}
      Then the request results are body $.Entries[0].Detail.file required
      And the record holds rpc.method = AWSEvents.PutEvents

    Scenario: An event to another bus is reported
      Given the shop api aws-eventbridge-shop
      And AWS answers 200 with the json body {"FailedEntryCount":0,"Entries":[{"EventId":"e-1"}]}
      When the app calls AWSEvents.PutEvents with the json body {"Entries":[{"EventBusName":"default","Source":"shop.invoices","DetailType":"InvoiceReady","Detail":"{\"orderId\":\"A-1001\",\"eventId\":\"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10\",\"file\":\"A-1001.pdf\",\"language\":\"en\",\"createdAt\":\"2026-11-01T10:00:00Z\"}"}]}
      Then the request results are body $.Entries[0].EventBusName const shop

    Scenario: A refund's input to the workflow is checked
      Given the shop api aws-sfn-credit-note
      And AWS answers 200 with the json body {"executionArn":"arn:aws:states:eu-west-1:192325781350:execution:credit-note:r-1","startDate":1793527200}
      When the app calls AWSStepFunctions.StartExecution with the json body {"stateMachineArn":"arn:aws:states:eu-west-1:192325781350:stateMachine:credit-note","input":"{\"orderId\":\"A-1001\",\"amount\":\"12.50\",\"currency\":\"EUR\",\"reason\":\"broken\"}"}
      Then the request results are body $.input.reason enum returned, damaged, late, other

    Scenario: The mailer's message, an EventBridge event, is checked when received
      Given the shop api aws-sqs-mail
      And AWS answers 200 with the json body {"Messages":[{"MessageId":"m-1","ReceiptHandle":"r-1","MD5OfBody":"x","Body":"{\"detail-type\":\"InvoiceReady\",\"source\":\"shop.invoices\",\"detail\":{\"orderId\":\"A-1001\",\"eventId\":\"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10\",\"language\":\"en\",\"createdAt\":\"2026-11-01T10:00:00Z\"}}"}]}
      When the app calls AmazonSQS.ReceiveMessage with the json body {"QueueUrl":"https://sqs.eu-west-1.amazonaws.com/192325781350/mail"}
      Then the reply results are body $.Messages[0].Body.detail.file required

    Scenario Outline: Each app reads only its own secret, setting and key
      Given the shop api <api>
      And AWS answers 200 with the json body {}
      When the app calls <action> with the json body <body>
      Then the request results are <results>

      Examples:
        | api                     | action                         | body | results |
        | aws-secrets-payment     | secretsmanager.GetSecretValue  | {"SecretId":"shop/other"} | body $.SecretId const shop/payment-key |
        | aws-ssm-shop            | AmazonSSM.GetParameter         | {"Name":"/shop/other"} | body $.Name const /shop/invoices/default-template |
        | aws-kms-invoice-signing | TrentService.Sign              | {"KeyId":"alias/other","Message":"AAAA","MessageType":"DIGEST","SigningAlgorithm":"ECDSA_SHA_256"} | body $.KeyId const alias/invoice-signing |
        | aws-logs-shop           | Logs_20140328.PutLogEvents     | {"logGroupName":"/shop/aws-shop","logStreamName":"payments","logEvents":[{"timestamp":1793527200000,"message":"hello"}]} | body $.logStreamName enum checkout, invoices, mailer, refunds |

  Rule: Checkout publishes the invoice job on the order-paid topic, in SNS's form body

    SNS names the action in the form field Action, so its spec carries
    x-hotline-action-field: Action. Docs: docs/proxy-aws-query.md.

    Background:
      Given the shop api aws-sns-order-paid
      And AWS answers 200 and the xml body <PublishResponse><PublishResult><MessageId>p-1</MessageId></PublishResult><ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata></PublishResponse>

    Scenario: A valid job has no results, and the record names the topic and the action
      When the app publishes {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-11-01T10:00:00Z"} to the topic arn:aws:sns:eu-west-1:192325781350:order-paid
      Then the record has url.template /#Action=Publish
      And the record has no request results
      And the record has no reply results
      And the record holds messaging.destination.name = arn:aws:sns:eu-west-1:192325781350:order-paid
      And the record holds messaging.operation.name = Publish

    Scenario: A mistake in the job is reported at its place
      When the app publishes {"orderId":"A-1001"} to the topic arn:aws:sns:eu-west-1:192325781350:order-paid
      Then the request results are body $.Message.schemaVersion required

    Scenario: A job for another topic is reported
      When the app publishes {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-11-01T10:00:00Z"} to the topic arn:aws:sns:eu-west-1:192325781350:other
      Then the request results are body $.TopicArn const arn:aws:sns:eu-west-1:192325781350:order-paid

    Scenario: SNS's error reply is a normal error reply
      Given AWS answers 403 and the xml body <ErrorResponse><Error><Type>Sender</Type><Code>AuthorizationError</Code><Message>no</Message></Error><RequestId>r-1</RequestId></ErrorResponse>
      When the app publishes {"schemaVersion":1,"eventId":"5f0c1b6e-3a52-4f7e-9d0e-2b8f6f1f2a10","orderId":"A-1001","templateId":"invoice-standard-v2","language":"en","requestedAt":"2026-11-01T10:00:00Z"} to the topic arn:aws:sns:eu-west-1:192325781350:order-paid
      Then the record has no request results
      And the record has no reply results

  Rule: The invoices worker takes on the invoice-worker role through STS

    Background:
      Given the shop api aws-sts-invoice-worker
      And AWS answers 200 and the xml body <AssumeRoleResponse><AssumeRoleResult><Credentials><AccessKeyId>ASIAFAKEROLEKEY00001</AccessKeyId><SecretAccessKey>fake-role-secret-key</SecretAccessKey><SessionToken>fake-role-session-token</SessionToken><Expiration>2026-11-01T11:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>

    Scenario: Taking on the role is recorded, and its keys never are
      When the app takes on the role arn:aws:iam::192325781350:role/invoice-worker
      Then the record has url.template /#Action=AssumeRole
      And the record has no request results
      And the record has no reply results
      And the record holds rpc.method = AssumeRole
      And no record and no span holds ASIAFAKEROLEKEY00001 or fake-role-secret-key or fake-role-session-token

    Scenario: Taking on another role is reported
      When the app takes on the role arn:aws:iam::192325781350:role/admin
      Then the request results are body $.RoleArn const arn:aws:iam::192325781350:role/invoice-worker

  Rule: The invoices worker asks the payment-qr function for the invoice's payment text

    Background:
      Given the shop api aws-lambda-payment-qr

    Scenario: The function's input and output are checked, and the record names the function
      Given AWS answers 200 and the json body {"qrText":"SPD*1.0*AM:10.00*CC:EUR*MSG:A-1001"}
      When the app sends AWS POST /2015-03-31/functions/payment-qr/invocations with the application/octet-stream body {"orderId":"A-1001","amount":"10.00","currency":"EUR"}
      Then the record has url.template /2015-03-31/functions/{FunctionName}/invocations
      And the record has no request results
      And the record has no reply results
      And the record's url.full ends with /2015-03-31/functions/payment-qr/invocations

    Scenario: A mistake in the input is reported at its place
      Given AWS answers 200 and the json body {"qrText":"x"}
      When the app sends AWS POST /2015-03-31/functions/payment-qr/invocations with the application/octet-stream body {"orderId":"A-1001"}
      Then the request results are body $.amount required

    Scenario: A failing function is reported, and its error header kept
      Given AWS answers 200 with "X-Amz-Function-Error: Unhandled" and the json body {"errorMessage":"boom","errorType":"ValueError"}
      When the app sends AWS POST /2015-03-31/functions/payment-qr/invocations with the application/octet-stream body {"orderId":"A-1001","amount":"10.00","currency":"EUR"}
      Then the reply results are body $.qrText required
      And the record keeps the reply header X-Amz-Function-Error as Unhandled

    Scenario: Calling another function is reported
      Given AWS answers 200 and the json body {"qrText":"x"}
      When the app sends AWS POST /2015-03-31/functions/admin-tool/invocations with the application/octet-stream body {"orderId":"A-1001","amount":"10.00","currency":"EUR"}
      Then the request results are path FunctionName const payment-qr

  Rule: The mailer sends the invoice email with SES, and no address reaches a record

    Background:
      Given the shop api aws-ses-invoice-email
      And AWS answers 200 and the json body {"MessageId":"m-1"}

    Scenario: A whole email has no results
      When the app sends AWS POST /v2/email/outbound-emails with the application/json body {"FromEmailAddress":"invoices@shop.example","Destination":{"ToAddresses":["success@simulator.amazonses.com"]},"Content":{"Raw":{"Data":"RnJvbTogaW52b2ljZXM="}}}
      Then the record has url.template /v2/email/outbound-emails
      And the record has no request results
      And no record and no span holds success@simulator.amazonses.com or invoices@shop.example

    Scenario: A wrong address is reported, without the address
      When the app sends AWS POST /v2/email/outbound-emails with the application/json body {"FromEmailAddress":"invoices@shop.example","Destination":{"ToAddresses":["not an address"]},"Content":{"Raw":{"Data":"RnJvbTogaW52b2ljZXM="}}}
      Then the request results are body $.Destination.ToAddresses[0] format email
      And no result holds address

  Rule: The whole story goes through Hotline, each app as its own integration

    The apps run as the real aws-shop program and talk to AWS's real host
    names; the proxy, with testdata/aws/hotline-proxy.yaml (only its
    addresses changed), reaches a fake AWS by them. Like the shop's
    EventBridge rule, the fake puts each InvoiceReady event on the mail
    queue (Peter, 2026-11-01).

    Background:
      Given a fake AWS for the shop, with the bucket shop-invoices-192325781350
      And the proxy runs with the shop's hotline-proxy.yaml

    Scenario: An order is paid, invoiced, mailed and refunded, with no results
      When checkout takes the payment for order A-1001
      And the invoices app works through its queue once, into the bucket
      And the mailer works through its queue once
      And refunds starts a credit note of 10.00 for order A-1001
      Then the mailer wrote A-1001.eml with the PDF attached
      And both queues are empty
      And every record has no results
      And the records come from the integrations checkout, invoices, mailer and refunds
      And the first invoices record is AssumeRole, and checkout made no SQS call

    Scenario: The mailer sends the invoice with SES, and no address reaches a record
      When checkout takes the payment for order A-1001, sending the invoice to success@simulator.amazonses.com
      And the invoices app works through its queue once, into the bucket
      And the mailer sends its queue's emails once with SES, from invoices@shop.example
      Then SES got one email for A-1001 with the PDF attached
      And every record has no results
      And no record and no span holds success@simulator.amazonses.com or invoices@shop.example

    Scenario: A broken job is reported by both checkout and the invoices worker
      When checkout publishes the text {"orderId":"A-1001"} to the order-paid topic
      And the invoices app tries its queue once
      Then the checkout record for /#Action=Publish has the request results body $.Message.schemaVersion required
      And the invoices record for /#X-Amz-Target=AmazonSQS.ReceiveMessage has the reply results body $.Messages[0].Body.schemaVersion required
      And the invoices queue still holds the job
