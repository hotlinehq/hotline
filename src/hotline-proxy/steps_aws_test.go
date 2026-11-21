package proxy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

const (
	sqsHost       = "sqs.eu-west-1.amazonaws.com"
	invoicesQueue = "https://sqs.eu-west-1.amazonaws.com/192325781350/invoices"
)

func awsSteps(sc *godog.ScenarioContext, w *world) {
	signedSteps(sc, w)
	sc.Step(`^the SQS spec$`, func() error {
		return w.sqsAPI("talon-shop-a", "aws-sqs", "")
	})
	sc.Step(`^the SQS spec with the invoices overlay$`, func() error {
		overlay, err := os.ReadFile(filepath.Join("testdata", "aws", "invoices.overlay.yaml"))
		w.writeFile("specs/invoices.overlay.yaml", string(overlay))
		if err != nil {
			return err
		}
		return w.sqsAPI("talon-shop-a", "aws-sqs-invoices", "    overlays: [./specs/invoices.overlay.yaml]\n")
	})
	sc.Step(`^the integration (\S+) with the plain SQS spec$`, func(id string) error {
		return w.sqsAPI(id, "aws-sqs", "")
	})
	sc.Step(`^the SQS api with a spec whose route is (\S+) and (no x-hotline-action-header|x-hotline-action-header: \S+)$`, func(route, mark string) error {
		header := ""
		if strings.HasPrefix(mark, "x-hotline-action-header:") {
			header = mark + "\n"
		}
		spec := "openapi: 3.2.0\ninfo: { title: SQS, version: '1' }\n" + header +
			"paths:\n  " + route + ":\n    post:\n      responses:\n        '200': { description: done }\n"
		w.addIntegration(integration("talon-shop-a", w.specAPI(sqsHost, spec, "")))
		return nil
	})
	sc.Step(`^the SQS api with a spec whose SendMessage body is (\S+) with (no x-hotline-body|x-hotline-body: \S+)$`, func(mediaType, mark string) error {
		if mark == "no x-hotline-body" {
			mark = ""
		}
		spec := "openapi: 3.2.0\ninfo: { title: SQS, version: '1' }\nx-hotline-action-header: X-Amz-Target\n" +
			"paths:\n  /#X-Amz-Target=AmazonSQS.SendMessage:\n    post:\n" +
			"      requestBody:\n        content:\n          " + mediaType + ":\n            " + mark + "\n" +
			"            schema: { type: object, properties: { DelaySeconds: { type: integer } } }\n" +
			"      responses:\n        '200': { description: done }\n"
		w.addIntegration(integration("talon-shop-a", w.specAPI(sqsHost, spec, "")))
		return nil
	})
	sc.Step(`^SQS answers (\d+)(?: with "([^"]*)")? and the json body (.*)$`, func(status int, headers, body string) error {
		w.sqsAnswers(status, headers, body)
		return nil
	})
	sc.Step(`^SQS answers 200 with the json body (.*)$`, func(body string) error {
		w.sqsAnswers(http.StatusOK, "", body)
		return nil
	})
	sc.Step(`^SQS answers 200 with one received message (.*)$`, func(message string) error {
		response, err := json.Marshal(map[string]any{"Messages": []any{map[string]any{
			"MessageId": "m-1", "ReceiptHandle": "r-1", "MD5OfBody": "x", "Body": message,
		}}})
		w.sqsAnswers(http.StatusOK, "", string(response))
		return err
	})
	sc.Step(`^the app sends (\w+) with the json body (.*)$`, func(action, body string) error {
		return w.sqsCall(w.appID, []string{"X-Amz-Target: AmazonSQS." + action}, body)
	})
	sc.Step(`^the app sends POST / (without X-Amz-Target|with "([^"]*)") with the json body (.*)$`, func(_, header, body string) error {
		var headers []string
		if header != "" {
			headers = append(headers, header)
		}
		return w.sqsCall(w.appID, headers, body)
	})
	sc.Step(`^(?:the app|(\S+)) sends SendMessage to (the invoices queue|\S+) with the message (.*)$`, func(id, queue, message string) error {
		if id == "" {
			id = w.appID
		}
		if queue == "the invoices queue" {
			queue = invoicesQueue
		}
		body, err := json.Marshal(map[string]string{"QueueUrl": queue, "MessageBody": message})
		if err != nil {
			return err
		}
		return w.sqsCall(id, []string{"X-Amz-Target: AmazonSQS.SendMessage"}, string(body))
	})
	sc.Step(`^no result holds (\S+)$`, func(value string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		all := fmt.Sprint(record["hotline.schema.request.errors"], record["hotline.schema.response.errors"])
		if strings.Contains(all, value) {
			return failf("the results %s hold a value from the call", all)
		}
		return nil
	})
	sc.Step(`^the record keeps the response headers (\S+) and (\S+)$`, func(first, second string) error {
		record, err := w.record()
		if err != nil {
			return err
		}
		return hasAll(record, "http.response.header."+first, "http.response.header."+second)
	})
}

// sqsAPI adds an api for SQS with the stable spec from the specs folder,
// and the integration id that uses it.
func (w *world) sqsAPI(id, name, extra string) error {
	spec, err := os.ReadFile(filepath.Join("..", "..", "specs", "aws", "sqs", "2012-11-05.openapi.yaml"))
	w.writeFile("specs/aws-sqs.openapi.yaml", string(spec))
	line := w.api(name, "    hosts: ["+sqsHost+"]\n    spec: ./specs/aws-sqs.openapi.yaml\n"+extra)
	w.addIntegration(integration(id, line))
	return err
}

