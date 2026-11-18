# Tier 1.6 (guestfs-probe) permissions model

**Status: open question, not yet rigorously characterized.** Everything in
this document is technical analysis / a proposed validation plan, not a
result of fleet-scale testing — the real-hardware validation done so far
(see `docs/known-gaps-remediation.md` gap #4) ran `guestfs-probe` as a
Windows Service under the default **LocalSystem** account, which trivially
has read access to everything on the host regardless of NTFS ACLs. That
tells us the code works; it does not tell us the *minimum* privilege it
actually needs — which matters once this ships as a native, broadly
deployed capability instead of one bespoke customer install (per the
native-productization plan's "permissions model needs an explicit
answer" callout).

## What we know for certain

- `internal/guestfs` never calls a Hyper-V API (WMI/`Get-VM`/`Get-VMHardDiskDrive`)
  itself. It only needs (a) the VHDX file path, which `cmd/guestfs-probe`
  gets from `internal/hyperv.BuildDiskMap` (a `Get-VM | Get-VMHardDiskDrive`
  PowerShell call — a Hyper-V *management* operation), and (b) `os.Open` +
  `ReadAt` read access to that file. These are two separate privilege
  questions.
- Opening the file is a plain, read-only `os.Open` — `internal/guestfs`
  never requests write access, never calls `LockFileEx`, and Tier 1.6's
  whole pitch depends on this remaining true (see `docs/architecture.md`'s
  "zero guest footprint" framing). But *Hyper-V itself* holds the file open
  while the VM is running, and Windows file sharing is governed by whatever
  share mode Hyper-V's own worker process (`vmwp.exe`) opened it with —
  not by anything this repo controls.

## Two distinct privilege requirements

1. **Hyper-V management access** (for `BuildDiskMap`'s `Get-VM`/
   `Get-VMHardDiskDrive` call). Microsoft documents the **Hyper-V
   Administrators** local group as the supported way to grant this without
   full local Administrator rights. This part is well-trodden — Tier 1
   (`cmd/host-companion`) already depends on the same kind of Hyper-V
   query and hasn't surfaced a permissions problem in testing.

2. **NTFS read access to the VHDX file itself.** This is the actual open
   question, for two reasons:
   - **Hyper-V Administrators group membership does not, by itself, grant
     NTFS-level file access.** It grants access to Hyper-V's management
     surface (WMI/RPC to `vmms.exe`); the VHDX file's ACL is a completely
     separate, independent permission check enforced by the filesystem
     driver, not by Hyper-V.
   - **VHDX storage location is not fixed.** Default-path VMs store VHDX
     files under `C:\ProgramData\Microsoft\Windows\Hyper-V\Virtual Hard
     Disks\`, where Hyper-V provisions ACLs automatically (typically
     granting access to `SYSTEM`, `Administrators`, and the per-VM
     `NT VIRTUAL MACHINE\<VmId>` SID). Production fleets very commonly use
     **custom storage paths** instead (dedicated data volumes, Cluster
     Shared Volumes, SAN-backed mount points) — and there is no guarantee
     those paths carry the same auto-provisioned ACLs. A customer's storage
     team could have locked a CSV path down to `SYSTEM`+`Administrators`
     only, with no `Hyper-V Administrators` grant at all.

## What this means for a least-privilege service account

A `guestfs-probe` service account needs **both**:
- Membership in **Hyper-V Administrators** (for the disk-map build), and
- Explicit **read** NTFS ACL grants on every VHDX storage path in use —
  which may require a customer-side ACL change on non-default storage
  locations, not just a group membership the installer can set up on its
  own.

That second requirement doesn't shrink cleanly to "add one group" the way
Tier 1's counters access does. Realistic options, roughly in order of
operational simplicity vs. least-privilege purity:

| Option | Privilege | Trade-off |
|---|---|---|
| Run as `LocalSystem` (today's default) | Full local access | Simplest, zero customer-side ACL work, but broadest possible privilege for a probe whose whole value proposition is being a narrow, low-risk add-on |
| Run as `NT SERVICE\<servicename>` virtual account + Hyper-V Administrators + `icacls`-granted read on known VHDX paths at install time | Narrower | Installer must enumerate VHDX storage paths (via the same `Get-VM`/`Get-VMHardDiskDrive` call) and grant ACLs — works for paths known at install time, not ones added later without a re-run |
| Dedicated gMSA/domain service account, ACL'd centrally by the customer's storage team | Narrowest | Matches enterprise least-privilege expectations, but pushes real setup work onto the customer and is a support-burden increase, not a code change |

## Recommended validation plan (not yet executed)

1. Create a dedicated local service account, add it to **Hyper-V
   Administrators** only (no Administrators, no explicit VHDX ACL grants).
   Run `guestfs-probe` under it against `vm-rocky1` (the same VM used for
   Tier 1.6's real-hardware validation, see
   `docs/full-test-environment-runbook.md`) and confirm whether `os.Open`
   succeeds or fails with access-denied — this answers whether Hyper-V
   Administrators alone is sufficient for the *default* storage path.
2. Repeat with the VM's VHDX moved to a custom (non-default) path with a
   locked-down ACL (`SYSTEM`+`Administrators` only, no inherited Hyper-V
   grant) — this is the CSV/SAN-path scenario and is expected to fail,
   confirming option 2/3 above is actually required for those deployments.
3. While the VM is running (not just powered-off), confirm the exact
   Win32 error `os.Open` surfaces if Hyper-V's own share mode ever conflicts
   with a concurrent read — today's code has no retry/backoff for a sharing
   violation, and this hasn't been exercised because LocalSystem testing
   wouldn't hit any ACL wall in the first place, only a genuine
   sharing-mode conflict would surface here.
4. Record results back into this doc and turn the "recommended service
   account" row above from analysis into a documented, supported install
   option in `installer/README.md`.
