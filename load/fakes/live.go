package fakes

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// AudioChunk is chunkLength of 16-bit mono sound at rate samples a
// second, written as base64 the way Gemini Live carries audio in JSON. It is
// a tone with a little noise, so compression shrinks it about as much as it
// shrinks speech, and the same every time.
func AudioChunk(rate int, chunkLength time.Duration) string {
	samples := int(int64(rate) * chunkLength.Milliseconds() / 1000)
	pcm := make([]byte, 2*samples)
	noise := uint32(1)
	for i := range samples {
		noise = noise*1664525 + 1013904223
		tone := 8000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate))
		v := uint16(int16(tone) + int16(noise>>24) - 128) //nolint:gosec // a 16-bit sample, written as its two bytes
		binary.LittleEndian.PutUint16(pcm[2*i:], v)
	}
	return base64.StdEncoding.EncodeToString(pcm)
}

// LiveVendor is a fake Gemini Live: a WebSocket that answers the app's
// setup with setupComplete, each chunk of the app's audio with a chunk of
// its own, and every ToolCallEvery chunks also asks for a tool call. It
// agrees to whatever permessage-deflate the app offers, so the app picks
// how messages are packed. Like Vendor, it listens on TLS (for the proxy)
// and on plain HTTP (for the app without the proxy).
type LiveVendor struct {
	TLSAddr, PlainAddr string
	CertPEM            []byte
	servers            []*http.Server
}

// ToolCallEvery is how many chunks of audio the vendor answers before it
// asks for one tool call.
const ToolCallEvery = 25

// vendorChunk is 40 ms of Gemini's 24 kHz answer.
const vendorChunk = 40 * time.Millisecond

// StartLiveVendor starts the vendor on free ports of 127.0.0.1.
func StartLiveVendor() (*LiveVendor, error) {
	certPEM, cert, err := selfSigned()
	if err != nil {
		return nil, err
	}
	audio := AudioChunk(24000, vendorChunk)
	v := &LiveVendor{CertPEM: certPEM}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
		if err != nil {
			return
		}
		c.SetReadLimit(1 << 20)
		serveLive(r.Context(), c, audio)
	})
	for _, secure := range []bool{true, false} {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			v.Close()
			return nil, err
		}
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		if secure {
			// A WebSocket opens on HTTP/1.1 only.
			srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS12}
			v.TLSAddr = ln.Addr().String()
			go func() { _ = srv.ServeTLS(ln, "", "") }()
		} else {
			v.PlainAddr = ln.Addr().String()
			go func() { _ = srv.Serve(ln) }()
		}
		v.servers = append(v.servers, srv)
	}
	return v, nil
}

// serveLive answers one session until the app closes it.
func serveLive(ctx context.Context, c *websocket.Conn, audio string) {
	defer func() { _ = c.CloseNow() }()
	chunks := 0
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Setup         json.RawMessage `json:"setup"`
			RealtimeInput json.RawMessage `json:"realtimeInput"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		var answers []string
		switch {
		case m.Setup != nil:
			answers = []string{`{"setupComplete":{}}`}
		case m.RealtimeInput != nil:
			chunks++
			answer := `{"serverContent":{"modelTurn":{"parts":[{"inlineData":{"mimeType":"audio/pcm;rate=24000","data":"` + audio + `"}}]}}`
			if chunks%ToolCallEvery == 0 {
				answer += `,"usageMetadata":{"totalTokenCount":` + strconv.Itoa(chunks*32) + `}`
			}
			answers = []string{answer + "}"}
			if chunks%ToolCallEvery == 0 {
				answers = append(answers, fmt.Sprintf(`{"toolCall":{"functionCalls":[{"id":"call-%d","name":"lookup_order","args":{"order":"A-%d"}}]}}`, chunks, chunks))
			}
		}
		for _, a := range answers {
			if c.Write(ctx, websocket.MessageText, []byte(a)) != nil {
				return
			}
		}
	}
}

// Close stops the vendor.
func (v *LiveVendor) Close() {
	for _, s := range v.servers {
		_ = s.Close()
	}
}
