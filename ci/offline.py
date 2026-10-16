#!/usr/bin/env python3
"""Run one command under strace and fail closed on any observed network attempt.

The whole process tree is traced with
``strace -f -qq -yy -s 4096 -e trace=%network -o TRACE -- COMMAND...``: INET and
INET6 socket creation, connect and destination-addressed send, plus AF_UNIX
connect and destination-addressed send that would bypass a resolver or proxy,
are refused. AF_UNIX socketpair and other in-process IPC stay allowed. Unknown,
blank or truncated trace lines are never rounded to zero.

Evidence is written next to ``--evidence PREFIX``: PREFIX.trace (raw trace),
PREFIX.stdout, PREFIX.stderr (the target's streams) and PREFIX.json (detector
result). Exit codes: 0 clean, 1 forbidden attempt, 2 invalid trace or unusable
tracer, 3 target exit mismatch, 4 timeout.
"""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass, field
from typing import Any

UNFINISHED = " <unfinished ...>"
MARKER = re.compile(r"^(?:\d+\s+)?(?:\+\+\+|---)")
RESUMED = re.compile(r"^<\.\.\.\s+([a-z_][a-z0-9_]*)\s+resumed>")
CALL = re.compile(r"^([a-z_][a-z0-9_]*)\((.*)$")
SOCKADDR = re.compile(r"sa_family=(AF_[A-Z0-9_]+)")
FAMILY = re.compile(r"\bAF_[A-Z0-9_]+")

SOCKET_FAMILY = ("socket", "socketpair")
IDENTIFY = ("listen", "accept", "accept4")
SEND = ("sendto", "sendmsg", "sendmmsg")
EXISTING_SOCKET = ("recvfrom", "recvmsg", "recvmmsg", "shutdown", "getsockname", "getpeername", "setsockopt", "getsockopt")
UNIX = "AF_UNIX"
PREFLIGHT_TIMEOUT = 120


def classify(name: str, args: str) -> tuple[str, str]:
    """Return (kind, detail) for one traced syscall; kind is violation, allowed or unparsed."""
    if name in SOCKET_FAMILY:
        families = FAMILY.findall(args)
        if not families:
            return "unparsed", f"{name} socket family not decoded"
        if families[0] == UNIX:
            return "allowed", f"{name}(AF_UNIX) in-process IPC"
        if families[0] == "AF_UNSPEC":
            return "unparsed", f"{name}(AF_UNSPEC) has no concrete family"
        return "violation", f"{name}({families[0]})"
    if name == "connect":
        match = SOCKADDR.search(args)
        if match is None:
            return "unparsed", "connect destination not decoded"
        return "violation", f"connect({match.group(1)})"
    if name == "bind":
        match = SOCKADDR.search(args)
        if match is None:
            return "unparsed", "bind address not decoded"
        if match.group(1) == UNIX:
            return "allowed", "bind(AF_UNIX)"
        return "violation", f"bind({match.group(1)})"
    if name in IDENTIFY:
        match = SOCKADDR.search(args)
        if match is not None and match.group(1) != UNIX:
            return "violation", f"{name}({match.group(1)})"
        return "allowed", name
    if name in SEND:
        if SOCKADDR.search(args) is not None:
            return "violation", f"{name} with destination address"
        if "NULL" in args:
            return "allowed", f"{name} to connected socket"
        return "unparsed", f"{name} destination not decoded"
    if name in EXISTING_SOCKET:
        return "allowed", name
    return "unparsed", f"unrecognized network syscall {name}"


@dataclass(slots=True)
class Analysis:
    lines: int = 0
    markers: int = 0
    unfinished: int = 0
    resumed: int = 0
    attempts: list[dict[str, Any]] = field(default_factory=list)
    allowed: list[dict[str, Any]] = field(default_factory=list)
    unparsed: list[dict[str, Any]] = field(default_factory=list)

    @property
    def reasons(self) -> list[str]:
        reasons = []
        if self.unparsed:
            reasons.append(f"{len(self.unparsed)} trace line(s) could not be classified")
        if self.unfinished != self.resumed:
            reasons.append(f"{self.unfinished} unfinished syscall line(s) do not pair with {self.resumed} resumed line(s)")
        return reasons


