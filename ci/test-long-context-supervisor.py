#!/usr/bin/env python3
"""Deterministic supervisor checks; these do not exercise inference."""
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

SCRIPT = pathlib.Path(__file__).with_name('run-long-context.py')

class SupervisorTests(unittest.TestCase):
    def test_failure_preserved_and_higher_stages_not_attempted(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake = root / 'fake'
            fake.write_text('#!/bin/sh\necho fake-failure >&2\nexit 3\n')
            fake.chmod(0o700)
            output = root / 'result'
            run = subprocess.run([sys.executable, str(SCRIPT), str(fake), 'missing-model', str(output), '--stages', '1024,4096'], capture_output=True, text=True)
            self.assertNotEqual(run.returncode, 0)
            evidence = json.loads((output/'supervisor.json').read_text())
            self.assertEqual(len(evidence), 1)
            self.assertEqual(evidence[0]['returncode'], 3)
            self.assertEqual((output/'1024-stop.stderr').read_text(), 'fake-failure\n')
            self.assertFalse((output/'4096-stop.json').exists())

    def test_default_and_override_budgets_reach_children(self):
        for options, timeout, memory in (
            ([], '300', '2300'),
            (['--request-timeout-seconds', '17', '--process-wall-seconds', '19',
              '--rss-limit-mib', '4096', '--host-available-min-mib', '1',
              '--go-memory-limit-mib', '2048'], '17', '2048'),
        ):
            with self.subTest(options=options), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                fake = root / 'fake'
                fake.write_text('#!/bin/sh\nprintf "%s\\n" "$@" "$GOMEMLIMIT" "$GOMAXPROCS" "${GOINFER_TEST-unset}"\n')
                fake.chmod(0o700)
                output = root / 'result'
                run = subprocess.run([sys.executable, str(SCRIPT), str(fake), 'model', str(output),
                                      '--stages', '1024,4096', *options], capture_output=True, text=True,
                                     env=dict(os.environ, GOINFER_TEST='ambient'))
                self.assertEqual(run.returncode, 0, run.stderr)
                evidence = json.loads((output/'supervisor.json').read_text())
                self.assertEqual([(r['target'], r['mode']) for r in evidence],
                                 [(1024, 'stop'), (1024, 'deadline'), (4096, 'stop'), (4096, 'deadline')])
                for record in evidence:
                    self.assertIsNone(record['watchdog_kill'])
                    self.assertEqual(record['returncode'], 0)
                    self.assertEqual((output/f"{record['target']}-{record['mode']}.json").read_text().splitlines(),
                                     ['long', 'model', str(record['target']), record['mode'],
                                      f'--request-timeout={timeout}s', f'{memory}MiB', '2', 'unset'])

    def test_invalid_budgets_rejected_before_output_or_spawn(self):
        options = ('request-timeout-seconds', 'process-wall-seconds', 'rss-limit-mib',
                   'host-available-min-mib', 'go-memory-limit-mib')
        cases = [[f'--{name}', value] for name in options for value in ('0', '-1')]
        cases += [['--process-wall-seconds', '300'], ['--process-wall-seconds', '299']]
        for options in cases:
            with self.subTest(options=options), tempfile.TemporaryDirectory() as tmp:
                output = pathlib.Path(tmp) / 'result'
                run = subprocess.run([sys.executable, str(SCRIPT), 'missing-binary', 'model',
                                      str(output), *options], capture_output=True, text=True)
                self.assertEqual(run.returncode, 2, run.stderr)
                self.assertFalse(output.exists())
                self.assertNotIn('Traceback', run.stderr)

    def test_configured_watchdogs_stop_later_cases(self):
        total = next(int(s.split()[1]) for s in pathlib.Path('/proc/meminfo').read_text().splitlines()
                         if s.startswith('MemTotal:'))
        host_limit = total // 1024 + 1
        for options, reason in (
            ([], '2-second process wall limit'),
            (['--rss-limit-mib', '1'], 'RSS > 1 MiB'),
            (['--host-available-min-mib', str(host_limit)], f'host MemAvailable < {host_limit} MiB'),
        ):
            with self.subTest(options=options), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                fake = root / 'fake'
                fake.write_text(f'#!{sys.executable}\nimport time\nprint("started", flush=True)\ntime.sleep(10)\n')
                fake.chmod(0o700)
                output = root / 'result'
                run = subprocess.run([sys.executable, str(SCRIPT), str(fake), 'model', str(output),
                                      '--stages', '1024,4096', '--request-timeout-seconds', '1',
                                      '--process-wall-seconds', '2', *options], capture_output=True, text=True)
                self.assertNotEqual(run.returncode, 0)
                evidence = json.loads((output/'supervisor.json').read_text())
                self.assertEqual(len(evidence), 1)
                self.assertEqual(evidence[0]['returncode'], -9)
                self.assertEqual(evidence[0]['watchdog_kill'], reason)
                self.assertFalse((output/'1024-deadline.json').exists())
                self.assertFalse((output/'4096-stop.json').exists())
                if not options:
                    self.assertEqual((output/'1024-stop.json').read_text(), 'started\n')

    def test_existing_evidence_not_overwritten(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            old = root/'supervisor.json'
            old.write_text('existing evidence')
            run = subprocess.run([sys.executable, str(SCRIPT), 'missing-binary', 'missing-model', tmp], capture_output=True, text=True)
            self.assertNotEqual(run.returncode, 0)
            self.assertIn('output directory must be empty', run.stderr)
            self.assertEqual(old.read_text(), 'existing evidence')

if __name__ == '__main__':
    unittest.main()
