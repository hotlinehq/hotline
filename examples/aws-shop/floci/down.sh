#!/usr/bin/env bash
# down.sh stops Floci and removes the lines up.sh put in /etc/hosts. What
# Floci held is gone with it: it keeps everything in memory.
set -euo pipefail
as_root() { if [[ "$(id -u)" == 0 ]]; then "$@"; else sudo "$@"; fi; }
as_root docker rm -f hotline-floci >/dev/null 2>&1 || true
as_root sed -i.bak '/# hotline-floci$/d' /etc/hosts && as_root rm -f /etc/hosts.bak
rm -rf "$(dirname "$0")"/terraform/terraform.tfstate*
echo "Floci stopped"
