import json
from pathlib import Path
import tempfile
import unittest
from release_guard import compatible_rollback


class ReleaseGuardTest(unittest.TestCase):
    def test_rollback_requires_behavior_and_schema(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'deploy').mkdir()
            marker = root / 'deploy/data-schema.json'
            database = root / 'untouched.db'
            database.write_bytes(b'not opened or modified by guard')
            self.assertFalse(compatible_rollback(root, database))
            current = json.loads(Path(__file__).with_name('data-schema.json').read_text())
            for missing in current:
                marker.write_text(json.dumps({k: v for k, v in current.items() if k != missing}))
                self.assertFalse(compatible_rollback(root, database), missing)
            for invalid in ['null', '{}', '{broken', '[]']:
                marker.write_text(invalid)
                self.assertFalse(compatible_rollback(root, database))
            marker.write_text(json.dumps(current))
            self.assertTrue(compatible_rollback(root, database))
            self.assertEqual(database.read_bytes(), b'not opened or modified by guard')


if __name__ == '__main__':
    unittest.main()
