#!/usr/bin/env bash
# run.sh runs the whole shop story on a local copy of AWS, through Hotline:
# starts Floci and makes the resources (up.sh), builds and starts the Hotline
# proxy, runs ../run-shop.sh, stops the proxy, and stops Floci.
#
# What it leaves in out/:
#   calls/calls.jsonl   every call, in full: all headers and the bodies (see
#                       docs/proxy-file-export.md), one JSON line per call
#   run-shop.log        what run-shop.sh printed
#   proxy.log           what the proxy printed
#
# When GRAFANA_OTLP_ENDPOINT and GRAFANA_OTLP_AUTHORIZATION are set, the
# proxy also sends its usual records, counts and traces to Grafana Cloud
# (never the bodies: only the file gets the full traffic).
#   GRAFANA_OTLP_ENDPOINT       such as https://otlp-gateway-prod-eu-west-2.grafana.net/otlp
#   GRAFANA_OTLP_AUTHORIZATION  the header value: "Basic " and the base64 of
#                               "<instance id>:<token with the metrics, logs and traces write scopes>"
# Set KEEP=1 to leave Floci running afterwards.
set -euo pipefail

cd "$(dirname "$0")"
rm -rf out && mkdir -p out/calls

cleanup() {
  [[ -n "${proxy_pid:-}" ]] && kill -TERM "$proxy_pid" 2>/dev/null && wait "$proxy_pid" 2>/dev/null || true
  [[ "${KEEP:-}" == 1 ]] || ./down.sh
}
trap cleanup EXIT

./up.sh

echo "Building the Hotline proxy"
go build -C ../../../src/hotline-proxy -o "$PWD/out/hotline-proxy" ./cmd

config=hotline-proxy.yaml
if [[ -n "${GRAFANA_OTLP_ENDPOINT:-}" && -n "${GRAFANA_OTLP_AUTHORIZATION:-}" ]]; then
  echo "Also sending records to Grafana Cloud"
  config=out/hotline-proxy.yaml
  # Paths in a config count from the config's folder, so the copy in out/
  # points one folder further up.
  awk '
    /^exporters:/ {
      print
      print "  - otlp:"
      print "      endpoint: " ENVIRON["GRAFANA_OTLP_ENDPOINT"]
      print "      headers:"
      print "        Authorization: { env: GRAFANA_OTLP_AUTHORIZATION }"
      next
    }
    { print }
    END {
      print ""
      print "telemetry:"
      print "  otlp:"
      print "    endpoint: " ENVIRON["GRAFANA_OTLP_ENDPOINT"]
      print "    headers:"
      print "      Authorization: { env: GRAFANA_OTLP_AUTHORIZATION }"
    }' hotline-proxy.yaml |
    sed -e 's#\.\./\.\./\.\./#../../../../#g' -e 's#path: \./out/calls#path: ./calls#' -e 's#\./out/tls/#./tls/#g' >"$config"
fi

# The proxy trusts the fake Gemini Live's TLS front, and the shop trusts
# the proxy's certificate authority for CONNECT: both are up.sh's test certificate authority, on top of
# this machine's own certificate authorities (Grafana Cloud needs those).
cat /etc/ssl/certs/ca-certificates.crt out/tls/ca.pem >out/tls/trusted.pem
export SSL_CERT_FILE="$PWD/out/tls/trusted.pem"

echo "Starting the Hotline proxy"
./out/hotline-proxy run --config "$config" >out/proxy.log 2>&1 &
proxy_pid=$!
for _ in $(seq 100); do
  curl -s -o /dev/null -m 1 http://127.0.0.1:8081/ && break
  kill -0 "$proxy_pid" 2>/dev/null || { cat out/proxy.log; exit 1; }
  sleep 0.1
done

export AWS_REGION=eu-west-1 AWS_ACCESS_KEY_ID=192325781350 AWS_SECRET_ACCESS_KEY=test
export MAIL_FROM=shop@example.com HOTLINE_PROXY=http://localhost:8080
# Floci starts empty, so the sample order A-1001 is new each run, and the
# fake Gemini Live's script (aimock/shop-assistant.json) knows it. The key
# is a test value: aimock takes any, and Hotline must hide it. The same goes
# for support's fake Anthropic (aimock/shop-support.json) and its key.
export ORDER=A-1001 GEMINI_API_KEY=test-key-not-real-7c1e ANTHROPIC_API_KEY=test-key-not-real-4b9d
# Same name as the config's file exporter folder, whichever config is used.
(cd .. && ./run-shop.sh) 2>&1 | tee out/run-shop.log

kill -TERM "$proxy_pid" && wait "$proxy_pid" 2>/dev/null || true
proxy_pid=
# The spec names x-goog-api-key in its security section, so the key is in
# no record, not even under content: full.
if grep -q "$GEMINI_API_KEY" out/calls/calls.jsonl out/proxy.log; then
  echo "The Gemini api key was written to out/calls/calls.jsonl or out/proxy.log"
  exit 1
fi
# The SDK sends the Anthropic key as Authorization: Bearer, which Hotline
# always hides, so it too is in no record.
if grep -q "$ANTHROPIC_API_KEY" out/calls/calls.jsonl out/proxy.log; then
  echo "The Anthropic api key was written to out/calls/calls.jsonl or out/proxy.log"
  exit 1
fi
# Each of support's streamed responses is checked event by event against
# the Anthropic spec: every stream record must be whole, with no broken event.
streams="$(jq -s '[.[] | select(.event == "hotline.stream")] | length' out/calls/calls.jsonl)"
broken="$(jq -s '[.[] | select(.event == "hotline.stream")
  | select(.attributes["hotline.stream.items_invalid"] != 0 or .attributes["hotline.stream.end"] != "complete")] | length' out/calls/calls.jsonl)"
if [[ "$streams" == 0 || "$broken" != 0 ]]; then
  echo "Support's streams: $streams recorded, $broken cut or with a broken event (want at least 1, and 0)"
  exit 1
fi
echo
echo "Every call, in full: floci/out/calls/calls.jsonl ($(wc -l <out/calls/calls.jsonl) calls)"
