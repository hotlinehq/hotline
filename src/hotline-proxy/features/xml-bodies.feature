# Requirements and decisions: docs/proxy-xml-bodies.md.
Feature: Checking XML bodies
  An XML body is read into the same tree a JSON body gives, guided by the
  schema and its OpenAPI xml notes: name (the element's name), attribute (a
  value written inside the tag) and wrapped (a list with an outer element).
  Then it is checked exactly like JSON, in calls and in replies, and its
  results look like JSON's (Peter, 2026-10-31). XML is for every vendor; S3
  is the first to use it.

  The vendor here is shaped like S3: a list of files, deleting several at
  once, and an error reply.

  Background:
    Given the XML files spec

  Rule: XML is checked like JSON

    Scenario: A valid XML reply has no results
      Given the files vendor answers 200 with the application/xml body <ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>shop-invoices</Name><KeyCount>2</KeyCount><IsTruncated>false</IsTruncated><Contents><Key>A-1001.pdf</Key><Size>51200</Size></Contents><Contents><Key>A-1002.pdf</Key><Size>48900</Size></Contents></ListBucketResult>
      When the app sends GET /?list-type=2 to the files vendor
      Then the record has no reply results
      And the record has no hotline.schema.skipped

    Scenario Outline: A mistake in an XML reply is reported at its place
      Given the files vendor answers <status> with the application/xml body <body>
      When the app sends GET /?list-type=2 to the files vendor
      Then the reply results are <results>

      Examples:
        | status | body                                                                                                                                                  | results                                  |
        | 200    | <ListBucketResult><Name>other</Name><KeyCount>0</KeyCount></ListBucketResult>                                                                          | body $.Name const shop-invoices          |
        | 200    | <ListBucketResult><Name>shop-invoices</Name></ListBucketResult>                                                                                         | body $.KeyCount required                 |
        | 200    | <ListBucketResult><Name>shop-invoices</Name><KeyCount>two</KeyCount></ListBucketResult>                                                                 | body $.KeyCount type integer             |
        | 200    | <ListBucketResult><Name>shop-invoices</Name><KeyCount>1</KeyCount><IsTruncated>maybe</IsTruncated></ListBucketResult>                                  | body $.IsTruncated type boolean          |
        | 200    | <ListBucketResult><Name>shop-invoices</Name><KeyCount>1</KeyCount><Contents><Key>A-1001.pdf</Key></Contents></ListBucketResult>                         | body $.Contents[0].Size required         |
        | 200    | <ListBucketResult><Name>shop-invoices</Name><KeyCount>2</KeyCount><Contents><Key>A-1001.pdf</Key><Size>1</Size></Contents><Contents><Key>notes.txt</Key><Size>0</Size></Contents></ListBucketResult> | body $.Contents[1].Key pattern ^[A-Z]-[0-9]+\.pdf$ |
        | 200    | <ListBucketResult><Name>shop-invoices</Name><KeyCount>1</KeyCount><Contents><Key>A-1.pdf</Key><Size>1</Size><LastModified>yesterday</LastModified></Contents></ListBucketResult> | body $.Contents[0].LastModified format date-time |
        | 403    | <Error><Message>no</Message></Error>                                                                                                                    | body $.Code required                     |
        | 403    | <Error><Code>Denied</Code><Cause><Message>no</Message></Cause></Error>                                                                                 | body $.Cause.Code required               |

    Scenario Outline: A mistake in an XML call is reported at its place
      Given the files vendor answers 200 with the application/xml body <DeleteResult/>
      When the app sends POST /delete to the files vendor with the application/xml body <body>
      Then the request results are <results>
      And the record has no reply results

      Examples:
        | body                                                                                                           | results                           |
        | <Delete><Quiet>true</Quiet></Delete>                                                                           | body $.Object required            |
        | <Delete><Quiet>yes</Quiet><Object><Key>a</Key></Object></Delete>                                               | body $.Quiet type boolean         |
        | <Delete><Object><Key>a</Key></Object><Object><Key>b</Key></Object><Object><Key>c</Key></Object><Object><Key>d</Key></Object></Delete> | body $.Object maxItems 3 |
        | <Delete><Object/></Delete>                                                                                      | body $.Object[0].Key required     |
        | <Delete><Object><Key>a</Key></Object><Extra>x</Extra></Delete>                                                 | body $.Extra additionalProperties |

    Scenario: A valid XML call has no results
      Given the files vendor answers 200 with the application/xml body <DeleteResult><Deleted><Key>a</Key></Deleted></DeleteResult>
      When the app sends POST /delete to the files vendor with the application/xml body <?xml version="1.0" encoding="UTF-8"?><Delete><!-- old invoices --><Quiet>true</Quiet><Object><Key>a</Key></Object></Delete>
      Then the record has no request results
      And the record has no reply results

    Scenario: Elements the schema doesn't name are ignored, unless it forbids extras
      Given the files vendor answers 200 with the application/xml body <ListBucketResult><Name>shop-invoices</Name><KeyCount>0</KeyCount><Owner><ID>o-1</ID><ID>o-2</ID><ID>o-3</ID></Owner><Tags><Tag>a</Tag></Tags><Tags>b</Tags></ListBucketResult>
      When the app sends GET /?list-type=2 to the files vendor
      Then the record has no reply results

    Scenario: Results never hold a value from the body
      Given the files vendor answers 200 with the application/xml body <ListBucketResult><Name>s3cr3t-bucket</Name><KeyCount>s3cr3t-count</KeyCount></ListBucketResult>
      When the app sends GET /?list-type=2 to the files vendor
      Then the reply results are body $.KeyCount type integer
      And no result holds s3cr3t

  Rule: The xml notes give names, attributes and outer elements

    Scenario: An attribute, a wrapped list and a renamed root are read by the notes
      Given the files vendor answers 201 with the application/xml body <label trackingId="ignored"><trackingId>T-1</trackingId></label>
      When the app sends POST /shipments to the files vendor with the text/xml body <shipment reference="A-1001"><express>true</express><parcels><parcel><weightGrams>1200</weightGrams></parcel><parcel><weightGrams>0</weightGrams></parcel></parcels></shipment>
      Then the request results are body $.parcels[1].weightGrams minimum 1
      And the record has no reply results

    Scenario Outline: A mistake in a shipment is found through its notes
      Given the files vendor answers 201 with the application/xml body <label><trackingId>T-1</trackingId></label>
      When the app sends POST /shipments to the files vendor with the text/xml body <body>
      Then the request results are <results>

      Examples:
        | body                                                                                     | results                       |
        | <shipment><parcels><parcel><weightGrams>1</weightGrams></parcel></parcels></shipment>     | body $.reference required     |
        | <shipment reference="A-1"><parcel><weightGrams>1</weightGrams></parcel></shipment>        | body $.parcels required       |
        | <shipment reference="A-1"><parcels></parcels></shipment>                                  | body $.parcels minItems 1     |
        | <shipment reference="A-1"><parcels><parcel><weightGrams>1</weightGrams></parcel></parcels><express>soon</express></shipment> | body $.express type boolean |

    Scenario: A renamed field is read by its element name
      Given the files vendor answers 201 with the application/xml body <label><trackingId>T-1</trackingId><barcode>B-1</barcode><code>not-this</code></label>
      When the app sends POST /shipments to the files vendor with the text/xml body <shipment reference="A-1"><parcels><parcel><weightGrams>1</weightGrams></parcel></parcels></shipment>
      Then the record has no reply results

    Scenario: A marked XML field is copied into the record
      Given the files vendor answers 201 with the application/xml body <label><trackingId>T-1</trackingId></label>
      When the app sends POST /shipments to the files vendor with the text/xml body <shipment reference="A-1"><parcels><parcel><weightGrams>1</weightGrams></parcel></parcels></shipment>
      Then the record holds shipment.tracking_id = T-1

  Rule: The XML must be readable, with the root element the schema names

    Scenario Outline: A body that isn't the expected XML is reported once
      Given the files vendor answers 200 with the application/xml body <body>
      When the app sends GET /?list-type=2 to the files vendor
      Then the reply results are <results>

      Examples:
        | body                                                                                                    | results                                  |
        | <Error><Code>NoSuchBucket</Code></Error>                                                                | body $ xml_root ListBucketResult         |
        | <ListBucketResult><Name>shop-invoices                                                                   | body $ xml                               |
        | <ListBucketResult/><ListBucketResult/>                                                                  | body $ xml                               |
        | not xml at all                                                                                          | body $ xml                               |
        | <!-- nothing here -->                                                                                   | body $ xml                               |
        | <!DOCTYPE l [<!ENTITY a "shop-invoices">]><ListBucketResult><Name>&a;</Name><KeyCount>0</KeyCount></ListBucketResult> | body $ xml       |

    Scenario: A root element named by the xml notes
      Given the files vendor answers 201 with the application/xml body <label><trackingId>T-1</trackingId></label>
      When the app sends POST /shipments to the files vendor with the text/xml body <Shipment reference="A-1"><parcels><parcel><weightGrams>1</weightGrams></parcel></parcels></Shipment>
      Then the request results are body $ xml_root shipment

    Scenario Outline: XML listed without a schema is only read
      Given the files vendor answers 200 with the application/xml body <ok/>
      When the app sends PUT /notes to the files vendor with the application/xml body <body>
      Then <outcome>

      Examples:
        | body                 | outcome                             |
        | <note>               | the request results are body $ xml  |
        | <note>anything</note> | the record has no request results  |

    Scenario: A body over the copy limit is skipped
      Given the files vendor answers 200 with an application/xml list of 3 MiB
      When the app sends GET /?list-type=2 to the files vendor
      Then the record says hotline.schema.skipped=body_too_large
      And the record has no reply results

  Rule: Which bodies are XML

    Scenario Outline: XML by its content type, or by the spec's mark
      Given the files vendor answers 201 with the application/xml body <label><trackingId>T-1</trackingId></label>
      When the app sends POST /shipments to the files vendor with the <type> body <shipment reference="A-1"><parcels/></shipment>
      Then <outcome>

      Examples:
        | type                       | outcome                                                         |
        | text/xml                   | the request results are body $.parcels minItems 1               |
        | application/shipment+xml   | the request results are body $.parcels minItems 1               |
        | application/vnd.parcels.v1 | the request results are body $.parcels minItems 1               |
        | application/vnd.parcels.v2 | the record says hotline.schema.skipped=unsupported_content_type |

    Scenario Outline: A mark the proxy doesn't know is refused
      Given the files api with a spec whose shipment body is application/vnd.parcels.v1 with <mark>
      When the proxy loads the config
      Then the config is refused with a message naming "x-hotline-body" and "application/vnd.parcels.v1"

      Examples:
        | mark                 |
        | x-hotline-body: yaml |
        | x-hotline-body: true |
