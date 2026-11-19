package proxy_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The shop's resources, as testdata/aws/hotline-proxy.yaml names them.
const (
	shopAccount   = "192325781350"
	mailQueue     = "https://sqs.eu-west-1.amazonaws.com/" + shopAccount + "/mail"
	paymentSecret = "shop/payment-key"
	templateParam = "/shop/invoices/default-template"
)

// shopHosts are the hosts of the shop's JSON apis, beside SQS and S3.
func shopHosts() []string {
	return []string{
		"dynamodb.eu-west-1.amazonaws.com", "kinesis.eu-west-1.amazonaws.com",
		"events.eu-west-1.amazonaws.com", "ssm.eu-west-1.amazonaws.com",
		"secretsmanager.eu-west-1.amazonaws.com", "kms.eu-west-1.amazonaws.com",
		"logs.eu-west-1.amazonaws.com", "states.eu-west-1.amazonaws.com",
		"sns.eu-west-1.amazonaws.com", "sts.eu-west-1.amazonaws.com",
		"lambda.eu-west-1.amazonaws.com", "email.eu-west-1.amazonaws.com",
	}
}

// The keys the fake STS gives for the invoice-worker role. No record or
// span may hold them.
//
//nolint:gosec // made-up keys, for the test
const (
	roleAccessKey = "ASIAFAKEROLEKEY00001"
	roleSecret    = "fake-role-secret-key"
	roleToken     = "fake-role-session-token"
)

// fakeShopAWS answers the few actions the shop calls on AWS's JSON apis,
// the way AWS does. Like the shop's EventBridge rule, it puts each event
// sent to the bus on the mail queue of sqs.
type fakeShopAWS struct {
	sqs *fakeSQS

	mu      sync.Mutex
	records int
	rows    map[string]map[string]any // "table/orderId", then the row
	runs    int
	emails  []string // the whole emails SES was asked to send
}

func newFakeShopAWS(sqs *fakeSQS) *fakeShopAWS {
	return &fakeShopAWS{sqs: sqs, rows: map[string]map[string]any{}}
}

func (f *fakeShopAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch service, _, _ := strings.Cut(r.Host, "."); service {
	case "sns", "sts":
		f.serveQuery(w, r)
		return
	case "lambda", "email":
		f.serveREST(w, r, service)
		return
	}
	data, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(data, &in)
	target := r.Header.Get("X-Amz-Target")
	f.mu.Lock()
	status, out := f.answer(target, in)
	f.mu.Unlock()
	w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(out); err != nil {
		panic(err)
	}
}

// answer is AWS's response to one call; the caller holds the lock.
//
//nolint:cyclop // one case per action, as AWS has
func (f *fakeShopAWS) answer(target string, in map[string]any) (int, any) {
	str := func(name string) string { s, _ := in[name].(string); return s }
	switch target {
	case "DynamoDB_20120810.PutItem":
		item, _ := in["Item"].(map[string]any)
		key := str("TableName") + "/" + orderOf(item)
		if strings.HasPrefix(str("ConditionExpression"), "attribute_not_exists") && f.rows[key] != nil {
			return http.StatusBadRequest, map[string]any{"__type": "com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException", "message": "The conditional request failed"}
		}
		f.rows[key] = item
		return http.StatusOK, map[string]any{}
	case "DynamoDB_20120810.GetItem":
		keyItem, _ := in["Key"].(map[string]any)
		if row := f.rows[str("TableName")+"/"+orderOf(keyItem)]; row != nil {
			return http.StatusOK, map[string]any{"Item": row}
		}
		return http.StatusOK, map[string]any{}
	case "Kinesis_20131202.PutRecord":
		f.records++
		return http.StatusOK, map[string]any{"ShardId": "shardId-000000000000", "SequenceNumber": strconv.Itoa(f.records)}
	case "AWSEvents.PutEvents":
		entries, _ := in["Entries"].([]any)
		ids := make([]any, 0, len(entries))
		for i, e := range entries {
			entry, _ := e.(map[string]any)
			f.sqs.put(mailQueue, ruleEvent(entry))
			ids = append(ids, map[string]any{"EventId": fmt.Sprintf("e-%d", i+1)})
		}
		return http.StatusOK, map[string]any{"FailedEntryCount": 0, "Entries": ids}
	case "AmazonSSM.GetParameter":
		return http.StatusOK, map[string]any{"Parameter": map[string]any{"Name": str("Name"), "Type": "String", "Value": "invoice-standard-v2"}}
	case "secretsmanager.GetSecretValue":
		return http.StatusOK, map[string]any{"Name": str("SecretId"), "SecretString": "pay-key-1"}
	case "TrentService.Sign":
		return http.StatusOK, map[string]any{
			"KeyId":            "arn:aws:kms:eu-west-1:" + shopAccount + ":key/fake",
			"Signature":        base64.StdEncoding.EncodeToString([]byte("signature")),
			"SigningAlgorithm": str("SigningAlgorithm"),
		}
	case "Logs_20140328.CreateLogStream", "Logs_20140328.PutLogEvents":
		return http.StatusOK, map[string]any{}
	case "AWSStepFunctions.StartExecution":
		f.runs++
		return http.StatusOK, map[string]any{
			"executionArn": fmt.Sprintf("arn:aws:states:eu-west-1:%s:execution:credit-note:run-%d", shopAccount, f.runs),
			"startDate":    time.Now().Unix(),
		}
	}
	return http.StatusBadRequest, map[string]any{"__type": "UnknownOperationException", "message": "the fake doesn't know " + target}
}

