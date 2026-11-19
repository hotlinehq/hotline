package proxy_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"go.yaml.in/yaml/v3"
)

// shopSteps drive the proxy with the apis of the shop example's config,
// testdata/aws/hotline-proxy.yaml: each its stable spec and its overlay.
func shopSteps(sc *godog.ScenarioContext, w *world) {
	var host string
	sc.Step(`^the shop api (\S+)$`, func(name string) error {
		var err error
		host, err = w.exampleAPI(name)
		return err
	})
	sc.Step(`^AWS answers (\d+) with the json body (.*)$`, func(status int, body string) error {
		w.vendor(host).setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", r.Header.Get("Content-Type"))
			rw.WriteHeader(status)
			_, _ = rw.Write([]byte(body))
		})
		return nil
	})
	sc.Step(`^the app calls (\S+) with the json body (.*)$`, func(target, body string) error {
		return w.awsCall(host, target, body)
	})
	sc.Step(`^the app puts a Kinesis record holding (.*)$`, func(data string) error {
		body, err := json.Marshal(map[string]string{
			"StreamName": "shop-events", "PartitionKey": "A-1001",
			"Data": base64.StdEncoding.EncodeToString([]byte(data)),
		})
		if err != nil {
			return err
		}
		return w.awsCall(host, "Kinesis_20131202.PutRecord", string(body))
	})
	shopNewerSteps(sc, w, &host)
}

