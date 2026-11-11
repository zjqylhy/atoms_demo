"""Activate a verified release while preserving server-only environment files."""
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import subprocess
import sys
import urllib.request

root = Path('/opt/atoms_lab')
target = Path(sys.argv[1]).resolve()
assert target.parent == root/'releases' and re.fullmatch(r'[0-9]{8}T[0-9]{6}Z', target.name)
assert (target/'atoms').is_file() and Path('/etc/atoms-lab/app.env').is_file()
with sqlite3.connect('file:' + str(root/'data/atoms.db') + '?mode=ro', uri=True) as db:
    assert not any(json.loads(row[0]).get('active_run') for row in db.execute('SELECT state FROM projects')), 'An application task is still running'
with urllib.request.urlopen('http://127.0.0.1:8091/healthz', timeout=5) as response:
    assert not json.load(response)['busy'], 'An execution job is still running'
subprocess.run(['python3',str(root/'current/deploy/backup.py')],check=True)
previous = str((root/'current').resolve())
subprocess.run(['systemctl','stop','atoms-web','atoms-runner'],check=True)
for service in ['atoms-web.service','atoms-runner.service']:
    shutil.copyfile(target/'deploy'/service,Path('/etc/systemd/system')/service)
link = root/('current-'+target.name)
link.symlink_to(target,target_is_directory=True)
os.replace(link,root/'current')
subprocess.run(['systemctl','daemon-reload'],check=True)
subprocess.run(['systemctl','start','atoms-runner','atoms-web'],check=True)
receipt = json.loads((root/'release-receipt.json').read_text())
receipt.update(release=str(target),previous=previous)
(root/'release-receipt.json').write_text(json.dumps(receipt,indent=2))
print(json.dumps(receipt))
