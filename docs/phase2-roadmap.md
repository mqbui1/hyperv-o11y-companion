# Phase 2 Roadmap

Tracks the guest filesystem coverage gaps confirmed by Royal Caribbean
fleet testing (September–November 2026). Approval status is from Chris
Ojeda's written review (November 17 PDF + Webex follow-up and fleet
data, November 20 2026).

Items 2a–2i map directly to `hyperv-o11y-companion`'s code paths. Where
the fix touches `internal/guestfs`, it must also be applied to
`hyperv-otel-receiver/internal/guestfs` — the two repos maintain
separate copies of that package.

---

## Status summary

| # | Fix | Status | Priority | Companion files touched |
|---|---|---|---|---|
| 2a | ext4 superblock parser | **Approved** | Low — build 2i first | `internal/guestfs/ext4.go` (new), `internal/guestfs/probe.go` |
| 2b | LVM Logical Volume enumeration | **Approved** | Low — build 2i first | `internal/guestfs/lvm.go` (new), `internal/guestfs/probe.go` |
| 2c | Multi-partition / multi-disk + diskmap fix | **Approved** | High — main Linux item | `internal/hyperv/types.go`, `internal/hyperv/diskmap.go`, `cmd/guestfs-probe/main.go` |
| 2d | MBR / Gen 1 VM support | **Approved** | **High — not a fast follow** | `internal/guestfs/mbr.go` (new), `internal/guestfs/probe.go` |
| 2e | 4Kn native-sector VHDX support | **Not needed — current fleet** | Deferred | `internal/guestfs/gpt.go`, `internal/guestfs/vhdx.go` |
| 2f | Checkpoint / differencing disk chain | **Approved** | Medium — 7% of Jewel | `internal/guestfs/vhdx.go` |
| 2g | Credential gap enforcement (Tier 1.5) | **Not Needed** | — | None |
| 2h | NTFS $Bitmap parser (Windows guest filesystem %) | **Approved** | **High — largest new coverage** | `internal/guestfs/ntfs.go` (new), `internal/guestfs/probe.go` |
| 2i | Probe failure observability | **Approved** | **High — build first** | `cmd/guestfs-probe/main.go`, `cmd/host-companion/main.go` |

## RC fleet data (as of 2026-11-20)

Reference for cardinality estimates and prioritization. Jewel (8 hosts,
230 VMs) is the production ship model.

| Cluster | Hosts | VMs in scope |
|---|---|---|
| Jewel | 8 | 230 |
| Culpeper dev | 2 | 151 |
| Silversea cloud | 3 | 113 |
| Suwannee | 3 | 30 |
| **Monitored total** | **16** | **524** |
| Full fleet | 130 | — |

VM breakdown (524 in scope):
- **Windows:** 359 (68%) — 37 pre-2016, avg 2.2 NTFS fixed volumes, p95 4, max 7
- **Linux:** 169 (32%) — 91 SCVMM-labeled + 78 confirmed by Tier 1.6 (all RHEL-family: RHEL 8×25, RHEL 9×7, RHEL 7×1, RHEL 5×2 labeled; remainder unlabeled)
- **Gen 1 / MBR:** 94 (18%) — including 13% on Jewel. Mix of Windows and Linux.
- **Data disks:** mean 2.3 disks/VM, p95 4, max 10

**Standard RHEL build (applies to all Linux VMs):** GPT, no LVM, three
partitions: EFI System (600 MB, FAT), `/boot` (1 GB, XFS), `/` (rest,
XFS). No separate `/var` or `/home`. Two probeable XFS filesystems per VM.

**Checkpoint data (read-only sweep, 2026-11-20):**
- 7% on Jewel (13% fleet-wide; dev clusters up to 37%)
- Chain depth: p50 1–2, p95 2–3
- One VM at the Hyper-V limit of 50 checkpoints
- 3 VMs with unreadable disks (broken chain or pass-through)

---

## Operational visibility

### 2i — Probe failure observability
**Status: Approved | Priority: Build first**

RC asked to build 2i before 2a/2b so real `skip_reason` counts from the
fleet size those items before code is written.

