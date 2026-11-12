# The shop's AWS resources, made against Floci (a free local copy of AWS)
# by floci/up.sh. They are the ones README.md lists for the real AWS, with
# the same names, in the same account number and region, so the Hotline
# config and the overlays need no change.
#
# The account number comes from the access key: Floci takes a key of exactly
# 12 digits as the account, so up.sh sets AWS_ACCESS_KEY_ID to it.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws     = { source = "hashicorp/aws", version = "~> 6.0" }
    archive = { source = "hashicorp/archive", version = "~> 2.4" }
  }
}

variable "region" {
  type    = string
  default = "eu-west-1"
}

variable "account" {
  type    = string
  default = "192325781350"
}

# Every service is reached through AWS_ENDPOINT_URL, which up.sh sets to
# Floci. The skips stop the provider asking AWS itself who we are.
provider "aws" {
  region                      = var.region
  access_key                  = var.account
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  skip_region_validation      = true
  s3_use_path_style           = true
}

locals {
  aws_dir = "${path.module}/../../aws"
}

# --- Queues -----------------------------------------------------------

# The provider waits 20 to 25 seconds for each queue, whatever Floci does, so
# a queue holds its policy itself (a separate policy resource would wait
# again), and no queue waits for another: they are made side by side.
resource "aws_sqs_queue" "invoices" {
  name                    = "invoices"
  sqs_managed_sse_enabled = false
  policy                  = file("${local.aws_dir}/invoices-queue-policy.json")
}

resource "aws_sqs_queue" "mail" {
  name                    = "mail"
  sqs_managed_sse_enabled = false
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "events.amazonaws.com" }
      Action    = "sqs:SendMessage"
      Resource  = "arn:aws:sqs:${var.region}:${var.account}:mail"
      Condition = { ArnEquals = { "aws:SourceArn" = "arn:aws:events:${var.region}:${var.account}:rule/shop/invoice-ready-to-mail" } }
    }]
  })
}

# --- Topic: each paid order goes as is to the invoices queue ---------------

resource "aws_sns_topic" "order_paid" {
  name = "order-paid"
}

resource "aws_sns_topic_subscription" "invoices" {
  topic_arn            = aws_sns_topic.order_paid.arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.invoices.arn
  raw_message_delivery = true
}

# --- Storage ----------------------------------------------------------------

resource "aws_s3_bucket" "invoices" {
  bucket = "shop-invoices-${var.account}"
}

resource "aws_s3_bucket_public_access_block" "invoices" {
  bucket                  = aws_s3_bucket.invoices.id
  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = true
  restrict_public_buckets = true
}

resource "aws_dynamodb_table" "tables" {
  for_each     = toset(["orders", "invoices"])
  name         = each.key
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "orderId"
  attribute {
    name = "orderId"
    type = "S"
  }
}

resource "aws_kinesis_stream" "shop_events" {
  name = "shop-events"
  stream_mode_details {
    stream_mode = "ON_DEMAND"
  }
}

# --- Settings, secrets, keys, logs -----------------------------------------

resource "aws_secretsmanager_secret" "payment_key" {
  name = "shop/payment-key"
}

resource "aws_secretsmanager_secret_version" "payment_key" {
  secret_id     = aws_secretsmanager_secret.payment_key.id
  secret_string = "any value, for the example"
}

resource "aws_ssm_parameter" "default_template" {
  name  = "/shop/invoices/default-template"
  type  = "String"
  value = "invoice-standard-v2"
}

resource "aws_kms_key" "invoice_signing" {
  customer_master_key_spec = "ECC_NIST_P256"
  key_usage                = "SIGN_VERIFY"
}

resource "aws_kms_alias" "invoice_signing" {
  name          = "alias/invoice-signing"
  target_key_id = aws_kms_key.invoice_signing.key_id
}

resource "aws_cloudwatch_log_group" "shop" {
  name = "/shop/aws-shop"
}

# --- Events: each InvoiceReady goes to the mail queue ---------------------

resource "aws_cloudwatch_event_bus" "shop" {
  name = "shop"
}

resource "aws_cloudwatch_event_rule" "invoice_ready" {
  name           = "invoice-ready-to-mail"
  event_bus_name = aws_cloudwatch_event_bus.shop.name
  event_pattern = jsonencode({
    source        = ["shop.invoices"]
    "detail-type" = ["InvoiceReady"]
  })
}

resource "aws_cloudwatch_event_target" "mail" {
  rule           = aws_cloudwatch_event_rule.invoice_ready.name
  event_bus_name = aws_cloudwatch_event_bus.shop.name
  target_id      = "mail"
  arn            = aws_sqs_queue.mail.arn
}

# --- Roles ------------------------------------------------------------------

resource "aws_iam_role" "invoice_worker" {
  name               = "invoice-worker"
  assume_role_policy = file("${local.aws_dir}/invoice-worker-trust.json")
}

resource "aws_iam_role_policy" "invoice_worker" {
  name   = "invoice-worker"
  role   = aws_iam_role.invoice_worker.id
  policy = file("${local.aws_dir}/invoice-worker-policy.json")
}

resource "aws_iam_role" "credit_note_workflow" {
  name = "credit-note-workflow"
  assume_role_policy = jsonencode({
    Statement = [{ Effect = "Allow", Principal = { Service = "states.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role" "payment_qr_function" {
  name = "payment-qr-function"
  assume_role_policy = jsonencode({
    Statement = [{ Effect = "Allow", Principal = { Service = "lambda.amazonaws.com" }, Action = "sts:AssumeRole" }]
  })
}

resource "aws_iam_role_policy_attachment" "payment_qr_logs" {
  role       = aws_iam_role.payment_qr_function.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# --- Workflow and function --------------------------------------------------

resource "aws_sfn_state_machine" "credit_note" {
  name     = "credit-note"
  role_arn = aws_iam_role.credit_note_workflow.arn
  definition = jsonencode({
    StartAt = "CreditNote"
    States  = { CreditNote = { Type = "Pass", End = true } }
  })
}

data "archive_file" "payment_qr" {
  type        = "zip"
  source_file = "${local.aws_dir}/payment_qr.py"
  output_path = "${path.module}/payment-qr.zip"
}

resource "aws_lambda_function" "payment_qr" {
  function_name    = "payment-qr"
  runtime          = "python3.13"
  handler          = "payment_qr.handler"
  role             = aws_iam_role.payment_qr_function.arn
  filename         = data.archive_file.payment_qr.output_path
  source_code_hash = data.archive_file.payment_qr.output_base64sha256
}

# --- Mail sender ------------------------------------------------------------
# The mailer sends from this address. Floci, like AWS, refuses an address that
# is not verified; it verifies this one at once. (The older SES resource is
# used because Floci's answer to the newer one's tag lookup is refused by the
# provider.)

resource "aws_ses_email_identity" "sender" {
  email = "shop@example.com"
}
