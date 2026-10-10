#!/usr/bin/env python3
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
# Run: python3 ci/run-cuda-evaluation.py --help
"""Serial, bounded CUDA evaluation supervisor; no runtime downloads."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import time
from dataclasses import dataclass
from types import FrameType


@dataclass(frozen=True, slots=True)
class Case:
    backend: str
    fixture: Path | None = None
    pair: int = 0


def gpu_sample() -> str | None:
    """Device-wide samples are not exact continuous or process peaks."""
    try:
        result = subprocess.run(
            ["nvidia-smi", "--query-gpu=index,memory.used", "--format=csv,noheader,nounits"],
            capture_output=True, text=True, timeout=2, check=False,
        )
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    return result.stdout.strip() if result.returncode == 0 else None


def process_sample(pid: int) -> int | None:
    try:
        result = subprocess.run(["nvidia-smi", "--query-compute-apps=pid,used_gpu_memory", "--format=csv,noheader,nounits"], capture_output=True, text=True, timeout=2, check=False)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    for line in result.stdout.splitlines():
        fields = [field.strip() for field in line.split(",")]
        if fields[0] == str(pid):
            return int(fields[1]) if fields[1].isdigit() else None
    return None


def run_case(case: Case, args: argparse.Namespace, output: Path) -> bool:
    """Own one process group and terminate it on supervisor failure or interruption."""
    stem = f"{case.fixture.stem}-pair{case.pair}-{case.backend}" if case.fixture else case.backend
    env = {key: value for key, value in os.environ.items() if not key.startswith("GOINFER_")}
    env.update(GOMAXPROCS="16", GOMEMLIMIT="14336MiB", YAKUORI_CUDA_MODEL=str(args.model.resolve()),
               YAKUORI_CUDA_BACKEND=case.backend, YAKUORI_CUDA_REPORT=str(output / f"{stem}.json"))
    if case.fixture:
        env.update(YAKUORI_CUDA_FIXTURE=str(case.fixture), YAKUORI_CUDA_PAIR=str(case.pair))
        if case.backend == "cuda":
            env["YAKUORI_CUDA_REFERENCE"] = str(output / f"{case.fixture.stem}-pair{case.pair}-cpu.json")
    test = "TestCUDAEvaluationMatrix" if case.fixture else "TestCUDAEvaluationContracts"
    command = [str(args.binary.resolve()), "-test.run", f"^{test}$", "-test.v", "-test.count=1", "-test.timeout=1800s"]
    baseline = gpu_sample()
    samples: list[dict[str, float | str | int | None]] = []
    device_peaks: dict[str, int] = {}
    process_peak = None
    peak = 0
    reason = None
    started = time.monotonic()
    with (output / f"{stem}.stdout").open("w") as stdout, (output / f"{stem}.stderr").open("w") as stderr:
        child = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=env, start_new_session=True)
        try:
            while child.poll() is None:
                try:
                    lines = Path(f"/proc/{child.pid}/status").read_text().splitlines()
                    rss = max((int(line.split()[1]) for line in lines if line.startswith(("VmRSS:", "VmHWM:"))), default=0)
                    peak = max(peak, rss)
                    available = next(int(line.split()[1]) for line in Path("/proc/meminfo").read_text().splitlines() if line.startswith("MemAvailable:"))
                    if rss > 16384 * 1024:
                        reason = "RSS exceeds 16384 MiB"
                    elif available < 1024 * 1024:
                        reason = "host available memory below 1024 MiB"
                except FileNotFoundError:
                    if child.poll() is None:
                        raise
                sample = gpu_sample()
                process_memory = process_sample(child.pid)
                if process_memory is not None:
                    process_peak = max(process_peak or 0, process_memory)
                if sample is not None:
                    for line in sample.splitlines():
                        device, memory = (field.strip() for field in line.split(","))
                        device_peaks[device] = max(device_peaks.get(device, 0), int(memory))
                    samples.append({"elapsed_seconds": time.monotonic() - started, "device_memory_used_mib": sample, "process_memory_used_mib": process_memory})
                if time.monotonic() - started > 1800:
                    reason = "process wall exceeds 1800 seconds"
                if reason:
                    os.killpg(child.pid, signal.SIGKILL)
                    break
                time.sleep(.1)
        finally:
            if child.poll() is None:
                os.killpg(child.pid, signal.SIGKILL)
            code = child.wait()
            result = {"backend": case.backend, "fixture": str(case.fixture) if case.fixture else None,
                      "pair": case.pair, "command": command, "pid": child.pid, "returncode": code,
                      "watchdog_kill": reason, "observed_peak_rss_kib": peak,
                      "process_wall_seconds": time.monotonic() - started, "cleanup": "process group terminated; child reaped",
                      "hard_cgroup_bounds": "absent; monitored bounds only; historical environment equivalence unverified",
                      "device_vram_baseline_mib": baseline, "device_vram_sampled_max_mib": device_peaks, "process_vram_sampled_max_mib": process_peak,
                      "cpu_affinity": sorted(os.sched_getaffinity(0)), "gomaxprocs": 16, "go_memory_limit_mib": 14336, "vram_nominal_sample_interval_seconds": .1,
                      "vram_samples": samples, "vram_scope": "device-wide and child PID samples; exact continuous peak unverified; process baseline absent before process exists"}
            (output / f"{stem}.supervisor.json").write_text(json.dumps(result, indent=2) + "\n")
    return code == 0 and (output / f"{stem}.json").is_file()


def terminate(signum: int, _frame: FrameType | None) -> None:
    raise SystemExit(128 + signum)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--model", type=Path, required=True)
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--mode", choices=("contracts", "matrix"), required=True)
    parser.add_argument("--backend", choices=("cpu", "cuda", "both"), required=True)
    args = parser.parse_args()
    if not args.binary.is_file() or not os.access(args.binary, os.X_OK):
        parser.error("--binary must be executable")
    if not args.model.is_file():
        parser.error(f"model does not exist: {args.model}")
    if args.mode == "matrix" and args.backend != "both":
        parser.error("matrix requires --backend both")
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error("output directory must be empty")
    cpus = sorted(os.sched_getaffinity(0))
    if len(cpus) < 16:
        parser.error("evaluation requires at least 16 allowed CPUs")
    os.sched_setaffinity(0, cpus[:16])
    backends = ["cpu", "cuda"] if args.backend == "both" else [args.backend]
    cases = [Case(backend) for backend in backends]
    if args.mode == "matrix":
        root = Path(__file__).resolve().parents[1] / "docs/evidence/cpu-long-context/index-translate"
        fixtures = [(root / "fixtures/01-fixture.json", 3), *((root / f"prose/fixtures/{name}.json", 3) for name in ("00-short", "01-medium", "02-long")), (root / "fixtures/02-x4.json", 1)]
        if any(not fixture.is_file() for fixture, _ in fixtures):
            parser.error("matrix fixture missing")
        cases = [Case(backend, fixture, pair) for fixture, pairs in fixtures for pair in range(1, pairs + 1) for backend in backends]
    signal.signal(signal.SIGTERM, terminate)
    failed = False
    schedule: list[dict[str, str | int]] = []
    for case in cases:
        status = "unrun: earlier evaluation failed"
        if not failed:
            try:
                passed = run_case(case, args, output)
            except (SystemExit, KeyboardInterrupt):
                schedule.append({"backend": case.backend, "fixture": str(case.fixture), "pair": case.pair, "status": "interrupted"})
                (output / "schedule.json").write_text(json.dumps(schedule, indent=2) + "\n")
                raise
            failed = not passed
            status = "passed" if passed else "failed"
        schedule.append({"backend": case.backend, "fixture": str(case.fixture), "pair": case.pair, "status": status})
        (output / "schedule.json").write_text(json.dumps(schedule, indent=2) + "\n")
    return int(failed)


if __name__ == "__main__":
    raise SystemExit(main())
