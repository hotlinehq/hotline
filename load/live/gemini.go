package live

import (
	"context"
	"encoding/json"
	"fmt"
)

// gemini is a Gemini Live session: a setup, then a chunk of audio each
// beat, each answered by a chunk of the vendor's; a tool call gets its
// tool response.
type gemini struct {
	audio string
}

// open sends Gemini's first message and waits for setupComplete.
func (g gemini) open(ctx context.Context, s *session) error {
	if err := s.write(ctx, `{"setup":{"model":"models/gemini-live","generationConfig":{"responseModalities":["AUDIO"]}}}`); err != nil {
		return err
	}
	data, err := s.first(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errNoSetup, err)
	}
	var m struct {
		SetupComplete json.RawMessage `json:"setupComplete"`
	}
	if json.Unmarshal(data, &m) != nil || m.SetupComplete == nil {
		return fmt.Errorf("%w: it sent %.40q", errNoSetup, data)
	}
	return nil
}

func (g gemini) beat(int) string {
	return `{"realtimeInput":{"audio":{"mimeType":"audio/pcm;rate=16000","data":"` + g.audio + `"}}}`
}

// read: the vendor's audio answers a chunk, and a tool call is answered.
func (gemini) read(data []byte) (bool, []string) {
	var m struct {
		ServerContent json.RawMessage `json:"serverContent"`
		ToolCall      *struct {
			FunctionCalls []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"functionCalls"`
		} `json:"toolCall"`
	}
	_ = json.Unmarshal(data, &m)
	var back []string
	if m.ToolCall != nil {
		for _, call := range m.ToolCall.FunctionCalls {
			back = append(back, fmt.Sprintf(`{"toolResponse":{"functionResponses":[{"id":%q,"name":%q,"response":{"status":"shipped"}}]}}`, call.ID, call.Name))
		}
	}
	return m.ServerContent != nil, back
}
