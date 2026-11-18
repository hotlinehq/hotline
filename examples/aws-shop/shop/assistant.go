package shop

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"google.golang.org/genai"
)

// Gemini says how the assistant reaches Gemini Live.
type Gemini struct {
	// APIKey is the Gemini api key. Required.
	APIKey string
	// Model is the Live model, such as gemini-live-2.5-flash.
	Model string
	// BaseURL replaces Google's own address, such as for a fake Gemini.
	// Empty: Google's.
	BaseURL string
}

// ErrNoGeminiKey means the assistant has no Gemini api key.
var ErrNoGeminiKey = errors.New("no Gemini api key: set GEMINI_API_KEY")

// ErrNoAnswer means the model ended its turn with neither an answer nor a
// tool call, so nothing more would come.
var ErrNoAnswer = errors.New("the model ended its turn with no answer")

// LiveProxy says which proxy Google's SDK takes for each WebSocket: secure
// ones (wss, which the SDK's dialer looks up as https) go through the
// proxy, with the app's name as the user name, its integration ID in
// Hotline; plain ones (ws, only a local fake) go directly, as HTTPS_PROXY
// would. The SDK dials with websocket.DefaultDialer, so the caller sets
// that dialer's Proxy to this. Setting HTTPS_PROXY instead would not do:
// Go reads it once per program, so a lookup made earlier would win.
func LiveProxy(proxy, name string) (func(*http.Request) (*url.URL, error), error) {
	address, err := withUser(proxy, name)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(address)
	if err != nil {
		return nil, err
	}
	return func(r *http.Request) (*url.URL, error) {
		if r.URL.Scheme != "https" {
			return nil, nil //nolint:nilnil // no proxy, as http.ProxyFromEnvironment says it
		}
		return u, nil
	}, nil
}

// LiveSession is the part of a Gemini Live session the assistant uses.
// *genai.Session has it.
type LiveSession interface {
	SendClientContent(genai.LiveClientContentInput) error
	SendToolResponse(genai.LiveToolResponseInput) error
	Receive() (*genai.LiveServerMessage, error)
}

// orderStatusTool is the one tool the assistant offers the model.
func orderStatusTool() *genai.Tool {
	return &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{
		Name:        "orderStatus",
		Description: "Whether a shop order is paid, and when, for how much",
		Parameters: &genai.Schema{
			Type:       genai.TypeObject,
			Properties: map[string]*genai.Schema{"orderId": {Type: genai.TypeString, Description: "The order number, such as A-1001"}},
			Required:   []string{"orderId"},
		},
	}}}
}

// OpenLive opens a Gemini Live session for the assistant, answering in
// text, with the orderStatus tool. The caller closes it. The session's
// connection goes through websocket.DefaultDialer's proxy (see LiveProxy).
func OpenLive(ctx context.Context, g Gemini) (*genai.Session, error) {
	if g.APIKey == "" {
		return nil, ErrNoGeminiKey
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      g.APIKey,
		Backend:     genai.BackendGeminiAPI,
		HTTPOptions: genai.HTTPOptions{APIVersion: "v1beta", BaseURL: g.BaseURL},
	})
	if err != nil {
		return nil, err
	}
	return client.Live.Connect(ctx, g.Model, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityText},
		Tools:              []*genai.Tool{orderStatusTool()},
	})
}

// Ask sends one question over the session, as text, and gives the model's
// answer. When the model calls orderStatus, the order is read from the
// orders table and handed back. The model ends the turn that held the tool
// call before it answers, so the answer is the text of the first turn that
// has any (found with aimock, 2026-11-18). A turn with neither text nor a
// tool call is ErrNoAnswer, as the model would then wait for us.
func (a App) Ask(ctx context.Context, s LiveSession, question string) (string, error) {
	if err := s.SendClientContent(genai.LiveClientContentInput{Turns: genai.Text(question)}); err != nil {
		return "", err
	}
	var answer strings.Builder
	called := false
	for {
		msg, err := s.Receive()
		if err != nil {
			return "", err
		}
		if msg.ToolCall != nil {
			if err := a.answerTools(ctx, s, msg.ToolCall); err != nil {
				return "", err
			}
			called = true
		}
		content := msg.ServerContent
		if content == nil {
			continue
		}
		if content.ModelTurn != nil {
			for _, p := range content.ModelTurn.Parts {
				answer.WriteString(p.Text)
			}
		}
		if content.TurnComplete {
			if answer.Len() > 0 {
				return answer.String(), nil
			}
			if !called {
				return "", ErrNoAnswer
			}
			called = false
		}
	}
}

// answerTools answers each tool call of the model. A call the model got
// wrong is answered with what was wrong, so the model can ask again; only
// an order that can't be read stops the question.
func (a App) answerTools(ctx context.Context, s LiveSession, call *genai.LiveServerToolCall) error {
	answers := make([]*genai.FunctionResponse, 0, len(call.FunctionCalls))
	for _, fc := range call.FunctionCalls {
		status, err := a.answerTool(ctx, fc)
		if err != nil {
			return err
		}
		answers = append(answers, &genai.FunctionResponse{ID: fc.ID, Name: fc.Name, Response: status})
	}
	return s.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: answers})
}

// answerTool answers one tool call.
func (a App) answerTool(ctx context.Context, fc *genai.FunctionCall) (map[string]any, error) {
	if fc.Name != "orderStatus" {
		return map[string]any{"error": "no such tool: " + fc.Name}, nil
	}
	orderID, _ := fc.Args["orderId"].(string)
	if orderID == "" {
		return map[string]any{"error": "orderId is missing"}, nil
	}
	return a.orderStatus(ctx, orderID)
}

// orderStatus reads the order from the orders table: paid, with when and
// how much, or not found.
func (a App) orderStatus(ctx context.Context, orderID string) (map[string]any, error) {
	row, err := a.DynamoDB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(a.Names.OrdersTable),
		Key:       map[string]types.AttributeValue{"orderId": &types.AttributeValueMemberS{Value: orderID}},
	})
	if err != nil {
		return nil, fmt.Errorf("read the order: %w", err)
	}
	if len(row.Item) == 0 {
		return map[string]any{"status": "not found"}, nil
	}
	status := map[string]any{"status": "paid"}
	for _, field := range []string{"paidAt", "total", "currency"} {
		switch v := row.Item[field].(type) {
		case *types.AttributeValueMemberS:
			status[field] = v.Value
		case *types.AttributeValueMemberN:
			status[field] = v.Value
		}
	}
	return status, nil
}
