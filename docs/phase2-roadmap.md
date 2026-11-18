# Phase 2 Roadmap

Tracks the guest filesystem coverage gaps confirmed by Royal Caribbean
fleet testing (September–November 2026). Approval status is from Chris
Ojeda's written review (November 17, 2026 PDF + Webex follow-up),
including his three technical corrections to the original scope document.

Items 2a–2i map directly to `hyperv-o11y-companion`'s code paths. Where
the fix touches `internal/guestfs`, it must also be applied to
`hyperv-otel-receiver/internal/guestfs` — the two repos maintain
separate copies of that package (import paths were rewritten when the
receiver was extracted; they are not a shared Go module dependency).

---

## Status summary

| # | Fix | Status | Companion files touched |
|---|---|---|---|
| 2a | ext4 superblock parser | **Approved** | `internal/guestfs/ext4.go` (new), `internal/guestfs/probe.go` |
| 2b | LVM Logical Volume enumeration | **Approved** | `internal/guestfs/lvm.go` (new), `internal/guestfs/probe.go` |
| 2c | Multi-partition / multi-disk iteration + diskmap fix | **Approved** | `internal/hyperv/types.go`, `internal/hyperv/diskmap.go`, `cmd/guestfs-probe/main.go` |
| 2d | MBR / Gen 1 VM support | **Approved** | `internal/guestfs/mbr.go` (new), `internal/guestfs/probe.go` |
| 2e | 4Kn native-sector VHDX support | **Approved** | `internal/guestfs/gpt.go`, `internal/guestfs/vhdx.go` |
| 2f | Checkpoint / differencing disk chain | **Needs Discussion** | `internal/guestfs/vhdx.go` — blocked on clarification |
| 2g | Credential gap enforcement (Tier 1.5) | **Not Needed** | None — Ansible handles via `guest_probe.enabled: false` |
| 2h | NTFS $Bitmap parser (Windows guest filesystem %) | **Approved** | `internal/guestfs/ntfs.go` (new), `internal/guestfs/probe.go` |
| 2i | Probe failure observability | **Approved** | `cmd/guestfs-probe/main.go`, `cmd/host-companion/main.go` |

---

## Linux guest filesystem coverage

### 2a — ext4 superblock parser
**Status: Approved**

XFS is RHEL/Rocky's default root filesystem. Ubuntu and Debian guests
use ext4. All ext4/btrfs VHDXs currently return
`skip_reason=ext4_btrfs` in `internal/guestfs/skipreason.go`.

**Fix:** Add `internal/guestfs/ext4.go`. The ext4 superblock sits at
byte offset 1024 with magic `0xEF53`. Used block count
(`s_blocks_count - s_free_blocks_count`) divided by total block count
gives used %. Works for standalone ext4 partitions and ext4 LVs on LVM
(falls out from 2b's LV iterator calling into this parser).

Update `internal/guestfs/probe.go`'s `FilesystemUsedPercent` to try
ext4 when `ErrNotXFS` is returned. Remove `ErrNotXFS` from producing
`skip_reason=ext4_btrfs` once the ext4 path is wired in.

---

### 2b — LVM Logical Volume enumeration
**Status: Approved**

RHEL and Rocky Linux default installs use LVM. The LVM partition type
is detected and returned as `ErrLVM` → `skip_reason=lvm`.

**Fix:** Add `internal/guestfs/lvm.go`. Parse the LVM Physical Volume
(PV) label and Metadata Area (MDA) from the VHDX to enumerate Logical
Volumes and their disk extent layout. For each LV, call the XFS or ext4
parser (from 2a) on its first extent. Emit
`vm.guest.filesystem.used_percent` tagged with `lv_name`
(`lv_root`, `lv_home`, `lv_var`, etc.) so each volume is independently
alertable.

**Chris's correction (Webex, 2026-11-17):** RHEL's `/boot` partition is
a plain XFS partition outside LVM on a standard Anaconda install — it
sits in a Linux-filesystem-typed GPT entry ahead of the LVM PV entry.
`cmd/guestfs-probe` currently reports `/boot`'s used % (not root's) for
RHEL VMs because `FindRootFilesystem` picks the first Linux-typed
partition, and `/boot` comes first in the GPT. The LVM fix naturally
surfaces root (via LV enumeration) alongside `/boot` (via the existing
XFS path), producing two separate `vm.guest.filesystem.used_percent`
series tagged `lv_name=lv_root` and `lv_name=boot` — which is the
correct behavior.

