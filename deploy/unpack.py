"""Verify an explicitly staged release before extracting only regular files."""
import hashlib
import json
from pathlib import Path
import re
import sys
import tarfile

root = Path('/opt/atoms_lab')
staging_root = root/'staging'
staging = Path(sys.argv[1]).resolve() if len(sys.argv) == 2 else staging_root
assert staging == staging_root or staging_root in staging.parents
receipt = json.loads((staging/'release.json').read_text())
assert re.fullmatch(r'[0-9]{8}T[0-9]{6}Z', receipt['release'])
assert receipt['archive'] == 'atoms-' + receipt['release'] + '.tar.gz'
archive = staging/receipt['archive']
assert hashlib.sha256(archive.read_bytes()).hexdigest() == receipt['archive_sha256']
target = root/'releases'/receipt['release']
target.mkdir(parents=True, exist_ok=True)
with tarfile.open(archive) as tar:
    for member in tar.getmembers():
        assert target in (target/member.name).resolve().parents and member.isfile()
    tar.extractall(target, filter='data')
(target/'atoms').chmod(0o755)
for name, expected in receipt['files'].items():
    assert target in (target/name).resolve().parents
    assert hashlib.sha256((target/name).read_bytes()).hexdigest() == expected
print('Verified release:', target)
