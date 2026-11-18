#!/usr/bin/env bash
# down.sh stops Floci and the fake Gemini Live, and removes the lines up.sh put in /etc/hosts. What
# Floci held is gone with it: it keeps everything in memory.
set -euo pipefail
as_root() { if [[ "$(id -u)" == 0 ]]; then "$@"; else sudo "$@"; fi; }
as_root docker rm -f hotline-floci hotline-aimock hotline-gemini-front >/dev/null 2>&1 || true
as_root docker network rm hotline-gemini >/dev/null 2>&1 || true
as_root sed -i.bak '/# hotline-floci$/d' /etc/hosts && as_root rm -f /etc/hosts.bak
rm -rf "$(dirname "$0")"/terraform/terraform.tfstate*
echo "Floci and the fake Gemini Live stopped"
