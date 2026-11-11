"""Single-job capacity observation on the audited Linux host, with no model calls."""
import json
import os
from pathlib import Path
import subprocess
import threading
import time
import urllib.request

root = Path(__file__).resolve().parents[1]
os.chdir(root)
env = os.environ.copy()
env.update(PATH='/opt/atoms_lab/runtime/node-v24.21.0-linux-x64/bin:' + env['PATH'],
           ATOMS_ACCEPTANCE_RUNTIME='runsc', ATOMS_SANDBOX_CPUS='0.5')
def services():
    ids = subprocess.check_output(['docker', 'ps', '-q'], text=True).split()
    records = json.loads(subprocess.check_output(['docker', 'inspect', *ids]))
    return {r['Name']: {'id': r['Id'], 'restart_count': r['RestartCount'], 'running': r['State']['Running']}
            for r in records if not r['Name'].startswith('/atoms-')}

before = services()
gateway_ip = subprocess.check_output(['docker','inspect','llm-gateway-lite-gateway-1',
                                     '--format','{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'], text=True).strip()
stop = threading.Event()
samples = []
health = []
def sample():
    count = 0
    while not stop.is_set():
        mem = {line.split(':')[0]: int(line.split()[1]) for line in Path('/proc/meminfo').read_text().splitlines()}
        samples.append({'at': time.time(), 'available_kib': mem['MemAvailable'],
                        'load1': float(Path('/proc/loadavg').read_text().split()[0])})
        if count % 10 == 0:
            start = time.monotonic()
            try:
                with urllib.request.urlopen('http://' + gateway_ip + ':8080/healthz', timeout=3) as response:
                    status = response.status
            except Exception as e:
                status = type(e).__name__
            health.append({'status': status, 'ms': round((time.monotonic()-start)*1000, 1)})
        count += 1
        stop.wait(0.5)

observer = threading.Thread(target=sample)
observer.start()
started = time.time()
try:
    result = subprocess.run(['node', 'scripts/runner_acceptance.cjs'], env=env, timeout=300)
finally:
    stop.set()
    observer.join()
after = services()
report = {'started': started, 'duration_s': round(time.time()-started, 2),
          'exit_code': result.returncode, 'existing_services_unchanged': before == after,
          'minimum_available_mib': round(min(s['available_kib'] for s in samples)/1024, 1),
          'gateway_health': health, 'samples': samples, 'services_before': before, 'services_after': after}
(root/'artifacts').mkdir(exist_ok=True)
(root/'artifacts'/Path(env.get('ATOMS_CAPACITY_REPORT','cloud-capacity.json')).name).write_text(json.dumps(report, indent=2))
print(json.dumps({k:v for k,v in report.items() if k not in ['samples','services_before','services_after']}))
raise SystemExit(result.returncode)
