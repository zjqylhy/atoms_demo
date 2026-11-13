"""Fail closed when an old executable cannot preserve account/data semantics."""
import json

def compatible_rollback(release, database):
    # Schema compatibility alone cannot permit a rollback that restores guest
    # execution or charges a shared run to the storage owner's account.
    marker = release / 'deploy/data-schema.json'
    try:
        schema = json.loads(marker.read_text())
        required = ['account_schema', 'app_data_schema', 'preview_runtime_api',
                    'login_required', 'shared_projects', 'run_actor_quota', 'project_deletion']
        return all(type(schema.get(key)) is int and schema[key] >= 1 for key in required)
    except (OSError, ValueError, TypeError, AttributeError):
        return False
