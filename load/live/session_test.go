package live_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/coder/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"hotline-load/fakes"
	"hotline-load/live"
)

var _ = Describe("A live session against the fake vendor", func() {
	var vendor *fakes.LiveVendor

	BeforeEach(func() {
		var err error
		vendor, err = fakes.StartLiveVendor()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(vendor.Close)
	})

	DescribeTable("sends 25 chunks a second and times each answer",
		func(packing string) {
			s, err := live.Session(context.Background(), live.Options{URL: "ws://" + vendor.PlainAddr + "/live", Packing: packing, Seconds: 1.2})
			Expect(err).NotTo(HaveOccurred())
			// The setup, 30 chunks of audio, and one tool response after the 25th.
			Expect(s.Sent).To(BeNumerically("~", 32, 2))
			// setupComplete, an answer to each chunk, and one tool call.
			Expect(s.Received).To(Equal(s.Sent))
			Expect(s.Unanswered).To(BeZero())
			Expect(s.Delays).To(HaveLen(s.Sent - 2))
			for _, d := range s.Delays {
				Expect(d).To(BeNumerically(">", 0))
				Expect(d).To(BeNumerically("<", 1000))
			}
		},
		Entry("plain", "plain"),
		Entry("each message packed alone", "alone"),
		Entry("packed against earlier messages", "context"),
	)

	It("refuses a vendor it does not know", func() {
		_, err := live.Session(context.Background(), live.Options{URL: "ws://" + vendor.PlainAddr + "/live", Vendor: "deribit", Seconds: 1})
		Expect(err).To(MatchError(ContainSubstring("gemini or kraken")))
	})

	It("refuses a packing it does not know", func() {
		_, err := live.Session(context.Background(), live.Options{URL: "ws://" + vendor.PlainAddr + "/live", Packing: "zip", Seconds: 1})
		Expect(err).To(MatchError(ContainSubstring("plain, alone or context")))
	})

	It("says so when the session cannot open", func() {
		_, err := live.Session(context.Background(), live.Options{URL: "ws://127.0.0.1:1/live", Seconds: 1})
		Expect(err).To(MatchError(ContainSubstring("opening the session")))
	})

	It("sends the Proxy-Authorization it is given", func() {
		seen := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r.Header.Get("Proxy-Authorization")
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			_ = c.Close(websocket.StatusNormalClosure, "")
		}))
		DeferCleanup(srv.Close)
		_, err := live.Session(context.Background(), live.Options{URL: "ws" + strings.TrimPrefix(srv.URL, "http"), ProxyAuthorization: "Basic YmVuY2gtc2hvcDo=", Seconds: 1})
		Expect(err).To(MatchError(ContainSubstring("did not answer the setup")))
		Expect(<-seen).To(Equal("Basic YmVuY2gtc2hvcDo="))
	})
})

var _ = Describe("A Kraken session against the fake Kraken", func() {
	var vendor *fakes.LiveVendor

	BeforeEach(func() {
		var err error
		vendor, err = fakes.StartKrakenVendor()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(vendor.Close)
	})

	DescribeTable("subscribes, then pings 25 times a second and times each pong",
		func(packing string) {
			s, err := live.Session(context.Background(), live.Options{URL: "ws://" + vendor.PlainAddr + "/v2", Vendor: "kraken", Packing: packing, Seconds: 1.2})
			Expect(err).NotTo(HaveOccurred())
			// One subscribe, then 30 pings.
			Expect(s.Sent).To(BeNumerically("~", 31, 2))
			// The status, a reply and a snapshot for BTC/USD, a failure for
			// NOPE/USD, then a pong and a book update for each ping, and a
			// heartbeat after every 25th.
			Expect(s.Received).To(BeNumerically("~", 4+2*(s.Sent-1)+1, 1))
			Expect(s.Unanswered).To(BeZero())
			Expect(s.Delays).To(HaveLen(s.Sent - 1))
			for _, d := range s.Delays {
				Expect(d).To(BeNumerically(">", 0))
				Expect(d).To(BeNumerically("<", 1000))
			}
		},
		Entry("plain", "plain"),
		Entry("each message packed alone", "alone"),
		Entry("packed against earlier messages", "context"),
	)
})
