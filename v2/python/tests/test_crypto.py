import sys
import os
import unittest

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '..')))

from anhurdb.crypto.quantizer import cosine_similarity

class TestCrypto(unittest.TestCase):
    def test_cosine_similarity(self):
        # Stub test validating the lack of implementation locally
        with self.assertRaises(NotImplementedError):
            cosine_similarity([1.0], [1.0])

if __name__ == '__main__':
    unittest.main()
