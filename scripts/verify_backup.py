"""Exercise backup and rollback policy using a local acceptance database."""
import argparse
import importlib.util
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile

parser=argparse.ArgumentParser()
parser.add_argument('--source',type=Path,required=True)
parser.add_argument('--output-dir',type=Path,required=True)
args=parser.parse_args()
root=Path(__file__).resolve().parents[1]
args.output_dir.mkdir(parents=True,exist_ok=True)
result=subprocess.run([sys.executable,str(root/'deploy/backup.py'),'--source',str(args.source),'--output-dir',str(args.output_dir)],check=True,capture_output=True,text=True)
receipt=json.loads(result.stdout)
with sqlite3.connect(args.source) as original,sqlite3.connect(receipt['backup']) as backup:
    for table in receipt['tables']:
        query='SELECT * FROM "'+table.replace('"','""')+'" ORDER BY rowid'
        assert original.execute(query).fetchall()==backup.execute(query).fetchall(),table
spec=importlib.util.spec_from_file_location('release_guard',root/'deploy/release_guard.py')
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
with tempfile.TemporaryDirectory(prefix='atoms-old-release-') as previous:
    assert not module.compatible_rollback(Path(previous),Path(receipt['backup']))
    assert module.compatible_rollback(root,Path(receipt['backup']))
receipt.update(all_table_rows_equal=True,legacy_rollback_rejected=True,compatible_rollback_allowed=True)
(args.output_dir/'verification.json').write_text(json.dumps(receipt,indent=2),encoding='utf-8')
print(json.dumps(receipt))
