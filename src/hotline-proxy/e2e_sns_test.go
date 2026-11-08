//go:build e2e

package proxy_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"
)

const orderPaidTopic = "arn:aws:sns:eu-west-1:192325781350:order-paid"

// fakeSNS is the fake AWS's SNS on localhost over TLS. Like the topic's
// subscription, it puts each message on the invoices queue of sqs.
type fakeSNS struct {
	server *httptest.Server
	sqs    *fakeSQS
	mu     sync.Mutex
	calls  []*http.Request
}

func newFakeSNS(cert tls.Certificate) *fakeSNS {
	f := &fakeSNS{sqs: &fakeSQS{}}
	apis := newFakeShopAWS(f.sqs)
	f.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.Clone(context.Background()))
		f.mu.Unlock()
		apis.serveQuery(w, r)
	}))
	f.server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	f.server.StartTLS()
	return f
}

func snsScenario(sc *godog.ScenarioContext, p *program, app *exampleApp) {
	var sns *fakeSNS
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		if sns != nil {
			sns.server.Close()
			sns = nil
		}
		return ctx, err
	})
	sc.Step(`^a fake SNS whose order-paid topic feeds the invoices queue$`, func() error {
		sns = newFakeSNS(p.issuer.leaf("localhost", time.Now().Add(24*time.Hour), false))
		return nil
	})
	sc.Step(`^the hotline-proxy program runs with the shop's hotline-proxy.yaml$`, func() error {
		p.pickPorts()
		config, err := exampleConfig(p.proxyAddr, p.receiver.endpoint)
		if err != nil {
			return err
		}
		p.writeConfig(config)
		return p.launch()
	})
	example := func(args ...string) error {
		binary, err := app.build()
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(context.Background(), binary, args...) //nolint:gosec // the example app
		cmd.Dir = p.dir
		cmd.Env = []string{
			"AWS_REGION=eu-west-1",
			"AWS_ACCESS_KEY_ID=AKIDEXAMPLE",
			"AWS_SECRET_ACCESS_KEY=secret",
			"AWS_EC2_METADATA_DISABLED=true",
			"ORDER_PAID_TOPIC=" + orderPaidTopic,
			"HOTLINE_PROXY=http://" + p.proxyAddr,
			"SNS_ENDPOINT=https://localhost:" + port(sns.server),
			"HOME=" + p.dir,
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			return failf("the example app %v failed: %v\n%s\nhotline-proxy said:\n%s", args, err, output, p.stderr.String())
		}
		return nil
	}
	sc.Step(`^checkout sends the message (.+)$`, func(body string) error {
		return example("checkout", "send", "--body", body)
	})
	sc.Step(`^the invoices queue has (\d+) messages? waiting$`, func(n int) error {
		if got := sns.sqs.waiting(); got != n {
			return failf("the invoices queue has %d messages, want %d", got, n)
		}
		return nil
	})
	sc.Step(`^the fake SNS got every call signed by the SDK, over https, without Proxy-Authorization$`, func() error {
		sns.mu.Lock()
		defer sns.mu.Unlock()
		if len(sns.calls) == 0 {
			return failf("the fake SNS got no calls")
		}
		for _, r := range sns.calls {
			if r.TLS == nil || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") ||
				r.Header.Get("Proxy-Authorization") != "" || r.Host != "localhost:"+port(sns.server) {
				return failf("the fake SNS got a call with Host %s, TLS %v and headers %v", r.Host, r.TLS != nil, r.Header)
			}
		}
		return nil
	})
	sc.Step(`^the receiver got one Publish record from the integration checkout, naming the topic, with no results$`, func() error {
		records, err := p.records()
		if err != nil {
			return err
		}
		if len(records) != 1 || records[0]["url.template"] != "/#Action=Publish" || records[0]["hotline.integration.id"] != "checkout" ||
			records[0]["messaging.destination.name"] != orderPaidTopic {
			return failf("want one Publish record from checkout naming the topic, got %v", records)
		}
		for _, key := range []string{"hotline.schema.request.errors", "hotline.schema.response.errors", "hotline.schema.skipped"} {
			if v, found := records[0][key]; found {
				return failf("the record has %s %v", key, v)
			}
		}
		return nil
	})
	sc.Step(`^the receiver got a Publish record with the request results (.+)$`, func(want string) error {
		records, err := p.records()
		if err != nil {
			return err
		}
		if len(records) != 1 || records[0]["url.template"] != "/#Action=Publish" {
			return failf("want one Publish record, got %v", records)
		}
		var got []string
		list, _ := records[0]["hotline.schema.request.errors"].([]any)
		for _, item := range list {
			r, _ := item.(map[string]any)
			parts := []string{fmt.Sprint(r["part"]), fmt.Sprint(r["place"]), fmt.Sprint(r["rule"])}
			if e, found := r["expected"]; found {
				parts = append(parts, fmt.Sprint(e))
			}
			got = append(got, strings.Join(parts, " "))
		}
		if strings.Join(got, "; ") != want {
			return failf("the request results are %q, want %q", strings.Join(got, "; "), want)
		}
		return nil
	})
}

// records are every request record the receiver got, once the program
// stopped.
func (p *program) records() ([]map[string]any, error) {
	if err := p.stop(); err != nil {
		return nil, err
	}
	received, _ := p.receiver.got()
	var out []map[string]any
	for _, r := range received {
		if r.eventName == "hotline.request" {
			out = append(out, r.attrs)
		}
	}
	return out, nil
}

// exampleConfig is testdata/aws/hotline-proxy.yaml with only the
// addresses changed: the local ports, the order-paid topic on the fake SNS
// on localhost (an internal address), and the specs, overlays and key read
// from where they are in the repo. The other AWS hosts stay as they are, so
// only the order-paid topic can be reached.
func exampleConfig(proxyAddr, otlpEndpoint string) (string, error) {
	config, err := shopConfig(
		[2]string{`":8080"`, fmt.Sprintf("%q", proxyAddr)},
		[2]string{"http://localhost:4318", otlpEndpoint},
	)
	if err != nil {
		return "", err
	}
	topicHost := regexp.MustCompile(`(?m)^(  aws-sns-order-paid:.*\n    hosts: )\[sns\.eu-west-1\.amazonaws\.com\]`)
	if !topicHost.MatchString(config) {
		return "", failf("the example config no longer names the order-paid topic's host")
	}
	return topicHost.ReplaceAllString(config, "${1}[localhost]\n    allowInternalIPs: true"), nil
}
