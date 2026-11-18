// Command aws-shop is Hotline's example of a Go app using AWS through the
// proxy: a small shop made of four apps, each its own Hotline integration.
// checkout takes payments, invoices makes the invoice PDFs, mailer mails
// them and refunds starts credit notes. See README.md.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"hotline-example/aws-shop/shop"
)

const usage = `usage:
  aws-shop checkout pay --order A-1001 --language en [--template default] [--send-to EMAIL]
  aws-shop checkout send --body TEXT          any text to the order-paid topic, unchanged: to try Hotline's checks
  aws-shop invoices work [--once] [--out FOLDER]
  aws-shop invoices status --order A-1001
  aws-shop invoices fetch --order A-1001 --out FILE
  aws-shop invoices list
  aws-shop invoices cleanup --before 2026-11-01
  aws-shop mailer work [--out FOLDER] [--once]   sends with SES from $MAIL_FROM, or writes to FOLDER
  aws-shop refunds start --order A-1001 --amount 10.00 --reason returned
  aws-shop assistant ask --question TEXT          one question about an order, answered by Gemini Live

every command also takes:
  --proxy URL        Hotline's forward proxy, such as http://localhost:8080; each app adds
                     its own name as the user name, its integration ID (default: $HOTLINE_PROXY;
                     empty: no proxy)
  --endpoint URL     a local SQS instead of AWS (default: $SQS_ENDPOINT)
  --s3-endpoint URL  a local S3 instead of AWS (default: $S3_ENDPOINT)
  --sns-endpoint URL a local SNS instead of AWS (default: $SNS_ENDPOINT)

AWS resources, each from its environment variable, else Peter's own:
  INVOICES_QUEUE_URL, MAIL_QUEUE_URL, ORDER_PAID_TOPIC, INVOICES_BUCKET, ORDERS_TABLE,
  INVOICES_TABLE, SHOP_STREAM, SHOP_BUS, PAYMENT_SECRET, TEMPLATE_PARAMETER, SIGNING_KEY,
  CREDIT_NOTE_WORKFLOW, SHOP_LOG_GROUP (empty: no CloudWatch Logs),
  INVOICE_WORKER_ROLE (empty: no role), PAYMENT_QR_FUNCTION (empty: no payment text),
  MAIL_FROM (an SES sender; empty: the mailer needs --out)

the assistant's Gemini Live settings:
  GEMINI_API_KEY (required), GEMINI_MODEL (default gemini-live-2.5-flash),
  GEMINI_BASE_URL (another address than Google's, such as a fake Gemini)
`