// exampleAPI adds the named api of the shop example's config, with its spec and
// overlays, used by the app's integration, and gives its host.
func (w *world) exampleAPI(name string) (string, error) {
	dir := filepath.Join("testdata", "aws")
	data, err := os.ReadFile(filepath.Join(dir, "hotline-proxy.yaml")) //nolint:gosec // the test's own file
	if err != nil {
		return "", err
	}
	var config struct {
		APIs map[string]struct {
			Hosts    []string `yaml:"hosts"`
			Spec     string   `yaml:"spec"`
			Overlays []string `yaml:"overlays"`
		} `yaml:"apis"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", err
	}
	api, ok := config.APIs[name]
	if !ok {
		return "", failf("the shop config has no api %s", name)
	}
	copyFile := func(from string) (string, error) {
		content, err := os.ReadFile(filepath.Join(dir, from)) //nolint:gosec // files the test's config names
		to := "specs/" + strings.NewReplacer("/", "-", "..-", "").Replace(from)
		w.writeFile(to, string(content))
		return "./" + to, err
	}
	spec, err := copyFile(api.Spec)
	if err != nil {
		return "", err
	}
	var overlays []string
	for _, o := range api.Overlays {
		path, err := copyFile(o)
		if err != nil {
			return "", err
		}
		overlays = append(overlays, path)
	}
	line := w.api(name, "    hosts: ["+api.Hosts[0]+"]\n    spec: "+spec+"\n    overlays: ["+strings.Join(overlays, ", ")+"]\n")
	w.addIntegration(integration(w.appID, line))
	return api.Hosts[0], nil
}

// awsCall sends one call on AWS's JSON protocol, as the SDK signs it, to
// the host, naming the action in X-Amz-Target.
func (w *world) awsCall(host, target, body string) error {
	// DynamoDB, SQS and Step Functions speak AWS JSON 1.0, the others 1.1.
	version := "1.1"
	for _, prefix := range []string{"DynamoDB_", "AmazonSQS.", "AWSStepFunctions."} {
		if strings.HasPrefix(target, prefix) {
			version = "1.0"
		}
	}
	lines := []string{
		"Content-Type: application/x-amz-json-" + version,
		"Authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261101/eu-west-1/x/aws4_request, SignedHeaders=host;x-amz-date;x-amz-target, Signature=0",
		"X-Amz-Date: 20261101T100000Z",
		"X-Amz-Target: " + target,
	}
	if w.appID != "" {
		lines = append([]string{basic(w.appID + ":x")}, lines...)
	}
	return w.send(request{
		method:  http.MethodPost,
		target:  "http://" + host + "/",
		headers: lines,
		body:    strings.NewReader(body),
		length:  len(body),
	})
}

// shopNewerSteps drive the shop's apis on AWS's query protocol (SNS and
// STS) and its REST and JSON protocol (Lambda and SES).
func shopNewerSteps(sc *godog.ScenarioContext, w *world, api *string) {
	host := func() (string, error) {
		if *api == "" {
			return "", failf("no shop api was added")
		}
		return *api, nil
	}
	sc.Step(`^AWS answers (\d+)(?: with "([^"]*)")? and the (xml|json) body (.*)$`, func(status int, headers, kind, body string) error {
		h, err := host()
		if err != nil {
			return err
		}
		contentType := map[string]string{"xml": "text/xml", "json": "application/json"}[kind]
		w.vendor(h).setAnswer(func(rw http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			rw.Header().Set("Content-Type", contentType)
			for _, line := range strings.Split(headers, ";") {
				if name, value, ok := strings.Cut(line, ":"); ok {
					rw.Header().Set(strings.TrimSpace(name), strings.TrimSpace(value))
				}
			}
			rw.WriteHeader(status)
			_, _ = rw.Write([]byte(body))
		})
		return nil
	})
	sc.Step(`^the app sends AWS the form body (.*)$`, func(body string) error {
		h, err := host()
		if err != nil {
			return err
		}
		return w.restCall(h, http.MethodPost, "/", "application/x-www-form-urlencoded", body)
	})
	sc.Step(`^the app publishes (.+) to the topic (\S+)$`, func(message, topic string) error {
		h, err := host()
		if err != nil {
			return err
		}
		body := url.Values{"Action": {"Publish"}, "Version": {"2010-03-31"}, "TopicArn": {topic}, "Message": {message}}.Encode()
		return w.restCall(h, http.MethodPost, "/", "application/x-www-form-urlencoded", body)
	})
	sc.Step(`^the app takes on the role (\S+)$`, func(role string) error {
		h, err := host()
		if err != nil {
			return err
		}
		body := url.Values{"Action": {"AssumeRole"}, "Version": {"2011-06-15"}, "RoleArn": {role}, "RoleSessionName": {"invoices"}}.Encode()
		return w.restCall(h, http.MethodPost, "/", "application/x-www-form-urlencoded", body)
	})
	sc.Step(`^the app sends AWS POST (\S+) with the (\S+) body (.*)$`, func(path, contentType, body string) error {
		h, err := host()
		if err != nil {
			return err
		}
		return w.restCall(h, http.MethodPost, path, contentType, body)
	})
	sc.Step(`^the record's url.full ends with (\S+)$`, func(end string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		if full := fmt.Sprint(record["url.full"]); !strings.HasSuffix(full, end) {
			return failf("the record's url.full is %s", full)
		}
		return nil
	})
	sc.Step(`^the record keeps the response header (\S+) as (\S+)$`, func(name, value string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		if got := fmt.Sprint(record["http.response.header."+strings.ToLower(name)]); !strings.Contains(got, value) {
			return failf("the record has the response header %s %s", name, got)
		}
		return nil
	})
	sc.Step(`^no record and no span holds (.+)$`, func(list string) error {
		var all []string
		for _, r := range w.records() {
			all = append(all, fmt.Sprint(r.attrs))
		}
		for _, s := range w.receivers[0].spans() {
			all = append(all, fmt.Sprint(s.attrs))
		}
		if len(all) == 0 {
			return failf("the receiver got nothing to look at")
		}
		text := strings.Join(all, "\n")
		for _, value := range strings.Split(list, " or ") {
			if value = strings.Trim(value, `"`); strings.Contains(text, value) {
				return failf("a record or span holds %s", value)
			}
		}
		return nil
	})
	sc.Step(`^the first invoices record is AssumeRole, and checkout made no SQS call$`, func() error {
		var first string
		for _, r := range w.records() {
			id, template := fmt.Sprint(r.attrs["hotline.integration.id"]), fmt.Sprint(r.attrs["url.template"])
			if id == "invoices" && first == "" {
				first = template
			}
			if id == "checkout" && r.attrs["server.address"] == sqsHost {
				return failf("checkout called SQS: %s", template)
			}
		}
		if first != "/#Action=AssumeRole" {
			return failf("the first invoices record is %s", first)
		}
		return nil
	})
	sc.Step(`^the (checkout|invoices) record for (\S+) has the (request|response) results (.+)$`, func(id, template, side, want string) error {
		for _, r := range w.records() {
			if r.attrs["hotline.integration.id"] != id || r.attrs["url.template"] != template {
				continue
			}
			got, err := resultsOf(r.attrs, side)
			if err != nil {
				return err
			}
			if strings.Join(got, "; ") != want {
				return failf("the %s results are %q, want %q", side, strings.Join(got, "; "), want)
			}
			return nil
		}
		return failf("no %s record for %s", id, template)
	})
}

// restCall sends one signed call with a body to the host.
func (w *world) restCall(host, method, path, contentType, body string) error {
	lines := []string{
		"Content-Type: " + contentType,
		"Authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261101/eu-west-1/x/aws4_request, SignedHeaders=host;x-amz-date, Signature=0",
		"X-Amz-Date: 20261101T100000Z",
	}
	if w.appID != "" {
		lines = append([]string{basic(w.appID + ":x")}, lines...)
	}
	return w.send(request{method: method, target: "http://" + host + path, headers: lines, body: strings.NewReader(body), length: len(body)})
}
