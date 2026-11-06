// The k6 script of the load test. Every setting comes from the environment:
// URL (with the path and query), METHOD, SERVICE (ssm or s3, the service the
// request is signed for), BODY and ACTION (SSM only), AUTH (1 to name an
// integration to the proxy), RATE and DURATION.
import http from 'k6/http';
import { check } from 'k6';
import encoding from 'k6/encoding';

const signed = 'AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261102/eu-west-1/' + __ENV.SERVICE + '/aws4_request, ';
const headers = __ENV.SERVICE === 's3'
  ? {
    'Authorization': signed + 'SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=00',
    'X-Amz-Date': '20261102T000000Z',
    // The checksum of an empty body.
    'X-Amz-Content-Sha256': 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
  }
  : {
    'Content-Type': 'application/x-amz-json-1.1',
    'X-Amz-Target': __ENV.ACTION,
    'Authorization': signed + 'SignedHeaders=host;x-amz-date;x-amz-target, Signature=00',
    'X-Amz-Date': '20261102T000000Z',
  };
if (__ENV.AUTH === '1') {
  headers['Proxy-Authorization'] = 'Basic ' + encoding.b64encode('bench-shop:');
}
// A GET sends no body.
const body = __ENV.METHOD === 'GET' ? null : __ENV.BODY;

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
  const r = http.request(__ENV.METHOD, __ENV.URL, body, { headers, tags: { name: 'call' } });
  check(r, { '200': (x) => x.status === 200 });
}
