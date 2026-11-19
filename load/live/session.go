// Package live is one app's Gemini Live session in the WebSocket load case:
// it sends a chunk of audio on a steady beat, answers the vendor's tool
// calls, and times how long each chunk waits for the vendor's answer. The
// k6 extension in load/k6ws runs one session per virtual user.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"hotline-load/fakes"
)

var (
	errPacking  = errors.New("live: packing must be plain, alone or context")
	errNoSetup  = errors.New("live: the vendor did not answer the setup")
	errReadLost = errors.New("live: the session broke before its end")
)

// Options say how one session runs.
type Options struct {
	// URL is the WebSocket address, ws://, straight to the vendor or through
	// the proxy's vendor-URL-in-path ingress.
	URL string `js:"url"`
	// ProxyAuthorization, when set, is sent as the Proxy-Authorization
	// header, to name the integration to the proxy.
	ProxyAuthorization string `js:"proxyAuthorization"`
	// Packing is how messages are compressed: plain (not at all), alone
	// (each message on its own, no_context_takeover) or context (each
	// against the earlier ones).
	Packing string `js:"packing"`
	// Seconds is how long the app sends audio.
	Seconds float64 `js:"seconds"`
	// Offset, from 0 to 1, delays the first chunk by that part of a beat,
	// so many sessions do not all send at the same instant.
	Offset float64 `js:"offset"`
}

// Stats is what one session saw.
type Stats struct {
	// Sent and Received count the messages each way.
	Sent     int `js:"sent"`
	Received int `js:"received"`
	// Delays are milliseconds from each chunk of audio leaving to the
	// vendor's answer to it arriving, in order.
	Delays []float64 `js:"delays"`
	// Unanswered counts the chunks with no answer when the session closed.
	Unanswered int `js:"unanswered"`
}

// Chunk is the app's beat: 40 ms of 16 kHz audio a message, as Gemini Live
// apps send, so 25 messages a second each way.
const Chunk = 40 * time.Millisecond

// Session runs one session and closes it normally.
func Session(ctx context.Context, o Options) (Stats, error) {
	mode, err := compression(o.Packing)
	if err != nil {
		return Stats{}, err
	}
	header := http.Header{}
	if o.ProxyAuthorization != "" {
		header.Set("Proxy-Authorization", o.ProxyAuthorization)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(dialCtx, o.URL, &websocket.DialOptions{HTTPHeader: header, CompressionMode: mode})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return Stats{}, fmt.Errorf("live: opening the session: %w", err)
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(1 << 20)
	s := &session{c: c}
	if err := s.setup(ctx); err != nil {
		return Stats{}, err
	}
	readDone := make(chan error, 1)
	go func() { readDone <- s.read(ctx) }()

	time.Sleep(time.Duration(o.Offset * float64(Chunk)))
	audio := `{"realtimeInput":{"audio":{"mimeType":"audio/pcm;rate=16000","data":"` + fakes.AudioChunk(16000, Chunk) + `"}}}`
	beat := time.NewTicker(Chunk)
	defer beat.Stop()
	end := time.After(time.Duration(o.Seconds * float64(time.Second)))
sending:
	for {
		select {
		case <-ctx.Done():
			break sending
		case <-end:
			break sending
		case err := <-readDone:
			return s.stats(), fmt.Errorf("%w: %w", errReadLost, err)
		case <-beat.C:
			s.mu.Lock()
			s.waiting = append(s.waiting, time.Now())
			s.mu.Unlock()
			if err := s.write(ctx, audio); err != nil {
				return s.stats(), err
			}
		}
	}
	// Give the last answers a moment, then close as an app does.
	s.drained(2 * time.Second)
	_ = c.Close(websocket.StatusNormalClosure, "done")
	return s.stats(), nil
}

func compression(packing string) (websocket.CompressionMode, error) {
	switch packing {
	case "plain", "":
		return websocket.CompressionDisabled, nil
	case "alone":
		return websocket.CompressionNoContextTakeover, nil
	case "context":
		return websocket.CompressionContextTakeover, nil
	}
	return 0, fmt.Errorf("%w, not %q", errPacking, packing)
}

type session struct {
	c *websocket.Conn

	mu       sync.Mutex
	sent     int
	received int
	waiting  []time.Time
	delays   []float64
}

func (s *session) write(ctx context.Context, msg string) error {
	if err := s.c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		return fmt.Errorf("live: sending: %w", err)
	}
	s.mu.Lock()
	s.sent++
	s.mu.Unlock()
	return nil
}

// setup sends Gemini's first message and waits for setupComplete.
func (s *session) setup(ctx context.Context) error {
	if err := s.write(ctx, `{"setup":{"model":"models/gemini-live","generationConfig":{"responseModalities":["AUDIO"]}}}`); err != nil {
		return err
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, data, err := s.c.Read(wait)
	if err != nil {
		return fmt.Errorf("%w: %w", errNoSetup, err)
	}
	s.mu.Lock()
	s.received++
	s.mu.Unlock()
	var m struct {
		SetupComplete json.RawMessage `json:"setupComplete"`
	}
	if json.Unmarshal(data, &m) != nil || m.SetupComplete == nil {
		return fmt.Errorf("%w: it sent %.40q", errNoSetup, data)
	}
	return nil
}

// read takes the vendor's messages until the session closes: an answer to
// a chunk ends that chunk's wait, a tool call gets its tool response.
func (s *session) read(ctx context.Context) error {
	for {
		_, data, err := s.c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
				return nil
			}
			return err
		}
		now := time.Now()
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
		s.mu.Lock()
		s.received++
		if m.ServerContent != nil && len(s.waiting) > 0 {
			s.delays = append(s.delays, float64(now.Sub(s.waiting[0]).Microseconds())/1000)
			s.waiting = s.waiting[1:]
		}
		s.mu.Unlock()
		if m.ToolCall != nil {
			for _, call := range m.ToolCall.FunctionCalls {
				answer := fmt.Sprintf(`{"toolResponse":{"functionResponses":[{"id":%q,"name":%q,"response":{"status":"shipped"}}]}}`, call.ID, call.Name)
				if err := s.write(ctx, answer); err != nil {
					return err
				}
			}
		}
	}
}

// drained waits up to limit for every chunk to have its answer.
func (s *session) drained(limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		left := len(s.waiting)
		s.mu.Unlock()
		if left == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *session) stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{Sent: s.sent, Received: s.received, Delays: s.delays, Unanswered: len(s.waiting)}
}
