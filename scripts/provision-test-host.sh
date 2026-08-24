#!/usr/bin/env bash
# Provisions the Azure resources for the nested-Hyper-V test environment used
# to validate Tier 1 (perfcounters), Tier 1.5 (PowerShell Direct guest probe),
# and Tier 1.6 (VHDX host-side guestfs read) end-to-end against real guests.
#
# This creates the "physical" Hyper-V host VM only. Nested guest VMs (Windows
# test VMs + the Rocky Linux 9 guest used for Tier 1.6) are created ON that
# host by scripts/setup-hyperv-host.ps1, run interactively over RDP — see
# docs/full-test-environment-runbook.md for why this isn't fully unattended
# (run-command reliability issues on this class of nested-virt host).
#
# Usage:
#   RESOURCE_GROUP=my-rg LOCATION=eastus VM_NAME=my-hyperv-test-host \
#   ADMIN_USERNAME=azureuser ADMIN_PASSWORD='<strong-password>' \
#   ./provision-test-host.sh

set -euo pipefail

RESOURCE_GROUP="${RESOURCE_GROUP:-hyperv-test-rg}"
LOCATION="${LOCATION:-eastus}"
VM_NAME="${VM_NAME:-hyperv-test-host}"
VM_SIZE="${VM_SIZE:-Standard_D4als_v7}"
VM_IMAGE="${VM_IMAGE:-MicrosoftWindowsServer:WindowsServer:2025-datacenter-g2:latest}"
ADMIN_USERNAME="${ADMIN_USERNAME:-azureuser}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:?Set ADMIN_PASSWORD to a strong password before running}"
STORAGE_ACCOUNT="${STORAGE_ACCOUNT:-hypervtest$((RANDOM % 90000 + 10000))}"
STORAGE_CONTAINER="${STORAGE_CONTAINER:-staging}"

echo "== Resource group =="
az group create --name "$RESOURCE_GROUP" --location "$LOCATION" --output none

echo "== Hyper-V host VM ($VM_SIZE, Gen2, TrustedLaunch) =="
# Nested virtualization needs a Dv3/Ev3-or-newer size and a Generation 2
# image. TrustedLaunch is used here to match the environment this repo was
# actually validated against; drop --security-type/--enable-secure-boot/
# --enable-vtpm if your subscription/image combo doesn't support it.
az vm create \
  --resource-group "$RESOURCE_GROUP" \
  --name "$VM_NAME" \
  --image "$VM_IMAGE" \
  --size "$VM_SIZE" \
  --admin-username "$ADMIN_USERNAME" \
  --admin-password "$ADMIN_PASSWORD" \
  --security-type TrustedLaunch \
  --enable-secure-boot true \
  --enable-vtpm true \
  --public-ip-sku Standard \
  --output none

echo "== Open RDP (3389) =="
az vm open-port --resource-group "$RESOURCE_GROUP" --name "$VM_NAME" --port 3389 --output none

echo "== Storage account for file staging (binaries, ISOs, kickstart VHDs) =="
# Public access is commonly disabled at the account level on these
# subscriptions (PublicAccessNotPermitted) - files are moved to/from the host
# via short-lived SAS URLs, not public blob URLs. See
# scripts/generate-staging-sas.sh.
az storage account create \
  --resource-group "$RESOURCE_GROUP" \
  --name "$STORAGE_ACCOUNT" \
  --location "$LOCATION" \
  --sku Standard_LRS \
  --allow-blob-public-access false \
  --output none

az storage container create \
  --account-name "$STORAGE_ACCOUNT" \
  --name "$STORAGE_CONTAINER" \
  --auth-mode login \
  --output none

PUBLIC_IP=$(az vm show -d --resource-group "$RESOURCE_GROUP" --name "$VM_NAME" --query publicIps -o tsv)

echo ""
echo "== Done =="
echo "RDP:              mstsc /v:${PUBLIC_IP}"
echo "Resource group:   ${RESOURCE_GROUP}"
echo "VM name:          ${VM_NAME}"
echo "Storage account:  ${STORAGE_ACCOUNT} (container: ${STORAGE_CONTAINER})"
echo ""
echo "Next: RDP into ${PUBLIC_IP} and run setup-hyperv-host.ps1 (see"
echo "docs/full-test-environment-runbook.md, Step 2)."
