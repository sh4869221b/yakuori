#!/usr/bin/env python3
"""Deterministic checks for ci/offline.py; requires strace on PATH."""
import json
import importlib.util
import os
from pathlib import Path
import select
import shutil
import signal
import subprocess
import sys
import tempfile
import textwrap
import time
import unittest
from unittest import mock

OFFLINE = Path(__file__).with_name("offline.py")
SPEC = importlib.util.spec_from_file_location("offline", OFFLINE)
offline = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = offline
SPEC.loader.exec_module(offline)

PROBE = textwrap.dedent(
    """\
    import socket, subprocess, sys, threading

    def ignore(attempt):
        try:
            attempt()
        except OSError:
            pass

    def tcp4():
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        ignore(lambda: sock.connect(("127.0.0.1", 9)))
        sock.close()

    def tcp6():
        sock = socket.socket(socket.AF_INET6, socket.SOCK_STREAM)
        ignore(lambda: sock.connect(("::1", 9)))
        sock.close()

    def udp4():
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        ignore(lambda: sock.sendto(b"probe", ("127.0.0.1", 9)))
        sock.close()

    def udp6():
        sock = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        ignore(lambda: sock.sendto(b"probe", ("::1", 9)))
        sock.close()

    def dns():
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        ignore(lambda: sock.sendto(b"\\x00" * 12, ("127.0.0.53", 53)))
        sock.close()

    def unix():
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        ignore(lambda: sock.connect("/run/systemd/resolve/io.systemd.Resolve"))
        sock.close()

    def unixgram():
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
        ignore(lambda: sock.sendto(b"probe", "/run/systemd/resolve/io.systemd.Resolve"))
        sock.close()

    def unixpair():
        left, right = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
        left.send(b"probe")
        right.recv(5)
        left.close()
        right.close()

    def mps():
        # The exact addresses a CUDA driver probes for its MPS control socket.
        for path in ("/run/nvidia-mps/control", "/tmp/nvidia-mps/control"):
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            ignore(lambda: sock.connect(path))
            sock.close()

    def mps_abstract():
        # An abstract address whose printed name spells out the control path: only the
        # exact filesystem path is the exception, so this must stay refused.
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        ignore(lambda: sock.connect("\\x00/run/nvidia-mps/control"))
        sock.close()

    def thread():
        worker = threading.Thread(target=tcp4)
        worker.start()
        worker.join()

    def child():
        subprocess.run([sys.executable, __file__, "tcp4"], check=False)

    MODES = {"tcp4": tcp4, "tcp6": tcp6, "udp4": udp4, "udp6": udp6, "dns": dns, "unix": unix,
             "unixgram": unixgram, "unixpair": unixpair, "mps": mps, "mps-abstract": mps_abstract,
             "thread": thread, "child": child}
    MODES[sys.argv[1]]()
    """
)

FAKE_STRACE = textwrap.dedent(
    """\
    #!/usr/bin/env python3
    import os, subprocess, sys

    mode = os.environ["FAKE_STRACE_MODE"]
    args = sys.argv[1:]
    trace = args[args.index("-o") + 1]
    command = args[args.index("--") + 1:]
    if mode == "missing":
        sys.stderr.write("strace: exec: No such file or directory\\n")
        sys.exit(127)
    if mode == "empty":
        open(trace, "w").close()
        sys.exit(subprocess.run(command).returncode)
    # Keep the real preflight and exec proof; inject only the network parser fixture.
    real = os.environ["REAL_STRACE"]
    if os.path.basename(trace) == "preflight.trace":
        os.execv(real, [real, *args])
    code = subprocess.run([real, *args]).returncode
    with open(trace, "ab") as output, open(mode.split(":", 1)[1], "rb") as fixture:
        output.write(fixture.read())
    sys.exit(code)
    """
)

RESUMED_TRACE = (
    '4242 connect(3<TCP:127.0.0.1:9>, {sa_family=AF_INET, sin_port=htons(9), sin_addr=inet_addr("127.0.0.1")}, 16 <unfinished ...>\n'
    "4242 <... connect resumed>) = -1 ECONNREFUSED (Connection refused)\n"
)
DANGLING_TRACE = (
    "4242 socket(AF_INET, SOCK_STREAM|SOCK_CLOEXEC, IPPROTO_TCP) = 3\n"
    '4242 connect(3<TCP:127.0.0.1:9>, {sa_family=AF_INET, sin_port=htons(9), sin_addr=inet_addr("127.0.0.1")}, 16 <unfinished ...>\n'
)
GARBAGE_TRACE = '4242 socket(AF_INET, SOCK_STREAM|SOCK_CLOEXEC, IPPROTO_TCP) = 3\nnot a syscall line\n'
# A complete AF_INET socket line precedes parser fixtures; preflight itself runs live.
INET_LINE = "4242 socket(AF_INET, SOCK_STREAM|SOCK_CLOEXEC, IPPROTO_TCP) = 3\n"
COMPLETE_LOCAL_IPC_TRACE = INET_LINE + "4242 socketpair(AF_UNIX, SOCK_STREAM, 0, [3, 4]) = 0\n"
CUT_LOCAL_IPC_TRACE = INET_LINE + "4242 socketpair(AF_UNIX, SOCK_STREAM, 0"
CUT_LOCAL_IPC_NEWLINE_TRACE = INET_LINE + "4242 socketpair(AF_UNIX, SOCK_STREAM, 0\n"
CUT_RECEIVE_TRACE = INET_LINE + "4242 recvfrom(3,"
CUT_RESUMED_TRACE = INET_LINE + "4242 <... recvfrom resumed>"
PAIRED_ALLOWED_IPC_TRACE = (INET_LINE + '4242 recvfrom(3, "probe", 5, 0, NULL, NULL <unfinished ...>\n'
                            "4242 <... recvfrom resumed>) = 5\n")
