#!/usr/bin/env bash
# up.sh starts Floci, a free local copy of AWS, and makes the shop's AWS
# resources in it with Terraform (or OpenTofu, whichever is installed). It
# also starts the fake Gemini Live for the shop's assistant and the fake
# Anthropic for support: aimock, behind a small TLS front.
#
# How the shop's calls reach Floci: the shop sends each call to Hotline as a
# plain http call to AWS's own host name, such as sqs.eu-west-1.amazonaws.com.
# Hotline looks the name up and connects to port 80. So this script
#   1. makes those host names point to this machine (lines in /etc/hosts), and
#   2. publishes Floci's port 4566 on port 80.
# Both need the right to change /etc/hosts and to use port 80, so it uses
# sudo when it is not root.
#
# How the assistant reaches the fake Gemini Live: it opens wss:// to
# Google's own host name through Hotline's CONNECT ingress, as it would reach
# Google. That name also points to this machine, where a TLS front on port
# 443 passes the bytes on to aimock, which speaks plain ws:// only. Support
# reaches the fake Anthropic the same way, at api.anthropic.com: aimock plays
# both on one port, and its streamed responses pass the same front. The
# front's certificate, and Hotline's certificate authority for CONNECT, come from a test
# certificate authority made here, in out/tls; run.sh makes the proxy and the shop trust
# it. Linux only: Go on macOS ignores SSL_CERT_FILE (Peter, 2026-11-18, the
# example runs only in the workflow).
#
#   ./up.sh     start Floci and make the resources
#   ./down.sh   stop Floci and remove the /etc/hosts lines
#
# Settings: FLOCI_IMAGE, AIMOCK_IMAGE and FRONT_IMAGE (default: the versions
# this example was tried with).
set -euo pipefail

cd "$(dirname "$0")"
image="${FLOCI_IMAGE:-floci/floci:2.4.0}"
aimock_image="${AIMOCK_IMAGE:-ghcr.io/copilotkit/aimock:1.48.0@sha256:51d8e426ff99f42fa8050bb06548ff541adef2d759e5f83a33bdbf1b47a5f29d}"
front_image="${FRONT_IMAGE:-alpine/socat:1.8.1.1@sha256:7f9a06753033f2b7de18edc2353f2c15153413d95a039163c6db270fc7a6c3b0}"
gemini=generativelanguage.googleapis.com
anthropic=api.anthropic.com
region=eu-west-1
account=192325781350
marker="# hotline-floci"
hosts=(
  sqs sns sts dynamodb kinesis events ssm secretsmanager kms logs states
  lambda email iam s3 "shop-invoices-${account}.s3"
)

as_root() { if [[ "$(id -u)" == 0 ]]; then "$@"; else sudo "$@"; fi; }

echo "Making the AWS host names point to this machine"
for h in "${hosts[@]}"; do
  name="${h}.${region}.amazonaws.com"
  [[ "$h" == "iam" ]] && name="iam.amazonaws.com"
  grep -q "[[:space:]]${name}[[:space:]]*${marker}\$" /etc/hosts ||
    echo "127.0.0.1 ${name} ${marker}" | as_root tee -a /etc/hosts >/dev/null
done
for name in "$gemini" "$anthropic"; do
  grep -q "[[:space:]]${name}[[:space:]]*${marker}\$" /etc/hosts ||
    echo "127.0.0.1 ${name} ${marker}" | as_root tee -a /etc/hosts >/dev/null
done

echo "Making the test certificate authority and the TLS front's certificate"
tls=out/tls
mkdir -p "$tls"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
  -subj "/CN=hotline floci test certificate authority" -keyout "$tls/ca-key.pem" -out "$tls/ca.pem" \
  -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign,cRLSign 2>/dev/null
openssl req -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -subj "/CN=${gemini}" -keyout "$tls/front-key.pem" -out "$tls/front.csr" 2>/dev/null
printf 'subjectAltName=DNS:%s,DNS:%s\nextendedKeyUsage=serverAuth\n' "$gemini" "$anthropic" >"$tls/front.ext"
openssl x509 -req -in "$tls/front.csr" -CA "$tls/ca.pem" -CAkey "$tls/ca-key.pem" -CAcreateserial \
  -days 2 -extfile "$tls/front.ext" -out "$tls/front.pem" 2>/dev/null
cat "$tls/front.pem" "$tls/front-key.pem" >"$tls/front-bundle.pem"
chmod 644 "$tls"/*.pem

echo "Starting the fake Gemini Live and the fake Anthropic: aimock ($aimock_image) behind a TLS front"
as_root docker rm -f hotline-aimock hotline-gemini-front >/dev/null 2>&1 || true
as_root docker network create hotline-gemini >/dev/null 2>&1 || true
as_root docker run -d --name hotline-aimock --network hotline-gemini \
  -v "$PWD/aimock:/fixtures:ro" "$aimock_image" -f /fixtures -h 0.0.0.0 >/dev/null
as_root docker run -d --name hotline-gemini-front --network hotline-gemini -p 127.0.0.1:443:443 \
  -v "$PWD/$tls:/tls:ro" "$front_image" \
  openssl-listen:443,reuseaddr,fork,cert=/tls/front-bundle.pem,verify=0 tcp:hotline-aimock:4010 >/dev/null

echo "Starting Floci ($image)"
docker rm -f hotline-floci >/dev/null 2>&1 || true
# The Docker socket is for Lambda: Floci runs a function in its own container.
as_root docker run -d --name hotline-floci -p 127.0.0.1:80:4566 \
  -v /var/run/docker.sock:/var/run/docker.sock "$image" >/dev/null

for _ in $(seq 100); do
  curl -s -o /dev/null -m 1 http://127.0.0.1/ && break
  sleep 0.1
done
curl -s -o /dev/null -m 2 http://127.0.0.1/ || { echo "Floci did not answer on port 80"; docker logs hotline-floci | tail -20; exit 1; }

for _ in $(seq 100); do
  curl -s -o /dev/null -m 1 --cacert "$tls/ca.pem" "https://${gemini}/" && break
  sleep 0.1
done
curl -s -o /dev/null -m 2 --cacert "$tls/ca.pem" "https://${gemini}/" ||
  { echo "The fake Gemini Live did not answer on port 443"; docker logs hotline-aimock | tail -20; exit 1; }

tf="$(command -v terraform || command -v tofu || true)"
[[ -n "$tf" ]] || { echo "Install Terraform or OpenTofu first"; exit 1; }

echo "Making the resources with $(basename "$tf")"
export AWS_ENDPOINT_URL=http://127.0.0.1
export AWS_REGION="$region" AWS_ACCESS_KEY_ID="$account" AWS_SECRET_ACCESS_KEY=test
"$tf" -chdir=terraform init -input=false
"$tf" -chdir=terraform apply -input=false -auto-approve
echo "Floci is ready: the shop's resources exist in account $account"