---

### 2c — Multi-partition / multi-disk iteration + diskmap `ByID` fix
**Status: Approved**

Two separate changes required:

**1. `internal/hyperv/diskmap.go` — backport the receiver's `ByID` fix.**

The current `BuildDiskMap` loop does a plain map overwrite for `ByID`
(last-write-wins). For multi-disk VMs, this means `ByID` holds whichever
disk `Get-VMHardDiskDrive` returns last — not necessarily the boot disk.
`cmd/guestfs-probe` iterates `ByID` to find the VHDX to probe per VM,
so multi-disk VMs are probed against the wrong disk.

This exact bug was found and fixed in `hyperv-otel-receiver` during
hardware validation on `vm-rocky1` (OS disk at controller 0:0 +
kickstart remnant at controller 0:2 — `ByID` was keeping the kickstart
disk, causing `skip_reason=no_linux_partition`). The companion needs the
same fix.

Changes:
- `internal/hyperv/types.go`: add `ControllerNumber int` and
  `ControllerLocation int` to `VMDiskEntry`.
- `internal/hyperv/diskmap.go`: update the PowerShell script to select
  `ControllerNumber` and `ControllerLocation` from
  `Get-VMHardDiskDrive`. Extract a pure `newDiskMap(entries)` function
  (unit-testable without PowerShell). `ByID` keeps the entry with the
  lowest `(ControllerNumber, ControllerLocation)` per VM — conventional
  boot disk position — instead of last-write-wins. Add
  `isEarlierController(a, b VMDiskEntry) bool` helper.

**2. `internal/guestfs` + `cmd/guestfs-probe/main.go` — iterate all disks.**

v1 probes only one VHDX per VM (via `ByID`). Data disks and additional
mount points (`/var`, `/home`, `/data`) are not inventoried.

After the `ByID` fix above, extend `cmd/guestfs-probe` to iterate all
Linux-filesystem-typed partitions across every entry in `ByPath` that
belongs to the VM (not just the `ByID` entry). Each partition/LV emits
its own `vm.guest.filesystem.used_percent` series. This falls out
naturally from the LVM work in 2b.

---

### 2d — MBR / Gen 1 VM support
**Status: Approved**

GPT parser returns `ErrNotGPT` for MBR partition tables →
`skip_reason=mbr`. Gen 1 Hyper-V VMs use MBR by definition.

**Fix:** Add `internal/guestfs/mbr.go`. The MBR partition table is at
sector 0 (well-documented 512-byte format). Find the active Linux
partition (type `0x83`) and pass to the existing XFS/ext4 reader. No
changes to XFS or ext4 parsers required.

**Pre-build:** RC to confirm how many Gen 1 VMs are in the active fleet.
The `skip_reason=mbr` count from a pilot run gives the exact number.

---

### 2e — 4Kn native-sector VHDX support
**Status: Approved**

