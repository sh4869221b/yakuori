#!/usr/bin/env python3
"""Deterministic supervisor checks; these do not exercise inference."""
import json
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
