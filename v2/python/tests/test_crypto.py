import sys
import os
import unittest

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '..')))

from anhurdb.crypto.quantizer import cosine_similarity

class TestCrypto(unittest.TestCase):
    def test_cosine_similarity(self):
        # Just a stub test for now
        res = cosine_similarity([1.0], [1.0])
        self.assertIsNone(res)

if __name__ == '__main__':
    unittest.main()
