#!/usr/bin/env python3
"""Bounded Linux-only experimental supervisor, not production admission policy."""
import argparse, json, os, pathlib, subprocess, time

p = argparse.ArgumentParser()
p.add_argument('binary')
p.add_argument('model')
p.add_argument('output')
p.add_argument('--translate-fixtures', type=pathlib.Path, help='directory of ordered research JSON fixtures')
p.add_argument('--contexts', default='1024,2048,4096')
p.add_argument('--generation-timeout-seconds', type=int, default=1500)
p.add_argument('--pairs', type=int, default=3)
p.add_argument('--deadline-probe', action='store_true')
p.add_argument('--max-output-tokens', type=int, help='translate output reservation override (default: context / 4)')
p.add_argument('--stages', default='1024,4096,8192,16384,32766')
p.add_argument('--request-timeout-seconds', type=int, default=300)
p.add_argument('--process-wall-seconds', type=int, default=360)
p.add_argument('--rss-limit-mib', type=int, default=3072)
p.add_argument('--host-available-min-mib', type=int, default=1024)
p.add_argument('--go-memory-limit-mib', type=int, default=2300)
a = p.parse_args()
for name in ('request_timeout_seconds', 'process_wall_seconds', 'rss_limit_mib', 'host_available_min_mib', 'go_memory_limit_mib'):
    if getattr(a, name) <= 0:
        p.error(f"--{name.replace('_', '-')} must be positive")
if a.max_output_tokens is not None and a.max_output_tokens <= 0:
    p.error('--max-output-tokens must be positive')
if a.process_wall_seconds <= a.request_timeout_seconds:
    p.error('--process-wall-seconds must exceed --request-timeout-seconds')
if a.translate_fixtures:
    if not a.translate_fixtures.is_dir():
        p.error('--translate-fixtures must be an existing directory')
    fixtures = sorted(a.translate_fixtures.glob('*.json'))
    if not fixtures:
        p.error('--translate-fixtures must contain at least one JSON fixture')
out = pathlib.Path(a.output)
out.mkdir(parents=True, exist_ok=True)
if any(out.iterdir()):
    raise SystemExit("output directory must be empty")
env = dict(os.environ, GOMAXPROCS='16' if a.translate_fixtures else '2', GOMEMLIMIT=f'{a.go_memory_limit_mib}MiB')
# Freeze the tested upstream defaults; prevent ambient tuning from changing evidence.
for name in list(env):
    if name.startswith('GOINFER_'):
        del env[name]
results = []
cases = []
if a.translate_fixtures:
    if a.generation_timeout_seconds <= 0 or a.pairs <= 0:
        p.error('generation timeout and pairs must be positive')
    for context in map(int, a.contexts.split(',')):
        for fixture in fixtures:
            for pair in range(1, a.pairs+1):
                cases.append((context, fixture.stem, pair, fixture))
else:
    cases = [(target, mode, 0, None) for target in map(int, a.stages.split(',')) for mode in ('stop', 'deadline')]
failed_conditions = set()
for target, mode, pair, fixture in cases:
        family = 'single' if mode.startswith('single') else 'units'
        if (target, family) in failed_conditions:
            results.append(dict(target=target, mode=mode, pair=pair, status='unrun: earlier failure in condition'))
            (out/'supervisor.json').write_text(json.dumps(results, indent=2)+'\n')
            continue
        stem = f'{target}-{mode}' + (f'-pair{pair}' if fixture else '')
        start = time.monotonic()
        peak = 0
        killed = None
        with (out / (stem+'.json')).open('w') as stdout, (out / (stem+'.stderr')).open('w') as stderr:
            command = [a.binary, 'long', a.model, str(target), mode, f'--request-timeout={a.request_timeout_seconds}s']
            if fixture:
                command = [a.binary, 'translate', '--model', a.model, '--fixture', str(fixture), '--context-tokens', str(target), '--max-output-tokens', str(a.max_output_tokens if a.max_output_tokens is not None else target//4), '--request-timeout', f'{a.request_timeout_seconds}s', '--generation-timeout', f'{a.generation_timeout_seconds}s']
                if a.deadline_probe:
                    command.append('--deadline-probe')
            child = subprocess.Popen(command, stdout=stdout, stderr=stderr, env=env)
            while child.poll() is None:
                try:
                    lines = pathlib.Path(f'/proc/{child.pid}/status').read_text().splitlines()
                    rss = max([int(s.split()[1]) for s in lines if s.startswith(('VmRSS:', 'VmHWM:'))], default=0)
                    peak = max(peak, rss)
                    available = next(int(s.split()[1]) for s in pathlib.Path('/proc/meminfo').read_text().splitlines() if s.startswith('MemAvailable:'))
                    if rss > a.rss_limit_mib*1024:
                        killed = f'RSS > {a.rss_limit_mib} MiB'
                    elif available < a.host_available_min_mib*1024:
                        killed = f'host MemAvailable < {a.host_available_min_mib} MiB'
                except FileNotFoundError:
                    pass
                if time.monotonic()-start > a.process_wall_seconds:
                    killed = f'{a.process_wall_seconds}-second process wall limit'
                if killed:
                    child.kill()
                    break
                time.sleep(.05)
            code = child.wait()
        memory_peak = pathlib.Path('/sys/fs/cgroup/memory.peak')
        results.append(dict(target=target, mode=mode, pair=pair, command=command, container_memory_peak_bytes=int(memory_peak.read_text()) if memory_peak.exists() else None, returncode=code, watchdog_kill=killed, observed_peak_rss_kib=peak, process_wall_seconds=round(time.monotonic()-start,3)))
        (out/'supervisor.json').write_text(json.dumps(results, indent=2)+'\n')
        print(json.dumps(results[-1]), flush=True)
        if code != 0:
            if fixture:
                failed_conditions.add((target, family))
            else:
                raise SystemExit(f'{stem} failed; higher stages not attempted')
if failed_conditions:
    raise SystemExit('translate conditions failed; higher stages recorded as unrun')
