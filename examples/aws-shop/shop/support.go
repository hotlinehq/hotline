package shop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// Claude says how support reaches Anthropic's Messages api.
type Claude struct {
	// APIKey is the Anthropic api key. Required.
	APIKey string
	// Model is the Claude model, such as claude-opus-5-5.
	Model string
	// BaseURL replaces Anthropic's own address, such as for a fake
	// Anthropic. Empty: Anthropic's.
	BaseURL string
	// HTTPClient carries the calls, such as through Hotline (see
	// SupportClient). Nil: Go's own.
	HTTPClient *http.Client
}

// ErrNoAnthropicKey means support has no Anthropic api key.
var ErrNoAnthropicKey = errors.New("no Anthropic api key: set ANTHROPIC_API_KEY")

// supportTurns is how many responses one question may take: one or two
// that ask for an order, then the answer. More means the model is going
// round in circles.
const supportTurns = 4

// supportPrompt tells the model what support is for. The fake Anthropic
// in the Floci workflow knows support's requests by it.
const supportPrompt = "You are the shop's support desk. Answer the customer's question about an order in one or two sentences. Use lookUpOrder to read the order."

// SupportClient is the HTTP client support's calls take: secure ones
// through the proxy, with the app's name as the user name, its integration
// ID in Hotline, and plain ones (only a local fake) directly, as
// HTTPS_PROXY would. Empty proxy: Go's own client.
func SupportClient(proxy, name string) (*http.Client, error) {
	address, err := withUser(proxy, name)
	if err != nil || address == "" {
		return nil, err
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(r *http.Request) (*url.URL, error) {
		if r.URL.Scheme != "https" {
			return nil, nil //nolint:nilnil // no proxy, as http.ProxyFromEnvironment says it
		}
		return u, nil
	}
	return &http.Client{Transport: transport}, nil
}

// lookUpOrderTool is the one tool support offers the model.
func lookUpOrderTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        "lookUpOrder",
		Description: anthropic.String("Whether a shop order is paid, and when, for how much"),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"orderId": map[string]any{"type": "string", "description": "The order number, such as A-1001"},
			},
			Required: []string{"orderId"},
		},
	}}
}

// Support answers one customer question, with the model's response
// streamed: each piece of text is written to out as it comes, and the
// whole answer is given at the end. When the model asks for an order with
// lookUpOrder, the order is read from the orders table and handed back,
// and the model streams again. The answer is the last response's text;
// the text of a response that asked for an order is printed too, on a line
// of its own.
func (a App) Support(ctx context.Context, c Claude, question string, out io.Writer) (string, error) {
	if c.APIKey == "" {
		return "", ErrNoAnthropicKey
	}
	opts := []option.RequestOption{option.WithAPIKey(c.APIKey), option.WithMaxRetries(0)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	if c.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(c.HTTPClient))
	}
	client := anthropic.NewClient(opts...)
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(question))}
	var answer strings.Builder
	for range supportTurns {
		msg, err := streamed(ctx, client, anthropic.MessageNewParams{
			Model:     c.Model,
			MaxTokens: 1024,
			System:    []anthropic.TextBlockParam{{Text: supportPrompt}},
			Tools:     []anthropic.ToolUnionParam{lookUpOrderTool()},
			Messages:  messages,
		}, &answer, out)
		if err != nil {
			return "", err
		}
		if msg.StopReason != anthropic.StopReasonToolUse {
			if answer.Len() == 0 {
				return "", ErrNoAnswer
			}
			return answer.String(), nil
		}
		if answer.Len() > 0 {
			_, _ = io.WriteString(out, "\n")
			answer.Reset()
		}
		messages = append(messages, msg.ToParam())
		results, err := a.lookUpOrders(ctx, msg)
		if err != nil {
			return "", err
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
	}
	return "", fmt.Errorf("%w after %d responses", ErrNoAnswer, supportTurns)
}

// streamed sends one request and reads its streamed response: each piece
// of text goes to answer and to out as it comes, and the whole message is
// put together for the tool calls and the reason it stopped.
func streamed(ctx context.Context, client anthropic.Client, params anthropic.MessageNewParams, answer *strings.Builder, out io.Writer) (anthropic.Message, error) {
	stream := client.Messages.NewStreaming(ctx, params)
	defer func() { _ = stream.Close() }()
	var msg anthropic.Message
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return msg, err
		}
		if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok {
				answer.WriteString(text.Text)
				_, _ = io.WriteString(out, text.Text)
			}
		}
	}
	return msg, stream.Err()
}

// lookUpOrders answers each lookUpOrder call of the message, all in one
// turn. A call the model got wrong is answered with what was wrong, so the
// model can ask again; only an order that can't be read stops the question.
func (a App) lookUpOrders(ctx context.Context, msg anthropic.Message) ([]anthropic.ContentBlockParamUnion, error) {
	var results []anthropic.ContentBlockParamUnion
	for _, block := range msg.Content {
		call, ok := block.AsAny().(anthropic.ToolUseBlock)
		if !ok {
			continue
		}
		status, err := a.lookUpOrder(ctx, call)
		if err != nil {
			return nil, err
		}
		_, failed := status["error"]
		text, err := json.Marshal(status)
		if err != nil {
			return nil, err
		}
		results = append(results, anthropic.NewToolResultBlock(call.ID, string(text), failed))
	}
	return results, nil
}

// lookUpOrder answers one tool call.
func (a App) lookUpOrder(ctx context.Context, call anthropic.ToolUseBlock) (map[string]any, error) {
	if call.Name != "lookUpOrder" {
		return map[string]any{"error": "no such tool: " + call.Name}, nil
	}
	var in struct {
		OrderID string `json:"orderId"`
	}
	// Input that is not an object leaves the order number empty.
	_ = json.Unmarshal(call.Input, &in)
	if in.OrderID == "" {
		return map[string]any{"error": "orderId is missing"}, nil
	}
	return a.orderStatus(ctx, in.OrderID)
}
