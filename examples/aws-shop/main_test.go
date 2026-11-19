package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-example/aws-shop/internal/fakeaws"
	"hotline-example/aws-shop/internal/fakes3"
	"hotline-example/aws-shop/internal/fakesqs"
)

const (
	invoicesQueue = "https://sqs.eu-west-1.amazonaws.com/192325781350/invoices"
	mailQueue     = "https://sqs.eu-west-1.amazonaws.com/192325781350/mail"
)

var _ = Describe("The aws-shop program", func() {
	var (
		sqs            *fakesqs.SQS
		aws            *fakeaws.AWS
		files          *fakes3.S3
		stdout, stderr *bytes.Buffer
	)

	BeforeEach(func() {
		sqs = fakesqs.Start()
		aws = fakeaws.New(sqs)
		files = fakes3.Start("shop-invoices", aws)
		aws.Secrets["shop/payment-key"] = "pk_test_123"
		aws.Parameters["/shop/invoices/default-template"] = "invoice-standard-v2"
		// The order-paid topic's subscription puts each message on the
		// invoices queue, unwrapped.
		aws.OnPublish = func(topic, message string) {
			if topic == "arn:aws:sns:eu-west-1:192325781350:order-paid" {
				sqs.Put(invoicesQueue, message)
			}
		}
		stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
		GinkgoT().Setenv("AWS_REGION", "eu-west-1")
		GinkgoT().Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
		GinkgoT().Setenv("AWS_SECRET_ACCESS_KEY", "secret")
		GinkgoT().Setenv("INVOICES_BUCKET", "shop-invoices")
		GinkgoT().Setenv("SHOP_LOG_GROUP", "/shop/aws-shop")
		GinkgoT().Setenv("HOTLINE_PROXY", "http://"+files.Addr)
	})

	run := func(args ...string) int {
		return run(context.Background(), args, stdout, stderr)
	}

	It("runs the whole story: pay, invoice, mail, status and refund", func() {
		Expect(run("checkout", "pay", "--order", "A-1001", "--language", "sk", "--send-to", "jana@example.com")).To(Equal(0), stderr.String())
		Expect(stdout.String()).To(ContainSubstring("order A-1001 paid, invoice job p-1"))

		Expect(run("invoices", "work", "--once")).To(Equal(0), stderr.String())
		Expect(files.File("A-1001.pdf")).To(HavePrefix("%PDF-1.4"))

		// The EventBridge rule on the bus puts the event on the mail queue.
		var detail map[string]any
		Expect(json.Unmarshal([]byte(aws.Events()[0]["Detail"].(string)), &detail)).To(Succeed())
		event, _ := json.Marshal(map[string]any{"detail-type": "InvoiceReady", "source": "shop.invoices", "detail": detail})
		sqs.Put(mailQueue, string(event))
		out := GinkgoT().TempDir()
		Expect(run("mailer", "work", "--once", "--out", out)).To(Equal(0), stderr.String())
		Expect(filepath.Join(out, "A-1001.eml")).To(BeAnExistingFile())

		stdout.Reset()
		Expect(run("invoices", "status", "--order", "A-1001")).To(Equal(0), stderr.String())
		Expect(stdout.String()).To(ContainSubstring("file=A-1001.pdf"))

		stdout.Reset()
		Expect(run("refunds", "start", "--order", "A-1001", "--amount", "10.00", "--reason", "returned")).To(Equal(0), stderr.String())
		Expect(stdout.String()).To(ContainSubstring("credit note started: arn:aws:states:"))

		Expect(aws.Logs()).To(ContainElements(
			ContainSubstring("order A-1001 paid"),
			ContainSubstring("invoice A-1001 made"),
			ContainSubstring("invoice A-1001 mailed"),
			ContainSubstring("refund for A-1001 started"),
		))
	})

	It("calls Hotline with each app's own integration ID", func() {
		Expect(run("checkout", "pay", "--order", "A-1001", "--language", "en")).To(Equal(0), stderr.String())
		Expect(run("refunds", "start", "--order", "A-1001", "--amount", "1.00", "--reason", "late")).To(Equal(0), stderr.String())
		ids := map[string]bool{}
		for _, c := range aws.Calls() {
			raw, err := base64.StdEncoding.DecodeString(c.ProxyAuth[len("Basic "):])
			Expect(err).NotTo(HaveOccurred())
			ids[string(raw)] = true
		}
		Expect(ids).To(Equal(map[string]bool{"checkout:x": true, "refunds:x": true}))
	})

	It("sends the email with SES when MAIL_FROM names a sender", func() {
		GinkgoT().Setenv("MAIL_FROM", "invoices@shop.example")
		Expect(run("checkout", "pay", "--order", "A-1001", "--language", "en", "--send-to", "success@simulator.amazonses.com")).To(Equal(0), stderr.String())
		Expect(run("invoices", "work", "--once")).To(Equal(0), stderr.String())
		var detail map[string]any
		Expect(json.Unmarshal([]byte(aws.Events()[0]["Detail"].(string)), &detail)).To(Succeed())
		event, _ := json.Marshal(map[string]any{"detail-type": "InvoiceReady", "source": "shop.invoices", "detail": detail})
		sqs.Put(mailQueue, string(event))
		Expect(run("mailer", "work", "--once")).To(Equal(0), stderr.String())
		Expect(aws.Emails()).To(HaveLen(1))
		Expect(aws.Emails()[0]).To(HaveKeyWithValue("Destination", map[string]any{"ToAddresses": []any{"success@simulator.amazonses.com"}}))
	})

	It("takes the new resources' names from their environment variables", func() {
		GinkgoT().Setenv("ORDER_PAID_TOPIC", "arn:aws:sns:eu-west-1:192325781350:test-topic")
		GinkgoT().Setenv("INVOICE_WORKER_ROLE", "")
		GinkgoT().Setenv("PAYMENT_QR_FUNCTION", "")
		Expect(run("checkout", "pay", "--order", "A-1001", "--language", "en")).To(Equal(0), stderr.String())
		Expect(aws.Published()[0].TopicArn).To(Equal("arn:aws:sns:eu-west-1:192325781350:test-topic"))
		sqs.Put(invoicesQueue, aws.Published()[0].Message)
		Expect(run("invoices", "work", "--once")).To(Equal(0), stderr.String())
		Expect(aws.Targets()).NotTo(ContainElements("STS.AssumeRole", "Lambda.Invoke"))
	})

	It("takes each resource's name from its environment variable", func() {
		GinkgoT().Setenv("ORDERS_TABLE", "orders-test")
		GinkgoT().Setenv("SHOP_LOG_GROUP", "")
		Expect(run("checkout", "pay", "--order", "A-1001", "--language", "en")).To(Equal(0), stderr.String())
		Expect(aws.Item("orders-test", "A-1001")).NotTo(BeNil())
		Expect(aws.Logs()).To(BeEmpty())
	})

	It("publishes any text unchanged, to try Hotline's checks", func() {
		Expect(run("checkout", "send", "--body", `{"schemaVersion":1}`)).To(Equal(0), stderr.String())
		Expect(aws.Published()[0].Message).To(Equal(`{"schemaVersion":1}`))
		Expect(sqs.WaitingIn(invoicesQueue)).To(Equal(1))
	})

	It("writes the PDF to a folder instead of the bucket", func() {
		Expect(run("checkout", "pay", "--order", "A-1001", "--language", "en")).To(Equal(0), stderr.String())
		out := GinkgoT().TempDir()
		Expect(run("invoices", "work", "--once", "--out", out)).To(Equal(0), stderr.String())
		Expect(filepath.Join(out, "invoice-A-1001.pdf")).To(BeAnExistingFile())
		Expect(files.File("A-1001.pdf")).To(BeNil())
	})

	Describe("the bucket's commands", func() {
		It("fetches an invoice", func() {
			files.Put("A-1001.pdf", []byte("%PDF-1.4 x"), time.Now())
			out := filepath.Join(GinkgoT().TempDir(), "a.pdf")
			Expect(run("invoices", "fetch", "--order", "A-1001", "--out", out)).To(Equal(0), stderr.String())
			pdf, err := os.ReadFile(out) //nolint:gosec // the test's own file
			Expect(err).NotTo(HaveOccurred())
			Expect(string(pdf)).To(Equal("%PDF-1.4 x"))
		})

		It("says when there is no invoice to fetch", func() {
			Expect(run("invoices", "fetch", "--order", "A-9999", "--out", filepath.Join(GinkgoT().TempDir(), "a.pdf"))).To(Equal(1))
			Expect(stderr.String()).To(ContainSubstring("no invoice"))
		})

		It("lists the invoices, one per line", func() {
			files.Put("A-1001.pdf", []byte("12345"), time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
			Expect(run("invoices", "list")).To(Equal(0))
			Expect(stdout.String()).To(Equal("A-1001.pdf\t5\t2026-10-01T08:00:00Z\n"))
		})

		It("cleans up the invoices stored before a day", func() {
			files.Put("A-1001.pdf", []byte("a"), time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC))
			files.Put("A-1002.pdf", []byte("b"), time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC))
			Expect(run("invoices", "cleanup", "--before", "2026-11-01")).To(Equal(0))
			Expect(stdout.String()).To(Equal("deleted A-1001.pdf\n"))
			Expect(files.Keys()).To(Equal([]string{"A-1002.pdf"}))
		})

		It("can reach a local SQS and S3 without Hotline", func() {
			GinkgoT().Setenv("HOTLINE_PROXY", "")
			GinkgoT().Setenv("INVOICE_WORKER_ROLE", "") // the role would come from AWS's own STS
			Expect(run("invoices", "list", "--s3-endpoint", "http://"+files.Addr)).To(Equal(0))
			Expect(files.Calls()[0].RequestURI).To(HavePrefix("/shop-invoices?list-type=2"))
			Expect(run("mailer", "work", "--once", "--out", GinkgoT().TempDir(), "--endpoint", "http://"+sqs.Addr)).To(Equal(0), stderr.String())
			Expect(sqs.Calls()[0].RequestURI).To(Equal("/"))
		})

		It("can reach a local SNS without Hotline", func() {
			GinkgoT().Setenv("HOTLINE_PROXY", "")
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r.Host = "sns.eu-west-1.amazonaws.com" // the fake knows SNS by its host
				aws.ServeHTTP(w, r)
			}))
			defer local.Close()
			Expect(run("checkout", "send", "--body", "x", "--sns-endpoint", local.URL)).To(Equal(0), stderr.String())
			Expect(aws.Published()).To(HaveLen(1))
			GinkgoT().Setenv("SNS_ENDPOINT", local.URL)
			Expect(run("checkout", "send", "--body", "y")).To(Equal(0), stderr.String())
			Expect(aws.Published()).To(HaveLen(2))
		})
	})

	DescribeTable("reports a failure with exit code 1",
		func(args ...string) {
			GinkgoT().Setenv("HOTLINE_PROXY", "http://127.0.0.1:1")
			GinkgoT().Setenv("AWS_MAX_ATTEMPTS", "1") // no retries, so the test is quick
			Expect(run(args...)).To(Equal(1))
			Expect(stderr.String()).NotTo(BeEmpty())
		},
		Entry("pay", "checkout", "pay", "--order", "A-1001", "--language", "en"),
		Entry("send", "checkout", "send", "--body", "x"),
		Entry("work", "invoices", "work", "--once"),
		Entry("status", "invoices", "status", "--order", "A-1001"),
		Entry("list", "invoices", "list"),
		Entry("cleanup", "invoices", "cleanup", "--before", "2026-11-01"),
		Entry("mail", "mailer", "work", "--once", "--out", "x"),
		Entry("refund", "refunds", "start", "--order", "A-1001", "--amount", "1.00", "--reason", "late"),
	)

	Describe("assistant ask", func() {
		BeforeEach(func() {
			// ask sets the proxy of Google's SDK's dialer; this puts it back after.
			proxy := websocket.DefaultDialer.Proxy
			DeferCleanup(func() { websocket.DefaultDialer.Proxy = proxy })
			GinkgoT().Setenv("GEMINI_API_KEY", "test-key-not-real")
		})

		It("answers a question over Gemini Live, as integration shop-assistant", func() {
			gemini := fakeGemini("I can only help with orders.")
			GinkgoT().Setenv("GEMINI_BASE_URL", "ws://"+gemini.Listener.Addr().String())
			Expect(run("assistant", "ask", "--question", "Hello!")).To(Equal(0), stderr.String())
			Expect(stdout.String()).To(Equal("I can only help with orders.\n"))
			// The fake is plain ws on this machine, so it is reached directly;
			// Google's wss address goes through the proxy, as shop-assistant.
			viaProxy, err := websocket.DefaultDialer.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "generativelanguage.googleapis.com"}})
			Expect(err).NotTo(HaveOccurred())
			Expect(viaProxy.String()).To(Equal("http://shop-assistant:x@" + files.Addr))
		})

		It("stops before connecting when GEMINI_API_KEY is not set", func() {
			GinkgoT().Setenv("GEMINI_API_KEY", "")
			Expect(run("assistant", "ask", "--question", "Hello!")).To(Equal(1))
			Expect(stderr.String()).To(ContainSubstring("set GEMINI_API_KEY"))
		})

		It("says so when Gemini Live can't be reached", func() {
			GinkgoT().Setenv("GEMINI_BASE_URL", "ws://127.0.0.1:1")
			Expect(run("assistant", "ask", "--question", "Hello!")).To(Equal(1))
			Expect(stderr.String()).To(ContainSubstring("open the Gemini Live session"))
		})
	})

	It("reports a proxy address it can't read", func() {
		GinkgoT().Setenv("HOTLINE_PROXY", "://nope")
		Expect(run("invoices", "list")).To(Equal(1))
		Expect(stderr.String()).To(ContainSubstring("proxy"))
	})

	DescribeTable("refuses a wrong command line with exit code 2",
		func(args ...string) {
			Expect(run(args...)).To(Equal(2))
			Expect(stderr.String()).NotTo(BeEmpty())
		},
		Entry("no app"),
		Entry("an app without a command", "checkout"),
		Entry("an unknown app", "warehouse", "work"),
		Entry("a command of another app", "mailer", "pay"),
		Entry("an unknown flag", "invoices", "work", "--fast"),
		Entry("pay without an order", "checkout", "pay", "--language", "en"),
		Entry("send without a body", "checkout", "send"),
		Entry("status without an order", "invoices", "status"),
		Entry("fetch without a file", "invoices", "fetch", "--order", "A-1001"),
		Entry("cleanup without a day", "invoices", "cleanup"),
		Entry("cleanup with a day it can't read", "invoices", "cleanup", "--before", "soon"),
		Entry("mail without a folder", "mailer", "work"),
		Entry("a refund without a reason", "refunds", "start", "--order", "A-1001", "--amount", "1.00"),
		Entry("a question to the assistant left out", "assistant", "ask"),
	)
})

// fakeGemini is a Gemini Live server that answers any question with response,
// in one model turn.
func fakeGemini(response string) *httptest.Server {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, err := conn.ReadMessage(); err != nil { // the setup
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"setupComplete":{}}`))
		if _, _, err := conn.ReadMessage(); err != nil { // the question
			return
		}
		answer, _ := json.Marshal(map[string]any{"serverContent": map[string]any{"modelTurn": map[string]any{"parts": []any{map[string]any{"text": response}}}}})
		_ = conn.WriteMessage(websocket.TextMessage, answer)
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"serverContent":{"turnComplete":true}}`))
		_, _, _ = conn.ReadMessage() // until the app closes
	}))
	DeferCleanup(server.Close)
	return server
}
