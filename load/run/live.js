// The k6 script of the WebSocket load case. It needs the k6 built with
// load/k6ws (make k6). Every setting comes from the environment: URL (ws://,
// straight to the vendor or through the proxy), AUTH (1 to name an
// integration to the proxy), VENDOR (gemini or kraken), PACKING (plain,
// alone or context), SESSIONS (how many at once) and SECONDS (how long each
// sends on its beat).
import live from 'k6/x/hotline-live';
import encoding from 'k6/encoding';
import { Counter, Trend } from 'k6/metrics';

const delay = new Trend('message_delay', true);
const sent = new Counter('messages_sent');
const received = new Counter('messages_received');
const unanswered = new Counter('messages_unanswered');
const failed = new Counter('sessions_failed');

const seconds = Number(__ENV.SECONDS);

export const options = {
  scenarios: {
    sessions: {
      // Each virtual user is one app with one session open the whole time.
      executor: 'per-vu-iterations',
      vus: Number(__ENV.SESSIONS), iterations: 1,
      maxDuration: (seconds + 30) + 's',
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const proxyAuthorization = __ENV.AUTH === '1' ? 'Basic ' + encoding.b64encode('bench-shop:') : '';
  let s;
  try {
    // The offset spreads the sessions' beats over one 40 ms chunk.
    s = live.session({ url: __ENV.URL, proxyAuthorization, vendor: __ENV.VENDOR, packing: __ENV.PACKING, seconds, offset: (__VU % 40) / 40 });
  } catch (e) {
    failed.add(1);
    console.error(String(e));
    return;
  }
  sent.add(s.sent);
  received.add(s.received);
  unanswered.add(s.unanswered);
  for (const d of s.delays) {
    delay.add(d);
  }
}
