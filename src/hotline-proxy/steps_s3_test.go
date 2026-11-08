package proxy_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

const invoicesBucketHost = "shop-invoices-192325781350.s3.eu-west-1.amazonaws.com"

// s3Templates are the routes of the S3 actions the example app uses.
func s3Templates() map[string]string {
	return map[string]string{
		"PutObject":     "/{Key+}#?x-id=PutObject",
		"GetObject":     "/{Key+}#?x-id=GetObject",
		"ListObjectsV2": "/#?list-type=2",
		"DeleteObjects": "/#?delete",
	}
}

func s3Steps(sc *godog.ScenarioContext, w *world, app *exampleApp) {
	var (
		s3          *fakeS3
		sqs         *fakeSQS
		apis        *fakeShopAWS
		mailFrom    string
		output      string
		appErr      error
		fetchedFile string
	)
	// example runs the example app against AWS's real host names, through
	// the proxy, which reaches the fakes by those names. Each app adds its
	// own name to the proxy address, as its integration ID.
	example := func(args ...string) error {
		binary, err := app.build()
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(context.Background(), binary, args...) //nolint:gosec // the example app
		cmd.Dir = w.dir
		cmd.Env = []string{
			"AWS_REGION=eu-west-1",
			"AWS_ACCESS_KEY_ID=AKIDEXAMPLE",
			"AWS_SECRET_ACCESS_KEY=secret",
			"AWS_EC2_METADATA_DISABLED=true",
			"INVOICES_QUEUE_URL=" + invoicesQueue,
			"INVOICES_BUCKET=shop-invoices-192325781350",
			"HOTLINE_PROXY=http://" + w.addr,
			"HOME=" + w.dir,
			"MAIL_FROM=" + mailFrom,
		}
		out, err := cmd.CombinedOutput()
		output, appErr = string(out), err
		return nil
	}
	succeed := func(args ...string) error {
		if err := example(args...); err != nil {
			return err
		}
		if appErr != nil {
			return failf("the example app %v failed: %v\n%s\nthe proxy said:\n%s", args, appErr, output, w.logs.String())
		}
		return nil
	}
	sc.Step(`^a fake AWS for the shop, with the bucket (\S+)$`, func(bucket string) error {
		s3 = newFakeS3(bucket)
		sqs = &fakeSQS{}
		w.vendor(sqsHost).setAnswer(sqs.ServeHTTP)
		w.vendor(bucket + ".s3.eu-west-1.amazonaws.com").setAnswer(s3.ServeHTTP)
		apis = newFakeShopAWS(sqs)
		for _, host := range shopHosts() {
			w.vendor(host).setAnswer(apis.ServeHTTP)
		}
		return nil
	})
	sc.Step(`^the proxy runs with the shop's hotline-proxy.yaml$`, func() error {
		w.addr = freePort()
		config, err := shopConfig(
			[2]string{`":8080"`, fmt.Sprintf("%q", w.addr)},
			[2]string{"http://localhost:4318", w.receivers[0].endpoint},
		)
		w.rawConfig = config
		if err != nil {
			return err
		}
		return w.start()
	})
	sc.Step(`^checkout takes the payment for order (\S+)$`, func(order string) error {
		return succeed("checkout", "pay", "--order", order, "--language", "en", "--template", "invoice-standard-v2", "--send-to", "customer@example.com")
	})
	sc.Step(`^the invoices app works through its queue once, into the bucket$`, func() error {
		return succeed("invoices", "work", "--once")
	})
	sc.Step(`^the mailer works through its queue once$`, func() error {
		return succeed("mailer", "work", "--once", "--out", filepath.Join(w.dir, "mail"))
	})
	sc.Step(`^checkout takes the payment for order (\S+), sending the invoice to (\S+)$`, func(order, to string) error {
		return succeed("checkout", "pay", "--order", order, "--language", "en", "--template", "invoice-standard-v2", "--send-to", to)
	})
	sc.Step(`^checkout publishes the text (.+) to the order-paid topic$`, func(text string) error {
		return succeed("checkout", "send", "--body", text)
	})
	sc.Step(`^the invoices app tries its queue once$`, func() error {
		return example("invoices", "work", "--once")
	})
	sc.Step(`^the mailer sends its queue's emails once with SES, from (\S+)$`, func(from string) error {
		mailFrom = from
		return succeed("mailer", "work", "--once")
	})
	sc.Step(`^SES got one email for (\S+) with the PDF attached$`, func(order string) error {
		emails := apis.sent()
		if len(emails) != 1 || !strings.Contains(emails[0], "application/pdf") || !strings.Contains(emails[0], order) {
			return failf("SES got %d emails: %q", len(emails), emails)
		}
		return nil
	})
	sc.Step(`^the invoices queue still holds the job$`, func() error {
		if n := sqs.waiting(); n != 1 {
			return failf("the queues hold %d messages, want the one job", n)
		}
		return nil
	})
	sc.Step(`^refunds starts a credit note of (\S+) for order (\S+)$`, func(amount, order string) error {
		return succeed("refunds", "start", "--order", order, "--amount", amount, "--reason", "returned")
	})
	sc.Step(`^the invoices app fetches the invoice for (\S+)$`, func(order string) error {
		fetchedFile = filepath.Join(w.dir, order+".pdf")
		return example("invoices", "fetch", "--order", order, "--out", fetchedFile)
	})
	sc.Step(`^the invoices app lists the invoices$`, func() error {
		return succeed("invoices", "list")
	})
	sc.Step(`^the invoices app cleans up the invoices stored before (\S+)$`, func(day string) error {
		return succeed("invoices", "cleanup", "--before", day)
	})
	sc.Step(`^the mailer wrote (\S+) with the PDF attached$`, func(name string) error {
		mail, err := os.ReadFile(filepath.Join(w.dir, "mail", name)) //nolint:gosec // the test's own folder
		if err != nil {
			return err
		}
		if !strings.Contains(string(mail), "application/pdf") {
			return failf("the mail has no PDF attached:\n%s", mail)
		}
		return nil
	})
	sc.Step(`^both queues are empty$`, func() error {
		if n := sqs.waiting(); n != 0 {
			return failf("the queues still hold %d messages", n)
		}
		return nil
	})
	sc.Step(`^every record has no results$`, func() error {
		for _, r := range w.records() {
			if err := hasNo(r.attrs, "hotline.schema.request.errors", "hotline.schema.response.errors"); err != nil {
				return fmt.Errorf("the record for %v %v: %w", r.attrs["server.address"], r.attrs["url.template"], err)
			}
		}
		return nil
	})
	sc.Step(`^the records come from the integrations (.+)$`, func(list string) error {
		seen := map[string]bool{}
		for _, r := range w.records() {
			seen[fmt.Sprint(r.attrs["hotline.integration.id"])] = true
		}
		got := make([]string, 0, len(seen))
		for id := range seen {
			got = append(got, id)
		}
		sort.Strings(got)
		want := strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' })
		want = slices.DeleteFunc(want, func(s string) bool { return s == "and" })
		sort.Strings(want)
		if !slices.Equal(got, want) {
			return failf("the records come from %v, want %v", got, want)
		}
		return nil
	})
	sc.Step(`^the bucket holds (\S+?)(?: and (\S+?))?(?:, stored on (\S+))?$`, func(first, second, day string) error {
		when := time.Now().UTC()
		if day != "" {
			var err error
			if when, err = time.Parse(time.DateOnly, day); err != nil {
				return err
			}
		}
		for _, key := range []string{first, second} {
			if key != "" {
				s3.put(key, []byte("%PDF-1.4 "+key), when)
			}
		}
		return nil
	})
	sc.Step(`^the app saved the same PDF the fake S3 holds as (\S+)$`, func(key string) error {
		if appErr != nil {
			return failf("the fetch failed: %v\n%s", appErr, output)
		}
		saved, err := os.ReadFile(fetchedFile) //nolint:gosec // the test's own file
		if err != nil {
			return err
		}
		if stored := s3.file(key); !strings.HasPrefix(string(saved), "%PDF-") || string(saved) != string(stored) {
			return failf("the app saved %d bytes, the fake S3 holds %d", len(saved), len(stored))
		}
		return nil
	})
	sc.Step(`^the app said there is no invoice for (\S+)$`, func(order string) error {
		if appErr == nil || !strings.Contains(output, "no invoice") || !strings.Contains(output, order) {
			return failf("the app said %q (%v)", output, appErr)
		}
		return nil
	})
	sc.Step(`^the fake S3 holds no files$`, func() error {
		if n := s3.count(); n != 0 {
			return failf("the fake S3 holds %d files", n)
		}
		return nil
	})
	sc.Step(`^the fake S3 got the PDF in AWS's chunks with a matching checksum, signed by the SDK, over https, without Proxy-Authorization$`, func() error {
		for _, c := range s3.received() {
			if c.method == "PUT" && c.chunked && c.checksum && c.tls &&
				strings.HasPrefix(c.authorization, "AWS4-HMAC-SHA256 ") && c.proxyAuth == "" {
				return nil
			}
		}
		return failf("no upload came as wanted; the fake S3 got %+v", s3.received())
	})
	sc.Step(`^the S3 records are PutObject with url.template (\S+) and GetObject with url.template (\S+), with no results$`, func(put, get string) error {
		records := w.s3Records()
		var templates []string
		for _, r := range records {
			templates = append(templates, fmt.Sprint(r["url.template"]))
			if err := hasNo(r, "hotline.schema.request.errors", "hotline.schema.response.errors"); err != nil {
				return err
			}
		}
		if !slices.Equal(templates, []string{put, get}) {
			return failf("the S3 records have the templates %v", templates)
		}
		return nil
	})
	sc.Step(`^the S3 record for (\w+) has status (\d+) and no results$`, func(action, status string) error {
		r, err := w.s3Record(action)
		if err != nil {
			return err
		}
		if fmt.Sprint(r["http.response.status_code"]) != status {
			return failf("the %s record has status %v", action, r["http.response.status_code"])
		}
		return hasNo(r, "hotline.schema.request.errors", "hotline.schema.response.errors")
	})
	sc.Step(`^the S3 record for (\w+) has url.template (\S+) and (?:no results|the (request|reply) results (.+))$`, func(action, template, side, want string) error {
		r, err := w.s3Record(action)
		if err != nil {
			return err
		}
		if r["url.template"] != template {
			return failf("the %s record has url.template %v", action, r["url.template"])
		}
		if side == "" {
			return hasNo(r, "hotline.schema.request.errors", "hotline.schema.response.errors")
		}
		got, err := resultsOf(r, side)
		if err != nil {
			return err
		}
		if strings.Join(got, "; ") != want {
			return failf("the %s results are %q, want %q", side, strings.Join(got, "; "), want)
		}
		return nil
	})
	sc.Step(`^no record holds the text of the PDF$`, func() error {
		for _, r := range w.records() {
			if text := fmt.Sprint(r.attrs); strings.Contains(text, "Jana Novak") || strings.Contains(text, "%PDF") {
				return failf("a record holds the PDF's text: %v", r.attrs)
			}
		}
		if s3.count() != 1 {
			return failf("the fake S3 holds %d files, want the one PDF", s3.count())
		}
		return nil
	})
}

// s3Records are the records of calls to the invoices bucket, in order.
func (w *world) s3Records() []map[string]any {
	var out []map[string]any
	for _, r := range w.records() {
		if r.attrs["server.address"] == invoicesBucketHost {
			out = append(out, r.attrs)
		}
	}
	return out
}

// s3Record is the one record of an S3 action.
func (w *world) s3Record(action string) (map[string]any, error) {
	var found []map[string]any
	for _, r := range w.s3Records() {
		if r["url.template"] == s3Templates()[action] {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		return nil, failf("want one %s record, got %d", action, len(found))
	}
	return found[0], nil
}
