"""Activate a verified release while preserving server-only environment files."""
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
import urllib.request
from release_guard import compatible_rollback

root = Path('/opt/atoms_lab')
target = Path(sys.argv[1]).resolve()
assert target.parent == root/'releases' and re.fullmatch(r'[0-9]{8}T[0-9]{6}Z', target.name)
assert (target/'atoms').is_file() and Path('/etc/atoms-lab/app.env').is_file()
assert 2 <= len(sys.argv) <= 3
runner_image = sys.argv[2] if len(sys.argv) == 3 else None
if (target/'deploy/data-schema.json').is_file():
    assert runner_image, 'This release requires a verified runner image with preview data host API 1'
    runtime_api = subprocess.check_output(['docker','image','inspect',runner_image,'--format','{{index .Config.Labels "org.atoms.preview-runtime"}}'], text=True).strip()
    assert runtime_api == '1', 'Runner image does not include preview data host API 1'
runner_env = Path('/etc/atoms-lab/runner.env')
runner_env_backup = None
runner_env_next = None
if runner_image:
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', runner_image), 'Expected a verified Docker image ID'
    subprocess.run(['docker', 'image', 'inspect', runner_image], stdout=subprocess.DEVNULL, check=True)
    runner_env_next, count = re.subn(
        rb'^ATOMS_RUNNER_IMAGE=[^\r\n]*$', f'ATOMS_RUNNER_IMAGE="{runner_image}"'.encode(),
        runner_env.read_bytes(), flags=re.M,
    )
    assert count == 1, 'Expected one ATOMS_RUNNER_IMAGE setting'


def runner_health():
    with urllib.request.urlopen('http://127.0.0.1:8091/healthz', timeout=3) as response:
        return json.load(response)


def ensure_idle():
    with sqlite3.connect('file:' + str(root/'data/atoms.db') + '?mode=ro', uri=True) as db:
        assert not any(json.loads(row[0]).get('active_run') for row in db.execute('SELECT state FROM projects')), 'An application task is still running'
    assert not runner_health()['busy'], 'An execution job is still running'


def write_runner_env(content):
    # Write beside the existing root-only environment file and atomically replace it.
    with tempfile.NamedTemporaryFile(dir=runner_env.parent, delete=False) as staged:
        staged.write(content)
        staged.flush()
        os.fsync(staged.fileno())
    os.replace(staged.name, runner_env)


def activate(release):
    for service in ['atoms-web.service','atoms-runner.service']:
        shutil.copyfile(release/'deploy'/service, Path('/etc/systemd/system')/service)
    link = root/f'current-{release.name}-{os.getpid()}'
    link.symlink_to(release, target_is_directory=True)
    os.replace(link, root/'current')
    subprocess.run(['systemctl','daemon-reload'], check=True)
    subprocess.run(['systemctl','start','atoms-runner','atoms-web'], check=True)
    for attempt in range(15):
        try:
            assert runner_health()['ok']
            subprocess.run(['systemctl','is-active','--quiet','atoms-web','atoms-runner'], check=True)
            return
        except (OSError, AssertionError, subprocess.CalledProcessError):
            if attempt == 14:
                raise
            time.sleep(1)


ensure_idle()
subprocess.run(['python3',str(root/'current/deploy/backup.py')], check=True)
previous = (root/'current').resolve()
if runner_env_next is not None:
    runner_env_backup = runner_env.with_name(f'runner.env.{target.name}.previous')
    shutil.copy2(runner_env, runner_env_backup)
ensure_idle()
subprocess.run(['systemctl','stop','atoms-web','atoms-runner'], check=True)
try:
    if runner_env_next is not None:
        write_runner_env(runner_env_next)
    activate(target)
except Exception:
    subprocess.run(['systemctl','stop','atoms-web','atoms-runner'], check=True)
    if runner_env_backup is not None:
        write_runner_env(runner_env_backup.read_bytes())
    if compatible_rollback(previous, root/'data/atoms.db'):
        activate(previous)
    else:
        raise RuntimeError('Previous release cannot preserve login, shared-project, quota and deletion semantics. Services remain stopped; use a compatible recovery build. Database has not been restored or erased.')
    raise
receipt = json.loads((root/'release-receipt.json').read_text())
receipt.update(release=str(target),previous=str(previous))
if runner_image:
    receipt.update(runner_image=runner_image, runner_env_backup=str(runner_env_backup))
(root/'release-receipt.json').write_text(json.dumps(receipt,indent=2))
print(json.dumps(receipt))
