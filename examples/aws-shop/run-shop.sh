#!/usr/bin/env bash
# run-shop.sh runs the shop's whole story in one go, through Hotline, on
# the AWS resources that already exist (see README.md). It never creates or
# removes anything.
#
#   1. checkout: a customer pays an order; it is saved and announced, and
#                the invoice job is published on the order-paid topic (SNS)
#   2. invoices: takes on its role (STS), then the PDF is made, with the
#                payment text from the payment-qr function (Lambda), signed,
#                stored, saved and announced
#   3. mailer:   the invoice is sent with SES when MAIL_FROM is set, else
#                written to ./mail
#   4. refunds:  a refund starts the credit note workflow
#   5. assistant: answers questions about orders over Gemini Live, with a
#                tool that reads the order from the orders table
#   6. support:   answers a customer's question about an order with Claude,
#                streamed, with a tool that reads the order from the orders
#                table
#   7. two mistakes on purpose, which still reach AWS and which Hotline
#      reports: a broken invoice job, and a write to a table that is not
#      checkout's own
#   7. a vendor that cannot be reached, on purpose: a call to SQS on a port
#      where nothing listens, which Hotline probes to say which side of the
#      connection the problem is on
#
# It expects Hotline to be running already, and reads the usual AWS
# settings (AWS_PROFILE, AWS_REGION, ...), HOTLINE_PROXY, the assistant's
# GEMINI_API_KEY and support's ANTHROPIC_API_KEY. It runs in the Floci
# workflow (floci/run.sh), which also starts a fake Gemini Live and a fake
# Anthropic whose scripts know the order A-1001. It stops at
# the first step that goes wrong, and says which.
#
#   HOTLINE_PROXY=http://localhost:8080 ./run-shop.sh
#
# Settings:
#   ORDER        the order to pay (default a new one each run, such as
#                A-20261101221714, from the date and time)
#   SEND_TO      where the invoice is mailed (default AWS's test inbox,
#                success@simulator.amazonses.com, which accepts the email
#                and delivers nothing)
#   MAIL_FROM    the sender, an address or domain verified in SES (default
#                empty: the email is written to ./mail, not sent)
#   WRONG_TABLE  the table for the second mistake (default orders-archive,
#                a table that doesn't exist, so nothing is written there)
#   DOWN_URL     the address of the vendor that cannot be reached (default
#                SQS's host on port 9, where nothing listens)
set -euo pipefail

cd "$(dirname "$0")"
export HOTLINE_PROXY="${HOTLINE_PROXY:-http://localhost:8080}"
order="${ORDER:-A-$(date +%Y%m%d%H%M%S)}"
send_to="${SEND_TO:-success@simulator.amazonses.com}"
wrong_table="${WRONG_TABLE:-orders-archive}"
down_url="${DOWN_URL:-http://sqs.eu-west-1.amazonaws.com:9/}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p mail
summary=()

echo "Building aws-shop"
go build -o "$work/aws-shop" .
shop() { "$work/aws-shop" "$@"; }

# step runs one command and stops the script when it fails.
step() {
  local name="$1"
  shift
  echo
  echo "== $name"
  if ! "$@"; then
    echo
    echo "Stopped: the step \"$name\" failed. Is Hotline running at $HOTLINE_PROXY?"
    exit 1
  fi
  summary+=("done      $name")
}

# mistake runs one command that is wrong on purpose. It may fail, as AWS
# may refuse it; Hotline reports it either way.
mistake() {
  local name="$1"
  shift
  echo
  echo "== $name (a mistake on purpose)"
  if "$@"; then
    summary+=("sent      $name")
  else
    summary+=("refused   $name (AWS said no; Hotline still reports it)")
  fi
}

# invoiced runs the invoices worker until the invoice of the order is
# stored. The topic passes the job on to the invoices queue a moment after
# checkout publishes it, so the first look can find the queue empty.
invoiced() {
  for _ in 1 2 3 4 5; do
    shop invoices work --once || return 1
    if shop invoices status --order "$order" 2>/dev/null; then
      return 0
    fi
    sleep 2
  done
  echo "no invoice job for $order reached the invoices queue"
  return 1
}

