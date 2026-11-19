package fakeaws

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// The fake's keys for a role taken on through STS. Calls signed with them
// show the app used the role.
//
//nolint:gosec // made-up keys, for the tests
const (
	RoleAccessKey = "ASIAFAKEROLEKEY00001"
	roleSecret    = "role-secret"
	roleToken     = "role-session-token"
)

// Function is a fake Lambda function: it gets the call's payload and gives
// its output, and says whether the function failed.
type Function func(payload []byte) (out []byte, failed bool)

// serveNewer answers SNS and STS, on AWS's query protocol (a form body
// naming the action, an XML response), and Lambda and SES, on its REST and
// JSON protocol. It says false for any other host.
func (f *AWS) serveNewer(w http.ResponseWriter, r *http.Request) bool {
	service, _, _ := strings.Cut(r.Host, ".")
	switch service {
	case "sns", "sts":
		f.serveQuery(w, r, strings.ToUpper(service))
	case "lambda", "email":
		f.serveREST(w, r, service)
	default:
		return false
	}
	return true
}

func (f *AWS) serveQuery(w http.ResponseWriter, r *http.Request, service string) {
	data, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(data))
	action := form.Get("Action")
	body := map[string]any{}
	for name := range form {
		body[name] = form.Get(name)
	}
	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Host: r.Host, Target: service + "." + action,
		ProxyAuth: r.Header.Get("Proxy-Authorization"), Authorization: r.Header.Get("Authorization"),
		Body: body,
	})
	publish := f.OnPublish
	var result string
	switch service + "." + action {
	case "SNS.Publish":
		f.published = append(f.published, Published{TopicArn: form.Get("TopicArn"), Message: form.Get("Message")})
		result = fmt.Sprintf("<MessageId>p-%d</MessageId>", len(f.published))
	case "STS.AssumeRole":
		result = "<Credentials><AccessKeyId>" + RoleAccessKey + "</AccessKeyId><SecretAccessKey>" + roleSecret +
			"</SecretAccessKey><SessionToken>" + roleToken + "</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>" +
			"<AssumedRoleUser><Arn>" + html.EscapeString(form.Get("RoleArn")) + "/" + html.EscapeString(form.Get("RoleSessionName")) +
			"</Arn><AssumedRoleId>AROAFAKE:" + html.EscapeString(form.Get("RoleSessionName")) + "</AssumedRoleId></AssumedRoleUser>"
	default:
		f.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, "<ErrorResponse><Error><Type>Sender</Type><Code>InvalidAction</Code><Message>the fake doesn't know %s</Message></Error><RequestId>r-1</RequestId></ErrorResponse>", html.EscapeString(action))
		return
	}
	f.mu.Unlock()
	if action == "Publish" && publish != nil {
		publish(form.Get("TopicArn"), form.Get("Message"))
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = fmt.Fprintf(w, "<%sResponse><%sResult>%s</%sResult><ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata></%sResponse>",
		action, action, result, action, action)
}

func (f *AWS) serveREST(w http.ResponseWriter, r *http.Request, service string) {
	data, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	call := Call{
		Host: r.Host, ProxyAuth: r.Header.Get("Proxy-Authorization"), Authorization: r.Header.Get("Authorization"),
		Body: body, Path: r.URL.Path, Payload: data,
	}
	function, isFunction := strings.CutPrefix(r.URL.Path, "/2015-03-31/functions/")
	function, isInvoke := strings.CutSuffix(function, "/invocations")
	isInvoke = isInvoke && isFunction
	switch {
	case service == "lambda" && isInvoke && r.Method == http.MethodPost:
		call.Target = "Lambda.Invoke"
		f.mu.Lock()
		f.calls = append(f.calls, call)
		run := f.Functions[function]
		f.mu.Unlock()
		if run == nil {
			w.Header().Set("X-Amzn-ErrorType", "ResourceNotFoundException")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"Type":"User","message":"Function not found"}`))
			return
		}
		out, failed := run(data)
		if failed {
			w.Header().Set("X-Amz-Function-Error", "Unhandled")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	case service == "email" && r.URL.Path == "/v2/email/outbound-emails" && r.Method == http.MethodPost:
		call.Target = "SES.SendEmail"
		f.mu.Lock()
		f.calls = append(f.calls, call)
		f.emails = append(f.emails, body)
		id := fmt.Sprintf("m-%d", len(f.emails))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"MessageId":%q}`, id)
	default:
		w.Header().Set("X-Amzn-ErrorType", "UnknownOperationException")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"the fake doesn't know this call"}`))
	}
}

// Published is one message published to an SNS topic.
type Published struct {
	TopicArn string
	Message  string
}

// Published lists the messages published to SNS topics.
func (f *AWS) Published() []Published {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Published(nil), f.published...)
}

// Emails lists the SES SendEmail calls' bodies.
func (f *AWS) Emails() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.emails...)
}

// PaymentQR is the shop's payment-qr function: it turns an order into the
// payment text a bank app reads from a QR code.
func PaymentQR(payload []byte) ([]byte, bool) {
	var in struct {
		OrderID  string `json:"orderId"`
		Amount   string `json:"amount"`
		Currency string `json:"currency"`
	}
	if err := json.Unmarshal(payload, &in); err != nil || in.OrderID == "" || in.Amount == "" || in.Currency == "" {
		return []byte(`{"errorMessage":"orderId, amount and currency are needed","errorType":"ValueError"}`), true
	}
	out, err := json.Marshal(map[string]string{"qrText": "SPD*1.0*AM:" + in.Amount + "*CC:" + in.Currency + "*MSG:" + in.OrderID})
	if err != nil {
		panic(err)
	}
	return out, false
}
