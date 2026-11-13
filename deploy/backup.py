"""Consistent SQLite backup; never copy a live database without its WAL."""
from datetime import datetime, timezone
from pathlib import Path
import argparse
import json
import os
import sqlite3

os.umask(0o077)
root = Path('/opt/atoms_lab')
parser = argparse.ArgumentParser()
parser.add_argument('--source', type=Path, default=root/'data/atoms.db')
parser.add_argument('--output-dir', type=Path, default=root/'backups')
args = parser.parse_args()
backups = args.output_dir.resolve()
backups.mkdir(mode=0o700, exist_ok=True)
target = backups / (datetime.now(timezone.utc).strftime('%Y%m%dT%H%M%S%fZ') + '.db')
with sqlite3.connect(args.source.resolve().as_uri() + '?mode=ro', uri=True) as source:
    with sqlite3.connect(target) as destination:
        source.backup(destination)
        assert destination.execute('PRAGMA integrity_check').fetchone()[0] == 'ok'
        assert destination.execute('PRAGMA foreign_key_check').fetchall() == []
        tables = [row[0] for row in destination.execute("SELECT name FROM sqlite_master WHERE type='table'")]
        counts = {name: destination.execute('SELECT COUNT(*) FROM "'+name.replace('"','""')+'"').fetchone()[0] for name in tables}
print(json.dumps({'backup':str(target),'integrity':'ok','tables':counts}))
