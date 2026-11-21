package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

var errNoSubscribe = errors.New("live: Kraken did not answer the subscribe")

// kraken is a session on Kraken's market data (Spot WebSocket v2): it
// subscribes to the order book of BTC/USD and of NOPE/USD, a pair Kraken
// does not know, then pings each beat. Each ping is answered by a pong with
// the same req_id; the book's updates and the heartbeats need no answer.
type kraken struct{}

// subscribeID is the req_id of the subscribe; the pings count on from it.
const subscribeID = 1

// open subscribes and waits for Kraken's status, the reply and snapshot
// for BTC/USD, and the failure for NOPE/USD, in any order.
func (kraken) open(ctx context.Context, s *session) error {
	subscribe := fmt.Sprintf(`{"method":"subscribe","params":{"channel":"book","symbol":["BTC/USD","NOPE/USD"],"depth":10},"req_id":%d}`, subscribeID)
	if err := s.write(ctx, subscribe); err != nil {
		return err
	}
	var replied, failed, snapshot bool
	for !replied || !failed || !snapshot {
		data, err := s.first(ctx)
		if err != nil {
			return fmt.Errorf("%w: %w", errNoSubscribe, err)
		}
		var m struct {
			Method  string `json:"method"`
			Success *bool  `json:"success"`
			ReqID   int    `json:"req_id"` //nolint:tagliatelle // Kraken's own field name
			Channel string `json:"channel"`
			Type    string `json:"type"`
		}
		_ = json.Unmarshal(data, &m)
		switch {
		case m.Method == "subscribe" && m.ReqID == subscribeID && m.Success != nil:
			replied = replied || *m.Success
			failed = failed || !*m.Success
		case m.Channel == "book" && m.Type == "snapshot":
			snapshot = true
		}
	}
	return nil
}

func (kraken) beat(n int) string {
	return fmt.Sprintf(`{"method":"ping","req_id":%d}`, subscribeID+n)
}

// read: a pong answers the oldest ping. Pings go and come back in order,
// so the oldest waiting is the one with the pong's req_id.
func (kraken) read(data []byte) (bool, []string) {
	var m struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(data, &m)
	return m.Method == "pong", nil
}