# mailed waits for the shop's EventBridge rule to put InvoiceReady on the
# mail queue, then mails it: with SES when MAIL_FROM is set, else to ./mail.
mailed() {
  local log
  for _ in 1 2 3 4 5; do
    # Plain if, not an empty array: macOS's bash 3.2 calls an empty array
    # unbound under set -u.
    if [[ -n "${MAIL_FROM:-}" ]]; then
      log="$(shop mailer work --once 2>&1)" || true
    else
      log="$(shop mailer work --once --out mail 2>&1)" || true
    fi
    echo "$log"
    if grep -q "invoice $order mailed" <<<"$log"; then
      echo "mailed the invoice of $order"
      return 0
    fi
    sleep 2
  done
  echo "no InvoiceReady event for $order reached the mail queue"
  return 1
}

# answers asks an app, the assistant or support, a question and checks the
# answer starts with what is expected.
answers() {
  local answer
  answer="$(shop "$1" ask --question "$2")" || return 1
  echo "$answer"
  [[ "$answer" == "$3"* ]] || { echo "expected an answer starting with: $3"; return 1; }
}

step "checkout pays $order" shop checkout pay --order "$order" --language en --send-to "$send_to"
step "invoices makes the invoice for $order" invoiced
step "mailer mails the invoice of $order" mailed
step "refunds starts a credit note for $order" shop refunds start --order "$order" --amount 10.00 --reason returned
step "assistant answers about $order" answers assistant "Where is order $order?" "Order $order is paid."
step "assistant says it can't find the order A-4040" answers assistant "Where is order A-4040?" "I can't find order A-4040."
step "assistant answers a greeting without a tool" answers assistant "Hello!" "I can only help with orders."
step "support answers about $order, streamed" answers support "Has my order $order been paid?" "Yes, your order $order is paid."
step "support says it can't find the order A-4040" answers support "Has my order A-4040 been paid?" "I can't find an order A-4040."
step "support answers a greeting without a tool" answers support "Hello!" "Hello! Which order can I help you with?"

mistake "checkout sends a broken invoice job" \
  shop checkout send --body "{\"schemaVersion\":1,\"orderId\":\"$order\"}"
mistake "checkout writes an order to the table $wrong_table" \
  env ORDERS_TABLE="$wrong_table" "$work/aws-shop" checkout pay --order "$order" --language en

# unreachable calls a vendor address where nothing listens, through Hotline,
# as the invoices app. Hotline answers 502 or 504 at once and, apart from the
# call, probes the address: DNS, the TCP connect and TLS (Peter, 2026-11-22).
unreachable() {
  local status
  status=$(curl -s -o /dev/null -m 40 -w '%{http_code}' -x "$HOTLINE_PROXY" -U invoices:x "$down_url" || true)
  echo "Hotline answered $status"
  [[ "$status" == 502 || "$status" == 504 ]]
}
mistake "invoices calls SQS on a port where nothing listens" unreachable

echo
echo "== Summary"
printf '  %s\n' "${summary[@]}"
cat <<EOF

Each app is its own integration in Hotline: look for the integration IDs
checkout, invoices, mailer, refunds, shop-assistant and shop-support in the
records. Each of the assistant's questions has an opening call to Gemini Live
(status 101) and a session record (hotline.websocket.session) in the same
trace. Each of support's streamed responses has its call, a record for each
event (hotline.stream.item), each checked against the Anthropic spec, and a
stream record (hotline.stream) in the same trace. The steps above
should have no results; the two mistakes should each have one:
  - the broken job:   body \$.Message.eventId required, and more, on SNS Publish
  - the wrong table:  body \$.TableName const orders
The topic passes the broken job to the invoices queue. The next time the
invoices worker runs, its ReceiveMessage is reported too, with
body \$.Messages[0].Body.eventId required, and the worker leaves the job on
the queue until SQS moves it to the dead letter queue.
The call to the port where nothing listens has error.type
connection_refused or connection_timeout, and a probe record
(hotline.probe) with its trace says which side the problem is on: on Floci
hotline.probe.verdict refused_by_vendor, on the vendor's side.
EOF
