// Package fakeaws is a small stand-in for the AWS apis the shop uses beside
// SQS and S3: DynamoDB, Kinesis, EventBridge, SSM, Secrets Manager, KMS,
// CloudWatch Logs and Step Functions on AWS's JSON protocol; SNS and STS on
// its query protocol; Lambda and SES on its REST and JSON protocol. It answers the few actions the shop
// calls, the way AWS does, and keeps what it was sent so a test can look.
package fakeaws

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Call is what the fake saw of one request.
type Call struct {
	Host          string
	Target        string
	ProxyAuth     string
	Authorization string
	Body          map[string]any
	// Path and Payload are the path and the raw body, for the REST apis.
	Path    string
	Payload []byte
}

// AWS answers calls named by X-Amz-Target, and calls to SNS, STS, Lambda
// and SES by their host. A call for SQS, or with no target, goes to Other,
// such as a fake SQS.
type AWS struct {
	Other http.Handler
	// Parameters and Secrets are what SSM and Secrets Manager hold, by name.
	Parameters map[string]string
	Secrets    map[string]string
	// Functions are the Lambda functions, by name.
	Functions map[string]Function
	// OnPublish, when set, hears of each message published to SNS, as a
	// topic's subscribers do.
	OnPublish func(topicArn, message string)

	mu        sync.Mutex
	calls     []Call
	items     map[string]map[string]map[string]any // table, then key, then the item
	records   []string                             // Kinesis data, decoded
	events    []map[string]any                     // EventBridge entries
	logs      []string                             // CloudWatch Logs messages
	runs      []map[string]any                     // Step Functions executions
	published []Published                          // SNS messages
	emails    []map[string]any                     // SES SendEmail bodies
}

// New makes a fake AWS in front of other.
func New(other http.Handler) *AWS {
	return &AWS{
		Other:      other,
		Parameters: map[string]string{},
		Secrets:    map[string]string{},
		Functions:  map[string]Function{"payment-qr": PaymentQR},
		items:      map[string]map[string]map[string]any{},
	}
}

func (f *AWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.serveNewer(w, r) {
		return
	}
	target := r.Header.Get("X-Amz-Target")
	if target == "" || strings.HasPrefix(target, "AmazonSQS.") {
		f.Other.ServeHTTP(w, r)
		return
	}
	data, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(data, &in)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{
		Host:          r.Host,
		Target:        target,
		ProxyAuth:     r.Header.Get("Proxy-Authorization"),
		Authorization: r.Header.Get("Authorization"),
		Body:          in,
	})
	status, out := f.answer(target, in)
	body, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
	// DynamoDB's own checksum of the response, which its SDK checks.
	w.Header().Set("X-Amz-Crc32", strconv.FormatUint(uint64(crc32.ChecksumIEEE(body)), 10))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func awsError(kind, message string) (int, any) {
	return http.StatusBadRequest, map[string]any{"__type": kind, "message": message}
}

//nolint:cyclop // one case per action, as AWS has
func (f *AWS) answer(target string, in map[string]any) (int, any) {
	str := func(name string) string { s, _ := in[name].(string); return s }
	switch target {
	case "DynamoDB_20120810.PutItem":
		table, item := str("TableName"), in["Item"].(map[string]any)
		key := keyOf(item)
		if f.items[table] == nil {
			f.items[table] = map[string]map[string]any{}
		}
		if strings.HasPrefix(str("ConditionExpression"), "attribute_not_exists") && f.items[table][key] != nil {
			return awsError("com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException", "The conditional request failed")
		}
		f.items[table][key] = item
		return http.StatusOK, map[string]any{}
	case "DynamoDB_20120810.GetItem":
		item := f.items[str("TableName")][keyOf(in["Key"].(map[string]any))]
		if item == nil {
			return http.StatusOK, map[string]any{}
		}
		return http.StatusOK, map[string]any{"Item": item}
	case "Kinesis_20131202.PutRecord":
		raw, err := base64.StdEncoding.DecodeString(str("Data"))
		if err != nil {
			return awsError("SerializationException", "Data is not base64")
		}
		f.records = append(f.records, string(raw))
		return http.StatusOK, map[string]any{"ShardId": "shardId-000000000000", "SequenceNumber": strconv.Itoa(len(f.records))}
	case "AWSEvents.PutEvents":
		entries, _ := in["Entries"].([]any)
		var ids []any
		for _, e := range entries {
			f.events = append(f.events, e.(map[string]any))
			ids = append(ids, map[string]any{"EventId": fmt.Sprintf("e-%d", len(f.events))})
		}
		return http.StatusOK, map[string]any{"FailedEntryCount": 0, "Entries": ids}
	case "AmazonSSM.GetParameter":
		value, ok := f.Parameters[str("Name")]
		if !ok {
			return awsError("ParameterNotFound", "no such parameter")
		}
		return http.StatusOK, map[string]any{"Parameter": map[string]any{"Name": str("Name"), "Type": "String", "Value": value}}
	case "secretsmanager.GetSecretValue":
		value, ok := f.Secrets[str("SecretId")]
		if !ok {
			return awsError("ResourceNotFoundException", "no such secret")
		}
		return http.StatusOK, map[string]any{"Name": str("SecretId"), "SecretString": value}
	case "TrentService.Sign":
		return http.StatusOK, map[string]any{
			"KeyId":            "arn:aws:kms:eu-west-1:192325781350:key/fake",
			"Signature":        base64.StdEncoding.EncodeToString([]byte("signature of " + str("Message"))),
			"SigningAlgorithm": str("SigningAlgorithm"),
		}
	case "Logs_20140328.CreateLogStream":
		return http.StatusOK, map[string]any{}
	case "Logs_20140328.PutLogEvents":
		events, _ := in["logEvents"].([]any)
		for _, e := range events {
			f.logs = append(f.logs, e.(map[string]any)["message"].(string))
		}
		return http.StatusOK, map[string]any{}
	case "AWSStepFunctions.StartExecution":
		f.runs = append(f.runs, in)
		return http.StatusOK, map[string]any{
			"executionArn": fmt.Sprintf("arn:aws:states:eu-west-1:192325781350:execution:credit-note:run-%d", len(f.runs)),
			"startDate":    1793527200,
		}
	}
	return awsError("UnknownOperationException", "the fake doesn't know "+target)
}

// keyOf is a row's key: its orderId, as the shop's tables use.
func keyOf(item map[string]any) string {
	id, _ := item["orderId"].(map[string]any)
	s, _ := id["S"].(string)
	return s
}

// Calls lists every call so far.
func (f *AWS) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Targets lists the X-Amz-Target of every call so far.
func (f *AWS) Targets() []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.Target)
	}
	return out
}

// Item is a table's row for the order, nil when there is none.
func (f *AWS) Item(table, orderID string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[table][orderID]
}

// PutItem stores a row, as if an app had saved it.
func (f *AWS) PutItem(table string, item map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.items[table] == nil {
		f.items[table] = map[string]map[string]any{}
	}
	f.items[table][keyOf(item)] = item
}

// Records lists the Kinesis records' data, decoded.
func (f *AWS) Records() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.records...)
}

// Events lists the EventBridge entries.
func (f *AWS) Events() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.events...)
}

// Logs lists the CloudWatch Logs messages.
func (f *AWS) Logs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.logs...)
}

// Runs lists the Step Functions executions started.
func (f *AWS) Runs() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.runs...)
}
