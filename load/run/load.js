// The k6 script of the load test. Every setting comes from the environment:
// URL (with the path and query), METHOD, SERVICE (ssm or s3, the service
// the request is signed for, or anthropic for a streamed answer), BODY and
// ACTION (SSM only), AUTH (1 to name an integration to the proxy), RATE and
// DURATION. SERVICE openai sends a multipart audio upload that the script
// builds itself: a file of FILE_BYTES bytes, the text parts model and
// language, and, when PROMPT_BYTES is not 0, a prompt of that many bytes.
import http from 'k6/http';
import { check } from 'k6';
import encoding from 'k6/encoding';

const signed = 'AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261102/eu-west-1/' + __ENV.SERVICE + '/aws4_request, ';
const anthropic = {
  'Content-Type': 'application/json',
  // Made up: the fake vendor reads no key.
  'X-Api-Key': 'sk-ant-load-test',
  'Anthropic-Version': '2023-06-01',
};
const aws = __ENV.SERVICE === 's3'
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
const boundary = 'hotline-load-boundary';
const openai = {
  'Content-Type': 'multipart/form-data; boundary=' + boundary,
  // Made up: the fake vendor reads no key.
  'Authorization': 'Bearer sk-load-test',
};
// part is one part of the upload, as an SDK writes it.
function part(headers, content) {
  return '--' + boundary + '\r\n' + headers + '\r\n\r\n' + content + '\r\n';
}
// upload is built once, as every call sends the same.
function upload() {
  let body = part('Content-Disposition: form-data; name="file"; filename="memo.mp3"\r\nContent-Type: audio/mpeg', 'a'.repeat(Number(__ENV.FILE_BYTES)))
    + part('Content-Disposition: form-data; name="model"', 'gpt-4o-transcribe')
    + part('Content-Disposition: form-data; name="language"', 'en');
  if (Number(__ENV.PROMPT_BYTES) > 0) {
    body += part('Content-Disposition: form-data; name="prompt"', 'b'.repeat(Number(__ENV.PROMPT_BYTES)));
  }
  return body + '--' + boundary + '--\r\n';
}
const headers = { anthropic, openai }[__ENV.SERVICE] || aws;
if (__ENV.AUTH === '1') {
  headers['Proxy-Authorization'] = 'Basic ' + encoding.b64encode('bench-shop:');
}
// A GET sends no body.
const body = __ENV.METHOD === 'GET' ? null : (__ENV.SERVICE === 'openai' ? upload() : __ENV.BODY);

export const options = {
  scenarios: {
    steady: {
      // Calls keep coming on schedule even when responses slow down, as real
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
