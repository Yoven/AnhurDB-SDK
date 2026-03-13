import unittest
import sys
import os

# Add the parent directory to the path so we can import anhurdb
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '..')))

from anhurdb.query.builder import QueryBuilder
from anhurdb.query.operators import SemanticMode

class TestQueryBuilder(unittest.TestCase):

    def test_basic_fluent_ast(self):
        builder = QueryBuilder()
        builder.select("id", "summary")
        builder.where(type="risk", weight__gt=0.8)
        builder.order_by("weight", "desc")
        builder.limit(10)
        
        ast = builder.build_ast()
        
        # Verify pagination
        self.assertEqual(ast["pagination"]["limit"], 10)
        self.assertEqual(ast["pagination"]["offset"], 0)
        
        # Verify select (set to list ordering might vary so we sort)
        self.assertEqual(sorted(ast["select"]), sorted(["id", "summary"]))
        
        # Verify sorts
        self.assertEqual(ast["sort"][0]["field"], "weight")
        self.assertEqual(ast["sort"][0]["order"], "desc")
        
        # Verify filters
        self.assertEqual(ast["filters"]["type"]["$eq"], "risk")
        self.assertEqual(ast["filters"]["weight"]["$gt"], 0.8)

    def test_semantic_search_ast(self):
        builder = QueryBuilder()
        builder.where(type__in=["fact", "episodic"])
        builder.semantic_search("cluster health", mode=SemanticMode.HYBRID)
        
        ast = builder.build_ast()
        
        self.assertEqual(ast["filters"]["type"]["$in"], ["fact", "episodic"])
        self.assertEqual(ast["filters"]["semantic_search"]["query"], "cluster health")
        self.assertEqual(ast["filters"]["semantic_search"]["mode"], "$hybrid")

    def test_invalid_column(self):
        builder = QueryBuilder()
        with self.assertRaises(ValueError):
            builder.where(invalid_column="foo")
            
    def test_invalid_operator(self):
        builder = QueryBuilder()
        with self.assertRaises(ValueError):
            builder.where(weight__foo=10)

if __name__ == '__main__':
    unittest.main()
