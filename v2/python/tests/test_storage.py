import unittest
import sys
import os

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '..')))

from anhurdb.storage.filesystem import FileStorage

class TestFileStorage(unittest.TestCase):
    def test_build_path(self):
        fs = FileStorage("/data/storage")
        path = fs.build_path("tenant_123", "sess_abc", 42)
        expected = os.path.join("/data/storage", "tenant_123", "sess_abc", "42.gz")
        self.assertEqual(path, expected)

if __name__ == '__main__':
    unittest.main()