When a VM probe fails, `vm.guest.filesystem.used_percent` stops emitting
silently. Same problem exists for Tier 1.5 in `cmd/host-companion`.

**Fix:** Emit two new metrics on failure, in both services:

| Metric | Type | Attributes | Purpose |
|---|---|---|---|
| `hyperv.guest_probe.failure` | Counter (monotonic, delta) | `vm.name`, `reason` | Incremented on each failed probe attempt. `reason`: `powered_off`, `credential_missing`, `integration_broken`, `other` |
| `hyperv.guest_probe.stale_seconds` | Gauge | `vm.name` | Seconds since the last successful probe for this VM |

Add a Splunk detector that fires when `stale_seconds` exceeds a configurable
threshold (default 15 minutes).

**`cmd/guestfs-probe/main.go`:** Add `lastSuccess map[string]time.Time`
protected by a mutex (or single-goroutine loop). Update `probeOneVM` to
record success time on success and emit both metrics on failure. Wire
through the existing `exp.Meter` OTel SDK instance (same pattern as
`vm.guestfs.probe.skip` counter on line 88).

**`cmd/host-companion/main.go`:** Same pattern for the Tier 1.5 probe
loop — the per-VM error log on line 252
(`log.Printf("guest probe: vm=%s: %v", ...)`) is the hook point.

---

## Windows guest filesystem coverage

### 2h — NTFS $Bitmap parser (Windows guest filesystem %)
**Status: Approved | Priority: High — largest new coverage (359 VMs, all currently dark)**

Tier 1.6 v1 only reads XFS. All Windows VMs return
`skip_reason=no_linux_partition`. Windows coverage depends entirely on
Tier 1.5's PowerShell Direct path, which requires credentials and cannot
reach pre-2016 guests (37 VMs).

**Fix:** Add `internal/guestfs/ntfs.go`:

1. **NTFS Boot Sector** (sector 0 of the NTFS volume): `Bytes Per Cluster`
   and `Total Clusters` give total volume size. `MFT Start LCN` locates
   the MFT.
2. **MFT record 6 ($Bitmap metafile):** Located via MFT base + 6 × record
   size. Contains data runs pointing to the $Bitmap file.
3. **$Bitmap:** One bit per cluster; counting set bits gives used cluster
   count. Used % = set bits / total clusters.

**Chris's correction (PDF review, 2026-11-17):** `$Bitmap` must be found
via MFT record 6, not a fixed byte offset. Full lookup chain:
boot sector → `MFT Start LCN` → MFT record 6 → `$Bitmap` data runs →
count set bits.

**Skip Recovery and System Reserved partitions.** Recovery partitions
are NTFS but sit nearly full by design and would trip the disk-full
detector. Identify by partition type GUID (Windows Recovery Environment:
`DE94BBA4-06D1-4D40-A16A-BFD50179D6AC`; Microsoft Reserved:
`E3C9E316-0B5C-4DB8-817D-F92DF00215AE`) and exclude them from probing.

**Tier preference and shared key (resolved 2026-11-20):**
- No `source` dimension needed — `service.name` already distinguishes
  `hyperv-host-companion` (Tier 1.5) from `hyperv-guestfs-probe` (Tier 1.6).
- Shared key for per-volume preference: **GPT partition GUID**.
  Tier 1.6 reads it from the GPT partition entry; Tier 1.5 reads it from
  `Get-Partition`'s `Guid` field. For MBR disks: disk signature +
  partition offset. The partition GUID maps 1:1 to `drive_letter` on
  Tier 1.5 — re-keys existing series without adding MTS.
- Preference: Tier 1.5 while fresh (5m samples), Tier 1.6 as fallback
  (15m samples). Detector SignalFlow uses `fill(duration='15m')` +
  `if_missing()` — see detector design in Phase 2 cardinality doc.

---

## Linux guest filesystem coverage

### 2c — Multi-partition / multi-disk iteration + diskmap `ByID` fix
**Status: Approved | Priority: High — main Linux item per RC**

Two separate changes required. This is the primary Linux work in Phase 2;
2a and 2b are low priority until 2i's skip_reason counts show otherwise.

