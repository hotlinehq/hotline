#!/bin/bash
# Loads the newest decomplect skill (hotlinehq/decomplect) at the start of
# every cloud session, so a review always follows its latest version
# (Peter, 2026-11-18). The project rules ask for a decomplect review before
# every push. The skill goes into this session's own skills folder, outside
# the repository, so nothing is committed by accident.
#
# It runs only in cloud sessions: on Peter's computer it would overwrite a
# skill he may keep there by hand.
set -uo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

target="$HOME/.claude/skills/decomplect"
fresh="$(mktemp -d)"
trap 'rm -rf "$fresh"' EXIT

# A failed download must not stop the session: keep the copy already
# there, if any, and say so.
if GIT_LFS_SKIP_SMUDGE=1 timeout 60 git clone --quiet --depth 1 \
  https://github.com/hotlinehq/decomplect "$fresh/repo" 2>/dev/null &&
  [ -f "$fresh/repo/.claude/skills/decomplect/SKILL.md" ]; then
  mkdir -p "$(dirname "$target")"
  rm -rf "$target"
  cp -R "$fresh/repo/.claude/skills/decomplect" "$target"
  echo "decomplect skill loaded fresh, commit $(git -C "$fresh/repo" rev-parse --short HEAD)"
elif [ -f "$target/SKILL.md" ]; then
  echo "decomplect skill: download failed, using the copy from an earlier session"
else
  echo "decomplect skill: download failed, the skill is not available in this session"
fi
exit 0
