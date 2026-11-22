# RX 9070 XT W3 acceptance on 2026-11-22

The measured runner exited 0 and recorded `status=pass` and complete
measurements for all six runs. The worker, runner and independent reader were
built at clean revision `c0eeecea36f3c8d4faa9fce47c0bbbb494c3fa78`.
The run used [fixture-candidates-v2](fixtures.json) and the approved
[criteria-amd-v1](criteria-amd.json). Build and run commands are in the
[AMD acceptance procedure](README.md#amd-webgpu-vulkan-run).

The host was Linux 7.3.5-2-cachyos on amd64 with a Ryzen 7 9700X, 16 logical
CPUs and 33,016,467,456 bytes RAM. The GPU was the RX 9070 XT at
`0000:03:00.0` with RADV GFX1201, Mesa 26.3.1-arch3.5 and
17,095,983,104 bytes dedicated VRAM. The desktop was active, the initial
device-wide use was 1,511,186,432 bytes, the Mesa shader disk cache was disabled
and the OS page cache was left in place. The test used one repetition.

The AMD binaries used Go 1.27.3, `CGO_ENABLED=1`, `-tags gpu` and goinfer
and its WebGPU module at fork revision
`994cbf5ee38dd8dcc612aad6e6803ac79f772f3b`.
The selected local model was Index-Translate-2B Q4_K_M, 1,312,164,352 bytes,
from the [official fixed revision](https://huggingface.co/IndexTeam/Index-Translate-2B-GGUF/blob/3a0eca6ce931d88d00614d427ab3ed8ff0743902/Index-Translate-2B.Q4_K_M.gguf).
Its SHA-256 was `044b313d29342bd3b2c77cbb64023ca0d209bd9b3247763f9d162767ef2d746a`.
All workers reported requested and effective `webgpu`,
`webgpu:vulkan-resident (int4)`, f32 KV and 4,096 configured and effective
context tokens. The generation policy used greedy sampling, seed 0, a
2,048-token output reservation and a 30-second request timeout. In the short
text smoke, the decoder declined batched prefill and completed with per-token
prefill on the resident path.

| Fixture | Fresh units passed | GPU counter increased / completed generation | TM reused and revalidated | Fresh seconds | Fresh worker RSS bytes | Fresh sampled worker VRAM bytes |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| BetterKeybinds | 19/19 | 19/19 | 19/19 | 14.25 | 3,949,441,024 | 2,844,053,504 |
| MonsterOfTheWeek | 30/30 | 30/30 | 30/30 | 19.80 | 3,865,088,000 | 2,844,053,504 |
| Authored | 7/7 | 6/6 | 7/7 | 7.91 | 3,917,697,024 | 2,844,053,504 |

Each fresh run passed the 60-second, 24 GiB RSS and 12 GiB sampled
worker VRAM limits. The 56 fresh units included one empty authored entry.
Each of the 55 completed generations increased a graphics or compute counter
for its worker on the selected PCI device between entry and return of
`Generate`. This interval excludes model load and the load-time self-test.
The separate reuse workers generated zero units and loaded the model afresh.
The three reuse intervals were 3.23, 3.20 and 3.20 seconds.

All six runs passed final validation, committed and verified the expected
SQLite TM rows, and published output that the independent w3strings 0.2.0
reader read and matched. Worker reaping, process-group checks, oracle
cleanup and each pair cleanup passed. Per-fixture time excludes the shared
preparation, oracle build and pair cleanup. RSS is wait4 high-water.
The nominal VRAM sampling interval was 100 ms. The VRAM value is the sampled
maximum of worker-attributed resident memory for the adopted PCI device.

The CLI and runner were launched from the normal Wayland desktop with
`DISPLAY=:0`, `WAYLAND_DISPLAY=wayland-1` and `XDG_RUNTIME_DIR` inherited.
The CLI switched to the headless environment in the same PID. The runner set
that environment when it launched each measured worker.

On the same clean revision, the strict offline observer recorded zero
connection attempts in all seven CLI cases. `doctor --check-model`, fresh
`translate`, repeat `translate` with `GOINFER_ATTN_KEYS=0` inherited, and
`localize --format w3strings` exited 0. The two text commands produced the
same output bytes. A missing Vulkan ICD, missing selected model file and
corrupt selected model file each exited 1. Each trace showed one in-place
re-exec. SIGINT and SIGTERM delivered to the re-executed translation process
during model snapshot creation each produced exit 1, no output or TM commit,
and removed the snapshot.

The standard CGO-disabled `ci/verify.sh` gate, explicit CUDA-tag build, and
the AMD-tag unit tests and vet passed on this revision.

The private `run.json` and observer traces retain the full execution record.
The initial measured run stopped after the first fixture's fresh mode because
the worker reported a full fork commit and the runner expected the identical
text in Go's pseudo-version field. At `9feb720` the measured W3 rerun passed,
and the strict Wayland desktop check recorded 14 connection attempts. Four
strict checks at that revision passed with a manually prepared display-free
environment. Its explicit CPU smoke passed with the pinned Qwen2.5-Coder-0.5B
fixture, two generations, three validated units, SQLite TM commit and
publication. Its first standard gate run stopped at a SQLite writer
deadline test while other verification was running. The rerun passed.
The historical
[CUDA run](gpu-db-run-2026-11-03.md) remains bound to its own host, pins and
criteria. This run assessed structure, GPU execution and the approved
performance limits. Human evaluation of translation meaning remains a
post-v1 task.
