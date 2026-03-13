import unittest
import sys
import os

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '..')))

from anhurdb.client.connection import HTTPConnection
from anhurdb.client import AnhurClient
from anhurdb.client.exceptions import AnhurAuthError

class TestClient(unittest.TestCase):
    def test_connection_init(self):
        conn = HTTPConnection("http://localhost:8080/", "test_key")
        self.assertEqual(conn.base_url, "http://localhost:8080")
        self.assertEqual(conn.headers["Authorization"], "Bearer test_key")
        
    def test_anhur_client_facade(self):
        client = AnhurClient("http://localhost:8080", "test_key")
        self.assertIsNotNone(client.memories)
        
        # Test query builder spawns correctly
        builder = client.memories.select("id").where(type="risk")
        ast = builder.build_ast()
        self.assertEqual(ast["filters"]["type"]["$eq"], "risk")

if __name__ == '__main__':
    unittest.main()
