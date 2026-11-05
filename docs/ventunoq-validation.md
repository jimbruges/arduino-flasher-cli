<!--
SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
SPDX-License-Identifier: GPL-3.0-or-later
-->

# VENTUNO Q capture and restore validation — 2026-11-04–05

Capture passed on VENTUNO Q serial `0x80796883`, using macOS arm64 and the
Firehose programmer from the official Ubuntu image `x13-20260729-248`.
The board's installed OS was not inspected; the image release identifies the
programmer used, not the captured OS. The output is `./golden-ventunoq`.

```sh
./build/arduino-flasher-cli golden capture ./golden-ventunoq \
  --board ventunoq --serial 0x80796883 \
  --programmer ./build/official-images/ventunoq-layout/arduino-ubuntu-monza-20260729-248/flash/prog_firehose_ddr.elf
```

The default eMMC capture selected GPT-backed LUNs 0 and 1, both with 512-byte
sectors. The bundle covers 63,685,787,648 bytes (59.31 GiB) in 61 chunks.

| Storage | Sectors | Bytes | GPT partitions |
| --- | ---: | ---: | ---: |
| LUN 0 | 124,321,792 | 63,652,757,504 | 71 |
| LUN 1 | 64,512 | 33,030,144 | 1 |

| Measurement | Elapsed | Outcome |
| --- | ---: | --- |
| Complete capture command | **28m53s** (1732.639862s) | Success |
| USB storage read | 28m26s | 35.6 MiB/s average |
| Capture checksum phase | 24s | All chunks hashed and manifest saved |
| Independent bundle validation | 23.761s | All source checksums and sizes matched |
| Two read-only storage digest requests | 2.909s | Both matched saved bytes |

The board remained enumerated at USB 2 speed during the actual capture
(`Device Speed=2`, `USBSpeed=3` in macOS IORegistry). This is a USB 2 benchmark;
it does not establish capture performance on a USB 3 connection. Capture used
the embedded Arduino QDL package; sample storage digests used the locally built
QDL v2.4 storage-digest helper.

Independent validation reloaded the complete bundle using a dry-run source
restore plan. The dry-run did not access USB or write any storage. A separate
layout checker verified primary and backup GPT header CRCs and partition-array
CRCs on both LUNs, matching primary/backup arrays, partition bounds and absence
of overlap. All checks passed.

Read-only `getsha256digest` commands then checked the first 64 MiB of LUN 0 and
the entire 31.5 MiB of LUN 1. Both target digests matched hashes of the captured
bytes. This establishes storage-digest command support on this programmer and
both selected LUNs. The 2.909s includes startup and is not a full-device digest
throughput measurement.

The capture and sample digest tests did not write target storage. A later
partial restore and complete verification passed as recorded below.
MCU flashing, post-restore boot and 16-board throughput remain untested.
Capture and sample digest commands left the source in EDL mode.

The initial attempt found no connected USB/EDL device and did not start a
capture. That exposed an empty-inventory discovery bug: macOS ioreg emits no
plist when no USB host devices are present. The CLI now returns an empty device
list instead of EOF; its regression test with the race detector passed.

Restore attempt on 5 November 2026: the box-fresh target `0x868C69F1` was
discovered, then disappeared from USB before QDL opened it for the geometry
probe. Source checksum validation and block digest preparation each completed
in 24s. The waiting probe was stopped after 58.4s; its diagnostic was
`Waiting for EDL device`. No target storage writes or verification occurred.
The failed attempt is retained in `build/ventunoq-restore-attempt1-results.json`
and `build/ventunoq-restore-attempt1-progress.log`.

After reconnecting, the same target passed this restore on 5 November 2026:

```sh
./build/arduino-flasher-cli golden flash ./golden-ventunoq \
  --board ventunoq --serial 0x868C69F1 --skip-unchanged \
  --verification digest --digest-qdl ./build/qdl-digest \
  --digest-block-mib 64 --timeout 3h --yes
```

| Measurement | Elapsed | Outcome |
| --- | ---: | --- |
| Complete restore command | **45m46s** | One verified, zero failed |
| Target processing | **44m59s** (2699.362268s) | Geometry probe, comparison, writes and verification passed |
| Source checksum validation | 24s | Complete bundle matched its manifest |
| Source block digest preparation | 24s | 64 MiB source hashes prepared |
| Coarse target digest comparison | 6m14s | 59.31 GiB checked at 162.3 MiB/s |
| Refine differing ranges | 7m54s | 59.28 GiB checked at 128.0 MiB/s |
| Write differing blocks | **25m48s** | 50.59 GiB, 105 chunks; 33.5 MiB/s average |
| Complete final target digest verification | **5m0s** | All 59.31 GiB matched; 202.8 MiB/s |