// serveQuery answers SNS and STS on AWS's query protocol: a form body
// naming the action, and an XML response. Like the topic's subscription with
// raw delivery, each message published goes, as is, on the invoices queue.
func (f *fakeShopAWS) serveQuery(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(data))
	action := form.Get("Action")
	var result string
	switch action {
	case "Publish":
		f.sqs.put(invoicesQueue, form.Get("Message"))
		result = "<MessageId>6a7e8feb-b491-4cf7-a9f1-bf3703467718</MessageId>"
	case "AssumeRole":
		result = "<Credentials><AccessKeyId>" + roleAccessKey + "</AccessKeyId><SecretAccessKey>" + roleSecret +
			"</SecretAccessKey><SessionToken>" + roleToken + "</SessionToken><Expiration>" +
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + "</Expiration></Credentials>" +
			"<AssumedRoleUser><Arn>arn:aws:sts::" + shopAccount + ":assumed-role/invoice-worker/invoices</Arn>" +
			"<AssumedRoleId>AROAFAKE:invoices</AssumedRoleId></AssumedRoleUser>"
	default:
		w.Header().Set("Content-Type", "text/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("<ErrorResponse><Error><Type>Sender</Type><Code>InvalidAction</Code></Error><RequestId>r-1</RequestId></ErrorResponse>"))
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	_, _ = fmt.Fprintf(w, "<%sResponse><%sResult>%s</%sResult><ResponseMetadata><RequestId>r-1</RequestId></ResponseMetadata></%sResponse>",
		action, action, result, action, action)
}

// serveREST answers Lambda's payment-qr function and SES's SendEmail, on
// AWS's REST and JSON protocol.
func (f *fakeShopAWS) serveREST(w http.ResponseWriter, r *http.Request, service string) {
	data, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	switch {
	case service == "lambda" && r.URL.Path == "/2015-03-31/functions/payment-qr/invocations":
		var in struct{ OrderID, Amount, Currency string }
		_ = json.Unmarshal(data, &in)
		_, _ = fmt.Fprintf(w, `{"qrText":"SPD*1.0*AM:%s*CC:%s*MSG:%s"}`, in.Amount, in.Currency, in.OrderID)
	case service == "email" && r.URL.Path == "/v2/email/outbound-emails":
		var in struct {
			Content struct{ Raw struct{ Data []byte } }
		}
		_ = json.Unmarshal(data, &in)
		f.mu.Lock()
		f.emails = append(f.emails, string(in.Content.Raw.Data))
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"MessageId":"m-1"}`))
	default:
		w.Header().Set("X-Amzn-ErrorType", "ResourceNotFoundException")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"the fake doesn't know this"}`))
	}
}

// sent lists the whole emails SES was asked to send.
func (f *fakeShopAWS) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.emails...)
}

// ruleEvent is EventBridge's event for an entry, as a rule puts it on a
// queue.
func ruleEvent(entry map[string]any) string {
	var detail any
	text, _ := entry["Detail"].(string)
	_ = json.Unmarshal([]byte(text), &detail)
	data, err := json.Marshal(map[string]any{
		"version": "0", "id": "6a7e8feb-b491-4cf7-a9f1-bf3703467718",
		"detail-type": entry["DetailType"], "source": entry["Source"],
		"account": shopAccount, "time": time.Now().UTC().Format(time.RFC3339),
		"region": "eu-west-1", "resources": []any{}, "detail": detail,
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

// orderOf is a row's key: its orderId, as the shop's tables use.
func orderOf(item map[string]any) string {
	id, _ := item["orderId"].(map[string]any)
	s, _ := id["S"].(string)
	return s
}
