<!--
SPDX-FileCopyrightText: Arduino s.r.l. and/or its affiliated companies
SPDX-License-Identifier: GPL-3.0-or-later
-->

# UNO Q: user-reported three-board hub run

The supplied CLI transcript reports **three verified targets, zero failures**
using `golden flash ./golden-unoq --board unoq --all --yes`. Each target received
15,675,162,624 bytes followed by complete readback and checksum verification.
This was a full restore, without `--skip-unchanged`. The source bundle's serial
was `0x9CAA860C`. No post-flash boot or MCU validation was reported for this batch.

| Target | Write | Readback | Checksums | Worker total |
| --- | ---: | ---: | ---: | ---: |
| `0x8ED963AB` | 15m 07s | 15m 52s | 6s | 31m 08s |
| `0xC2B2F977` | 19m 56s | 15m 49s | 6s | 35m 53s |
| `0x4A712118` | 20m 30s | 15m 25s | 6s | 36m 03s |

Total batch wall time was **36m 08s**, including about five seconds of shared
image validation. Per-phase values above are rounded log values; JSON worker
durations were 1867.656725416s, 2152.781266708s, and 2163.163142875s respectively.
Totals include probing and other overhead, so the displayed columns need not sum
exactly. Aggregate verified-image throughput was 20.69 MiB/s, or about 4.98
boards/hour for this batch. Counting both writes and readback gives approximately
41.37 MiB/s of storage payload averaged over the whole batch.

## Comparison with the earlier single-board run

The [earlier full restore](unoq-validation.md) took 16m 31s: approximately
14m 28s writing and 1m 49s reading back. Three repeats of that baseline would take
about 49m 33s. This batch therefore used about **27% less wall time**, equivalent
to **1.37× the baseline throughput**, rather than scaling threefold.

Readback is the largest difference: it took roughly 8.5–8.7 times longer per
board. Write times increased by approximately 5%, 38%, and 42% relative to that
baseline. The batch's readback averages were 15.7–16.2 MiB/s per board. While all
three boards were reading, their percentage changes indicate roughly 42 MiB/s
combined; after the first finished, the two remaining boards accelerated. This
is consistent with contention on a shared USB path. The log alone does not
establish negotiated USB speed, hub topology, or whether storage also limited
writes. Board, source image, cable and connection differences make this an
observational comparison, not a controlled hub benchmark.

For a controlled follow-up, time one of these same targets on this hub alone,
then directly on the host, then distribute the batch across independent USB
controllers. Inspect negotiated speed during the Firehose transfer; the initial
EDL enumeration may differ. These follow-up measurements have not been run.

## Logging findings

The original output printed repeated unchanged write counts every five to ten
seconds. It divided the last acknowledged bytes by continuously increasing
elapsed time, causing speed to appear to fall and the ETA to rise between
acknowledgements. Those swings were a reporting artifact, not evidence that
transfer speed was repeatedly collapsing.

Compact progress now reports phase boundaries and meaningful progress changes,
with a one-minute heartbeat between acknowledgements. Write rates use the time
of the last acknowledgement; an unchanged heartbeat omits speed and ETA.
Chunk counts make the measurement granularity explicit. Quick probes and final
checksum phases no longer generate routine progress lines, and the final table
lists each target's phase durations and status. `--verbose-progress` enables
frequent diagnostic updates. These logging changes do not improve USB throughput.

## Subsequent batch with `--skip-unchanged`

A second user-supplied transcript reports **three verified targets, zero
failures**, with the same golden bundle and `--skip-unchanged`. Two serials match
the full-flash batch; `0x7FE12D45` replaces `0x4A712118`, so batch comparisons
include a change of target. No post-flash boot or MCU test was reported.

| Target | Pre-read | Compare | Write | Final readback | Worker total | Written | Skipped |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| `0xC2B2F977` | 17m 47s | 3s | none | none | 17m 52s | 0 B | 100% |
| `0x8ED963AB` | 17m 48s | 3s | 13s | 11m 00s | 29m 12s | 102 MiB | 99.32% |
| `0x7FE12D45` | 17m 59s | 3s | 7m 41s | 8m 33s | 34m 23s | 7.48 GiB | 48.73% |

The batch took **34m 29s**, 1m 39s (4.57%) shorter than the previous full batch.
Worker JSON durations were 1071.920698625s, 1752.222293s and 2063.279731375s
respectively. Final checksum phases for the two written targets took about six
seconds each; probing and other overhead are included in worker totals.

Total writes fell from 47,025,487,872 bytes for three full images to
8,143,241,216 bytes (7.58 GiB): **82.68% fewer bytes written**. This compares write
volume for three full images with this batch, without assuming the replacement
target matched the original third board.

`0xC2B2F977` matched every byte during the pre-read, so the tool issued no writes
or second readback. Its worker time fell from 35m 53s to 17m 52s, approximately
50%. `0x8ED963AB` needed only 106,954,752 bytes written but still required a full
final readback; its worker time fell by approximately 1m 55s (6.18%). The log
does not identify what caused those differences. `0x7FE12D45` needed
8,036,286,464 bytes written and determined when the batch finished.

The shared pre-read transferred three full images in about 18 minutes, averaging
roughly 41.6 MiB/s combined. This is a practical time floor for this fixture's
unchanged-board verification. Targets requiring any writes add a full final
readback. That explains why saving most of the writes did not save most of the
batch time. After `0x8ED963AB` finished, the remaining target's readback accelerated,
again supporting a shared-bandwidth explanation.

The compact logger now prints the comparison's planned write/skip counts and
explicitly identifies a board verified by the pre-read without writes. The final
table includes written/skipped bytes as well as phase timings. The supplied
transcript uses the earlier progress format; logging updates affect subsequent
invocations and do not change transfers already running.
