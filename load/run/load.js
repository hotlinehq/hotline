// The k6 script of the load test. Every setting comes from the environment:
// URL, BODY, ACTION, AUTH (1 to name an integration to the proxy), RATE and
// DURATION.
import http from 'k6/http';
import { check } from 'k6';
import encoding from 'k6/encoding';

const headers = {
  'Content-Type': 'application/x-amz-json-1.1',
  'X-Amz-Target': __ENV.ACTION,
  'Authorization': 'AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261102/eu-west-1/ssm/aws4_request, SignedHeaders=host;x-amz-date;x-amz-target, Signature=00',
  'X-Amz-Date': '20261102T000000Z',
};
if (__ENV.AUTH === '1') {
  headers['Proxy-Authorization'] = 'Basic ' + encoding.b64encode('bench-shop:');
}

export const options = {
  scenarios: {
    steady: {
      // Calls keep coming on schedule even when replies slow down, as real
      // traffic does.
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE), timeUnit: '1s',
      duration: __ENV.DURATION,
      preAllocatedVUs: 50, maxVUs: 500,
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
};

export default function () {
  const r = http.post(__ENV.URL, __ENV.BODY, { headers });
  check(r, { '200': (x) => x.status === 200 });
}
