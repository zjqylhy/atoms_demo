"""Consistent SQLite backup; never copy a live database without its WAL."""
from datetime import datetime, timezone
from pathlib import Path
import os
import sqlite3

os.umask(0o077)
root = Path('/opt/atoms_lab')
backups = root / 'backups'
backups.mkdir(mode=0o700, exist_ok=True)
target = backups / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '.db')
with sqlite3.connect('file:' + str(root / 'data/atoms.db') + '?mode=ro', uri=True) as source:
    with sqlite3.connect(target) as destination:
        source.backup(destination)
        assert destination.execute('PRAGMA integrity_check').fetchone()[0] == 'ok'
        projects = destination.execute('SELECT COUNT(*) FROM projects').fetchone()[0]
print('Verified backup:', target, 'projects:', projects)
