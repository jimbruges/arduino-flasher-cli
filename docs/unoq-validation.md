<!--
SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
SPDX-License-Identifier: GPL-3.0-or-later
-->

# UNO Q hardware validation — 2026-11-04

The storage round-trip passed on UNO Q serial `0x8ED963AB`: a repeated full
capture matched the original, and restoring the original followed by complete
readback produced identical SHA-256 hashes for every chunk. Normal boot then
passed: Debian reported `running`, with no failed systemd units.

The test used one physical board, macOS arm64, and the Firehose programmer from
official UNO Q image `20260528-558`. QDL was the embedded Arduino package
`v2.4-26` (the executable reports `v2.4-dirty`). Storage was eMMC LUN 0, with
512-byte sectors and 15,675,162,624 bytes (14.60 GiB), stored in fifteen chunks.

| Measurement | Elapsed |
| --- | ---: |
| Complete capture, including checksums | 2m 44s |
| Write phase, including programmer setup | approximately 14m 28s |
| Readback transfer and device reset | approximately 1m 49s |
| Restore including image validation, preflight and readback checksums | 16m 31s |
| Capture + verified restore | 19m 15s |
| Normal boot (systemd measurement) | 30.403s |
| Restore with `--skip-unchanged`, including full verification | 4m 43s |

The approximate phase split uses the QDL invocation timestamps and the creation
of its first readback file. Whole-command timings use a monotonic host clock.
Capture averaged 91.15 MiB/s. End-to-end restore averaged 15.09 MiB/s of image
content, including both the write and verification; aggregate read-plus-write
traffic was 30.17 MiB/s. These measurements describe this board and connection,
not guaranteed performance for other capacities or concurrent batches.

The optimized restore took 283.104s, 3.50× faster than the 990.914s full restore.
It wrote 176,160,768 bytes (168 MiB) in 59 coalesced regions and skipped
15,499,001,856 bytes (98.88%). The target was the same board after the full
restore, normal boot, and return to EDL, so its contents already closely matched
the golden image. Factory boards may differ substantially and may not benefit.
The pre-read QDL invocation took approximately 145.451s; write plus final readback
took approximately 121.665s. Every final readback chunk matched the original.
The board remained in EDL afterward; no additional boot test followed this pass.

Additional checks passed:

- Both GPT header CRCs and partition-array CRCs; primary and backup GPT agree.
- All 69 populated GPT partitions are non-overlapping and within storage bounds.
- Repeated capture matches the initial golden image exactly.
- Full hardware restore through `golden flash --restore-source --yes`, followed
  by mandatory readback of every byte and checksum comparison.
- Repository tests with the race detector, real QDL parser dry-run compatibility,
  and `go vet`.

The captured ext4 filesystems indicated journal recovery was needed. After
removing the EDL jumper and power cycling, root filesystem recovery completed,
and both root (`/dev/mmcblk0p68`) and user data (`/dev/mmcblk0p69`, mounted at
`/home/arduino`) were mounted read/write. ADB serial `2396611499` matches EDL
serial `0x8ED963AB`.

Debian 13.2 booted with kernel `6.16.7-g0dd6551ae96b`. Arduino App CLI, Arduino
Router, and its serial proxy were active. App CLI and Router had no restarts;
the serial proxy had one startup restart and remained active. Systemd reported
no failed units. Boot time was 6.317s firmware + 1.446s loader + 6.218s kernel +
16.420s userspace = 30.403s; graphical.target was reached 13.538s into userspace.

The board's wall clock was not NTP-synchronized. Host monotonic timings and
systemd boot-duration measurements do not depend on that clock. Network/cloud
connectivity and application-specific behavior were not tested.

A later [user-reported three-board hub run](unoq-three-board-validation.md)
completed with three verified targets in 36m 08s. Concurrency limits, target
isolation, timeout handling, and failure isolation also passed simulated-device
tests. Post-flash boot was not reported for that batch.
This validation covers application-processor storage, not the MCU's internal flash.

Local artifacts are under `build/unoq-validation/`: `timings.json`, capture and
restore logs, `layout.json`, `boot.json`, `optimized-timings.json`,
`optimized-restore.log`, and the repeated capture. The original golden image
is retained separately in `build/golden-unoq-8ed963ab/`. These large/sensitive
artifacts are excluded from Git.
