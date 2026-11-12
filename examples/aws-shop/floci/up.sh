#!/usr/bin/env bash
# up.sh starts Floci, a free local copy of AWS, and makes the shop's AWS
# resources in it with Terraform (or OpenTofu, whichever is installed).
#
# How the shop's calls reach Floci: the shop sends each call to Hotline as a
# plain http call to AWS's own host name, such as sqs.eu-west-1.amazonaws.com.
# Hotline looks the name up and connects to port 80. So this script
#   1. makes those host names point to this machine (lines in /etc/hosts), and
#   2. publishes Floci's port 4566 on port 80.
# Both need the right to change /etc/hosts and to use port 80, so it uses
# sudo when it is not root.
#
#   ./up.sh     start Floci and make the resources
#   ./down.sh   stop Floci and remove the /etc/hosts lines
#
# Settings: FLOCI_IMAGE (default: the version this example was tried with).
set -euo pipefail

cd "$(dirname "$0")"
image="${FLOCI_IMAGE:-floci/floci:2.4.0}"
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

tf="$(command -v terraform || command -v tofu || true)"
[[ -n "$tf" ]] || { echo "Install Terraform or OpenTofu first"; exit 1; }

echo "Making the resources with $(basename "$tf")"
export AWS_ENDPOINT_URL=http://127.0.0.1
export AWS_REGION="$region" AWS_ACCESS_KEY_ID="$account" AWS_SECRET_ACCESS_KEY=test
"$tf" -chdir=terraform init -input=false
"$tf" -chdir=terraform apply -input=false -auto-approve
echo "Floci is ready: the shop's resources exist in account $account"