The restore wrote 54,324,625,408 bytes and skipped 9,361,162,240 bytes (14.7%).
Both selected LUNs were verified, including all skipped regions. The source
board `0x80796883` was not targeted. macOS reported `Device Speed=2` and
`USBSpeed=3` during writing, establishing another USB 2 benchmark. No boot check
was performed; storage verification does not establish successful boot or MCU
firmware compatibility. The restore left the target in EDL, ready for physical jumper
removal and a power-cycle.

This factory target shared only 14.7% of its captured storage at the configured
block size. Comparison and refinement added 14m08s before writing; this run alone
does not establish whether skip mode beats a full write on factory VENTUNO Qs.

Read-only analysis of the captured GPT and ext4 superblock found a 55.88 GiB
`system` partition with about 47.31 GiB allocated and 8.56 GiB free, including
filesystem metadata in the allocated count. These are captured filesystem
counters, not a live boot/`df` check. The golden image therefore contains a large
allocated filesystem, rather than mostly empty disk capacity. A deterministic
sample of the first 1 MiB in each 64 MiB interval found 143 entirely zero samples
and 807 mixed samples out of 950; this is not a complete zero-range inventory.
Sample results are retained in `build/ventunoq-source-sample-analysis.json`.

## Full-write digest benchmark

On the same target and USB connection, omitting `--skip-unchanged` passed:

```sh
./build/arduino-flasher-cli golden flash ./golden-ventunoq \
  --board ventunoq --serial 0x868C69F1 \
  --verification digest --digest-qdl ./build/qdl-digest \
  --timeout 3h --yes
```

| Measurement | Elapsed | Outcome |
| --- | ---: | --- |
| Complete command | **36m47s** | One verified, zero failed |
| Target processing | **36m24s** (2184.100154s) | All 63,685,787,648 bytes written and verified |
| Write | **31m31s** | 59.31 GiB in 61 chunks; 32.1 MiB/s |
| Final complete storage digests | **4m48s** | 211.1 MiB/s; every range matched |

This was 8m59s (about 20%) less than the preceding 45m46s skip-mode restore.
It used the same board, image, programmer, and USB hub path; the target already
contained the golden image, so it is a repeat-write benchmark rather than an
independent factory-board comparison. No boot or MCU check was performed.
Results are in `build/ventunoq-full-digest-results.json` and
`build/ventunoq-full-digest-progress.log`.

The single-board result must not be treated as a multi-board recommendation.
If 16 factory targets share one USB 2 hub uplink and have the same matching
ranges as the tested board, full writes total 948.992 GiB and partial writes
809.5 GiB, avoiding 139.492 GiB. At an assumed aggregate 32 MiB/s, that represents
74.4 minutes of write time. This is an illustrative calculation, not measured
batch throughput. Target comparisons can run concurrently; avoiding shared-bus
transfers may outweigh their cost. For that topology, benchmark single-pass block
comparison first, alongside full-write digest mode. Neither strategy has yet been
timed on a factory VENTUNO Q batch; different factory releases may skip different
amounts. Independent USB uplinks can change the tradeoff.

## USB transport checks