def analyze(text: str) -> Analysis:
    result = Analysis()
    for raw in text.splitlines():
        result.lines += 1
        stripped = raw.strip()
        if not stripped:
            result.unparsed.append({"line": raw, "reason": "blank trace line"})
            continue
        if MARKER.match(stripped) is not None:
            result.markers += 1
            continue
        pid, body = None, stripped
        parts = stripped.split(None, 1)
        if len(parts) == 2 and parts[0].isdigit():
            pid, body = int(parts[0]), parts[1]
        if body.endswith(UNFINISHED):
            result.unfinished += 1
            body = body[: -len(UNFINISHED)]
        if RESUMED.match(body) is not None:
            if pid is None:
                result.unparsed.append({"line": raw, "reason": "trace line without pid prefix; -f following unverified"})
                continue
            result.resumed += 1
            continue
        match = CALL.match(body)
        if match is None:
            result.unparsed.append({"line": raw, "reason": "neither a syscall line nor a marker"})
            continue
        name, args = match.groups()
        entry = {"pid": pid, "line": raw, "syscall": name}
        if pid is None:
            result.unparsed.append({**entry, "reason": "trace line without pid prefix; -f following unverified"})
            continue
        kind, detail = classify(name, args)
        entry["detail"] = detail
        if kind == "violation":
            result.attempts.append(entry)
        elif kind == "allowed":
            result.allowed.append(entry)
        else:
            result.unparsed.append({**entry, "reason": detail})
    return result


def terminate(signum: int, _frame: Any) -> None:
    raise SystemExit(128 + signum)


def run_strace(strace: str, trace: Path, command: list[str], stdout: Path, stderr: Path, timeout: int) -> tuple[int, bool, int]:
    argv = [strace, "-f", "-qq", "-yy", "-s", "4096", "-e", "trace=%network", "-o", str(trace), "--", *command]
    with stdout.open("wb") as out, stderr.open("wb") as err:
        child = subprocess.Popen(argv, stdout=out, stderr=err, start_new_session=True)
        group = os.getpgid(child.pid)
        timed_out = False
        try:
            try:
                code = child.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                timed_out = True
                os.killpg(group, signal.SIGKILL)
                code = child.wait()
        finally:
            if child.poll() is None:
                os.killpg(group, signal.SIGKILL)
                code = child.wait()
    return code, timed_out, group


def group_members(group: int) -> list[dict[str, Any]]:
    alive = []
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        try:
            stat = Path(f"/proc/{entry}/stat").read_text()
        except OSError:
            continue
        fields = stat.rsplit(")", 1)[-1].split()
        if len(fields) < 3 or fields[2] != str(group) or fields[0] == "Z":
            continue
        alive.append({"pid": int(entry), "state": fields[0]})
    return alive


def stop_group(group: int) -> list[dict[str, Any]]:
    """Kill every surviving group member and wait, bounded, for the group to empty."""
    for _ in range(40):
        alive = group_members(group)
        if not alive:
            return []
        for entry in alive:
            try:
                os.kill(entry["pid"], signal.SIGKILL)
            except ProcessLookupError:
                pass
        time.sleep(0.05)
    return group_members(group)


def preflight_tracer(strace: str) -> dict[str, Any]:
    """Prove with the tracer's own probe that tracing works and this parser reads its output."""
    with tempfile.TemporaryDirectory(prefix="yakuori-offline-preflight-") as tmp:
        root = Path(tmp)
        trace = root / "preflight.trace"
        script = "import socket; socket.socket(socket.AF_INET, socket.SOCK_STREAM).close()"
        _, _, group = run_strace(strace, trace, [sys.executable, "-c", script], root / "stdout", root / "stderr", timeout=PREFLIGHT_TIMEOUT)
        stop_group(group)
        if not trace.is_file():
            return {"ok": False, "detail": "the tracer produced no trace file for its own probe", "syscall": None}
        parsed = analyze(trace.read_text(errors="replace"))
        hit = next((entry for entry in parsed.attempts if "AF_INET" in entry["detail"]), None)
        if hit is None:
            return {"ok": False, "detail": "the tracer did not record its own AF_INET socket; tracing cannot be trusted", "syscall": None}
        return {"ok": True, "detail": hit["detail"], "syscall": hit["syscall"]}


