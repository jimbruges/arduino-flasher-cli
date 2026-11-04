# Scaling golden flashing to 16 or more UNO Q boards

## Measured bottleneck

The three-board full batch took 36m 08s; the partial-write batch took 34m 29s,
despite reducing write volume by 82.68%. The latter read every target before
comparison and reread every target that needed writes. Shared pre-read throughput
was approximately 41.6 MiB/s. At that same aggregate rate, a single full read of
16 boards would take approximately 96 minutes. This extrapolates transfer volume;
it is not a measured 16-board result or a prediction of write throughput.

A single hub shares its upstream bandwidth. More workers cannot create bandwidth.
Use independent USB host-controller paths where available, and benchmark jobs 1,
2 and 4 per path. Separate hubs on the same controller can still share bandwidth.
Golden jobs now default to sixteen, allowing sixteen connected targets to run
concurrently. Use --jobs to tune concurrency for the USB fixture. Verify negotiated transfer speed during Firehose,
since initial EDL enumeration alone does not establish transfer speed.

## Download size versus USB writes

Inspected release: arduino-unoq-debian-image-20260528-558.tar.zst.
Archive size: 2,278,254,042 bytes (2.28 GB). Regular extracted files total
8,813,159,677 bytes (8.81 GB). All program entries specify sparse=false.

QDL v2.4 firehose_issue_program derives the transmitted sector count from the
file length, rounded up to a sector, capped by a nonzero XML sector count.
It does not automatically write padding all the way to the partition boundary.
The inspected default XML therefore sends approximately 8,821,022,720 bytes
(8.82 GB / 8.22 GiB), accounting for duplicated payloads and sector padding.
The root payload is about 7.55 GB, although its XML partition spans 10.71 GB.
CLI resizing or another release can change these numbers. This is source/archive
analysis, not a timed official flash on a board.

A golden full flash writes all 15,675,162,624 bytes (15.68 GB / 14.60 GiB).
The golden path covers every captured sector, including unused filesystem space
and unallocated regions. Compressing the host bundle saves disk/network space,
but does not reduce raw USB writes. Official partition flashing leaves omitted
regions untouched and cannot guarantee every raw byte matches a golden board.

## Read-only storage digest prototype

The official programmer implements getsha256digest. An isolated QDL v2.4 research
build requests SHA-256 of storage ranges and captures the returned digest, requiring
both a valid 64-digit digest and ACK. No program/erase requests are issued.
The production embedded QDL and golden verification behavior are unchanged.

On 0x8ED963AB, digests of the first 1 MiB, 64 MiB and 1 GiB match the golden
image. The combined 64 MiB + 1 GiB invocation took 10.67 seconds, including startup.
Complete 15,675,162,624-byte checks then ran concurrently on 0x8ED963AB and
0xC2B2F977. All 15 range digests on each target matched the golden manifest.
Elapsed times were 125.91s and 125.32s, including programmer startup: approximately
119 MiB/s per board. Both completed together, suggesting the USB hub did not
limit this two-board digest test. Sixteen-board behavior remains untested.
The prototype and measurement files are under ignored build/hash-research/.

## Recommended implementation direction

Precompute golden hashes once. Hash large target ranges locally; skip matches.
Subdivide mismatched ranges before writing, balancing command overhead against
write amplification. Finally hash the complete target again and compare every
range. This preserves complete-image verification while replacing large USB reads
with small digest responses. Measure target hashing throughput and command latency
before choosing block sizes. Reject missing/malformed digests, NAKs and timeouts;
retain full readback fallback for unsupported programmers.

Older factory releases can differ across most filesystem metadata and data.
Avoid assuming an older board shares a baseline; target hashes must establish
which bytes match. Sparse zero skipping is safe only after establishing the target
already contains the required bytes, or using a validated operation that produces
them. A separate partition/filesystem provisioning mode could omit free space,
but has different semantics from an exact raw clone.

## Integrated implementation validation

The CLI now implements explicit digest verification and hierarchical partial-write
comparison; see [usage and helper setup](golden-images.md#storage-digest-verification).
Readback remains the default. The native helper is a reproducible pinned-source
build rather than a replacement for embedded QDL on every supported platform.

On 2026-11-04, the integrated CLI ran two UNO Q targets concurrently with
--skip-unchanged --verification digest. Both matched the complete golden image:
0x8ED963AB finished in 131.850s and 0xC2B2F977 in 131.666s. The batch took 2m23s,
including source checks and shared block-hash preparation. Each skipped all
15,675,162,624 bytes, with no writes or USB storage readback beyond geometry probes.
Storage digest comparison itself took approximately 129s per target.

The embedded QDL then rewrote two 1 MiB ranges on 0x8ED963AB with identical golden
bytes, including a range with a nonzero source file offset. Subsequent helper
digests matched the source for both ranges. These checks exercised actual writes
and storage digest verification without changing target contents. Actual changed-
range selection/coalescing and complete final verification after partial/full writes
are exercised by byte-addressed device tests, including corruption outside writes.
A factory-board partial-write batch and 16-board throughput remain to be measured.

The full Go race suite and vet passed. Native parser tests reject malformed,
duplicate and missing digests and preserve ACK/NAK handling. Platform/board limits
are documented: the helper is built locally on macOS/Linux; VENTUNO Q storage
digest hardware support has not yet been established. Golden cloning covers AP
storage, not MCU firmware.

References: [QDL v2.4](https://github.com/linux-msm/qdl/tree/v2.4),
[Firehose digest command](https://github.com/bkerler/edl/blob/master/edlclient/Library/firehose.py).