// sqsAnswers makes the fake SQS answer every call the way SQS does.
func (w *world) sqsAnswers(status int, headers, body string) {
	w.vendor(sqsHost).setAnswer(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		rw.Header().Set("Content-Type", "application/x-amz-json-1.0")
		for _, h := range splitHeaders(headers) {
			name, value, _ := strings.Cut(h, ": ")
			rw.Header().Set(name, value)
		}
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(body))
	})
}

// sqsCall sends an SQS call through the forward proxy, the way the AWS SDK
// does after the app's client turned https into http.
func (w *world) sqsCall(id string, headers []string, body string) error {
	lines := []string{
		"Content-Type: application/x-amz-json-1.0",
		"Authorization: AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261030/eu-west-1/sqs/aws4_request, SignedHeaders=host;x-amz-date;x-amz-target, Signature=0",
		"X-Amz-Date: 20261030T193000Z",
	}
	if id != "" {
		lines = append([]string{basic(id + ":x")}, lines...)
	}
	return w.send(request{
		method:  http.MethodPost,
		target:  "http://" + sqsHost + "/",
		headers: append(lines, headers...),
		body:    strings.NewReader(body),
		length:  len(body),
	})
}

// signedCall is the fixture: one SendMessage call the AWS SDK signed.
type signedCall struct {
	host   string
	header http.Header
	body   string
}

func readSignedCall() (signedCall, error) {
	data, err := os.ReadFile(filepath.Join("testdata", "aws", "sqs-sendmessage-signed.http"))
	if err != nil {
		return signedCall{}, err
	}
	// The file is written as on the wire: lines end in CRLF.
	head, body, _ := strings.Cut(string(data), "\r\n\r\n")
	c := signedCall{header: http.Header{}, body: body}
	for _, line := range strings.Split(head, "\r\n")[1:] {
		name, value, _ := strings.Cut(line, ": ")
		c.header.Add(name, value)
	}
	c.host = c.header.Get("Host")
	return c, nil
}

// signedHeaders are the header names in the fixture's own Authorization
// line, never typed into the test.
func (c signedCall) signedHeaders() []string {
	auth := c.header.Get("Authorization")
	_, after, _ := strings.Cut(auth, "SignedHeaders=")
	list, _, _ := strings.Cut(after, ",")
	return strings.Split(list, ";")
}

func signedSteps(sc *godog.ScenarioContext, w *world) {
	var call signedCall
	sc.Step(`^the recorded signed SQS call from the fixture, with Proxy-Authorization added after signing$`, func() error {
		var err error
		call, err = readSignedCall()
		call.header.Set("Proxy-Authorization", strings.TrimPrefix(basic(w.appID+":x"), "Proxy-Authorization: "))
		return err
	})
	sc.Step(`^the app sends it to the proxy over (the forward proxy|the Host header) with (HTTP/1.1|plain HTTP/2)$`, func(ingress, protocol string) error {
		target := "/"
		if ingress == "the forward proxy" {
			target = "http://" + call.host + "/"
		}
		if protocol == "plain HTTP/2" {
			return w.sendHTTP2Body(call)
		}
		var lines []string
		for name, values := range call.header {
			if name != "Content-Length" {
				for _, v := range values {
					lines = append(lines, name+": "+v)
				}
			}
		}
		return w.send(request{method: http.MethodPost, target: target, headers: lines, body: strings.NewReader(call.body), length: len(call.body)})
	})
	sc.Step(`^every signed part reaches the vendor exactly as in the fixture$`, func() error {
		got, err := w.lastVendorCall(sqsHost)
		if err != nil {
			return err
		}
		if got.method != http.MethodPost || got.uri != "/" || got.host != call.host || string(got.body) != call.body {
			return failf("the vendor got %s %s, Host %s and the body %q", got.method, got.uri, got.host, got.body)
		}
		for _, name := range call.signedHeaders() {
			switch name {
			case "host", "content-length":
				continue // checked above and below
			}
			if want, have := call.header.Values(name), got.header.Values(name); strings.Join(want, ",") != strings.Join(have, ",") {
				return failf("the signed header %s reached the vendor as %q, want %q", name, have, want)
			}
		}
		return nil
	})
	sc.Step(`^SQS never sees Proxy-Authorization$`, func() error {
		got, err := w.lastVendorCall(sqsHost)
		if err != nil {
			return err
		}
		if got.header.Get("Proxy-Authorization") != "" {
			return failf("SQS got Proxy-Authorization")
		}
		return nil
	})
	sc.Step(`^the vendor receives Content-Length with the body's length, not a body in chunks$`, func() error {
		got, err := w.lastVendorCall(sqsHost)
		if err != nil {
			return err
		}
		if got.contentLength != int64(len(call.body)) {
			return failf("the vendor got a Content-Length of %d, want %d", got.contentLength, len(call.body))
		}
		return nil
	})
}

func (w *world) lastVendorCall(host string) (vendorCall, error) {
	calls := w.vendor(host).received()
	if len(calls) == 0 {
		return vendorCall{}, failf("the vendor %s got no call; the app got %v %v %s", host, w.resp, w.respErr, w.respBody)
	}
	return calls[len(calls)-1], nil
}

// sendHTTP2Body sends the signed call over plain HTTP/2 from the first
// byte, as a Host header call.
func (w *world) sendHTTP2Body(c signedCall) error {
	if err := w.start(); err != nil {
		return err
	}
	w.vendor(c.host)
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+w.addr+"/", strings.NewReader(c.body))
	if err != nil {
		return err
	}
	req.Host = c.host
	for name, values := range c.header {
		switch name {
		case "Host", "Content-Length":
		default:
			req.Header[name] = values
		}
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	w.resp, w.respErr = resp, err
	if err != nil {
		return nil
	}
	w.respBody, w.respErr = io.ReadAll(resp.Body)
	return resp.Body.Close()
}