**1. `internal/hyperv/diskmap.go` — backport the receiver's `ByID` fix.**

The current `BuildDiskMap` loop uses last-write-wins for `ByID` —
whichever disk `Get-VMHardDiskDrive` returns last wins, not the boot disk.
`cmd/guestfs-probe` iterates `ByID` to find the VHDX path per VM, so
multi-disk VMs are probed against the wrong disk. The receiver already
has this fix (`isEarlierController`); the companion needs it backported.

Changes:
- `internal/hyperv/types.go`: add `ControllerNumber int` and
  `ControllerLocation int` to `VMDiskEntry`.
- `internal/hyperv/diskmap.go`: update the PowerShell script to select
  `ControllerNumber` and `ControllerLocation`. Extract `newDiskMap(entries)`
  (unit-testable). `ByID` keeps the entry with lowest
  `(ControllerNumber, ControllerLocation)` per VM. Add `isEarlierController`.

**2. Multi-partition / multi-disk iteration (RC fleet data, 2026-11-20).**

Standard RHEL build: GPT, no LVM, 3 partitions (EFI, `/boot` XFS 1 GB,
`/` XFS rest of disk). `/boot` is a full filesystem — a full `/boot`
breaks kernel updates, so RC wants it reported as its own series. Mean
2.3 disks/VM across the fleet means most VMs have at least one data disk.

Changes:
- Probe all Linux-filesystem-typed GPT partitions across every VHDX in
  `ByPath` for the VM (not just the `ByID` boot disk entry).
- Emit `vm.guest.filesystem.used_percent` per partition, keyed by
  `vm.name` + `partition_guid`.
- `/boot` and `/` each get their own series. Data disk partitions included.

**Note on 2b (LVM):** RC's standard build has no LVM — every Linux VM
is GPT + plain partitions + XFS. The `/boot` misreport documented in the
earlier scope doc only occurs on LVM installs. `FindRootFilesystem` already
picks the largest partition (/) correctly on the standard build. 2b is
deferred until 2i's skip_reason counts show LVM VMs in the fleet.

---

### 2d — MBR / Gen 1 VM support
**Status: Approved | Priority: High — not a fast follow (94 VMs, 18%)**

94 of 524 VMs are Gen 1, including 13% on Jewel. All Gen 1 VMs use MBR.
None can be probed by Tier 1.6 until MBR support exists — this is the
largest remaining Linux/Windows coverage gap after 2h.

**Fix:** Add `internal/guestfs/mbr.go`. MBR partition table at sector 0
(512-byte format). For Linux guests: find type `0x83` (Linux filesystem)
partition and pass to XFS/ext4 reader. For Windows guests: enumerate
NTFS partitions (type `0x07`), skip type `0x27` (Recovery) and
type `0x0C` (FAT32/System Reserved if present).

