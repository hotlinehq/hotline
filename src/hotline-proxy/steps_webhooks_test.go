package proxy_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// The vendor specs as the repository holds them, made by tools/stripe-openapi
// and written by hand.
const (
	stripeSpec = "../../specs/stripe/api/2026-09-30.endive.openapi.yaml"
	slackSpec  = "../../specs/slack/incoming-webhooks/2026-11-23.openapi.yaml"
)

// The made-up parts of the app's Slack webhook address: each is part of
// the secret, so no record, span, log, console line or file line may hold
// any of them.
const (
	slackTeam  = "T0WEBHOOK1"
	slackBot   = "B0WEBHOOK2"
	slackToken = "abcdEFGHijklMNOPqrstUVWX"
)

// stripeLogin is the app's made-up Stripe key, which the spec's security
// section asks for.
const stripeLogin = "Authorization: Bearer sk_test_not_a_real_key"

// newWebhookEndpoint is Stripe's answer to creating a webhook endpoint, as
// its docs show it.
const newWebhookEndpoint = `{"id":"we_1Mr5","object":"webhook_endpoint","api_version":null,"application":null,"created":1700000000,"description":null,"enabled_events":["invoice.paid"],"livemode":false,"metadata":{},"secret":"whsec_not_a_real_secret","status":"enabled","url":"https://shop.example/hooks"}`

// deletedWebhookEndpoint is Stripe's answer to deleting one.
const deletedWebhookEndpoint = `{"id":"we_1Mr5","object":"webhook_endpoint","deleted":true}`

func webhookSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the Stripe spec from specs, with Stripe answering (a new|a deleted) webhook endpoint(, content full and a file exporter)?$`, func(which, full string) error {
		body := map[string]string{"a new": newWebhookEndpoint, "a deleted": deletedWebhookEndpoint}[which]
		extra := ""
		if full != "" {
			extra = "    content: full\n"
			w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
		}
		return w.useRepositorySpec("api.stripe.com", stripeSpec, http.StatusOK, "application/json", body, extra)
	})
	sc.Step(`^the Slack spec from specs, with Slack answering (\d+) "([^"]*)"(, content full, a file exporter and a console exporter)?$`, func(status int, text, full string) error {
		extra := ""
		if full != "" {
			extra = "    content: full\n"
			w.files = append(w.files, fileExporter{path: filepath.Join(w.dir, "calls")})
			w.consoleExporter = "  - console: {}\n"
		}
		return w.useRepositorySpec("hooks.slack.com", slackSpec, status, "text/html", text, extra)
	})
	sc.Step(`^the app posts to Stripe's (/\S+) the form "([^"]*)"$`, func(path, form string) error {
		return w.post("https://api.stripe.com"+path, "application/x-www-form-urlencoded", form, stripeLogin)
	})
	sc.Step(`^the app deletes Stripe's (/\S+)$`, func(path string) error {
		return w.call("DELETE", "https://api.stripe.com"+path, stripeLogin)
	})
	sc.Step(`^the app posts the message (.+) to its Slack webhook$`, func(message string) error {
		return w.post("https://hooks.slack.com/services/"+slackTeam+"/"+slackBot+"/"+slackToken, "application/json", message)
	})
	sc.Step(`^the one request result is body (\S+) enum, with Stripe's (\d+) event names$`, func(place string, count int) error {
		got, err := w.results("request")
		if err != nil {
			return err
		}
		if len(got) != 1 {
			return failf("the request results are %q, want one", got)
		}
		prefix := "body " + place + " enum "
		names, ok := strings.CutPrefix(got[0], prefix)
		if !ok || len(strings.Split(names, ", ")) != count {
			return failf("the request result is %q, want %q and %d names", got[0], prefix, count)
		}
		return nil
	})
	sc.Step(`^no record, span, log, console line or file line holds any part of the webhook address$`, func() error {
		w.settle()
		var all strings.Builder
		for _, r := range w.receivers {
			for _, record := range r.got() {
				all.WriteString(sprint(record.attrs))
			}
			for _, s := range r.spans() {
				all.WriteString(sprint(s.attrs))
			}
		}
		all.WriteString(w.logs.String())
		all.WriteString(w.console.String())
		file, err := os.ReadFile(filepath.Join(w.files[0].path, "calls.jsonl"))
		if err != nil {
			return err
		}
		all.Write(file)
		if !strings.Contains(all.String(), "services/REDACTED") {
			return failf("nothing exported shows the hidden address, so there is nothing to look at")
		}
		for _, part := range []string{slackTeam, slackBot, slackToken} {
			if strings.Contains(all.String(), part) {
				return failf("%s appears in what the proxy exported", part)
			}
		}
		return nil
	})
}
