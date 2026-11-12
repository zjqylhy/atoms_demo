"""Package only explicitly selected deployment artifacts; no runtime data or env."""
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import tarfile

root = Path(__file__).resolve().parents[1]
release = datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ')
files = {'atoms': root / 'bin/atoms-linux'}
for folder in ['web/dist', 'deploy']:
    for file in (root / folder).rglob('*'):
        if file.is_file() and '__pycache__' not in file.parts:
            files[file.relative_to(root).as_posix()] = file
files['runner/server.cjs'] = root / 'runner/server.cjs'
assert 'web/dist/index.html' in files and files['atoms'].is_file()
out = root / 'artifacts'
out.mkdir(exist_ok=True)
archive = out / ('atoms-' + release + '.tar.gz')
with tarfile.open(archive, 'w:gz', compresslevel=3) as tar:
    for name, file in sorted(files.items()):
        info = tar.gettarinfo(str(file), arcname=name)
        info.mode = 0o755 if name == 'atoms' else 0o644
        with file.open('rb') as stream:
            tar.addfile(info, stream)
receipt = {'release': release, 'archive': archive.name,
           'archive_sha256': hashlib.sha256(archive.read_bytes()).hexdigest(),
           'files': {name: hashlib.sha256(file.read_bytes()).hexdigest() for name, file in sorted(files.items())}}
(out / 'release.json').write_text(json.dumps(receipt, indent=2), encoding='utf-8')
print(json.dumps({k:v for k,v in receipt.items() if k != 'files'}))