Shared key for MBR (resolved 2026-11-20): disk signature (4-byte field
at offset 0x1B8) + partition byte offset. Both Tier 1.5
(`Get-Partition`'s `Offset`) and Tier 1.6 (from MBR partition entry) can
compute this. No GUID available on MBR disks.

---

### 2f — Checkpoint / differencing disk chain
**Status: Approved — design finalized 2026-11-20**

**Fleet data:** 7% of Jewel VMs hold a checkpoint at any given time (13%
fleet-wide; dev clusters up to 37%). Chain depth p50 1–2, p95 2–3. One
VM is at the Hyper-V maximum of 50 checkpoints. 3 VMs have unreadable
disks (broken chain or pass-through).

**Design (finalized per Chris's response, 2026-11-20):**

The VHDX reader itself must be chain-aware. For every block read (partition
table, LVM metadata, superblock, MFT record 6, $Bitmap data runs): use the
child's block if present in the differencing disk's BAT, otherwise the
parent's, with the sector bitmap for partially present blocks. Every parser
above sees correct merged data automatically.

- Follow parent links until a non-differencing disk is reached.
- Design for any depth. Test with both 3-level and 50-level chains.
- No depth limit that would skip VMs — handle the 50-checkpoint outlier.

**Two new `skip_reason` values** (replace the single `differencing`):

| `skip_reason` | Meaning | Disposition |
|---|---|---|
| `chain_modified` | Checkpoint merge in progress; chain changed mid-read | Retry next cycle — not an alert |
| `chain_corrupt` | Corrupt or missing parent link | Needs own alert — problem with the VM itself |

**No base-disk fallback.** A stale tag on every chart and detector is
unacceptable. If the chain can't be read, emit the appropriate skip_reason
and retry.

Update `internal/guestfs/skipreason.go`: replace `SkipReasonDifferencing`
with `SkipReasonChainModified` and `SkipReasonChainCorrupt`. Update
`ClassifySkipReason` accordingly.

---

### 2a — ext4 superblock parser
**Status: Approved | Priority: Low — build 2i first to size from real counts**

RC's entire Linux fleet uses RHEL-family. No Ubuntu or Debian VMs exist
in the current fleet. 2a only matters for non-standard builds, appliances,
or vendor images. Build 2i first; use `skip_reason=ext4_btrfs` counts
from the pilot to determine actual prevalence before committing.

**Fix (when prioritized):** Add `internal/guestfs/ext4.go`. ext4
superblock at byte offset 1024, magic `0xEF53`. Used block count
(`s_blocks_count - s_free_blocks_count`) / total. Update `probe.go` to
try ext4 when `ErrNotXFS` is returned.

---

### 2b — LVM Logical Volume enumeration
**Status: Approved | Priority: Low — build 2i first to size from real counts**

RC's standard RHEL build has no LVM. 2b only matters for non-standard
builds. Build 2i first; use `skip_reason=lvm` counts from the pilot to
determine actual prevalence before committing.

**Fix (when prioritized):** Add `internal/guestfs/lvm.go`. Parse LVM PV
label and MDA to enumerate LVs and their disk extents. Call XFS/ext4 parser
per LV. Add `lv_name` attribute to `vm.guest.filesystem.used_percent` for
LVM-sourced data points.

---

### 2e — 4Kn native-sector VHDX support
**Status: Not needed for current fleet | Priority: Deferred**

Read-only sweep of all 16 monitored hosts confirmed 0 of 524 VMs have a
VHDX with 4096-byte logical sectors (Chris Ojeda, 2026-11-20). No build
required for RC's current fleet.

Design notes retained for future fleets: read `PhysicalSectorSize` from
the VHDX footer (`vhdx.go`) and pass as a parameter to `ParseGPT` and
both filesystem parsers, replacing the `gptSectorSize = 512` constant in
`internal/guestfs/gpt.go` (line 18).

---

### 2g — Credential gap enforcement (Tier 1.5 / PowerShell Direct)
**Status: Not Needed — per Chris's review**

Ansible gates Tier 1.5 with `guest_probe.enabled: false` for clusters
without credentials. No code change required.

---

## Cross-repo note

`internal/guestfs` changes (2a, 2b, 2d, 2e, 2f, 2h) must be applied to
**both repos** independently:

- `hyperv-o11y-companion/internal/guestfs/` — this repo, deployed at RC
- `hyperv-otel-receiver/internal/guestfs/` — native OTel receiver

Apply to companion first (RC is in production), then port to the receiver.

`internal/hyperv/diskmap.go` changes (2c) are companion-only — the
receiver already has the `isEarlierController` fix.

---

## Build order (updated based on RC fleet data, 2026-11-20)

1. **2i (probe failure observability)** — RC asked for this first so
   real `skip_reason` counts can size 2a/2b before code is written
2. **2h (NTFS)** — 359 Windows VMs currently dark; highest new coverage
3. **2d (MBR)** — 94 VMs (18%), 13% of Jewel; not a fast follow per Chris
4. **2c diskmap fix** — small, self-contained; prerequisite for multi-partition
5. **2c multi-partition** — `/boot` + `/` + data disks; main Linux item
6. **2f (checkpoint)** — 7% of Jewel; design finalized
7. **2a (ext4)** — pending real counts from 2i pilot
8. **2b (LVM)** — pending real counts from 2i pilot
9. **2e (4Kn)** — deferred; 0/524 VMs in current fleet (confirmed 2026-11-20)
