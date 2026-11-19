package shop_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"google.golang.org/genai"

	"hotline-example/aws-shop/internal/fakeaws"
	"hotline-example/aws-shop/internal/fakes3"
	"hotline-example/aws-shop/internal/fakesqs"
	"hotline-example/aws-shop/invoice"
	"hotline-example/aws-shop/shop"
)

func testAWS() []func(*config.LoadOptions) error {
	return []func(*config.LoadOptions) error{
		config.WithRegion("eu-west-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "")),
	}
}

var _ = Describe("The shop's four apps", func() {
	var (
		ctx   context.Context
		sqs   *fakesqs.SQS
		aws   *fakeaws.AWS
		files *fakes3.S3
		names shop.Names
		now   time.Time
		logs  *bytes.Buffer
	)

	BeforeEach(func() {
		ctx = context.Background()
		sqs = fakesqs.Start()
		aws = fakeaws.New(sqs)
		files = fakes3.Start("shop-invoices", aws)
		names = shop.DefaultNames()
		names.Bucket = "shop-invoices"
		names.LogGroup = "" // the log group tests turn it on
		aws.Secrets[names.PaymentSecret] = "pk_test_123"
		aws.Parameters[names.TemplateParameter] = "invoice-compact-v1"
		// The topic's subscription puts each message on the invoices queue,
		// unwrapped (raw message delivery).
		aws.OnPublish = func(topic, message string) {
			if topic == names.OrderPaidTopic {
				sqs.Put(names.InvoicesQueue, message)
			}
		}
		now = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		logs = &bytes.Buffer{}
	})

	// app gives the clients one app uses, through the fake proxy, with the
	// app's name as the integration ID.
	app := func(name string) shop.App {
		a, err := shop.NewApp(ctx, name, shop.Settings{Proxy: "http://" + files.Addr}, names, testAWS()...)
		Expect(err).NotTo(HaveOccurred())
		a.Log = slog.New(slog.NewJSONHandler(logs, nil))
		return a
	}

	pay := func() {
		_, err := app("checkout").Pay(ctx, shop.Payment{OrderID: "A-1001", Language: "en", Template: "invoice-standard-v2", SendTo: "jana@example.com"}, now)
		Expect(err).NotTo(HaveOccurred())
	}

	Describe("checkout", func() {
		It("reads the payment key, saves the order, announces it, and publishes the invoice job on the order-paid topic", func() {
			pay()
			Expect(aws.Targets()).To(Equal([]string{
				"secretsmanager.GetSecretValue",
				"DynamoDB_20120810.PutItem",
				"Kinesis_20131202.PutRecord",
				"SNS.Publish",
			}))
			published := aws.Published()
			Expect(published).To(HaveLen(1))
			Expect(published[0].TopicArn).To(Equal("arn:aws:sns:eu-west-1:192325781350:order-paid"))
			job, err := invoice.Check([]byte(published[0].Message))
			Expect(err).NotTo(HaveOccurred())
			Expect(job.OrderID).To(Equal("A-1001"))
			Expect(sqs.Calls()).To(BeEmpty(), "checkout no longer calls SQS itself")
			Expect(aws.Item("orders", "A-1001")).To(Equal(map[string]any{
				"orderId":  map[string]any{"S": "A-1001"},
				"total":    map[string]any{"N": "65.97"},
				"currency": map[string]any{"S": "EUR"},
				"language": map[string]any{"S": "en"},
				"paidAt":   map[string]any{"S": "2026-11-01T10:00:00Z"},
			}))
			Expect(aws.Records()).To(Equal([]string{`{"type":"order-paid","orderId":"A-1001","at":"2026-11-01T10:00:00Z","total":"65.97"}`}))
			Expect(sqs.WaitingIn(names.InvoicesQueue)).To(Equal(1))
		})

		It("calls through Hotline as the integration checkout, on AWS's own host names", func() {
			pay()
			for _, c := range aws.Calls() {
				Expect(c.ProxyAuth).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("checkout:x"))))
				Expect(c.Authorization).To(HavePrefix("AWS4-HMAC-SHA256 "))
			}
			Expect(aws.Calls()[1].Host).To(Equal("dynamodb.eu-west-1.amazonaws.com"))
			Expect(aws.Calls()[2].Host).To(Equal("kinesis.eu-west-1.amazonaws.com"))
			Expect(aws.Calls()[3].Host).To(Equal("sns.eu-west-1.amazonaws.com"))
		})

		It("publishes any text on the topic, unchanged, to try Hotline's checks", func() {
			_, err := app("checkout").Publish(ctx, `{"orderId":"A-1001"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(aws.Published()[0].Message).To(Equal(`{"orderId":"A-1001"}`))
		})

		It("stops before saving anything when the payment key is missing", func() {
			delete(aws.Secrets, names.PaymentSecret)
			_, err := app("checkout").Pay(ctx, shop.Payment{OrderID: "A-1001", Language: "en", Template: "default"}, now)
			Expect(err).To(HaveOccurred())
			Expect(aws.Item("orders", "A-1001")).To(BeNil())
		})

		It("refuses an order it doesn't know", func() {
			_, err := app("checkout").Pay(ctx, shop.Payment{OrderID: "Z-9999", Language: "en", Template: "default"}, now)
			Expect(err).To(MatchError(invoice.ErrUnknownOrder))
		})
	})

	Describe("invoices", func() {
		work := func() {
			Expect(app("invoices").Work(ctx, true, "")).To(Succeed())
		}

		It("makes the PDF, signs it, stores it, saves its row and announces it", func() {
			pay()
			work()
			Expect(files.File("A-1001.pdf")).To(HavePrefix("%PDF-1.4"))
			Expect(aws.Targets()).To(ContainElements(
				"AmazonSSM.GetParameter",
				"TrentService.Sign",
				"DynamoDB_20120810.PutItem",
				"AWSEvents.PutEvents",
			))
			row := aws.Item("invoices", "A-1001")
			Expect(row).To(HaveKeyWithValue("file", map[string]any{"S": "A-1001.pdf"}))
			Expect(row).To(HaveKeyWithValue("pages", map[string]any{"N": "1"}))
			Expect(row).To(HaveKeyWithValue("templateId", map[string]any{"S": "invoice-standard-v2"}))
			Expect(row).To(HaveKey("signature"))

			event := aws.Events()[0]
			Expect(event).To(HaveKeyWithValue("EventBusName", "shop"))
			Expect(event).To(HaveKeyWithValue("Source", "shop.invoices"))
			Expect(event).To(HaveKeyWithValue("DetailType", "InvoiceReady"))
			var detail map[string]any
			Expect(json.Unmarshal([]byte(event["Detail"].(string)), &detail)).To(Succeed())
			Expect(detail).To(HaveKeyWithValue("file", "A-1001.pdf"))
			Expect(detail).To(HaveKeyWithValue("sendTo", "jana@example.com"))

			Expect(aws.Records()).To(HaveLen(2))
			Expect(aws.Records()[1]).To(HavePrefix(`{"type":"invoice-made","orderId":"A-1001"`))
			Expect(sqs.WaitingIn(names.InvoicesQueue)).To(Equal(0))
		})

		It("takes on the invoice-worker role through STS first, and signs every later call with its keys", func() {
			pay()
			before := len(aws.Calls())
			work()
			calls := aws.Calls()[before:]
			Expect(calls[0].Target).To(Equal("STS.AssumeRole"))
			Expect(calls[0].Host).To(Equal("sts.eu-west-1.amazonaws.com"))
			Expect(calls[0].ProxyAuth).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("invoices:x"))))
			Expect(calls[0].Body).To(HaveKeyWithValue("RoleArn", "arn:aws:iam::192325781350:role/invoice-worker"))
			Expect(calls[0].Authorization).To(ContainSubstring("Credential=AKIDEXAMPLE/"))
			for _, c := range calls[1:] {
				Expect(c.Target).NotTo(Equal("STS.AssumeRole"), "the keys are kept for the whole run")
				Expect(c.Authorization).To(ContainSubstring("Credential="+fakeaws.RoleAccessKey+"/"), c.Target)
				// Keys from STS name their account, and newer SDKs would then
				// send DynamoDB and Kinesis to a host named after it, one the
				// proxy config doesn't list.
				Expect(c.Host).NotTo(HavePrefix("192325781350."), c.Target)
			}
			for _, c := range sqs.Calls() {
				Expect(c.Authorization).To(ContainSubstring("Credential="+fakeaws.RoleAccessKey+"/"), c.Target)
			}
		})

		It("takes on no role when none is set", func() {
			names.WorkerRole = ""
			pay()
			work()
			Expect(aws.Targets()).NotTo(ContainElement("STS.AssumeRole"))
		})

		It("puts the payment text from the payment-qr function on the invoice", func() {
			pay()
			work()
			var invoke fakeaws.Call
			for _, c := range aws.Calls() {
				if c.Target == "Lambda.Invoke" {
					invoke = c
				}
			}
			Expect(invoke.Host).To(Equal("lambda.eu-west-1.amazonaws.com"))
			Expect(invoke.Path).To(Equal("/2015-03-31/functions/payment-qr/invocations"))
			Expect(invoke.Payload).To(MatchJSON(`{"orderId":"A-1001","amount":"65.97","currency":"EUR"}`))
			Expect(string(files.File("A-1001.pdf"))).To(ContainSubstring("SPD*1.0*AM:65.97*CC:EUR*MSG:A-1001"))
		})

		It("leaves the job on the queue when the payment-qr function fails", func() {
			aws.Functions["payment-qr"] = func([]byte) ([]byte, bool) { return []byte(`{"errorMessage":"boom"}`), true }
			pay()
			Expect(app("invoices").Work(ctx, true, "")).NotTo(Succeed())
			Expect(sqs.WaitingIn(names.InvoicesQueue)).To(Equal(1))
		})

		It("calls no function when none is set", func() {
			names.PaymentQRFunction = ""
			pay()
			work()
			Expect(aws.Targets()).NotTo(ContainElement("Lambda.Invoke"))
		})

		It("signs the PDF's SHA-256 fingerprint with the invoice key", func() {
			pay()
			work()
			for _, c := range aws.Calls() {
				if c.Target == "TrentService.Sign" {
					Expect(c.Body).To(HaveKeyWithValue("KeyId", "alias/invoice-signing"))
					Expect(c.Body).To(HaveKeyWithValue("MessageType", "DIGEST"))
					Expect(c.Body).To(HaveKeyWithValue("SigningAlgorithm", "ECDSA_SHA_256"))
				}
			}
		})

		It("uses the template from the shop's settings for a job asking for the default", func() {
			_, err := app("checkout").Pay(ctx, shop.Payment{OrderID: "A-1001", Language: "en", Template: "default"}, now)
			Expect(err).NotTo(HaveOccurred())
			work()
			Expect(aws.Item("invoices", "A-1001")).To(HaveKeyWithValue("templateId", map[string]any{"S": "invoice-compact-v1"}))
		})

		It("doesn't announce an invoice twice when its row is already there", func() {
			pay()
			work()
			pay()
			work()
			Expect(aws.Events()).To(HaveLen(1))
			Expect(sqs.WaitingIn(names.InvoicesQueue)).To(Equal(0))
		})

		It("tells an invoice's status from its row", func() {
			pay()
			work()
			status, err := app("invoices").Status(ctx, "A-1001")
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(ContainSubstring("A-1001.pdf"))
			_, err = app("invoices").Status(ctx, "A-1002")
			Expect(err).To(MatchError(invoice.ErrNoInvoice))
		})
	})

	Describe("mailer", func() {
		ready := func() {
			pay()
			Expect(app("invoices").Work(ctx, true, "")).To(Succeed())
			// The EventBridge rule on the bus puts the event on the mail queue.
			event := map[string]any{"detail-type": "InvoiceReady", "source": "shop.invoices"}
			var detail map[string]any
			Expect(json.Unmarshal([]byte(aws.Events()[0]["Detail"].(string)), &detail)).To(Succeed())
			event["detail"] = detail
			body, _ := json.Marshal(event)
			sqs.Put(names.MailQueue, string(body))
		}

		It("sends the ready invoice by email with SES, with the PDF attached, and deletes the message", func() {
			names.MailFrom = "invoices@shop.example"
			ready()
			Expect(app("mailer").Mail(ctx, true, "")).To(Succeed())
			emails := aws.Emails()
			Expect(emails).To(HaveLen(1))
			Expect(emails[0]).To(HaveKeyWithValue("FromEmailAddress", "invoices@shop.example"))
			Expect(emails[0]).To(HaveKeyWithValue("Destination", map[string]any{"ToAddresses": []any{"jana@example.com"}}))
			raw, err := base64.StdEncoding.DecodeString(emails[0]["Content"].(map[string]any)["Raw"].(map[string]any)["Data"].(string))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).To(ContainSubstring("To: jana@example.com"))
			Expect(string(raw)).To(ContainSubstring("From: invoices@shop.example"))
			Expect(string(raw)).To(ContainSubstring("Content-Type: application/pdf"))
			Expect(sqs.WaitingIn(names.MailQueue)).To(Equal(0))
			for _, c := range aws.Calls() {
				if c.Target == "SES.SendEmail" {
					Expect(c.Host).To(Equal("email.eu-west-1.amazonaws.com"))
					Expect(c.ProxyAuth).To(Equal("Basic " + base64.StdEncoding.EncodeToString([]byte("mailer:x"))))
				}
			}
		})

		It("needs a sender or a folder", func() {
			names.MailFrom = ""
			Expect(app("mailer").Mail(ctx, true, "")).To(MatchError(shop.ErrNoMailWay))
		})

		It("writes the ready invoice to the folder instead, sending nothing, when given one", func() {
			names.MailFrom = "invoices@shop.example"
			ready()
			out := GinkgoT().TempDir()
			Expect(app("mailer").Mail(ctx, true, out)).To(Succeed())
			mail, err := os.ReadFile(filepath.Join(out, "A-1001.eml")) //nolint:gosec // the test's own file
			Expect(err).NotTo(HaveOccurred())
			Expect(string(mail)).To(ContainSubstring("To: jana@example.com"))
			Expect(string(mail)).To(ContainSubstring("Content-Type: application/pdf"))
			Expect(sqs.WaitingIn(names.MailQueue)).To(Equal(0))
			Expect(aws.Emails()).To(BeEmpty())
		})

		It("leaves a message it can't read on the queue", func() {
			sqs.Put(names.MailQueue, "not an event")
			Expect(app("mailer").Mail(ctx, true, GinkgoT().TempDir())).To(Succeed())
			Expect(sqs.WaitingIn(names.MailQueue)).To(Equal(1))
			Expect(logs.String()).To(ContainSubstring("message refused"))
		})
	})

	Describe("refunds", func() {
		It("reads the order and starts the credit note workflow", func() {
			pay()
			run, err := app("refunds").Refund(ctx, "A-1001", "10.00", "returned")
			Expect(err).NotTo(HaveOccurred())
			Expect(run).To(HaveSuffix(":run-1"))
			started := aws.Runs()[0]
			Expect(started).To(HaveKeyWithValue("stateMachineArn", names.CreditNoteWorkflow))
			Expect(started["input"]).To(MatchJSON(`{"orderId":"A-1001","amount":"10.00","currency":"EUR","reason":"returned"}`))
		})

		It("refuses a refund for an order that was never paid", func() {
			_, err := app("refunds").Refund(ctx, "A-1002", "10.00", "returned")
			Expect(err).To(MatchError(shop.ErrNoOrder))
			Expect(aws.Runs()).To(BeEmpty())
		})
	})

	Describe("assistant", func() {
		// ask asks the assistant one question over a scripted Gemini Live
		// session.
		ask := func(live *fakeLive, question string) (string, error) {
			return app("shop-assistant").Ask(ctx, live, question)
		}

		It("answers about a paid order with the orderStatus tool, reading the order from the orders table", func() {
			pay()
			live := &fakeLive{script: []*genai.LiveServerMessage{
				toolCall("call-1", "A-1001"),
				turnComplete(), // the turn that held the tool call ends first
				text("Order A-1001 is paid."),
				turnComplete(),
			}}
			answer, err := ask(live, "Where is order A-1001?")
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(Equal("Order A-1001 is paid."))
			Expect(live.asked).To(Equal([]string{"Where is order A-1001?"}))
			Expect(live.answered).To(HaveLen(1))
			Expect(live.answered[0].ID).To(Equal("call-1"))
			Expect(live.answered[0].Name).To(Equal("orderStatus"))
			Expect(live.answered[0].Response).To(Equal(map[string]any{
				"status": "paid", "paidAt": "2026-11-01T10:00:00Z", "total": "65.97", "currency": "EUR",
			}))
			Expect(aws.Targets()[len(aws.Targets())-1]).To(Equal("DynamoDB_20120810.GetItem"))
		})

		It("tells the model an order it can't find is not found", func() {
			live := &fakeLive{script: []*genai.LiveServerMessage{
				toolCall("call-1", "A-4040"),
				turnComplete(),
				text("I can't find order A-4040."),
				turnComplete(),
			}}
			answer, err := ask(live, "Where is order A-4040?")
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(Equal("I can't find order A-4040."))
			Expect(live.answered[0].Response).To(Equal(map[string]any{"status": "not found"}))
		})

		It("gives the model's response when it calls no tool", func() {
			live := &fakeLive{script: []*genai.LiveServerMessage{
				text("I can only help with orders. "),
				text("Which order number?"),
				turnComplete(),
			}}
			answer, err := ask(live, "Hello!")
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(Equal("I can only help with orders. Which order number?"))
			Expect(live.answered).To(BeEmpty())
		})

		It("says so when the model ends its turn with neither an answer nor a tool call", func() {
			for _, script := range [][]*genai.LiveServerMessage{
				{turnComplete()},
				{toolCall("call-1", "A-1001"), turnComplete(), turnComplete()},
			} {
				_, err := ask(&fakeLive{script: script}, "Where is order A-1001?")
				Expect(err).To(MatchError(shop.ErrNoAnswer))
			}
		})

		It("tells the model a call without an order number is wrong, without reading the table", func() {
			before := len(aws.Targets())
			call := &genai.LiveServerMessage{ToolCall: &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{
				{ID: "call-1", Name: "orderStatus", Args: map[string]any{}},
			}}}
			live := &fakeLive{script: []*genai.LiveServerMessage{call, turnComplete(), text("Which order?"), turnComplete()}}
			answer, err := ask(live, "Where is my order?")
			Expect(err).NotTo(HaveOccurred())
			Expect(answer).To(Equal("Which order?"))
			Expect(live.answered[0].Response).To(Equal(map[string]any{"error": "orderId is missing"}))
			Expect(aws.Targets()).To(HaveLen(before))
		})

		It("tells the model a tool it doesn't have is unknown", func() {
			call := &genai.LiveServerMessage{ToolCall: &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{
				{ID: "call-1", Name: "refund", Args: map[string]any{"orderId": "A-1001"}},
			}}}
			live := &fakeLive{script: []*genai.LiveServerMessage{call, turnComplete(), text("I can't."), turnComplete()}}
			_, err := ask(live, "Refund A-1001")
			Expect(err).NotTo(HaveOccurred())
			Expect(live.answered[0].Response).To(Equal(map[string]any{"error": "no such tool: refund"}))
		})

		It("sends secure WebSockets through the proxy, as the app's integration, and plain ones directly", func() {
			proxy, err := shop.LiveProxy("http://localhost:8080", "shop-assistant")
			Expect(err).NotTo(HaveOccurred())
			secure, err := proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "generativelanguage.googleapis.com"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(secure.String()).To(Equal("http://shop-assistant:x@localhost:8080"))
			plain, err := proxy(&http.Request{URL: &url.URL{Scheme: "http", Host: "127.0.0.1:4010"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(plain).To(BeNil())
		})

		It("refuses a proxy address it can't read", func() {
			_, err := shop.LiveProxy("http://[::1", "shop-assistant")
			Expect(err).To(HaveOccurred())
		})

		It("says so when the order can't be read", func() {
			// A proxy that nobody answers on: DynamoDB can't be reached.
			a, err := shop.NewApp(ctx, "shop-assistant", shop.Settings{Proxy: "http://127.0.0.1:1"}, names, testAWS()...)
			Expect(err).NotTo(HaveOccurred())
			live := &fakeLive{script: []*genai.LiveServerMessage{toolCall("call-1", "A-1001")}}
			_, err = a.Ask(ctx, live, "Where is order A-1001?")
			Expect(err).To(MatchError(ContainSubstring("read the order")))
		})

		It("stops when the session breaks off before the answer", func() {
			live := &fakeLive{script: []*genai.LiveServerMessage{text("Order")}}
			_, err := ask(live, "Where is order A-1001?")
			Expect(err).To(MatchError(io.EOF))
		})

		It("stops when the question can't be sent", func() {
			live := &fakeLive{sendErr: io.ErrClosedPipe}
			_, err := ask(live, "Hello!")
			Expect(err).To(MatchError(io.ErrClosedPipe))
		})

		It("stops when the tool's answer can't be sent", func() {
			live := &fakeLive{script: []*genai.LiveServerMessage{toolCall("call-1", "A-1001")}, toolErr: io.ErrClosedPipe}
			_, err := ask(live, "Where is order A-1001?")
			Expect(err).To(MatchError(io.ErrClosedPipe))
		})
	})

	Describe("the log group", func() {
		It("gets each app's log lines, in the app's own log stream", func() {
			names.LogGroup = "/shop/aws-shop"
			pay()
			var streams []string
			for _, c := range aws.Calls() {
				if strings.HasPrefix(c.Target, "Logs_") {
					streams = append(streams, c.Body["logStreamName"].(string))
				}
			}
			Expect(streams).To(HaveEach("checkout"))
			Expect(aws.Logs()).To(ContainElement(ContainSubstring("order A-1001 paid")))
		})

		It("is left alone without a log group", func() {
			names.LogGroup = ""
			pay()
			Expect(aws.Logs()).To(BeEmpty())
		})
	})
})

// fakeLive is a scripted Gemini Live session: it hands out its script's
// messages one by one, then io.EOF, and keeps what the app sent.
type fakeLive struct {
	script           []*genai.LiveServerMessage
	sendErr, toolErr error
	asked            []string
	answered         []*genai.FunctionResponse
}

func (f *fakeLive) SendClientContent(in genai.LiveClientContentInput) error {
	for _, turn := range in.Turns {
		for _, p := range turn.Parts {
			f.asked = append(f.asked, p.Text)
		}
	}
	return f.sendErr
}

func (f *fakeLive) SendToolResponse(in genai.LiveToolResponseInput) error {
	f.answered = append(f.answered, in.FunctionResponses...)
	return f.toolErr
}

func (f *fakeLive) Receive() (*genai.LiveServerMessage, error) {
	if len(f.script) == 0 {
		return nil, io.EOF
	}
	next := f.script[0]
	f.script = f.script[1:]
	return next, nil
}

func toolCall(id, orderID string) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ToolCall: &genai.LiveServerToolCall{FunctionCalls: []*genai.FunctionCall{
		{ID: id, Name: "orderStatus", Args: map[string]any{"orderId": orderID}},
	}}}
}

func text(t string) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{ModelTurn: &genai.Content{Parts: []*genai.Part{{Text: t}}}}}
}

func turnComplete() *genai.LiveServerMessage {
	return &genai.LiveServerMessage{ServerContent: &genai.LiveServerContent{TurnComplete: true}}
}
