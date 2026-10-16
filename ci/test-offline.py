#!/usr/bin/env python3
"""Deterministic checks for ci/offline.py; requires strace on PATH."""
import json
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

OFFLINE = Path(__file__).with_name("offline.py")

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

    def thread():
        worker = threading.Thread(target=tcp4)
        worker.start()
        worker.join()

    def child():
        subprocess.run([sys.executable, __file__, "tcp4"], check=False)

    MODES = {"tcp4": tcp4, "tcp6": tcp6, "udp4": udp4, "udp6": udp6, "dns": dns, "unix": unix,
             "unixgram": unixgram, "unixpair": unixpair, "thread": thread, "child": child}
    MODES[sys.argv[1]]()
    """
)

FAKE_STRACE = textwrap.dedent(
    """\
    #!/usr/bin/env python3
    import os, shutil, subprocess, sys

    mode = os.environ["FAKE_STRACE_MODE"]
    args = sys.argv[1:]
    trace = args[args.index("-o") + 1]
    command = args[args.index("--") + 1:]
    if mode == "missing":
        sys.stderr.write("strace: exec: No such file or directory\\n")
        sys.exit(127)
    if mode == "empty":
        open(trace, "w").close()
    else:
        shutil.copyfile(mode.split(":", 1)[1], trace)
    sys.exit(subprocess.run(command).returncode)
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

    def run_observer(self, prefix, expect_exit, command, timeout_seconds=None, path=None, env_extra=None, replace_path=False):
        args = [sys.executable, str(OFFLINE), "--evidence", str(prefix), "--expect-exit", str(expect_exit)]
        if timeout_seconds is not None:
            args += ["--timeout-seconds", str(timeout_seconds)]
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
        return str(directory), {"FAKE_STRACE_MODE": mode}

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
        self.assertEqual(self.process_field(daemon, 1), "1", "the daemon must be reparented to init")
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


if __name__ == "__main__":
    if shutil.which("strace") is None:
        sys.exit("strace is required to check the observer; provision it in the test image")
    unittest.main()