# The only ') = <value>' text in each cut fixture sits inside a quoted argument, so the
# record is still incomplete and must not be read as a completed syscall.
QUOTED_MARKER_LINE = '4242 recvfrom(3, "abc) = 5'
CUT_QUOTED_MARKER_TRACE = INET_LINE + QUOTED_MARKER_LINE
CUT_QUOTED_MARKER_NEWLINE_TRACE = INET_LINE + QUOTED_MARKER_LINE + "\n"
CUT_QUOTED_ESCAPE_TRACE = INET_LINE + '4242 recvfrom(3, "a\\") = 5'
CUT_QUOTED_MARKER_AFTER_ARG_TRACE = INET_LINE + '4242 recvfrom(3, "abc) = 5", 7'
COMPLETE_QUOTED_MARKER_TRACE = INET_LINE + '4242 recvfrom(3, "abc) = 5", 7, 0, NULL, NULL) = 7\n'
# Real strace prints the result after the argument list and -yy prints descriptor paths
# verbatim, so a record cut mid-result must not read as a completed syscall: a partial failure
# ('= -', '= -1 EAGAIN (Resource temporaril'), an open descriptor annotation
# ('= 8<UNIX-STREAM:[...]'), or ') = ' text inside a real fd path such as
# sendto(4</tmp/a) = 5.bin>, ...) are all incomplete.
CUT_NEGATIVE_RESULT_TRACE = INET_LINE + '4242 recvfrom(3, "probe", 5, 0, NULL, NULL) = -'
CUT_NEGATIVE_RESULT_NEWLINE_TRACE = CUT_NEGATIVE_RESULT_TRACE + "\n"
CUT_ERRNO_RESULT_TRACE = INET_LINE + '4242 recvfrom(3, "probe", 5, 0, NULL, NULL) = -1 EAGAIN (Resource temporaril'
CUT_RESULT_ANNOTATION_TRACE = INET_LINE + "4242 socket(AF_UNIX, SOCK_STREAM|SOCK_CLOEXEC, 0) = 8<UNIX-STREAM:[6120774]"
CUT_FD_PATH_MARKER_TRACE = INET_LINE + '4242 sendto(4</tmp/a) = 5.bin>, "x", 1, 0, NULL'
# The same shapes as complete records: a closed error result, a closed descriptor annotation,
# and ') = ' plus an escaped quote inside real -yy fd paths.
COMPLETE_ERRNO_RESULT_TRACE = INET_LINE + '4242 recvfrom(3, "probe", 5, 0, NULL, NULL) = -1 EAGAIN (Resource temporarily unavailable)\n'
COMPLETE_RESULT_ANNOTATION_TRACE = INET_LINE + "4242 socket(AF_UNIX, SOCK_STREAM|SOCK_CLOEXEC, 0) = 8<UNIX-STREAM:[6120774]>\n"
COMPLETE_FD_PATH_MARKER_TRACE = INET_LINE + '4242 sendto(4</tmp/a) = 5.bin>, "x", 1, 0, NULL, 0) = -1 ENOTSOCK (Socket operation on non-socket)\n'
COMPLETE_ESCAPED_QUOTE_PATH_TRACE = INET_LINE + '4242 sendto(6</tmp/a\\"b.bin>, "x", 1, 0, NULL, 0) = -1 ENOTSOCK (Socket operation on non-socket)\n'

# The recorded shape of the two NVIDIA MPS control connects libcuda probes (the same -yy
# descriptor annotation, sockaddr and addrlen the CUDA host produced; only the errno text is
# the C locale). One failure and one success prove the outcome does not decide the
# classification: under the opt-in both complete records are IPC, not attempts.
MPS_OPT_IN = ("--allow-nvidia-mps",)
MPS_CONTROL_CONNECTS_TRACE = INET_LINE + (
    '4242 connect(10<UNIX:[3214705]>, {sa_family=AF_UNIX, sun_path="/run/nvidia-mps/control"}, 26) = -1 ENOENT (No such file or directory)\n'
    '4242 connect(10<UNIX:[3218510]>, {sa_family=AF_UNIX, sun_path="/tmp/nvidia-mps/control"}, 26) = 0\n'
)
# Near misses that stay forbidden attempts: a longer name, a deeper path, a sibling path and
# a relative path.
MPS_NEARBY_PATHS_TRACE = INET_LINE + (
    '4242 connect(10<UNIX:[1]>, {sa_family=AF_UNIX, sun_path="/run/nvidia-mps/controls"}, 26) = -1 ENOENT (No such file or directory)\n'
    '4242 connect(10<UNIX:[2]>, {sa_family=AF_UNIX, sun_path="/run/nvidia-mps/control/more"}, 26) = -1 ENOENT (No such file or directory)\n'
    '4242 connect(10<UNIX:[3]>, {sa_family=AF_UNIX, sun_path="/tmp/nvidia-mps/control-old"}, 26) = -1 ENOENT (No such file or directory)\n'
    '4242 connect(10<UNIX:[4]>, {sa_family=AF_UNIX, sun_path="nvidia-mps/control"}, 26) = -1 ENOENT (No such file or directory)\n'
)
CUT_MPS_CONNECT_ARGS_TRACE = INET_LINE + '4242 connect(10<UNIX:[1]>, {sa_family=AF_UNIX, sun_path="/run/nvidia-mps/control"}, 26'
CUT_MPS_CONNECT_RESULT_TRACE = INET_LINE + '4242 connect(10<UNIX:[1]>, {sa_family=AF_UNIX, sun_path="/run/nvidia-mps/control"}, 26) = -1 ENOENT (No such file or directory'
PAIRED_MPS_CONNECT_TRACE = INET_LINE + (
    '4242 connect(10<UNIX:[1]>, {sa_family=AF_UNIX, sun_path="/run/nvidia-mps/control"}, 26 <unfinished ...>\n'
    '4242 <... connect resumed>) = -1 ENOENT (No such file or directory)\n'
)
DANGLING_MPS_CONNECT_TRACE = INET_LINE + '4242 connect(10<UNIX:[1]>, {sa_family=AF_UNIX, sun_path="/tmp/nvidia-mps/control"}, 26 <unfinished ...>\n'


class ObserverTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.probe = self.root / "probe.py"
        self.probe.write_text(PROBE)
        self.runs = 0

    def tearDown(self):
        self.tmp.cleanup()

    def prefix(self, name="case"):
        self.runs += 1
        return self.root / f"{name}-{self.runs}"

    def run_observer(self, prefix, expect_exit, command, timeout_seconds=None, path=None, env_extra=None, replace_path=False, observer_args=()):
        args = [sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", str(expect_exit)]
        if timeout_seconds is not None:
            args += ["--timeout-seconds", str(timeout_seconds)]
        args += list(observer_args)
        args += ["--", *command]
        env = dict(os.environ, **(env_extra or {}))
        if path is not None:
            env["PATH"] = str(path) if replace_path else str(path) + os.pathsep + env.get("PATH", "")
        return subprocess.run(args, capture_output=True, text=True, env=env, timeout=120)

    def evidence(self, prefix):
        path = Path(f"{prefix}.json")
        self.assertTrue(path.is_file(), f"observer wrote no evidence JSON at {path}")
        return json.loads(path.read_text())

    def probe_run(self, mode, prefix, expect_exit=0):
        return self.run_observer(prefix, expect_exit, [sys.executable, str(self.probe), mode])

    def fake_strace(self, mode):
        directory = self.root / f"fake-{mode.replace(':', '-').replace('/', '-')}"
        directory.mkdir(exist_ok=True)
        shim = directory / "strace"
        shim.write_text(FAKE_STRACE)
        shim.chmod(0o700)
        return str(directory), {"FAKE_STRACE_MODE": mode, "REAL_STRACE": shutil.which("strace")}

    def assert_attempt(self, prefix, mode):
        run = self.probe_run(mode, prefix)
        self.assertEqual(run.returncode, 1, f"{mode}: stdout={run.stdout} stderr={run.stderr}")
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "attempts", f"{mode}: {evidence}")
        self.assertTrue(evidence["attempts"], f"{mode}: no attempt recorded")
        return evidence

    def process_state(self, pid):
        try:
            return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[-1].split()[0]
        except FileNotFoundError:
            return None

    def process_field(self, pid, index):
        try:
            return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[-1].split()[index]
        except FileNotFoundError:
            return None

    def ancestors(self):
        """This test process and every kernel parent up to init, read from /proc."""
        chain, pid = [], os.getpid()
        while str(pid) not in chain:
            chain.append(str(pid))
            parent = self.process_field(pid, 1)
            if parent is None or parent == "0":
                break
            pid = int(parent)
        return chain

    def traced_by(self, pid, observer):
        """The tracer holding `pid`, failing unless this observer started that tracer.

        A double-forked, setsid descendant leaves the parent chain and the process
        group, so the ptrace link is the only thing that still connects it to the run:
        it has to be this observer's own tracer that holds it.
        """
        try:
            status = Path(f"/proc/{pid}/status").read_text()
        except FileNotFoundError:
            self.fail(f"no /proc entry for traced process {pid}")
        tracer = next((int(line.split()[1]) for line in status.splitlines()
                       if line.startswith("TracerPid:")), 0)
        self.assertNotEqual(tracer, 0, f"{pid} is not traced by the observer's tracer")
        self.assertEqual(self.process_field(tracer, 1), str(observer.pid),
                         "the traced process is held by a tracer this observer did not start")
        return tracer

    def ready_fifo(self):
        fifo = self.root / f"ready-{self.runs + 1}.fifo"
        os.mkfifo(fifo)
        return fifo

    def await_ready(self, fifo, timeout=20):
        """Block on the traced process's own readiness write instead of polling for it."""
        handle = os.open(fifo, os.O_RDONLY | os.O_NONBLOCK)
        try:
            readable, _, _ = select.select([handle], [], [], timeout)
            if not readable:
                self.fail(f"traced process never signalled readiness through {fifo}")
            payload = os.read(handle, 64)
            if b"ready" not in payload:
                self.fail(f"unexpected readiness payload {payload!r}")
        finally:
            os.close(handle)

    def assert_no_live_process(self, marker):
        survivors = []
        for entry in os.listdir("/proc"):
            if not entry.isdigit():
                continue
            try:
                cmdline = Path(f"/proc/{entry}/cmdline").read_bytes()
            except OSError:
                continue
            state = self.process_state(int(entry))
            if marker.encode() in cmdline and state not in (None, "Z"):
                survivors.append((int(entry), state, cmdline[:160]))
        self.assertEqual(survivors, [], "traced process still alive after teardown")

    def test_clean_run_has_valid_empty_trace(self):
        prefix = self.prefix("clean")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"])
        self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "clean")
        self.assertEqual(evidence["observed_exit"], 0)
        self.assertEqual(evidence["attempts"], [])
        self.assertTrue(evidence["preflight"]["ok"])

    def test_ipv4_and_ipv6_attempts_are_detected(self):
        for mode, family in (("tcp4", "AF_INET"), ("tcp6", "AF_INET6")):
            with self.subTest(mode=mode):
                evidence = self.assert_attempt(self.prefix(mode), mode)
                self.assertTrue(any(family in entry["detail"] for entry in evidence["attempts"]), evidence["attempts"])

    def test_udp_dns_and_unix_attempts_are_detected(self):
        for mode, needle in (("udp4", "sendto with destination address"), ("udp6", "sendto with destination address"),
                             ("dns", "sendto with destination address"), ("unix", "connect(AF_UNIX)"),
                             ("unixgram", "sendto with destination address")):
            with self.subTest(mode=mode):
                evidence = self.assert_attempt(self.prefix(mode), mode)
                self.assertTrue(any(needle in entry["detail"] for entry in evidence["attempts"]), evidence["attempts"])

    def test_thread_and_child_attempts_are_detected(self):
        for mode in ("thread", "child"):
            with self.subTest(mode=mode):
                evidence = self.assert_attempt(self.prefix(mode), mode)
                self.assertTrue(any("AF_INET" in entry["detail"] for entry in evidence["attempts"]), evidence["attempts"])

    def test_in_process_unix_socketpair_is_not_an_attempt(self):
        prefix = self.prefix("unixpair")
        run = self.probe_run("unixpair", prefix)
        self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "clean")
        self.assertEqual(evidence["attempts"], [])
        self.assertTrue(any(entry["syscall"] == "socketpair" for entry in evidence["allowed_local"]), evidence["allowed_local"])

    def test_unfinished_then_resumed_trace_is_detected(self):
        fixture = self.root / "resumed.trace"
        fixture.write_text(RESUMED_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("resumed")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual((evidence["trace"]["unfinished"], evidence["trace"]["resumed"]), (1, 1))
        self.assertEqual(len(evidence["attempts"]), 1)
        self.assertEqual(evidence["attempts"][0]["detail"], "connect(AF_INET)")

    def test_dangling_unfinished_trace_is_invalid(self):
        fixture = self.root / "dangling.trace"
        fixture.write_text(DANGLING_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("dangling")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "invalid")
        self.assertTrue(any("unfinished" in reason for reason in evidence["invalid_reasons"]), evidence["invalid_reasons"])

    def test_unparsed_trace_line_is_not_rounded_to_zero(self):
        fixture = self.root / "garbage.trace"
        fixture.write_text(GARBAGE_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("garbage")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "invalid")
        self.assertEqual(evidence["trace"]["unparsed"][0]["line"], "not a syscall line")

    def test_complete_local_ipc_trace_stays_allowed(self):
        fixture = self.root / "complete-ipc.trace"
        fixture.write_text(COMPLETE_LOCAL_IPC_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("complete-ipc")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual([entry["detail"] for entry in evidence["attempts"]], ["socket(AF_INET)"], evidence["attempts"])
        self.assertEqual(evidence["invalid_reasons"], [])
        self.assertEqual(evidence["trace"]["unparsed"], [])
        self.assertTrue(any(entry["syscall"] == "socketpair" for entry in evidence["allowed_local"]), evidence["allowed_local"])

    def test_truncated_trace_records_are_invalid(self):
        # Each fixture is a complete INET line followed by one record cut mid-write: the
        # cut record cannot be classified, so the whole trace has to be invalid rather
        # than silently reporting only the complete attempt.
        for name, text in (("cut-ipc", CUT_LOCAL_IPC_TRACE),
                           ("cut-ipc-newline", CUT_LOCAL_IPC_NEWLINE_TRACE),
                           ("cut-receive", CUT_RECEIVE_TRACE),
                           ("cut-resumed", CUT_RESUMED_TRACE)):
            with self.subTest(name=name):
                fixture = self.root / f"{name}.trace"
                fixture.write_text(text)
                path, env = self.fake_strace(f"fixture:{fixture}")
                prefix = self.prefix(name)
                run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
                self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertEqual(evidence["status"], "invalid")
                self.assertEqual(evidence["allowed_local"], [])
                self.assertEqual([entry["line"] for entry in evidence["trace"]["unparsed"]], [text.splitlines()[-1]], evidence["trace"])
                self.assertTrue(any("could not be classified" in reason for reason in evidence["invalid_reasons"]),
                                evidence["invalid_reasons"])

    def test_paired_unfinished_resumed_allowed_ipc_is_not_invalid(self):
        fixture = self.root / "paired-ipc.trace"
        fixture.write_text(PAIRED_ALLOWED_IPC_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("paired-ipc")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual((evidence["trace"]["unfinished"], evidence["trace"]["resumed"]), (1, 1))
        self.assertEqual(evidence["invalid_reasons"], [])
        self.assertEqual(evidence["trace"]["unparsed"], [])
        self.assertTrue(any(entry["syscall"] == "recvfrom" for entry in evidence["allowed_local"]), evidence["allowed_local"])

    def test_quoted_marker_truncations_are_invalid(self):
        for name, text in (("cut-quoted-marker", CUT_QUOTED_MARKER_TRACE),
                           ("cut-quoted-marker-newline", CUT_QUOTED_MARKER_NEWLINE_TRACE),
                           ("cut-quoted-escape", CUT_QUOTED_ESCAPE_TRACE),
                           ("cut-quoted-marker-after-arg", CUT_QUOTED_MARKER_AFTER_ARG_TRACE)):
            with self.subTest(name=name):
                fixture = self.root / f"{name}.trace"
                fixture.write_text(text)
                path, env = self.fake_strace(f"fixture:{fixture}")
                prefix = self.prefix(name)
                run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
                self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertEqual(evidence["status"], "invalid")
                self.assertEqual(evidence["allowed_local"], [])
                self.assertEqual([entry["line"] for entry in evidence["trace"]["unparsed"]], [text.splitlines()[-1]], evidence["trace"])

    def test_complete_quoted_marker_record_stays_allowed(self):
        fixture = self.root / "complete-quoted-marker.trace"
        fixture.write_text(COMPLETE_QUOTED_MARKER_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("complete-quoted-marker")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["invalid_reasons"], [])
        self.assertEqual(evidence["trace"]["unparsed"], [])
        self.assertTrue(any(entry["syscall"] == "recvfrom" for entry in evidence["allowed_local"]), evidence["allowed_local"])

    def test_truncated_result_records_are_invalid(self):
        # The argument list closes but the printed result is cut: a partial failure, an
        # unclosed -yy descriptor annotation, or ') = ' text inside a real fd path. A partial
        # result is not a completed syscall, so such a trace has to be invalid.
        for name, text in (("cut-negative-result", CUT_NEGATIVE_RESULT_TRACE),
                           ("cut-negative-result-newline", CUT_NEGATIVE_RESULT_NEWLINE_TRACE),
                           ("cut-errno-result", CUT_ERRNO_RESULT_TRACE),
                           ("cut-result-annotation", CUT_RESULT_ANNOTATION_TRACE),
                           ("cut-fd-path-marker", CUT_FD_PATH_MARKER_TRACE)):
            with self.subTest(name=name):
                fixture = self.root / f"{name}.trace"
                fixture.write_text(text)
                path, env = self.fake_strace(f"fixture:{fixture}")
                prefix = self.prefix(name)
                run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
                self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertEqual(evidence["status"], "invalid")
                self.assertEqual(evidence["allowed_local"], [])
                self.assertEqual([entry["line"] for entry in evidence["trace"]["unparsed"]], [text.splitlines()[-1]], evidence["trace"])

    def test_complete_result_records_stay_allowed(self):
        # Controls: these are the records strace prints complete, so the completeness check
        # must keep accepting them instead of rejecting real records.
        for name, text, syscall in (("complete-errno-result", COMPLETE_ERRNO_RESULT_TRACE, "recvfrom"),
                                    ("complete-result-annotation", COMPLETE_RESULT_ANNOTATION_TRACE, "socket"),
                                    ("complete-fd-path-marker", COMPLETE_FD_PATH_MARKER_TRACE, "sendto"),
                                    ("complete-escaped-quote-path", COMPLETE_ESCAPED_QUOTE_PATH_TRACE, "sendto")):
            with self.subTest(name=name):
                fixture = self.root / f"{name}.trace"
                fixture.write_text(text)
                path, env = self.fake_strace(f"fixture:{fixture}")
                prefix = self.prefix(name)
                run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
                self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertEqual(evidence["invalid_reasons"], [])
                self.assertEqual(evidence["trace"]["unparsed"], [])
                self.assertTrue(any(entry["syscall"] == syscall for entry in evidence["allowed_local"]), evidence["allowed_local"])

    def test_missing_trace_file_is_invalid(self):
        path, env = self.fake_strace("missing")
        prefix = self.prefix("missing")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        self.assertEqual(self.evidence(prefix)["status"], "unusable_tracer")

    def test_tracer_without_real_tracing_is_rejected(self):
        path, env = self.fake_strace("empty")
        prefix = self.prefix("empty")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "unusable_tracer")
        self.assertFalse(evidence["preflight"]["ok"])

    def test_missing_strace_on_path_is_rejected(self):
        empty = self.root / "empty-path"
        empty.mkdir()
        prefix = self.prefix("no-strace")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=str(empty), replace_path=True)
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "unusable_tracer")
        self.assertIn("not on PATH", evidence["invalid_reasons"][0])

    def test_target_exit_mismatch_is_reported(self):
        prefix = self.prefix("mismatch")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "raise SystemExit(3)"])
        self.assertEqual(run.returncode, 3, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "exit_mismatch")
        self.assertEqual(evidence["observed_exit"], 3)

    def test_timeout_stops_descendants_and_fails(self):
        script = self.root / "hang.sh"
        child = self.root / "hang-child.sh"
        child.write_text("#!/bin/sh\nsleep 600\n")
        child.chmod(0o700)
        script.write_text(f"#!/bin/sh\n{child} &\necho $! > {self.root}/child.pid\nwait\n")
        script.chmod(0o700)
        prefix = self.prefix("timeout")
        run = self.run_observer(prefix, 0, [str(script)], timeout_seconds=2)
        self.assertEqual(run.returncode, 4, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "timeout")
        self.assertEqual(evidence["descendants_after_exit"], [])
        pid = int((self.root / "child.pid").read_text())
        self.assertIn(self.process_state(pid), (None, "Z"))

    def test_interrupted_observer_stops_descendants_and_fails(self):
        fifo = self.ready_fifo()
        script = self.root / "interrupt.sh"
        child = self.root / "interrupt-child.sh"
        child.write_text("#!/bin/sh\nsleep 600 &\nwait\n")
        child.chmod(0o700)
        script.write_text(f"#!/bin/sh\n{child} &\necho $! > {self.root}/interrupt.pid\necho ready > {fifo}\nwait\n")
        script.chmod(0o700)
        prefix = self.prefix("interrupt")
        args = [sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", "0", "--", str(script)]
        observer = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.await_ready(fifo)
        observer.send_signal(signal.SIGTERM)
        self.assertEqual(observer.wait(timeout=20), 143)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "interrupted")
        self.assertEqual(evidence["descendants_after_exit"], [])
        pid = int((self.root / "interrupt.pid").read_text())
        self.assertIn(self.process_state(pid), (None, "Z"))
        self.assert_no_live_process(str(child))

    def test_repeated_interruptions_stop_the_whole_traced_tree(self):
        fifo = self.ready_fifo()
        child = self.root / "repeated-child.sh"
        child.write_text("#!/bin/sh\nsleep 600 &\nwait\n")
        child.chmod(0o700)
        target = self.root / "repeated-target.sh"
        target.write_text(f"#!/bin/sh\n{child} &\necho $! > {self.root}/repeated.pid\necho ready > {fifo}\nwait\n")
        target.chmod(0o700)
        prefix = self.prefix("repeated")
        args = [sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", "0", "--", str(target)]
        observer = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.await_ready(fifo)
        # SIGTERM and SIGINT cannot coalesce, so the second one is delivered inside the
        # teardown: an interruptible teardown aborted there and left strace, the target
        # and the child alive while still claiming the traced group had stopped.
        observer.send_signal(signal.SIGTERM)
        observer.send_signal(signal.SIGINT)
        # Both signals end the run; which one Python processes first decides 128+signum.
        self.assertIn(observer.wait(timeout=20), (130, 143))
        self.assert_no_live_process(str(child))
        self.assertIn(self.process_state(int((self.root / "repeated.pid").read_text())), (None, "Z"))
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "interrupted")
        self.assertEqual(evidence["descendants_after_exit"], [])
        self.assertIn("verified", evidence["invalid_reasons"][0])
        self.assertIn(int((self.root / "repeated.pid").read_text()), evidence["descendants_stopped"])

    def test_group_escaping_descendant_is_stopped_on_timeout(self):
        fifo = self.ready_fifo()
        holder = self.root / "escape-holder.sh"
        holder.write_text("#!/bin/sh\nsleep 600 &\nwait\n")
        holder.chmod(0o700)
        target = self.root / "escape-target.py"
        target.write_text(textwrap.dedent(f"""\
            import os
            pid = os.fork()
            if pid == 0:
                os.setsid()
                os.execvp("/bin/sh", ["/bin/sh", "{holder}"])
            open("{self.root}/escape.pid", "w").write(str(pid))
            with open("{fifo}", "w") as handle:
                handle.write("ready\\n")
            os.waitpid(pid, 0)
            """))
        prefix = self.prefix("escape")
        args = [sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", "0",
                "--timeout-seconds", "5", "--", sys.executable, str(target)]
        observer = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.await_ready(fifo)
        escapee = int((self.root / "escape.pid").read_text())
        self.assertNotIn(self.process_state(escapee), (None, "Z"), "the escaping descendant must be alive before the timeout")
        self.assertEqual(observer.wait(timeout=20), 4)
        self.assertIn(self.process_state(escapee), (None, "Z"), "the escaping descendant survived the timeout")
        self.assert_no_live_process(str(holder))
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "timeout")
        self.assertEqual(evidence["descendants_after_exit"], [])
        self.assertIn(escapee, evidence["descendants_stopped"])

    def test_reparented_daemon_descendant_is_stopped_on_timeout(self):
        fifo = self.ready_fifo()
        holder = self.root / "daemon-holder.sh"
        holder.write_text("#!/bin/sh\nsleep 600 &\nwait\n")
        holder.chmod(0o700)
        target = self.root / "daemon-target.py"
        target.write_text(textwrap.dedent(f"""\
            import os
            middle = os.fork()
            if middle == 0:
                os.setsid()
                if os.fork() == 0:
                    open("{self.root}/daemon.pid", "w").write(str(os.getpid()))
                    with open("{fifo}", "w") as handle:
                        handle.write("ready\\n")
                    os.execvp("/bin/sh", ["/bin/sh", "{holder}"])
                os._exit(0)
            os.waitpid(middle, 0)
            """))
        prefix = self.prefix("daemon")
        args = [sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", "0",
                "--timeout-seconds", "5", "--", sys.executable, str(target)]
        observer = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        self.await_ready(fifo)
        daemon = int((self.root / "daemon.pid").read_text())
        self.traced_by(daemon, observer)
        # The double-forked daemon is an orphan: the kernel hands it to the nearest child
        # subreaper ancestor, which is pid 1 only when nothing between this test and init
        # supervises children (the systemd user manager does). Both are legitimate
        # adoptions, so the escape is proven by the adopted parent sitting on this test's
        # own ancestry instead of by pid 1 exactly.
        self.assertIn(self.process_field(daemon, 1), self.ancestors(),
                      "the daemon must be reparented to init or a subreaper ancestor")
        self.assertNotIn(self.process_state(daemon), (None, "Z"), "the daemon must be alive before the timeout")
        self.assertEqual(observer.wait(timeout=20), 4)
        self.assertIn(self.process_state(daemon), (None, "Z"), "the reparented daemon survived the timeout")
        self.assert_no_live_process(str(holder))
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "timeout")
        self.assertEqual(evidence["descendants_after_exit"], [])
        self.assertIn(daemon, evidence["descendants_stopped"])

    def test_unrelated_sibling_process_is_not_signalled(self):
        fifo = self.ready_fifo()
        sleeper = subprocess.Popen(["/bin/sh", "-c", "exec sleep 600"], start_new_session=True)
        try:
            child = self.root / "sibling-child.sh"
            child.write_text("#!/bin/sh\nsleep 600 &\nwait\n")
            child.chmod(0o700)
            target = self.root / "sibling-target.sh"
            target.write_text(f"#!/bin/sh\n{child} &\necho $! > {self.root}/sibling.pid\necho ready > {fifo}\nwait\n")
            target.chmod(0o700)
            prefix = self.prefix("sibling")
            observer = subprocess.Popen([sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", "0", "--", str(target)],
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            self.await_ready(fifo)
            # The sibling is in its own session, is not a child of our tracer and is not
            # traced by it: the teardown must leave it alone while it stops its own tree.
            observer.send_signal(signal.SIGTERM)
            self.assertEqual(observer.wait(timeout=20), 143)
            evidence = self.evidence(prefix)
            self.assertEqual(evidence["descendants_after_exit"], [])
            self.assertNotIn(sleeper.pid, evidence["descendants_stopped"])
            self.assertNotIn(self.process_state(sleeper.pid), (None, "Z"), "an unrelated sibling process was signalled")
        finally:
            sleeper.kill()
            sleeper.wait(timeout=10)

    def test_existing_evidence_is_not_overwritten(self):
        prefix = self.prefix("existing")
        Path(f"{prefix}.json").write_text("existing evidence")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"])
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        self.assertIn("already exists", run.stderr)
        self.assertEqual(Path(f"{prefix}.json").read_text(), "existing evidence")

    def test_each_existing_artifact_prevents_preflight(self):
        for suffix in (".trace", ".stdout", ".stderr", ".json"):
            with self.subTest(suffix=suffix):
                prefix = self.prefix("existing-artifact")
                artifact = Path(f"{prefix}{suffix}")
                artifact.write_bytes(b"original evidence\n")
                with mock.patch.object(sys, "argv", [str(OFFLINE), "--evidence", str(prefix),
                                                     "--expect-exit", "0", "--", "/bin/true"]), \
                        mock.patch.object(offline, "preflight_tracer", wraps=offline.preflight_tracer) as preflight:
                    with self.assertRaises(SystemExit) as raised:
                        offline.main()
                self.assertEqual(raised.exception.code, 2)
                preflight.assert_not_called()
                self.assertEqual(artifact.read_bytes(), b"original evidence\n")

    def test_send_destination_is_not_inferred_from_payload_or_control(self):
        for name, args in (
            ("sendto", '3, NULL, 0, 0, 0x1234, 16'),
            ("sendto", '3, "NULL, sa_family=AF_UNIX", 23, 0, 0x1234, 16'),
            ("sendmsg", '3, {msg_name=0x1234, msg_namelen=16, msg_iov=[], msg_control=NULL}, 0'),
            ("sendmmsg", '3, [{msg_hdr={msg_name=NULL, msg_iov=[], msg_control=NULL}, msg_len=0}, '
                         '{msg_hdr={msg_name=0x1234, msg_iov=[], msg_control=NULL}, msg_len=0}], 2, 0'),
        ):
            with self.subTest(name=name, args=args):
                parsed = offline.analyze(f"4242 {name}({args}) = 0\n")
                self.assertTrue(parsed.reasons)
                self.assertEqual(parsed.allowed, [])

    def test_connected_message_sends_ignore_payload_address_text(self):
        for name, args in (
            ("sendto", '3, "sa_family=AF_INET, NULL", 23, 0, NULL, 0'),
            ("sendmsg", '3, {msg_name=NULL, msg_namelen=0, msg_iov=[{iov_base="msg_name=0x1", iov_len=12}], msg_control=NULL}, 0'),
            ("sendmmsg", '3, [{msg_hdr={msg_name=NULL, msg_namelen=0, msg_iov=[], msg_control=NULL}, msg_len=0}], 1, 0'),
        ):
            with self.subTest(name=name):
                parsed = offline.analyze(f"4242 {name}({args}) = 0\n")
                self.assertEqual(parsed.reasons, [])
                self.assertEqual(parsed.attempts, [])
                self.assertEqual(len(parsed.allowed), 1)

    def test_unfinished_records_require_same_pid_and_syscall(self):
        for resumed in ("4242 <... connect resumed>) = 0",
                        "4243 <... recvfrom resumed>) = 0"):
            with self.subTest(resumed=resumed):
                parsed = offline.analyze('4242 recvfrom(3, "", 0, 0, NULL, NULL <unfinished ...>\n'
                                         + resumed + "\n")
                self.assertTrue(parsed.reasons)

    def test_resumed_destination_is_classified_after_pairing(self):
        parsed = offline.analyze('4242 sendto(3, "x", 1, 0, <unfinished ...>\n'
                                 '4242 <... sendto resumed>{sa_family=AF_INET, sin_port=htons(9)}, 16) = 1\n')
        self.assertEqual(parsed.reasons, [])
        self.assertEqual(len(parsed.attempts), 1)
        self.assertEqual(parsed.attempts[0]["syscall"], "sendto")

    def test_reused_group_is_not_adopted_after_owner_exit(self):
        with subprocess.Popen(["/bin/sleep", "600"], start_new_session=True) as stranger:
            try:
                identity = {}
                offline.record_tree(identity, stranger.pid, "previous-owner-starttime", stranger.pid)
                self.assertEqual(identity, {})
                self.assertIsNone(stranger.poll())
            finally:
                stranger.kill()
                stranger.wait(timeout=10)

    def test_failed_preflight_never_runs_target(self):
        for failure in ("exit", "timeout", "survivor", "unverified", "incomplete"):
            with self.subTest(failure=failure):
                prefix = self.prefix(f"preflight-{failure}")
                marker = self.root / f"target-{failure}"
                real_run = offline.run_strace

                def fail_probe(strace, trace, command, stdout, stderr, timeout):
                    if failure == "exit":
                        command = [*command[:-1], command[-1] + "; raise SystemExit(7)"]
                    elif failure == "timeout":
                        command = [*command[:-1], command[-1] + "; import signal; signal.pause()"]
                        timeout = 1
                    result = real_run(strace, trace, command, stdout, stderr, timeout)
                    if failure == "survivor":
                        offline.TEARDOWN["descendants_after_exit"] = [99999999]
                    elif failure == "unverified":
                        offline.TEARDOWN["verified"] = False
                    elif failure == "incomplete":
                        with trace.open("a") as output:
                            output.write("4242 recvfrom(3,\n")
                    return result

                argv = [str(OFFLINE), "--evidence", str(prefix), "--expect-exit", "0", "--",
                        sys.executable, "-c", f"open({str(marker)!r}, 'w').close()"]
                with mock.patch.object(sys, "argv", argv), mock.patch.object(offline, "run_strace", side_effect=fail_probe):
                    code = offline.main()
                self.assertEqual(code, 2)
                self.assertFalse(marker.exists())
                self.assertEqual(self.evidence(prefix)["status"], "unusable_tracer")

    def test_exec_failure_is_not_an_expected_target_exit(self):
        inaccessible = self.root / "non-executable"
        inaccessible.write_text("#!/bin/sh\nexit 1\n")
        for command in ([str(self.root / "absent")], [str(inaccessible)]):
            with self.subTest(command=command):
                prefix = self.prefix("exec-failure")
                run = self.run_observer(prefix, 1, command)
                self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
                self.assertEqual(self.evidence(prefix)["status"], "invalid")
        prefix = self.prefix("genuine-exit-one")
        run = self.run_observer(prefix, 1, [sys.executable, "-c", "raise SystemExit(1)"])
        self.assertEqual(run.returncode, 0, run.stdout + run.stderr)
        self.assertEqual(self.evidence(prefix)["observed_exit"], 1)

    def test_spawn_and_preflight_interrupts_stop_owned_tracer(self):
        for phase in ("spawn", "preflight"):
            for signum in (signal.SIGINT, signal.SIGTERM):
                with self.subTest(phase=phase, signum=signum):
                    prefix = self.prefix(f"{phase}-{signum}")
                    pidfile = self.root / f"tracer-{self.runs}.pid"
                    ready_read, ready_write = os.pipe()
                    gate_read, gate_write = os.pipe()
                    launcher = textwrap.dedent(f"""\
                        import importlib.util, os, select, signal, sys
                        spec = importlib.util.spec_from_file_location("offline", {str(OFFLINE)!r})
                        module = importlib.util.module_from_spec(spec)
                        sys.modules[spec.name] = module
                        spec.loader.exec_module(module)
                        terminate = module.terminate
                        def interrupted(signum, frame):
                            os.write({ready_write}, b"signal")
                            terminate(signum, frame)
                        module.terminate = interrupted
                        popen = module.subprocess.Popen
                        def spawn(argv, **kwargs):
                            preflight = argv[argv.index("-o") + 1].endswith("/preflight.trace")
                            if preflight and {phase!r} == "preflight":
                                argv[-1] += "; import signal; signal.pause()"
                            child = popen(argv, **kwargs)
                            if preflight == ({phase!r} == "preflight"):
                                open({str(pidfile)!r}, "w").write(str(child.pid))
                                if {phase!r} == "spawn":
                                    os.write({ready_write}, b"ready")
                                    assert select.select([{gate_read}], [], [], 20)[0]
                                    os.read({gate_read}, 1)
                                else:
                                    wait = child.wait
                                    def ready_wait(*args, **kwargs):
                                        os.write({ready_write}, b"ready")
                                        return wait(*args, **kwargs)
                                    child.wait = ready_wait
                            return child
                        module.subprocess.Popen = spawn
                        raise SystemExit(module.main())
                        """)
                    args = [sys.executable, "-c", launcher, "--evidence", str(prefix),
                            "--expect-exit", "0", "--", sys.executable, "-c", "import signal; signal.pause()"]
                    observer = subprocess.Popen(args, pass_fds=(ready_write, gate_read),
                                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                    os.close(ready_write)
                    os.close(gate_read)
                    try:
                        self.assertTrue(select.select([ready_read], [], [], 20)[0])
                        self.assertEqual(os.read(ready_read, 5), b"ready")
                        observer.send_signal(signum)
                        if phase == "spawn":
                            self.assertTrue(select.select([ready_read], [], [], 20)[0])
                            self.assertEqual(os.read(ready_read, 6), b"signal")
                            try:
                                os.write(gate_write, b"x")
                            except BrokenPipeError:
                                pass
                        out, err = observer.communicate(timeout=20)
                        self.assertEqual(observer.returncode, 128 + signum, (out, err))
                        evidence = self.evidence(prefix)
                        self.assertEqual(evidence["status"], "interrupted")
                        self.assertEqual(evidence["descendants_after_exit"], [])
                        self.assertIn(self.process_state(int(pidfile.read_text())), (None, "Z"))
                    finally:
                        os.close(ready_read)
                        os.close(gate_write)
                        if observer.poll() is None:
                            observer.kill()
                            observer.wait(timeout=10)
                        if pidfile.exists():
                            try:
                                os.killpg(int(pidfile.read_text()), signal.SIGKILL)
                            except ProcessLookupError:
                                pass

    def test_mps_control_connect_is_refused_by_default(self):
        # The recorded control-connect shape replayed through the fake tracer: with no
        # opt-in the policy is exactly the strict one that recorded it as an attempt.
        fixture = self.root / "mps-default.trace"
        fixture.write_text(MPS_CONTROL_CONNECTS_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("mps-default")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["allow_nvidia_mps"], False, evidence)
        self.assertEqual(evidence["allowed_local"], [])
        self.assertEqual([entry["detail"] for entry in evidence["attempts"]],
                         ["socket(AF_INET)", "connect(AF_UNIX)", "connect(AF_UNIX)"], evidence["attempts"])

    def test_mps_opt_in_records_complete_success_and_failure_connects_as_ipc(self):
        fixture = self.root / "mps-ipc.trace"
        fixture.write_text(MPS_CONTROL_CONNECTS_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("mps-ipc")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env, observer_args=MPS_OPT_IN)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["allow_nvidia_mps"], True, evidence)
        self.assertEqual(evidence["invalid_reasons"], [])
        self.assertEqual(evidence["trace"]["unparsed"], [])
        # Only the preflight fixture line is still a violation: both control connects, the
        # failed one and the successful one, moved to the allowed local IPC records.
        self.assertEqual([entry["detail"] for entry in evidence["attempts"]], ["socket(AF_INET)"], evidence["attempts"])
        self.assertEqual([entry["detail"] for entry in evidence["allowed_local"]],
                         ["connect(AF_UNIX) NVIDIA MPS control IPC /run/nvidia-mps/control",
                          "connect(AF_UNIX) NVIDIA MPS control IPC /tmp/nvidia-mps/control"], evidence["allowed_local"])

    def test_real_strace_probe_decides_the_two_control_paths_by_policy(self):
        # The positive control for the CLI option is a live probe, not a fixture: the same
        # two connects are refused by default and recorded as IPC only when opted in.
        strict = self.prefix("mps-probe-strict")
        strict_run = self.run_observer(strict, 0, [sys.executable, str(self.probe), "mps"])
        self.assertEqual(strict_run.returncode, 1, strict_run.stdout + strict_run.stderr)
        strict_evidence = self.evidence(strict)
        self.assertEqual(strict_evidence["allow_nvidia_mps"], False, strict_evidence)
        self.assertEqual([entry["detail"] for entry in strict_evidence["attempts"]], ["connect(AF_UNIX)"] * 2,
                         strict_evidence["attempts"])

        opted = self.prefix("mps-probe-opted")
        opted_run = self.run_observer(opted, 0, [sys.executable, str(self.probe), "mps"], observer_args=MPS_OPT_IN)
        self.assertEqual(opted_run.returncode, 0, opted_run.stdout + opted_run.stderr)
        opted_evidence = self.evidence(opted)
        self.assertEqual(opted_evidence["status"], "clean")
        self.assertEqual(opted_evidence["allow_nvidia_mps"], True, opted_evidence)
        self.assertEqual(opted_evidence["attempts"], [])
        self.assertEqual(opted_evidence["invalid_reasons"], [])
        self.assertEqual([entry["detail"] for entry in opted_evidence["allowed_local"] if entry["syscall"] == "connect"],
                         ["connect(AF_UNIX) NVIDIA MPS control IPC /run/nvidia-mps/control",
                          "connect(AF_UNIX) NVIDIA MPS control IPC /tmp/nvidia-mps/control"], opted_evidence["allowed_local"])

    def test_mps_opt_in_refuses_nearby_paths(self):
        fixture = self.root / "mps-nearby.trace"
        fixture.write_text(MPS_NEARBY_PATHS_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("mps-nearby")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env, observer_args=MPS_OPT_IN)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["allowed_local"], [])
        self.assertEqual([entry["detail"] for entry in evidence["attempts"]],
                         ["socket(AF_INET)"] + ["connect(AF_UNIX)"] * 4, evidence["attempts"])

    def test_mps_opt_in_refuses_other_unix_destinations_and_addressed_sends(self):
        for mode, expected in (("unix", "connect(AF_UNIX)"),
                               ("mps-abstract", "connect(AF_UNIX)"),
                               ("unixgram", "sendto with destination address")):
            with self.subTest(mode=mode):
                prefix = self.prefix(f"mps-other-{mode}")
                run = self.run_observer(prefix, 0, [sys.executable, str(self.probe), mode], observer_args=MPS_OPT_IN)
                self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertEqual(evidence["allow_nvidia_mps"], True, evidence)
                self.assertEqual(evidence["status"], "attempts", evidence)
                self.assertIn(expected, [entry["detail"] for entry in evidence["attempts"]], evidence["attempts"])
                self.assertEqual([entry for entry in evidence["allowed_local"] if entry["syscall"] == "connect"], [])

    def test_mps_opt_in_does_not_relax_inet_families(self):
        for mode, expected in (("tcp4", "socket(AF_INET)"), ("tcp6", "socket(AF_INET6)")):
            with self.subTest(mode=mode):
                prefix = self.prefix(f"mps-inet-{mode}")
                run = self.run_observer(prefix, 0, [sys.executable, str(self.probe), mode], observer_args=MPS_OPT_IN)
                self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertIn(expected, [entry["detail"] for entry in evidence["attempts"]], evidence["attempts"])

    def test_mps_opt_in_keeps_truncated_control_records_invalid(self):
        for name, text in (("cut-mps-args", CUT_MPS_CONNECT_ARGS_TRACE), ("cut-mps-result", CUT_MPS_CONNECT_RESULT_TRACE)):
            with self.subTest(name=name):
                fixture = self.root / f"{name}.trace"
                fixture.write_text(text)
                path, env = self.fake_strace(f"fixture:{fixture}")
                prefix = self.prefix(name)
                run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env, observer_args=MPS_OPT_IN)
                self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
                evidence = self.evidence(prefix)
                self.assertEqual(evidence["status"], "invalid")
                self.assertEqual(evidence["allowed_local"], [])
                self.assertEqual([entry["line"] for entry in evidence["trace"]["unparsed"]], [text.splitlines()[-1]], evidence["trace"])

    def test_mps_opt_in_keeps_unfinished_and_resumed_forms_honest(self):
        fixture = self.root / "paired-mps.trace"
        fixture.write_text(PAIRED_MPS_CONNECT_TRACE)
        path, env = self.fake_strace(f"fixture:{fixture}")
        prefix = self.prefix("paired-mps")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env, observer_args=MPS_OPT_IN)
        self.assertEqual(run.returncode, 1, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual((evidence["trace"]["unfinished"], evidence["trace"]["resumed"]), (1, 1))
        self.assertEqual(evidence["invalid_reasons"], [])
        self.assertEqual([entry["detail"] for entry in evidence["allowed_local"]],
                         ["connect(AF_UNIX) NVIDIA MPS control IPC /run/nvidia-mps/control"], evidence["allowed_local"])
        self.assertEqual([entry["detail"] for entry in evidence["attempts"]], ["socket(AF_INET)"], evidence["attempts"])

        # The exception cannot hide a pending record: an unfinished control connect whose
        # resumed line never arrived leaves the whole trace invalid.
        dangling = self.root / "dangling-mps.trace"
        dangling.write_text(DANGLING_MPS_CONNECT_TRACE)
        path, env = self.fake_strace(f"fixture:{dangling}")
        prefix = self.prefix("dangling-mps")
        run = self.run_observer(prefix, 0, [sys.executable, "-c", "pass"], path=path, env_extra=env, observer_args=MPS_OPT_IN)
        self.assertEqual(run.returncode, 2, run.stdout + run.stderr)
        evidence = self.evidence(prefix)
        self.assertEqual(evidence["status"], "invalid")
        self.assertTrue(any("unfinished" in reason for reason in evidence["invalid_reasons"]), evidence["invalid_reasons"])
        self.assertEqual([entry["detail"] for entry in evidence["attempts"]], ["socket(AF_INET)"], evidence["attempts"])


if __name__ == "__main__":
    if shutil.which("strace") is None:
        sys.exit("strace is required to check the observer; provision it in the test image")
    unittest.main()
