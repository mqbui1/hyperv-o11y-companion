# Local test harness (no Windows host required)

Validates the trickiest pieces of `../hypervisor-host-config.yaml` —
`transform/vm_name` extraction (including the Perfmon `#N` duplicate-suffix
fix, gap #6), the `count/migration_failures` connector (gap #9), and the
`metrics/vm_companion` pipeline that receives `hyperv-host-companion`'s
OTLP output (gap #3's `vm.disk.*`, gap #4's
`vm.guest.filesystem.used_percent` from Tier 1.5) — using plain Docker,
with no Windows machine and no Splunk account/token needed. This does NOT
test the actual `Invoke-Command -VMId` PowerShell Direct call itself (see
`../../docs/nested-hyperv-azure-test-plan.md` for that, which needs a real
or nested Hyper-V host) — it tests that once host-companion emits a
correctly-shaped OTLP metric, the collector tags it onto the right entity.

## Why this exists

`windowsperfcounters` and `windowseventlogreceiver` only work on Windows —
neither can run in a Linux Docker container. `test-config.yaml` swaps those
two receivers for a plain `otlp` receiver but keeps every processor/connector
from the real config **verbatim**, so this is a true test of the transform
logic, not a reimplementation of it.

## Run it

```
docker run --rm -p 4318:4318 --name hyperv-test-otelcol \
  -v "$(pwd)/test-config.yaml:/etc/otelcol-contrib/config.yaml" \
  otel/opentelemetry-collector-contrib:latest
```

If port 4318 is already in use on your machine (e.g. another OTel collector
running), map to a different host port instead, e.g. `-p 4419:4318`, and edit
`ENDPOINT` at the top of `send_test_data.py` to match (`http://localhost:4419`).

In another terminal:

```
pip install requests
python3 send_test_data.py
```

Then read the collector's stdout (`docker logs -f hyperv-test-otelcol`) via the
`debug` exporter. Confirmed results from the last run:

| Synthetic instance string(s) | Resulting `host.name` |
|---|---|
| `WebServer:Hv VP 0`, `WebServer#1:Hv VP 0` | `WebServer` (deduped) |
| `SQLNode_Network Adapter_{GUID}`, `Default Switch_{GUID}` | `SQLNode` (`Default Switch` dropped) |
| `...Virtual Hard Disks-ProdDB.vhdx`, `...ProdDB#1.vhdx` | `ProdDB` (deduped) |
| `{GUID}.vmgs`, `install.iso` | dropped by `filter/vm_noise` |
| Synthetic Event ID 21026 | `hyperv.vmms.migration_failures` = 1 |
| `vm.guest.filesystem.used_percent` with `vm.name=GuestProbeVM` (already resolved, no raw `instance` string — simulates `hyperv-host-companion`'s OTLP output) | `host.name=GuestProbeVM`, `host.type=hypervisor_managed_vm`, `drive_letter=C` preserved as a datapoint attribute |
| `vm.guest.memory.used_percent` with `vm.name=GuestProbeVM` (gap #2) | `host.name=GuestProbeVM`, `host.type=hypervisor_managed_vm` |

Stop the container with `docker stop hyperv-test-otelcol` when done.

## What this does NOT test

- Real Windows Perfmon counter availability/values on an actual Hyper-V host
- The `resourcedetection` processors (hostname auto-detection) — this harness
  hardcodes `hypervisor.host.name`
- Delivery to Splunk Observability Cloud — swap the `debug` exporter for
  `signalfx` (see `../hypervisor-host-config.yaml`) with a real access
  token if you want to confirm end-to-end delivery into an org.
