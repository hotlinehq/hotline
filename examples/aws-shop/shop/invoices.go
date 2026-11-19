package shop

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	ebtypes "github.com/aws/aws-sdk-go-v2/service/eventbridge/types"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"hotline-example/aws-shop/invoice"
)

// invoiceReady is the Detail of the InvoiceReady event.
type invoiceReady struct {
	OrderID   string `json:"orderId"`
	EventID   string `json:"eventId"`
	File      string `json:"file"`
	Language  string `json:"language"`
	SendTo    string `json:"sendTo,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// Work is the invoices worker: it reads the default template from the
// shop's settings, then takes jobs off the invoices queue. For each it asks
// the payment-qr function for the payment text, renders the PDF, stores it in the bucket (or the folder out, when set),
// signs it, saves its row, announces InvoiceReady on the bus and writes
// invoice-made to the stream. With once set, it stops when the queue is
// empty.
func (a App) Work(ctx context.Context, once bool, out string) error {
	param, err := a.SSM.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(a.Names.TemplateParameter)})
	if err != nil {
		return fmt.Errorf("read the default template: %w", err)
	}
	w := &invoice.Worker{
		Client:          a.SQS,
		QueueURL:        a.Names.InvoicesQueue,
		OutDir:          out,
		Log:             a.Log,
		DefaultTemplate: aws.ToString(param.Parameter.Value),
		Made:            a.invoiced,
	}
	if a.Names.PaymentQRFunction != "" {
		w.PaymentText = a.paymentText
	}
	if out == "" {
		w.Files = &a.Files
	}
	return w.Run(ctx, once)
}

// invoiced is what the worker does once an invoice is kept.
func (a App) invoiced(ctx context.Context, job invoice.Job, pdf []byte) error {
	started := time.Now()
	digest := sha256.Sum256(pdf)
	signed, err := a.KMS.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(a.Names.SigningKey),
		Message:          digest[:],
		MessageType:      kmstypes.MessageTypeDigest,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return fmt.Errorf("sign the invoice: %w", err)
	}
	created := rfc3339(time.Now())
	_, err = a.DynamoDB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(a.Names.InvoicesTable),
		Item: map[string]types.AttributeValue{
			"orderId":    &types.AttributeValueMemberS{Value: job.OrderID},
			"eventId":    &types.AttributeValueMemberS{Value: job.EventID},
			"templateId": &types.AttributeValueMemberS{Value: job.TemplateID},
			"language":   &types.AttributeValueMemberS{Value: job.Language},
			"file":       &types.AttributeValueMemberS{Value: invoice.Key(job.OrderID)},
			"pages":      &types.AttributeValueMemberN{Value: "1"},
			"signature":  &types.AttributeValueMemberB{Value: signed.Signature},
			"createdAt":  &types.AttributeValueMemberS{Value: created},
		},
		// One invoice per order: a job delivered again, or asked for again,
		// doesn't replace it or announce it twice.
		ConditionExpression: aws.String("attribute_not_exists(orderId)"),
	})
	var exists *types.ConditionalCheckFailedException
	if errors.As(err, &exists) {
		return a.note(ctx, "invoice "+job.OrderID+" already made", "orderId", job.OrderID)
	}
	if err != nil {
		return fmt.Errorf("save the invoice row: %w", err)
	}
	detail, err := json.Marshal(invoiceReady{
		OrderID: job.OrderID, EventID: job.EventID, File: invoice.Key(job.OrderID),
		Language: job.Language, SendTo: job.SendTo, CreatedAt: created,
	})
	if err != nil {
		return err
	}
	sent, err := a.EventBridge.PutEvents(ctx, &eventbridge.PutEventsInput{Entries: []ebtypes.PutEventsRequestEntry{{
		EventBusName: aws.String(a.Names.Bus),
		Source:       aws.String("shop.invoices"),
		DetailType:   aws.String("InvoiceReady"),
		Detail:       aws.String(string(detail)),
	}}})
	if err != nil {
		return fmt.Errorf("announce the invoice: %w", err)
	}
	if sent.FailedEntryCount > 0 {
		return fmt.Errorf("announce the invoice: %w", errRefusedEvent)
	}
	ms := time.Since(started).Milliseconds()
	if err := a.announce(ctx, event{Type: "invoice-made", OrderID: job.OrderID, At: created, Pages: 1, RenderMs: &ms}); err != nil {
		return fmt.Errorf("write invoice-made: %w", err)
	}
	return a.note(ctx, "invoice "+job.OrderID+" made", "orderId", job.OrderID)
}

var errRefusedEvent = errors.New("EventBridge refused the event")

// errFunctionFailed means the payment-qr function ran but failed.
var errFunctionFailed = errors.New("the payment-qr function failed")

// paymentText asks the payment-qr function for the payment text a bank app
// reads from a QR code. An order the shop doesn't know gets none: the
// worker refuses its job anyway.
func (a App) paymentText(ctx context.Context, job invoice.Job) (string, error) {
	amount, currency, err := invoice.OrderTotal(job.OrderID)
	if err != nil {
		return "", nil //nolint:nilerr // the job is refused when rendered
	}
	payload, err := json.Marshal(map[string]string{"orderId": job.OrderID, "amount": amount, "currency": currency})
	if err != nil {
		return "", err
	}
	out, err := a.Lambda.Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(a.Names.PaymentQRFunction), Payload: payload})
	if err != nil {
		return "", fmt.Errorf("ask payment-qr: %w", err)
	}
	if out.FunctionError != nil {
		return "", fmt.Errorf("%w: %s", errFunctionFailed, out.Payload)
	}
	var response struct {
		QRText string `json:"qrText"`
	}
	if err := json.Unmarshal(out.Payload, &response); err != nil || response.QRText == "" {
		return "", fmt.Errorf("%w: no qrText in %s", errFunctionFailed, out.Payload)
	}
	return response.QRText, nil
}

// Status tells what the invoices table holds for the order.
func (a App) Status(ctx context.Context, orderID string) (string, error) {
	out, err := a.DynamoDB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(a.Names.InvoicesTable),
		Key:       map[string]types.AttributeValue{"orderId": &types.AttributeValueMemberS{Value: orderID}},
	})
	if err != nil {
		return "", err
	}
	if out.Item == nil {
		return "", fmt.Errorf("%s: %w", orderID, invoice.ErrNoInvoice)
	}
	var parts []string
	for _, name := range []string{"orderId", "file", "templateId", "language", "pages", "createdAt"} {
		switch v := out.Item[name].(type) {
		case *types.AttributeValueMemberS:
			parts = append(parts, name+"="+v.Value)
		case *types.AttributeValueMemberN:
			parts = append(parts, name+"="+v.Value)
		}
	}
	return strings.Join(parts, " "), nil
}
