#!/usr/bin/env bash
# Copies this directory and the repo files the image build needs to the
# KVM host under /srv/aether/1/repo. Run from moss.
set -euo pipefail
HOST=${HOST:-ubuntu@98.93.21.225}
KEY=${KEY:-/root/.ssh/aether_kvm_test}
ROOT=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
cd "$ROOT"
tar -czf - --exclude 'research/microvm-measure/mockmodel/mockmodel.go' \
  research/microvm-measure images/agents/Dockerfile internal/harness/acpregistry.json \
  | ssh -i "$KEY" "$HOST" 'mkdir -p /srv/aether/1/repo && tar -xzf - -C /srv/aether/1/repo'