def emit(prefix: Path, record: dict[str, Any], code: int) -> int:
    record["exit_code"] = code
    prefix.with_name(prefix.name + ".json").write_text(json.dumps(record, indent=2) + "\n")
    summary = {"status": record["status"], "exit_code": code, "attempts": len(record["attempts"]), "invalid_reasons": record["invalid_reasons"]}
    print(json.dumps(summary))
    return code


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--evidence", required=True, metavar="PREFIX", help="writes PREFIX.trace/.stdout/.stderr/.json")
    parser.add_argument("--expect-exit", required=True, type=int, metavar="CODE", help="exit code the target must produce")
    parser.add_argument("--timeout-seconds", type=int, default=1800, metavar="SECONDS")
    parser.add_argument("command", nargs=argparse.REMAINDER, help="command after --")
    args = parser.parse_args()
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        parser.error("a command after -- is required")
    if args.timeout_seconds < 1:
        parser.error("--timeout-seconds must be positive")
    prefix = Path(args.evidence).resolve()
    if prefix.with_name(prefix.name + ".json").exists():
        parser.error(f"evidence already exists: {prefix}.json")
    prefix.parent.mkdir(parents=True, exist_ok=True)
    trace, stdout, stderr = (prefix.with_name(prefix.name + suffix) for suffix in (".trace", ".stdout", ".stderr"))

    record: dict[str, Any] = {
        "command": command,
        "expect_exit": args.expect_exit,
        "observed_exit": None,
        "status": None,
        "timeout_seconds": args.timeout_seconds,
        "strace": {"path": None, "returncode": None, "timed_out": None},
        "trace": {"path": str(trace), "bytes": 0, "lines": 0, "unfinished": 0, "resumed": 0, "markers": 0, "unparsed": []},
        "attempts": [],
        "allowed_local": [],
        "invalid_reasons": [],
        "descendants_after_exit": [],
        "preflight": None,
        "note": "strace messages are suppressed with -qq, so PREFIX.stdout/PREFIX.stderr carry the target's streams",
        "exit_code": None,
    }

    strace = shutil.which("strace")
    if strace is None:
        record["status"] = "unusable_tracer"
        record["invalid_reasons"] = ["strace is not on PATH"]
        return emit(prefix, record, 2)
    record["strace"]["path"] = strace
    preflight = preflight_tracer(strace)
    record["preflight"] = preflight
    if not preflight["ok"]:
        record["status"] = "unusable_tracer"
        record["invalid_reasons"] = [preflight["detail"]]
        return emit(prefix, record, 2)

    signal.signal(signal.SIGINT, terminate)
    signal.signal(signal.SIGTERM, terminate)
    try:
        code, timed_out, group = run_strace(strace, trace, command, stdout, stderr, args.timeout_seconds)
    except SystemExit as interrupted:
        record["status"] = "interrupted"
        record["invalid_reasons"] = [f"observer received signal {int(interrupted.code) - 128}; traced group stopped"]
        return emit(prefix, record, int(interrupted.code))
    record["observed_exit"] = code
    record["strace"]["returncode"] = code
    record["strace"]["timed_out"] = timed_out
    record["descendants_after_exit"] = stop_group(group)

    reasons: list[str] = []
    if trace.is_file():
        text = trace.read_text(errors="replace")
        parsed = analyze(text)
        record["trace"] = {"path": str(trace), "bytes": len(text), "lines": parsed.lines, "unfinished": parsed.unfinished,
                           "resumed": parsed.resumed, "markers": parsed.markers, "unparsed": parsed.unparsed}
        record["attempts"] = parsed.attempts
        record["allowed_local"] = parsed.allowed
        reasons = parsed.reasons
    else:
        reasons = ["strace did not create the trace file"]
    if record["descendants_after_exit"]:
        reasons.append("traced descendants survived the tracer exit")
    record["invalid_reasons"] = reasons

    if timed_out:
        status, code = "timeout", 4
    elif reasons:
        status, code = "invalid", 2
    elif record["attempts"]:
        status, code = "attempts", 1
    elif record["observed_exit"] != args.expect_exit:
        status, code = "exit_mismatch", 3
    else:
        status, code = "clean", 0
    record["status"] = status
    return emit(prefix, record, code)


if __name__ == "__main__":
    raise SystemExit(main())
