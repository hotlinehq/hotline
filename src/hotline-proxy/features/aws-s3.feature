# Requirements and decisions: docs/proxy-xml-bodies.md.
Feature: AWS S3 through the proxy, with the shop example
  The shop example (examples/aws-shop) has an invoices app that stores each
  invoice PDF in the S3 bucket shop-invoices-192325781350, and fetches, lists and cleans
  up the PDFs there, with the AWS SDK for Go. S3 drives the proxy's XML checks:
  its lists come back as XML, and deleting several files sends XML (Peter,
  2026-10-31).

  The apps run as their own program and talk to AWS's real host names; the
  proxy, with testdata/aws/hotline-proxy.yaml (only its addresses
  changed), reaches a fake AWS by them. The bucket is in the
  host name, shop-invoices-192325781350.s3.eu-west-1.amazonaws.com, as the SDK sends it.

  Background:
    Given a fake AWS for the shop, with the bucket shop-invoices-192325781350
    And the proxy runs with the shop's hotline-proxy.yaml

  Scenario: The worker stores the PDF and the shop fetches it
    When checkout takes the payment for order A-1001
    And the invoices app works through its queue once, into the bucket
    And the invoices app fetches the invoice for A-1001
    Then the app saved the same PDF the fake S3 holds as A-1001.pdf
    And the S3 records are PutObject with url.template /{Key+}#?x-id=PutObject and GetObject with url.template /{Key+}#?x-id=GetObject, with no results

  Scenario: An upload in AWS's chunks reaches S3 unchanged
    When checkout takes the payment for order A-1001
    And the invoices app works through its queue once, into the bucket
    Then the fake S3 got the PDF in AWS's chunks with a matching checksum, signed by the SDK, over https, without Proxy-Authorization

  Scenario: A missing PDF is a normal error response
    When the invoices app fetches the invoice for A-9999
    Then the app said there is no invoice for A-9999
    And the S3 record for GetObject has status 404 and no results

  Scenario: The shop lists the invoices, and the XML response is checked
    Given the bucket holds A-1001.pdf and notes.txt
    When the invoices app lists the invoices
    Then the S3 record for ListObjectsV2 has url.template /#?list-type=2 and the response results body $.Contents[1].Key pattern ^[A-Z]-[0-9]+\.pdf$

  Scenario: The shop deletes old invoices in one call, and the XML call is checked
    Given the bucket holds A-1001.pdf and A-1002.pdf, stored on 2026-10-01
    When the invoices app cleans up the invoices stored before 2026-11-01
    Then the fake S3 holds no files
    And the S3 record for DeleteObjects has url.template /#?delete and no results

  Scenario: Deleting a file that isn't an invoice is reported
    Given the bucket holds notes.txt, stored on 2026-10-01
    When the invoices app cleans up the invoices stored before 2026-11-01
    Then the S3 record for DeleteObjects has url.template /#?delete and the request results body $.Objects[0].Key pattern ^[A-Z]-[0-9]+\.pdf$

  Scenario: The PDF itself is never read or recorded
    When checkout takes the payment for order A-1001
    And the invoices app works through its queue once, into the bucket
    Then no record holds the text of the PDF