// commands lists each app's commands.
func commands() map[string][]string {
	return map[string][]string{
		"checkout":  {"pay", "send"},
		"invoices":  {"work", "status", "fetch", "list", "cleanup"},
		"mailer":    {"work"},
		"refunds":   {"start"},
		"assistant": {"ask"},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

type options struct {
	proxy, endpoint, s3Endpoint, snsEndpoint             string
	order, language, template, sendTo, body, out, before string
	amount, reason, question                             string
	once                                                 bool
}

// run carries out one command and returns the exit code: 0 done, 1 failed,
// 2 a wrong command line.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 || !known(args[0], args[1]) {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	app, command := args[0], args[1]
	var o options
	flags := flag.NewFlagSet(app+" "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&o.proxy, "proxy", os.Getenv("HOTLINE_PROXY"), "")
	flags.StringVar(&o.endpoint, "endpoint", os.Getenv("SQS_ENDPOINT"), "")
	flags.StringVar(&o.s3Endpoint, "s3-endpoint", os.Getenv("S3_ENDPOINT"), "")
	flags.StringVar(&o.snsEndpoint, "sns-endpoint", os.Getenv("SNS_ENDPOINT"), "")
	flags.StringVar(&o.order, "order", "", "")
	flags.StringVar(&o.language, "language", "", "")
	flags.StringVar(&o.template, "template", "default", "")
	flags.StringVar(&o.sendTo, "send-to", "", "")
	flags.StringVar(&o.body, "body", "", "")
	flags.StringVar(&o.out, "out", "", "")
	flags.StringVar(&o.before, "before", "", "")
	flags.StringVar(&o.amount, "amount", "", "")
	flags.StringVar(&o.reason, "reason", "", "")
	flags.StringVar(&o.question, "question", "", "")
	flags.BoolVar(&o.once, "once", false, "")
	if err := flags.Parse(args[2:]); err != nil {
		return 2
	}
	if missing := missingFlags(app+" "+command, o); missing != "" {
		_, _ = fmt.Fprintf(stderr, "aws-shop %s %s needs %s\n%s", app, command, missing, usage)
		return 2
	}
	cutoff, err := time.Parse(time.DateOnly, o.before)
	if command == "cleanup" && err != nil {
		_, _ = fmt.Fprintf(stderr, "aws-shop invoices cleanup needs --before as a day, such as 2026-11-01\n%s", usage)
		return 2
	}

	a, err := shop.NewApp(ctx, integrationOf(app), shop.Settings{Proxy: o.proxy, SQSEndpoint: o.endpoint, S3Endpoint: o.s3Endpoint, SNSEndpoint: o.snsEndpoint}, names())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	a.Log = slog.New(slog.NewJSONHandler(stderr, nil))
	if err := carryOut(ctx, a, command, o, cutoff, stdout); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// integrationOf is the app's integration ID in Hotline: its own name, but
// shop-assistant for the assistant.
func integrationOf(app string) string {
	if app == "assistant" {
		return "shop-assistant"
	}
	return app
}

func known(app, command string) bool {
	for _, c := range commands()[app] {
		if c == command {
			return true
		}
	}
	return false
}

// missingFlags names what a command needs but didn't get.
func missingFlags(command string, o options) string {
	switch {
	case command == "checkout pay" && (o.order == "" || o.language == ""):
		return "--order and --language"
	case command == "checkout send" && o.body == "":
		return "--body"
	case (command == "invoices status" || command == "invoices fetch") && o.order == "":
		return "--order"
	case command == "invoices fetch" && o.out == "":
		return "--out"
	case command == "invoices cleanup" && o.before == "":
		return "--before"
	case command == "mailer work" && o.out == "" && os.Getenv("MAIL_FROM") == "":
		return "--out, or MAIL_FROM to send with SES"
	case command == "refunds start" && (o.order == "" || o.amount == "" || o.reason == ""):
		return "--order, --amount and --reason"
	case command == "assistant ask" && o.question == "":
		return "--question"
	}
	return ""
}

// names are the AWS resources, each from its environment variable when set.
func names() shop.Names {
	n := shop.DefaultNames()
	for env, field := range map[string]*string{
		"INVOICES_QUEUE_URL":   &n.InvoicesQueue,
		"MAIL_QUEUE_URL":       &n.MailQueue,
		"ORDER_PAID_TOPIC":     &n.OrderPaidTopic,
		"INVOICE_WORKER_ROLE":  &n.WorkerRole,
		"PAYMENT_QR_FUNCTION":  &n.PaymentQRFunction,
		"MAIL_FROM":            &n.MailFrom,
		"INVOICES_BUCKET":      &n.Bucket,
		"ORDERS_TABLE":         &n.OrdersTable,
		"INVOICES_TABLE":       &n.InvoicesTable,
		"SHOP_STREAM":          &n.Stream,
		"SHOP_BUS":             &n.Bus,
		"PAYMENT_SECRET":       &n.PaymentSecret,
		"TEMPLATE_PARAMETER":   &n.TemplateParameter,
		"SIGNING_KEY":          &n.SigningKey,
		"CREDIT_NOTE_WORKFLOW": &n.CreditNoteWorkflow,
		"SHOP_LOG_GROUP":       &n.LogGroup,
	} {
		if v, set := os.LookupEnv(env); set {
			*field = v
		}
	}
	return n
}

//nolint:cyclop // one case per command
func carryOut(ctx context.Context, a shop.App, command string, o options, cutoff time.Time, stdout io.Writer) error {
	switch a.Name + " " + command {
	case "checkout pay":
		id, err := a.Pay(ctx, shop.Payment{OrderID: o.order, Language: o.language, Template: o.template, SendTo: o.sendTo}, time.Now())
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "order %s paid, invoice job %s\n", o.order, id)
	case "checkout send":
		id, err := a.Publish(ctx, o.body)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "sent %s\n", id)
	case "invoices work":
		return a.Work(ctx, o.once, o.out)
	case "invoices status":
		status, err := a.Status(ctx, o.order)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, status)
	case "invoices fetch":
		pdf, err := a.Files.Fetch(ctx, o.order)
		if err != nil {
			return err
		}
		return os.WriteFile(o.out, pdf, 0o600) //nolint:gosec // the user names the file to write
	case "invoices list":
		list, err := a.Files.List(ctx)
		if err != nil {
			return err
		}
		for _, f := range list {
			_, _ = fmt.Fprintf(stdout, "%s\t%d\t%s\n", f.Key, f.Size, f.Modified.UTC().Format(time.RFC3339))
		}
	case "invoices cleanup":
		deleted, err := a.Files.Cleanup(ctx, cutoff)
		if err != nil {
			return err
		}
		for _, key := range deleted {
			_, _ = fmt.Fprintf(stdout, "deleted %s\n", key)
		}
	case "mailer work":
		return a.Mail(ctx, o.once, o.out)
	case "refunds start":
		arn, err := a.Refund(ctx, o.order, o.amount, o.reason)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "credit note started: %s\n", arn)
	case "shop-assistant ask":
		answer, err := ask(ctx, a, o)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, answer)
	}
	return nil
}

// ask opens a Gemini Live session through Hotline and asks the question.
// Google's SDK dials with websocket.DefaultDialer, so its proxy is set
// first, with the assistant's integration ID (see shop.LiveProxy).
func ask(ctx context.Context, a shop.App, o options) (string, error) {
	g := shop.Gemini{APIKey: os.Getenv("GEMINI_API_KEY"), Model: cmp.Or(os.Getenv("GEMINI_MODEL"), "gemini-live-2.5-flash"), BaseURL: os.Getenv("GEMINI_BASE_URL")}
	if o.proxy != "" {
		proxy, err := shop.LiveProxy(o.proxy, a.Name)
		if err != nil {
			return "", err
		}
		websocket.DefaultDialer.Proxy = proxy
	}
	live, err := shop.OpenLive(ctx, g)
	if err != nil {
		return "", fmt.Errorf("open the Gemini Live session: %w", err)
	}
	defer func() { _ = live.Close() }()
	return a.Ask(ctx, live, o.question)
}
