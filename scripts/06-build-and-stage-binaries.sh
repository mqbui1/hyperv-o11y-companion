#!/usr/bin/env bash
# Run on the build machine (macOS/Linux) - cross-compiles the three
# companion binaries plus the MSI (if WiX is available) and uploads them to
# the test host's staging storage account so they can be pulled down inside
# the VM via a SAS URL (see generate-staging-sas.sh; the account has public
# blob access disabled).
#
# Usage:
#   STORAGE_ACCOUNT=hypervtest14754 ./06-build-and-stage-binaries.sh

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STORAGE_ACCOUNT="${STORAGE_ACCOUNT:?Set STORAGE_ACCOUNT}"
STORAGE_CONTAINER="${STORAGE_CONTAINER:-staging}"

cd "$REPO_ROOT"

echo "== Cross-compiling for GOOS=windows GOARCH=amd64 =="
mkdir -p /tmp/hyperv-o11y-stage
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/hyperv-o11y-stage/host-companion.exe ./cmd/host-companion
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/hyperv-o11y-stage/guestfs-probe.exe ./cmd/guestfs-probe
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o /tmp/hyperv-o11y-stage/scvmm-poller.exe ./cmd/scvmm-poller

if command -v wix >/dev/null 2>&1; then
  echo "== Building MSI via WiX =="
  (cd installer && ./build.ps1) 2>/dev/null || pwsh -NoProfile -File installer/build.ps1
  cp installer/out/HyperVO11yCompanion.msi /tmp/hyperv-o11y-stage/ 2>/dev/null || \
    echo "MSI build skipped/unavailable on this machine - transferring raw .exe binaries instead"
fi

echo "== Uploading to az://${STORAGE_ACCOUNT}/${STORAGE_CONTAINER} =="
for f in /tmp/hyperv-o11y-stage/*; do
  name=$(basename "$f")
  az storage blob upload \
    --account-name "$STORAGE_ACCOUNT" \
    --container-name "$STORAGE_CONTAINER" \
    --name "$name" \
    --file "$f" \
    --auth-mode login \
    --overwrite \
    --output none
  echo "  uploaded: $name"
done

echo ""
echo "Generate a download SAS for each file on the host with:"
echo "  STORAGE_ACCOUNT=$STORAGE_ACCOUNT BLOB_NAME=<file> ./generate-staging-sas.sh download"