All byte-offset calculations in `internal/guestfs/gpt.go` use
`gptSectorSize = 512` (line 18 — `gpt.go` has this constant hardcoded
with a comment: "v1 assumes 512-byte logical sectors; 4Kn disks are not
handled"). VHDXs on 4Kn storage silently produce wrong offsets, falling
through to `skip_reason=other`.

**Chris's correction (PDF review, 2026-11-17):** The physical sector
size for 4Kn detection must be read from the **VHDX footer's
`PhysicalSectorSize` field**, not from the GPT or filesystem superblock.

**Fix:** Read `PhysicalSectorSize` from the VHDX footer in
`internal/guestfs/vhdx.go` and pass it as a parameter to
`ParseGPT` in `gpt.go`. Change `gptSectorSize` from a package-level
constant to a parameter. All offset calculations already use the
value internally — no parser logic changes beyond replacing the constant
with the parameter.

**Pre-build:** RC to confirm how many VHDXs on the active fleet are on
4Kn storage.

---

### 2f — Checkpoint / differencing disk chain support
**Status: Needs Discussion — blocked on three open questions sent to Chris**

Any VM with an active Hyper-V checkpoint has a differencing disk (.avhdx)
as its current virtual disk. `internal/guestfs/vhdx.go` returns
`ErrDifferencingDisk` → `skip_reason=differencing` for any VM in this
state (line 181).

**Original v1 proposal:** Read the base `.vhdx` (disk state at checkpoint
time, slightly stale but far better than no data).

**RC position (Chris's review, 2026-11-17):** "Needs Discussion" —
RC wants live-chain read (the full merged differencing + parent disk
state), not just the base. This is significantly more complex: requires
parsing the AVHDX chain's Block Allocation Table (BAT) and merging reads
across the differencing and parent disks in-memory.

**Three open questions sent to Chris (Webex, 2026-11-17), awaiting reply:**

1. **Chain depth:** What is the maximum active checkpoint chain depth
   in RC's production fleet? A depth limit must be enforced to prevent
   unbounded memory/CPU on pathological chains.
2. **Staleness fallback:** If live-chain read is not feasible within
   Phase 2 scope, is base-disk read acceptable as an interim with an
   explicit `source=checkpoint_base` dimension so operators know the
   value is stale?
3. **Fleet frequency:** How many VMs typically hold active checkpoints?
   `skip_reason=differencing` from a pilot run gives the exact count.

**Do not begin implementation until Chris responds.** The scope
difference between base-disk read (parsing one static VHDX) and
live-chain read (BAT merge across a chain) is substantial and affects
test surface as well.

---

## Windows guest filesystem coverage

### 2g — Credential gap enforcement (Tier 1.5 / PowerShell Direct)
**Status: Not Needed — per Chris's review**

The original proposal called for emitting a
`hyperv.tier15.credential_missing` gauge when group_vars credentials are
absent for a cluster, so the gap is a visible metric rather than a
silent blind spot.

**Chris's decision:** Not needed. Ansible already gates Tier 1.5 with
`guest_probe.enabled: false` for clusters without credentials —
operators who haven't configured credentials will never have the probe
enabled, so the silent blind spot described in the proposal doesn't
actually occur in RC's deployment model. No code change required.

---

### 2h — NTFS $Bitmap parser (Windows guest filesystem %)
**Status: Approved**

Tier 1.6 v1 only reads XFS. Windows guest VHDXs use NTFS. Any Windows
VM currently returns `skip_reason=no_linux_partition`. Windows filesystem
% depends entirely on Tier 1.5's PowerShell Direct path, which requires
credentials and cannot reach Windows Server 2008/2012/2012 R2 guests.

**Fix:** Add `internal/guestfs/ntfs.go`. Two structures required:

1. **NTFS Boot Sector** (sector 0 of the NTFS volume): `Bytes Per Cluster`
   and `Total Clusters` give total volume size.
2. **$Bitmap metafile** (MFT record 6): one bit per cluster; counting
   set bits gives used cluster count.

**Chris's correction (PDF review, 2026-11-17):** The `$Bitmap` file
location must be found via **MFT record 6** — the `$Bitmap` system
metafile always occupies this fixed position in the MFT. The MFT itself
is located via `MFT Start LCN` in the boot sector. Full lookup chain:
boot sector → `MFT Start LCN` → MFT record 6 → `$Bitmap` data runs →
count set bits. No full MFT traversal required.

**Coverage:** All Windows versions (2008, 2012 R2, 2016, 2019, 2022)
directly from the host — no PowerShell Direct, no credentials needed.

**Open question (pending Chris's reply, Webex 2026-11-17):** When both
Tier 1.5 (`internal/guestprobe`, via `cmd/host-companion`) and Tier 1.6
(`cmd/guestfs-probe`) are enabled on the same host for a Windows guest,
both will emit `vm.guest.filesystem.used_percent` for the same VM.

Proposed approach: add a `source` dimension (`vhdx` vs.
`powershell_direct`) to distinguish the two. Operators can then:
- Use either series independently in dashboards/detectors, or
- Alert on the `vhdx` source as the always-available baseline (no
  credentials needed) and use `powershell_direct` for the per-volume
  breakdown when it's available.

Confirm this approach with Chris before wiring the dimension in — if RC
prefers muting one source when the other is available, the detector
design changes.

---

## Operational visibility

### 2i — Probe failure observability
**Status: Approved**

When a VM probe fails in `cmd/guestfs-probe`, the service logs the
error and `vm.guest.filesystem.used_percent` simply stops emitting for
that VM. Operators see a blank chart with no signal about why.
`cmd/host-companion`'s Tier 1.5 probe (`internal/guestprobe`) has the
same silent-failure behavior.

**Fix:** Emit two new metrics on failure, in both services:

| Metric | Type | Attributes | Purpose |
|---|---|---|---|
| `hyperv.guest_probe.failure` | Counter (monotonic, delta) | `vm.name`, `reason` | Incremented on each failed probe attempt. `reason`: `powered_off`, `credential_missing`, `integration_broken`, `other` |
| `hyperv.guest_probe.stale_seconds` | Gauge | `vm.name` | Seconds since the last successful probe for this VM. |

Add a Splunk detector that fires when `stale_seconds` exceeds a
configurable threshold (e.g. 15 minutes).

**`cmd/guestfs-probe/main.go`:** Add a `lastSuccess map[string]time.Time`
protected by a mutex (or by the existing single-goroutine probe loop).
Update `probeOneVM` to record success time on success and emit
`stale_seconds` + `failure` on failure. Wire both metrics through the
existing `exp.Meter` OTel SDK instance (same pattern as the existing
`vm.guestfs.probe.skip` counter on line 88).

**`cmd/host-companion/main.go`:** Same pattern for the Tier 1.5 probe
loop — the existing per-VM error log on line 252
(`log.Printf("guest probe: vm=%s: %v", ...)`) is the right hook point.
Add stale-seconds tracking and emit via the host-companion's existing
OTel SDK instance.

---

## Cross-repo note

`internal/guestfs` changes (2a, 2b, 2d, 2e, 2f, 2h) must be applied to
**both repos** independently:

- `hyperv-o11y-companion/internal/guestfs/` — this repo, deployed at RC
- `hyperv-otel-receiver/internal/guestfs/` — the native OTel receiver
  (long-term productization path; keeps parity with the companion)

The two copies are not linked by a Go module dependency — they diverged
when the receiver was extracted and import paths were rewritten. Apply
changes to the companion first (RC is running this in production), then
port to the receiver.

`internal/hyperv/diskmap.go` changes (2c) are companion-only — the
receiver already has the `ControllerNumber`/`ControllerLocation` +
`isEarlierController` fix applied.

---

## Build order (suggested, based on Chris's review)

1. **2h (NTFS)** — highest impact; unblocks all Windows guest coverage
   without credentials or Tier 1.5
2. **2a (ext4)** — unblocks Ubuntu/Debian guests
3. **2c diskmap fix** — small, self-contained, unblocks correct
   boot-disk selection for all multi-disk VMs; prerequisite for 2b/2c
   multi-partition work
4. **2b + 2c multi-partition** — unblocks RHEL/Rocky at scale
5. **2i (probe failure observability)** — can build in parallel with any
   of the above
6. **2d (MBR)** — after fleet count confirmed with RC
7. **2e (4Kn)** — after fleet count confirmed with RC
8. **2f (checkpoint)** — after Chris responds to the three open questions
