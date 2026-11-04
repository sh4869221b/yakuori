#!/usr/bin/env python3
"""Linux-only regression evidence; temporary fixtures never edit the checkout."""
from pathlib import Path
import argparse, json, os, signal, subprocess, tempfile, time

parser = argparse.ArgumentParser(description='Exercise the real verification prefix with controlled child failures.')
parser.add_argument('--repo', type=Path, default=Path(__file__).resolve().parents[3])
args = parser.parse_args()
source = (args.repo / 'ci/verify.sh').read_text()
assert source.count('# Model-free acquisition failures') == 1
prefix = source.split('# Model-free acquisition failures', 1)[0] + "printf 'MAIN_COMPLETE\\n'\n"
go_stub = '''#!/usr/bin/python3
import os, pathlib, sys, time
args = sys.argv[1:]
root = pathlib.Path(os.environ['STUB_ROOT'])
if args[:1] == ['env']:
    print({'GOOS':'linux', 'GOARCH':'amd64', 'GOVERSION':'go1.27.3'}[args[1]])
elif args[:1] == ['version']:
    print('go version go1.27.3 linux/amd64')
elif args[:2] == ['run', 'ci/check-cgo.go']:
    if len(args) > 2:
        sys.exit(1)
    root.joinpath('audit-started').touch()
    sys.exit(int(os.environ.get('STUB_AUDIT_EXIT','0')))
elif args[:1] == ['build']:
    root.joinpath('build-started').touch()
    time.sleep(0.12)
    root.joinpath('build-finished').touch()
    sys.exit(int(os.environ.get('STUB_BUILD_EXIT','0')))
'''
observer = '''import json, os, pathlib, signal, subprocess, sys, time
root = pathlib.Path(os.environ['STUB_ROOT'])
root.joinpath('observer-started').touch()
root.joinpath('signals.json').write_text(json.dumps({'int_default':signal.getsignal(signal.SIGINT)==signal.default_int_handler,'quit_default':signal.getsignal(signal.SIGQUIT)==signal.SIG_DFL}))
child = subprocess.Popen([sys.executable, '-c', 'import pathlib,sys,time; time.sleep(0.35); pathlib.Path(sys.argv[1]).touch()',str(root/'observer-child-finished')])
print('OBSERVER_STDOUT',flush=True)
print('OBSERVER_STDERR',file=sys.stderr,flush=True)
child.wait()
root.joinpath('observer-finished').touch()
sys.exit(int(os.environ.get('STUB_OBSERVER_EXIT','0')))
'''
cases = [('success',0,0,0,None),('observer_failure',0,0,17,None),('audit_failure',23,0,0,None),('both_fail',23,0,17,None),('build_failure',0,29,0,None),('term',0,0,0,signal.SIGTERM),('int',0,0,0,signal.SIGINT),('hup',0,0,0,signal.SIGHUP),('launch_term',0,0,0,None)]
for name,audit,build,obs,sig in cases:
    with tempfile.TemporaryDirectory(prefix='yakuori-observer-control-') as directory:
        root=Path(directory)
        (root/'ci').mkdir(); (root/'tools').mkdir(); (root/'tmp').mkdir()
        (root/'verify.sh').write_text(prefix.replace('observer_pid=$!\n', 'kill -TERM $$\nobserver_pid=$!\n', 1) if name == 'launch_term' else prefix)
        (root/'ci/test-offline.py').write_text(observer)
        (root/'ci/test-test-events.py').write_text('pass\n')
        (root/'tools/go').write_text(go_stub); (root/'tools/go').chmod(0o755)
        (root/'tools/gofmt').write_text('#!/bin/sh\nexit 0\n'); (root/'tools/gofmt').chmod(0o755)
        env=dict(os.environ,STUB_ROOT=str(root),STUB_AUDIT_EXIT=str(audit),STUB_BUILD_EXIT=str(build),STUB_OBSERVER_EXIT=str(obs),TMPDIR=str(root/'tmp'),PATH=str(root/'tools')+':'+os.environ['PATH'])
        started=time.monotonic()
        run=subprocess.Popen(['/bin/sh','verify.sh'],cwd=root,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True)
        if sig:
            deadline=time.monotonic()+5
            while not (root/'build-finished').exists():
                assert run.poll() is None, name
                assert time.monotonic()<deadline, name
                time.sleep(.005)
            time.sleep(.02)
            run.send_signal(sig)
            time.sleep(.02)
            run.send_signal(signal.SIGTERM)
        stdout,stderr=run.communicate(timeout=10)
        expected=143 if name == 'launch_term' else 128+sig if sig else audit or build or obs
        assert run.returncode==expected,(name,run.returncode,expected,stdout,stderr)
        assert (root/'observer-child-finished').exists(),(name,'child teardown incomplete')
        assert (root/'observer-finished').exists(),(name,'observer not joined')
        assert stdout.count('OBSERVER_STDOUT')==1,(name,stdout)
        assert stdout.count('OBSERVER_STDERR')==1,(name,stdout)
        assert ('MAIN_COMPLETE' in stdout)==(expected==0),(name,stdout)
        assert not list((root/'tmp').iterdir()),(name,'leaked temp dirs')
        assert all(json.loads((root/'signals.json').read_text()).values()),(name,'foreground signal defaults not restored')
        print(f'{name}: exit={expected}, child joined, logs emitted once, temporary dirs removed, signals restored ({time.monotonic()-started:.3f}s)')
print('All 9 actual shell-control-flow cases passed')
