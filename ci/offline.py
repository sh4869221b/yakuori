#!/usr/bin/env python3
"""Run one command under strace and fail closed on any observed network attempt.

The whole process tree is traced with
``strace -f -qq -yy -s 4096 -e trace=%network -o TRACE -- COMMAND...``: INET and
INET6 socket creation, connect and destination-addressed send, plus AF_UNIX
connect and destination-addressed send that would bypass a resolver or proxy,
are refused. AF_UNIX socketpair and other in-process IPC stay allowed. Unknown,
blank or truncated trace lines are never rounded to zero: a record counts only
once its argument list closes outside quotes and ``-yy`` annotations and its printed
result is complete.

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
ASSIGNMENT = re.compile(r"\s*=\s*")
COUNT = re.compile(r"[0-9]+")
ERRNO = re.compile(r"(?:-1|\?) [A-Z][A-Z0-9_]* \([^()]*\)\Z")
OPENERS = {"(": ")", "[": "]", "{": "}", "<": ">"}
CLOSERS = ")]}>"
SOCKADDR = re.compile(r"sa_family=(AF_[A-Z0-9_]+)")
FAMILY = re.compile(r"\bAF_[A-Z0-9_]+")

SOCKET_FAMILY = ("socket", "socketpair")
IDENTIFY = ("listen", "accept", "accept4")
SEND = ("sendto", "sendmsg", "sendmmsg")
EXISTING_SOCKET = ("recvfrom", "recvmsg", "recvmmsg", "shutdown", "getsockname", "getpeername", "setsockopt", "getsockopt")
UNIX = "AF_UNIX"
PREFLIGHT_TIMEOUT = 120
TEARDOWN_ROUNDS = 40
TEARDOWN_INTERVAL = 0.05
TEARDOWN: dict[str, Any] = {"descendants_stopped": [], "descendants_after_exit": None, "verified": False}


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


def closer(body: str, start: int, stack: list[str]) -> int | None:
    """Index of the character closing the bracket sequence already open at ``start``, or None.

    strace ends a completed record with ``) = <value>``, but that text also occurs inside a
    printed value: in a quoted argument, and in the unquoted ``-yy`` descriptor annotation a
    real trace prints verbatim, such as ``sendto(4</tmp/a) = 5.bin>, ...)``. Quote, escape and
    bracket state therefore decide which characters are delimiters - ``-yy`` escapes quotes,
    ``<`` and spaces inside descriptor paths, and the ``->`` of a path or of a ``[128 => 2]``
    pair must not close anything - and a sequence that never closes is a record cut mid-write.
    """
    quoted = False
    index = start
    while index < len(body):
        char = body[index]
        if quoted:
            if char == "\\":
                index += 2
                continue
            if char == '"':
                quoted = False
        elif char == "\\":
            index += 2
            continue
        elif char == '"':
            quoted = True
        elif char in OPENERS:
            stack.append(OPENERS[char])
        elif char in CLOSERS and char == stack[-1]:
            stack.pop()
            if not stack:
                return index
        index += 1
    return None


def complete_result(tail: str) -> bool:
    """True only for a result strace prints after a completed argument list.

    A completed record ends in ``?``, in an integer, in that integer plus a closed ``-yy``
    annotation such as ``4<TCP:[127.0.0.1:9->127.0.0.1:1]>``, in a failure ``-1 ERRNO
    (message)``, or in an interrupted restart ``? ERRNO (message)``. Every other tail - the
    ``= -``, ``= -1``, ``= -1 EAGAIN``, ``= -1 EAGAIN (Resource temporaril`` and
    ``= 8<UNIX-STREAM:[`` that a cut line leaves behind - is a partial result, so the record
    is not completed.
    """
    match = ASSIGNMENT.match(tail)
    if match is None:
        return False
    text = tail[match.end():]
    if text == "?" or ERRNO.match(text) is not None:
        return True
    count = COUNT.match(text)
    if count is None:
        return False
    rest = text[count.end():]
    return rest == "" or (rest.startswith("<") and closer(rest, 1, [">"]) == len(rest) - 1)


def complete_record(body: str, start: int) -> bool:
    """True when the argument list already open at ``start`` closes and carries a complete result."""
    end = closer(body, start, [")"])
    return end is not None and complete_result(body[end + 1:])


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
        unfinished = body.endswith(UNFINISHED)
        if unfinished:
            result.unfinished += 1
            body = body[: -len(UNFINISHED)]
        resumed = RESUMED.match(body)
        if resumed is not None:
            if pid is None:
                result.unparsed.append({"line": raw, "reason": "trace line without pid prefix; -f following unverified"})
                continue
            if not complete_record(body, resumed.end()):
                result.unparsed.append({"line": raw, "reason": "resumed line without a completed syscall result; truncated trace record"})
                continue
            result.resumed += 1
            continue
        match = CALL.match(body)
        if match is None:
            result.unparsed.append({"line": raw, "reason": "neither a syscall line nor a marker"})
            continue
        if not unfinished and not complete_record(body, match.start(2)):
            result.unparsed.append({"line": raw, "reason": "syscall line without a completed result; truncated trace record"})
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


INTERRUPTED = False
IN_TEARDOWN = False


def terminate(signum: int, _frame: Any) -> None:
    """End the run on the first interrupt and absorb every later one.

    A repeated SIGINT/SIGTERM used to be delivered while the teardown ran and aborted
    it, leaking strace plus its target while the result still claimed the group had
    stopped. Only the first interrupt raises, and any delivery during the teardown is
    absorbed instead of replacing that teardown, so no later signal can abort it. No
    signal disposition is changed: that call is itself racy while signals are in flight.
    """
    global INTERRUPTED
    if INTERRUPTED or IN_TEARDOWN:
        return
    INTERRUPTED = True
    raise SystemExit(128 + signum)


def process_record(pid: int) -> dict[str, str] | None:
    """State, parent, group and start time of a live process; None once gone or zombie."""
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[-1].split()
    except OSError:
        return None
    if len(fields) < 20 or fields[0] == "Z":
        return None
    return {"state": fields[0], "ppid": fields[1], "pgrp": fields[2], "starttime": fields[19]}


def live_tree(roots: list[int]) -> list[int]:
    """Live processes reachable by parent chain from any root, setsid escapees included."""
    children: dict[str, list[int]] = {}
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        record = process_record(int(entry))
        if record is None:
            continue
        children.setdefault(record["ppid"], []).append(int(entry))
    found: list[int] = []
    queue = [str(root) for root in roots]
    seen = set(queue)
    while queue:
        for child in children.get(queue.pop(), []):
            if str(child) in seen:
                continue
            seen.add(str(child))
            found.append(child)
            queue.append(str(child))
    return found


def traced_pids(tracer: int) -> list[int]:
    """Every process the tracer still follows, read from the existing ptrace relationship.

    This is the link that survives a descendant double-forking, calling setsid and being
    reparented to pid 1: such a daemon is still traced, so it must still be stopped.
    """
    found: list[int] = []
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        try:
            status = Path(f"/proc/{entry}/status").read_text()
        except OSError:
            continue
        for line in status.splitlines():
            if line.startswith("TracerPid:"):
                if line.split()[1] == str(tracer):
                    found.append(int(entry))
                break
    return found


def owned_roots(root: int, tracer: str, identity: dict[int, str]) -> list[int]:
    """Only pids that still hold the process we recorded may seed a tree walk.

    A pid we killed can be reused by an unrelated process; walking from the number alone
    would adopt that stranger, so the recorded start time has to still match.
    """
    roots = [pid for pid in sorted(identity) if owned_alive(pid, identity[pid])]
    if owned_alive(root, tracer):
        roots.append(root)
    return roots


def record_tree(identity: dict[int, str], root: int, tracer: str, group: int) -> None:
    """Record every live traced process: parent chain, process group and ptrace link.

    Ownership stays exact: tree walks start only from pids that still hold the recorded
    process, the ptrace link is read only while our own tracer is still that process, and
    group membership counts only because this run created that session itself.
    """
    members = [member["pid"] for member in group_members(group)]
    linked = traced_pids(root) if owned_alive(root, tracer) else []
    for pid in [*live_tree([*owned_roots(root, tracer, identity), *members, *linked]), *members, *linked]:
        record = process_record(pid)
        if record is not None:
            identity.setdefault(pid, record["starttime"])


def group_members(group: int) -> list[dict[str, Any]]:
    alive = []
    for entry in os.listdir("/proc"):
        if not entry.isdigit():
            continue
        record = process_record(int(entry))
        if record is None or record["pgrp"] != str(group):
            continue
        alive.append({"pid": int(entry), "state": record["state"], "starttime": record["starttime"]})
    return alive


def owned_alive(pid: int, starttime: str) -> bool:
    """True only while the same process, identified by start time, is still alive."""
    record = process_record(pid)
    return record is not None and record["starttime"] == starttime


def stop_traced(root: int, tracer: str, group: int) -> None:
    """Kill the traced tree and record in TEARDOWN what actually stayed alive.

    Only processes this run owns are ever signalled: children of our own tracer, members
    of the session our tracer created, and processes that same tracer still follows.

    Three verified failures shape this: a repeated SIGINT/SIGTERM delivered while the
    cleanup ran aborted it and leaked strace plus its target while the result claimed
    the group had stopped; a descendant that called setsid left the process group and
    survived a timeout; and a descendant that double-forked, called setsid and was
    reparented to pid 1 survived a timeout because nothing linked it back to the tracer.
    Interrupts are absorbed for the whole region, descendants are found by parent chain,
    by process group and by the tracer's own ptrace link, and each kill is sent only
    while the recorded start time still matches so a reused pid is never signalled.
    """
    global IN_TEARDOWN
    identity: dict[int, str] = {}
    stopped: list[int] = []
    IN_TEARDOWN = True
    try:
        for _ in range(TEARDOWN_ROUNDS):
            record_tree(identity, root, tracer, group)
            # Freeze what is known before killing it: a stopped process cannot fork the
            # replacement descendant that the next scan would otherwise miss.
            for pid in sorted(identity):
                if owned_alive(pid, identity[pid]):
                    try:
                        os.kill(pid, signal.SIGSTOP)
                    except ProcessLookupError:
                        pass
            record_tree(identity, root, tracer, group)
            alive = [pid for pid in sorted(identity) if owned_alive(pid, identity[pid])]
            if not alive:
                TEARDOWN.update({"descendants_stopped": stopped, "descendants_after_exit": [], "verified": True})
                return
            for pid in alive:
                if owned_alive(pid, identity[pid]):
                    try:
                        os.kill(pid, signal.SIGKILL)
                        stopped.append(pid)
                    except ProcessLookupError:
                        pass
            if owned_alive(root, tracer):
                # The group is ours only while the tracer that created it is that process.
                try:
                    os.killpg(group, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            time.sleep(TEARDOWN_INTERVAL)
        survivors = [pid for pid in sorted(identity) if owned_alive(pid, identity[pid])]
        TEARDOWN.update({"descendants_stopped": stopped, "descendants_after_exit": survivors, "verified": True})
    finally:
        IN_TEARDOWN = False


def interrupt_reason(signal_number: int) -> str:
    if not TEARDOWN["verified"]:
        return f"observer received signal {signal_number}; traced group teardown could not be verified"
    if TEARDOWN["descendants_after_exit"]:
        return f"observer received signal {signal_number}; {len(TEARDOWN['descendants_after_exit'])} traced descendant(s) survived teardown"
    return f"observer received signal {signal_number}; traced group stopped and verified"


def run_strace(strace: str, trace: Path, command: list[str], stdout: Path, stderr: Path, timeout: int) -> tuple[int, bool, int]:
    argv = [strace, "-f", "-qq", "-yy", "-s", "4096", "-e", "trace=%network", "-o", str(trace), "--", *command]
    TEARDOWN.update({"descendants_stopped": [], "descendants_after_exit": None, "verified": False})
    with stdout.open("wb") as out, stderr.open("wb") as err:
        child = subprocess.Popen(argv, stdout=out, stderr=err, start_new_session=True)
        group = os.getpgid(child.pid)
        root = child.pid
        tracer_record = process_record(root)
        tracer = tracer_record["starttime"] if tracer_record is not None else ""
        timed_out = False
        try:
            try:
                code = child.wait(timeout=timeout)
            except subprocess.TimeoutExpired:
                timed_out = True
            # Exactly one teardown per run: it kills the traced tree (after a timeout or
            # after a clean exit) and records what it actually stopped.
            stop_traced(root, tracer, group)
            code = child.wait()
        except SystemExit:
            stop_traced(root, tracer, group)
            if child.poll() is None:
                child.wait()
            raise
    return code, timed_out, group


def preflight_tracer(strace: str) -> dict[str, Any]:
    """Prove with the tracer's own probe that tracing works and this parser reads its output."""
    with tempfile.TemporaryDirectory(prefix="yakuori-offline-preflight-") as tmp:
        root = Path(tmp)
        trace = root / "preflight.trace"
        script = "import socket; socket.socket(socket.AF_INET, socket.SOCK_STREAM).close()"
        run_strace(strace, trace, [sys.executable, "-c", script], root / "stdout", root / "stderr", timeout=PREFLIGHT_TIMEOUT)
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
        "descendants_stopped": [],
        "descendants_after_exit": None,
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
        record["descendants_stopped"] = TEARDOWN["descendants_stopped"]
        record["descendants_after_exit"] = TEARDOWN["descendants_after_exit"] if TEARDOWN["verified"] else None
        record["invalid_reasons"] = [interrupt_reason(int(interrupted.code) - 128)]
        return emit(prefix, record, int(interrupted.code))
    record["observed_exit"] = code
    record["strace"]["returncode"] = code
    record["strace"]["timed_out"] = timed_out
    record["descendants_stopped"] = TEARDOWN["descendants_stopped"]
    record["descendants_after_exit"] = TEARDOWN["descendants_after_exit"] if TEARDOWN["verified"] else None

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
    elif record["descendants_after_exit"] is None:
        reasons.append("traced group teardown could not be verified")
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
