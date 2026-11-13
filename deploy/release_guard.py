"""Fail closed when an old executable cannot preserve account/data semantics."""
import json
import sqlite3

def compatible_rollback(release, database):
    marker = release / 'deploy/data-schema.json'
    if marker.is_file():
        schema = json.loads(marker.read_text())
        if schema.get('account_schema', 0) >= 1 and schema.get('app_data_schema', 0) >= 1:
            return True
    with sqlite3.connect('file:' + str(database) + '?mode=ro', uri=True) as db:
        tables = {row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        for table in ['users', 'workspace_accounts', 'app_states', 'share_links']:
            if table in tables and db.execute('SELECT COUNT(*) FROM ' + table).fetchone()[0]:
                return False
    return True
