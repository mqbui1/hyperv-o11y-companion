#!/usr/bin/env bash
# Generates a short-lived SAS URL for uploading/downloading files (compiled
# .exe binaries, ISOs, kickstart VHDs) to/from the staging container on the
# test host's storage account. Needed because these accounts commonly have
# public blob access disabled (PublicAccessNotPermitted) - az storage blob
# upload/download over --auth-mode login also works if you have RBAC access,
# this SAS route is for pasting a URL into a PowerShell Invoke-WebRequest
# inside the VM (over RDP or run-command) without needing Azure AD auth there.
#
# Usage:
#   STORAGE_ACCOUNT=hypervtest14754 BLOB_NAME=host-companion.exe \
#   ./generate-staging-sas.sh [upload|download]

set -euo pipefail

STORAGE_ACCOUNT="${STORAGE_ACCOUNT:?Set STORAGE_ACCOUNT}"
STORAGE_CONTAINER="${STORAGE_CONTAINER:-staging}"
BLOB_NAME="${BLOB_NAME:?Set BLOB_NAME}"
MODE="${1:-download}"
EXPIRY_HOURS="${EXPIRY_HOURS:-4}"

KEY=$(az storage account keys list --account-name "$STORAGE_ACCOUNT" --query '[0].value' -o tsv)
EXPIRY=$(date -u -v+"${EXPIRY_HOURS}"H '+%Y-%m-%dT%H:%MZ' 2>/dev/null || date -u -d "+${EXPIRY_HOURS} hours" '+%Y-%m-%dT%H:%MZ')

if [[ "$MODE" == "upload" ]]; then
  PERMS="racw"
else
  PERMS="r"
fi

SAS=$(az storage blob generate-sas \
  --account-name "$STORAGE_ACCOUNT" \
  --account-key "$KEY" \
  --container-name "$STORAGE_CONTAINER" \
  --name "$BLOB_NAME" \
  --permissions "$PERMS" \
  --expiry "$EXPIRY" \
  --https-only \
  --output tsv)

echo "https://${STORAGE_ACCOUNT}.blob.core.windows.net/${STORAGE_CONTAINER}/${BLOB_NAME}?${SAS}"