Native libusb inspection independently confirmed USB 2 High Speed (`speed=3`
in libusb's enumeration), `bcdUSB=0210`, and two 512-byte bulk endpoints. macOS
showed the path `AppleT8142USBXHCI → USB2.1 Hub → USB2.1 Hub → QUSB_BULK`.
The descriptor check did not claim the USB interface or access target storage.
Evidence is saved in `build/ventunoq-usb-test/hub-descriptors.log` and
`build/ventunoq-usb-topology.json`. USB 2 hub nodes describe the active path;
they do not prove the physical hubs lack USB 3 companion ports.

A subsequent read-only helper run checked a 1 MiB digest and read the first
64 MiB of LUN 0; both matched saved source bytes. The combined invocation took
3.904s including startup. QDL reported the read at 32,768 KiB/s. The loader's
configuration ACK advertised both accepted and supported maximum payloads as
1,048,576 bytes, matching QDL's existing 1 MiB default. Increasing the host's
output chunk setting beyond that would not increase the negotiated payload.
Logs/results are in `build/ventunoq-usb-test/hub-probe.log` and
`build/ventunoq-usb-test/hub-probe-results.json`.

The first direct-cable reconnect produced no USB/EDL or ADB device, including
a repeat check after 15s. No storage probe ran during that attempt.

After a further reconnect/power-cycle, the direct test passed. macOS showed
`AppleT8142USBXHCI → QUSB_BULK` on a different controller (location 0x01000000),
with no intervening hub. Native libusb reported USB 2 High Speed, `bcdUSB=0210`
and 512-byte bulk endpoints both before and after Firehose loaded. The cable was
reported as USB 3 capable by the user; its capability was not separately tested
with a known SuperSpeed device. These results establish the active link speed
for this configuration, not a universal limitation of VENTUNO Q hardware.

| Read-only transport check | Hub path | Direct cable |
| --- | ---: | ---: |
| 1 MiB target digest | Matched | Matched |
| 64 MiB target read | Matched | Matched |
| Combined invocation, including startup | 3.904s | 4.001s |
| QDL reported read rate | 32,768 KiB/s | 32,768 KiB/s |
| Accepted and supported maximum payload | 1 MiB | 1 MiB |
| USB speed before/after Firehose | USB 2 High Speed | USB 2 High Speed |

The small-read rate is rounded by QDL and is not a sustained full-image benchmark.
Removing the hubs did not demonstrate a USB 3 link or a transfer improvement in
this test. The native client's existing 1 MiB payload already matches the loader's
reported maximum; there is no USB 3 selection switch in the current QDL CLI or
Firehose configuration. No storage writes were made during either transport
check. USB 3 EDL operation remains unproven and would need a confirmed SuperSpeed
cable/path and, if it still negotiates High Speed, investigation of the supplied
programmer and board's EDL USB implementation.

Direct evidence is retained in `build/ventunoq-usb-test/direct-topology.json`,
`direct-descriptors-before.log`, `direct-descriptors-after.log`,
`direct-probe.log`, and `direct-probe-results.json` in that directory. The earlier
empty topology snapshot was replaced by the successful connection's topology.

## Single-pass comparison implementation

`--skip-unchanged --verification digest --digest-comparison blocks` directly
compares all selected storage at `--digest-block-mib` granularity, without the
initial coarse pass. It uses the existing changed-block coalescing and complete
final verification. The default `hierarchical` strategy remains available for
identical targets or differences confined to a few source chunks.

Tests passed for both strategies: identical images, partial and coalesced writes,
corruption outside the written regions, invalid/missing/NAK/cancelled digest
responses, and concurrent multi-LUN targets with 512- and 4096-byte sectors.
Invalid comparison settings are rejected before USB access. The full Go race
suite and `go vet ./...` passed. The rebuilt CLI includes the new flag.
Full test output is retained in
`build/ventunoq-speed-go-tests.log`.

A hardware run on the same already-matching VENTUNO Q and USB 2 path passed:

```sh
./build/arduino-flasher-cli golden flash ./golden-ventunoq \
  --board ventunoq --serial 0x868C69F1 --skip-unchanged \
  --verification digest --digest-qdl ./build/qdl-digest \
  --digest-comparison blocks --digest-block-mib 64 --timeout 3h --yes
```

The complete command took **7m18s**, including shared source checks and block
hash preparation. Target processing took **6m31s** (391.082467s); the direct
comparison checked all 59.31 GiB in **6m28s** at **156.4 MiB/s**. All blocks
matched, so no writes, refinement, or second verification pass were needed.
Logs/results are retained in `build/ventunoq-block-comparison-progress.log` and
`build/ventunoq-block-comparison-results.json`.

This is matching-target validation, not a factory partial-write benchmark. It
supports avoiding an additional coarse pass when differences are widely spread,
but direct blocks are slower than coarse hashes for an identical image: the
preceding full restore's coarse final verification took 4m48s. Partial-write
correctness and full final coverage were exercised in simulated device tests.

Local artifacts, excluded from commits:

- `build/ventunoq-capture-progress.log` and `build/ventunoq-capture-results.json`.
- `build/ventunoq-validation/results.json`, layout checks, independent bundle
  validation logs/timing, and storage digest sample XML/results/timing.
- `build/ventunoq-restore-results.json`, `build/ventunoq-restore-progress.log`,
  `build/ventunoq-restore-usb.json`, and the first attempt's results/progress.
- `build/ventunoq-full-digest-{results.json,progress.log}` and
  `build/ventunoq-block-comparison-{results.json,progress.log}`.
- `build/ventunoq-usb-test/`, containing topology, descriptor and read-only
  probe results for both connections; `build/ventunoq-usb-topology.json`.
- `build/ventunoq-source-sample-analysis.json` and
  `build/ventunoq-speed-go-tests.log`.
- `golden-ventunoq/`, including the manifest, programmer and raw image chunks.
