package fakes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// StartKrakenVendor starts a fake Kraken (Spot WebSocket v2, market data)
// on free ports of 127.0.0.1. It sends its status when the session opens,
// answers a subscribe to the book of BTC/USD with a reply and a snapshot,
// and of any other pair with a failure, answers each ping with a pong and
// an update of the book, and sends a heartbeat every HeartbeatEvery pings.
func StartKrakenVendor() (*LiveVendor, error) {
	return startWebSocketVendor(serveKraken)
}

// HeartbeatEvery is how many pings the fake Kraken answers before it
// sends a heartbeat: about once a second at the load case's beat, as
// Kraken does.
const HeartbeatEvery = 25

// krakenTime is the time every message of the fake Kraken carries, so its
// messages are the same in every run.
const krakenTime = "2026-11-21T10:00:00.000000Z"

// serveKraken answers one session until the app closes it.
func serveKraken(ctx context.Context, c *websocket.Conn) {
	defer func() { _ = c.CloseNow() }()
	status := `{"channel":"status","type":"update","data":[{"api_version":"v2","connection_id":7781230981,"system":"online","version":"2.0.9"}]}`
	if c.Write(ctx, websocket.MessageText, []byte(status)) != nil {
		return
	}
	pings := 0
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Method string `json:"method"`
			ReqID  int    `json:"req_id"` //nolint:tagliatelle // Kraken's own field name
			Params struct {
				Channel string   `json:"channel"`
				Symbol  []string `json:"symbol"`
			} `json:"params"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		var answers []string
		switch m.Method {
		case "subscribe":
			for _, symbol := range m.Params.Symbol {
				answers = append(answers, subscribed(m.ReqID, m.Params.Channel, symbol)...)
			}
		case "ping":
			pings++
			answers = []string{
				fmt.Sprintf(`{"method":"pong","req_id":%d,"time_in":%q,"time_out":%q}`, m.ReqID, krakenTime, krakenTime),
				book("update", 1, pings),
			}
			if pings%HeartbeatEvery == 0 {
				answers = append(answers, `{"channel":"heartbeat"}`)
			}
		}
		for _, a := range answers {
			if c.Write(ctx, websocket.MessageText, []byte(a)) != nil {
				return
			}
		}
	}
}

// subscribed is Kraken's answer to a subscribe for one pair: a reply and
// the book's snapshot for BTC/USD, a failure for any other.
func subscribed(reqID int, channel, symbol string) []string {
	if symbol != "BTC/USD" {
		return []string{fmt.Sprintf(`{"method":"subscribe","req_id":%d,"success":false,"error":"Currency pair not supported %s","time_in":%q,"time_out":%q}`,
			reqID, symbol, krakenTime, krakenTime)}
	}
	return []string{
		fmt.Sprintf(`{"method":"subscribe","req_id":%d,"success":true,"result":{"channel":%q,"symbol":%q,"depth":10,"snapshot":true},"time_in":%q,"time_out":%q}`,
			reqID, channel, symbol, krakenTime, krakenTime),
		book("snapshot", 10, 0),
	}
}

// book is a message of the book channel for BTC/USD: a snapshot or an
// update, with levels price levels on each side, moved by step.
func book(kind string, levels, step int) string {
	side := func(from float64, by float64) string {
		var out []string
		for i := range levels {
			out = append(out, fmt.Sprintf(`{"price":%.1f,"qty":%.8f}`, from+by*float64(i)+float64(step%50)/10, 0.25+float64(i)/100))
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	return fmt.Sprintf(`{"channel":"book","type":%q,"data":[{"symbol":"BTC/USD","bids":%s,"asks":%s,"checksum":%d,"timestamp":%q}]}`,
		kind, side(97000, -0.5), side(97001, 0.5), 2440000000+step, time.Date(2026, 11, 21, 10, 0, 0, step*int(40*time.Millisecond), time.UTC).Format(time.RFC3339Nano))
}
