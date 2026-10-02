#!/usr/bin/env python3
"""Bounded Linux-only experimental supervisor, not production admission policy."""
import argparse, json, os, pathlib, subprocess, time

p = argparse.ArgumentParser()
p.add_argument('binary')
p.add_argument('model')
p.add_argument('output')
p.add_argument('--stages', default='1024,4096,8192,16384,32766')
a = p.parse_args()
out = pathlib.Path(a.output)
out.mkdir(parents=True, exist_ok=True)
if any(out.iterdir()):
    raise SystemExit("output directory must be empty")
env = dict(os.environ, GOMAXPROCS='2', GOMEMLIMIT='2300MiB')
# Freeze the tested upstream defaults; prevent ambient tuning from changing evidence.
for name in list(env):
    if name.startswith('GOINFER_'):
        del env[name]
results = []
for target in map(int, a.stages.split(',')):
    for mode in ('stop', 'deadline'):
        stem = f'{target}-{mode}'
        start = time.monotonic()
        peak = 0
        killed = None
        with (out / (stem+'.json')).open('w') as stdout, (out / (stem+'.stderr')).open('w') as stderr:
            child = subprocess.Popen([a.binary, 'long', a.model, str(target), mode], stdout=stdout, stderr=stderr, env=env)
            while child.poll() is None:
                try:
                    lines = pathlib.Path(f'/proc/{child.pid}/status').read_text().splitlines()
                    rss = max([int(s.split()[1]) for s in lines if s.startswith(('VmRSS:', 'VmHWM:'))], default=0)
                    peak = max(peak, rss)
                    available = next(int(s.split()[1]) for s in pathlib.Path('/proc/meminfo').read_text().splitlines() if s.startswith('MemAvailable:'))
                    if rss > 3*1024*1024 or available < 1024*1024:
                        killed = 'RSS > 3 GiB or host MemAvailable < 1 GiB'
                except FileNotFoundError:
                    pass
                if time.monotonic()-start > 360:
                    killed = '360-second process wall limit'
                if killed:
                    child.kill()
                    break
                time.sleep(.05)
            code = child.wait()
        results.append(dict(target=target, mode=mode, returncode=code, watchdog_kill=killed, observed_peak_rss_kib=peak, process_wall_seconds=round(time.monotonic()-start,3)))
        (out/'supervisor.json').write_text(json.dumps(results, indent=2)+'\n')
        print(json.dumps(results[-1]), flush=True)
        if code != 0:
            raise SystemExit(f'{stem} failed; higher stages not attempted')
