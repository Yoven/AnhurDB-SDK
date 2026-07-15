"""
Comprehensive tests for the AnhurDB QueryBuilder AST generation.

Tests cover:
  - Basic fluent API (where, select, order_by, limit, offset)
  - All supported operators ($eq, $gt, $gte, $lt, $lte, $in)
  - Removed operators ($neq, $nin, $like) must raise ValueError
  - Column whitelist enforcement
  - Semantic search block
  - Pagination defaults and bounds
  - Sort validation
  - Filter shorthand (Eq, Filter)
  - Combined complex queries
  - Edge cases and error handling

Reference: AnhurDB AST query contract (whitelisted filter columns).
"""

import unittest
import sys
import os

sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), "..")))

from anhurdb.query.builder import QueryBuilder, Filter, Eq, ALLOWED_WHERE_COLUMNS
from anhurdb.query.operators import QueryOperator, SemanticMode


class TestQueryBuilderBasic(unittest.TestCase):
    """Tests for basic fluent API operations."""

    def test_empty_builder_defaults(self):
        ast = QueryBuilder().build_ast()
        self.assertEqual(ast["filters"], {})
        self.assertEqual(ast["pagination"]["limit"], 50)
        self.assertEqual(ast["pagination"]["offset"], 0)
        self.assertNotIn("select", ast)
        self.assertNotIn("sort", ast)

    def test_select_fields(self):
        ast = QueryBuilder().select("id", "summary").build_ast()
        self.assertIn("select", ast)
        self.assertCountEqual(ast["select"], ["id", "summary"])

    def test_select_deduplicates(self):
        ast = QueryBuilder().select("id", "id", "summary").build_ast()
        self.assertEqual(len(ast["select"]), 2)

    def test_where_exact_match(self):
        ast = QueryBuilder().where(type="risk").build_ast()
        self.assertEqual(ast["filters"]["type"]["$eq"], "risk")

    def test_where_multiple_fields(self):
        ast = QueryBuilder().where(type="fact", status="saved").build_ast()
        self.assertEqual(ast["filters"]["type"]["$eq"], "fact")
        self.assertEqual(ast["filters"]["status"]["$eq"], "saved")

    def test_chaining(self):
        ast = (
            QueryBuilder()
            .select("id", "summary")
            .where(type="risk", weight__gt=0.8)
            .order_by("weight", "desc")
            .limit(10)
            .offset(20)
            .build_ast()
        )
        self.assertCountEqual(ast["select"], ["id", "summary"])
        self.assertEqual(ast["filters"]["type"]["$eq"], "risk")
        self.assertEqual(ast["filters"]["weight"]["$gt"], 0.8)
        self.assertEqual(ast["sort"][0]["field"], "weight")
        self.assertEqual(ast["sort"][0]["order"], "desc")
        self.assertEqual(ast["pagination"]["limit"], 10)
        self.assertEqual(ast["pagination"]["offset"], 20)


class TestQueryBuilderOperators(unittest.TestCase):
    """Tests for all supported filter operators."""

    def test_eq_operator(self):
        ast = QueryBuilder().where(type__eq="fact").build_ast()
        self.assertEqual(ast["filters"]["type"]["$eq"], "fact")

    def test_gt_operator(self):
        ast = QueryBuilder().where(weight__gt=0.5).build_ast()
        self.assertEqual(ast["filters"]["weight"]["$gt"], 0.5)

    def test_gte_operator(self):
        ast = QueryBuilder().where(score__gte=7).build_ast()
        self.assertEqual(ast["filters"]["score"]["$gte"], 7)

    def test_lt_operator(self):
        ast = QueryBuilder().where(weight__lt=0.3).build_ast()
        self.assertEqual(ast["filters"]["weight"]["$lt"], 0.3)

    def test_lte_operator(self):
        ast = QueryBuilder().where(score__lte=3).build_ast()
        self.assertEqual(ast["filters"]["score"]["$lte"], 3)

    def test_in_operator(self):
        ast = QueryBuilder().where(type__in=["fact", "risk"]).build_ast()
        self.assertEqual(ast["filters"]["type"]["$in"], ["fact", "risk"])

    def test_multiple_operators_on_same_field(self):
        """Combining gt and lt on weight should produce a range filter."""
        ast = QueryBuilder().where(weight__gt=0.3).where(weight__lt=0.9).build_ast()
        self.assertEqual(ast["filters"]["weight"]["$gt"], 0.3)
        self.assertEqual(ast["filters"]["weight"]["$lt"], 0.9)

    def test_removed_neq_raises(self):
        """$neq was removed — server silently ignores it."""
        with self.assertRaises(ValueError) as ctx:
            QueryBuilder().where(type__neq="episodic")
        self.assertIn("neq", str(ctx.exception))

    def test_removed_nin_raises(self):
        """$nin was removed — server silently ignores it."""
        with self.assertRaises(ValueError):
            QueryBuilder().where(type__nin=["a", "b"])

    def test_removed_like_raises(self):
        """$like was removed — server silently ignores it."""
        with self.assertRaises(ValueError):
            QueryBuilder().where(summary__like="%test%")

    def test_invalid_operator_raises(self):
        with self.assertRaises(ValueError) as ctx:
            QueryBuilder().where(weight__foo=10)
        self.assertIn("foo", str(ctx.exception))


class TestQueryBuilderWhitelist(unittest.TestCase):
    """Tests for column whitelist enforcement."""

    def test_all_server_columns_allowed(self):
        """Every column the server accepts should be in the SDK whitelist."""
        server_columns = {
            "id", "uuid", "type", "dimension", "weight", "score",
            "status", "consolidated", "archived", "created_at", "updated_at",
            "prefix", "metadata", "summary",
            "superseded_by", "valid_from", "valid_until",
        }
        self.assertEqual(ALLOWED_WHERE_COLUMNS, server_columns)

    def test_invalid_column_in_where(self):
        with self.assertRaises(ValueError) as ctx:
            QueryBuilder().where(invalid_column="foo")
        self.assertIn("invalid_column", str(ctx.exception))

    def test_invalid_column_in_operator_form(self):
        with self.assertRaises(ValueError):
            QueryBuilder().where(bad_field__gt=5)

    def test_invalid_column_in_order_by(self):
        with self.assertRaises(ValueError):
            QueryBuilder().order_by("nonexistent")

    def test_valid_columns_dont_raise(self):
        """Smoke test: all valid columns work without raising."""
        for col in ALLOWED_WHERE_COLUMNS:
            qb = QueryBuilder().where(**{col: "test"})
            ast = qb.build_ast()
            self.assertIn(col, ast["filters"])

    def test_superseded_by_column(self):
        """superseded_by is valid (temporal versioning)."""
        ast = QueryBuilder().where(superseded_by__gt=0).build_ast()
        self.assertEqual(ast["filters"]["superseded_by"]["$gt"], 0)

    def test_valid_from_column(self):
        ast = QueryBuilder().where(valid_from__gte="2026-01-01").build_ast()
        self.assertEqual(ast["filters"]["valid_from"]["$gte"], "2026-01-01")


class TestQueryBuilderPagination(unittest.TestCase):
    """Tests for pagination limits and bounds."""

    def test_default_limit(self):
        ast = QueryBuilder().build_ast()
        self.assertEqual(ast["pagination"]["limit"], 50)

    def test_custom_limit(self):
        ast = QueryBuilder().limit(25).build_ast()
        self.assertEqual(ast["pagination"]["limit"], 25)

    def test_limit_max_1000(self):
        """SDK allows up to 1000 (server also caps at 1000)."""
        ast = QueryBuilder().limit(1000).build_ast()
        self.assertEqual(ast["pagination"]["limit"], 1000)

    def test_limit_zero_raises(self):
        with self.assertRaises(ValueError):
            QueryBuilder().limit(0)

    def test_limit_negative_raises(self):
        with self.assertRaises(ValueError):
            QueryBuilder().limit(-1)

    def test_limit_over_1000_raises(self):
        with self.assertRaises(ValueError):
            QueryBuilder().limit(1001)

    def test_default_offset(self):
        ast = QueryBuilder().build_ast()
        self.assertEqual(ast["pagination"]["offset"], 0)

    def test_custom_offset(self):
        ast = QueryBuilder().offset(100).build_ast()
        self.assertEqual(ast["pagination"]["offset"], 100)

    def test_offset_negative_raises(self):
        with self.assertRaises(ValueError):
            QueryBuilder().offset(-1)


class TestQueryBuilderSort(unittest.TestCase):
    """Tests for sort clauses."""

    def test_single_sort(self):
        ast = QueryBuilder().order_by("weight", "desc").build_ast()
        self.assertEqual(ast["sort"], [{"field": "weight", "order": "desc"}])

    def test_multiple_sorts(self):
        ast = (
            QueryBuilder()
            .order_by("weight", "desc")
            .order_by("id", "asc")
            .build_ast()
        )
        self.assertEqual(len(ast["sort"]), 2)
        self.assertEqual(ast["sort"][0], {"field": "weight", "order": "desc"})
        self.assertEqual(ast["sort"][1], {"field": "id", "order": "asc"})

    def test_default_direction_is_desc(self):
        ast = QueryBuilder().order_by("score").build_ast()
        self.assertEqual(ast["sort"][0]["order"], "desc")

    def test_case_insensitive_direction(self):
        ast = QueryBuilder().order_by("score", "ASC").build_ast()
        self.assertEqual(ast["sort"][0]["order"], "asc")

    def test_invalid_direction_raises(self):
        with self.assertRaises(ValueError):
            QueryBuilder().order_by("score", "random")


class TestQueryBuilderSemanticSearch(unittest.TestCase):
    """Tests for semantic search blocks (forward compatibility)."""

    def test_semantic_search_hybrid(self):
        ast = (
            QueryBuilder()
            .semantic_search("cluster health", SemanticMode.HYBRID)
            .build_ast()
        )
        self.assertEqual(ast["filters"]["semantic_search"]["query"], "cluster health")
        self.assertEqual(ast["filters"]["semantic_search"]["mode"], "$hybrid")

    def test_semantic_search_text(self):
        ast = (
            QueryBuilder()
            .semantic_search("query", SemanticMode.TEXT)
            .build_ast()
        )
        self.assertEqual(ast["filters"]["semantic_search"]["mode"], "$text")

    def test_semantic_with_filters(self):
        """Semantic search combined with regular filters."""
        ast = (
            QueryBuilder()
            .where(type__in=["fact", "episodic"])
            .semantic_search("cluster health")
            .build_ast()
        )
        self.assertEqual(ast["filters"]["type"]["$in"], ["fact", "episodic"])
        self.assertEqual(ast["filters"]["semantic_search"]["query"], "cluster health")


class TestFilterShorthand(unittest.TestCase):
    """Tests for Eq() and Filter() helpers."""

    def test_eq_helper(self):
        result = Eq("type", "risk")
        self.assertEqual(result, {"type": {"$eq": "risk"}})

    def test_filter_with_dict(self):
        f = Filter({"type": {"$eq": "risk"}, "weight": {"$gt": 0.8}})
        ast = f.ast()
        self.assertEqual(ast["filters"]["type"]["$eq"], "risk")
        self.assertEqual(ast["filters"]["weight"]["$gt"], 0.8)

    def test_filter_empty(self):
        f = Filter()
        ast = f.ast()
        self.assertEqual(ast["filters"], {})

    def test_filter_default_pagination(self):
        f = Filter({"type": {"$eq": "fact"}})
        ast = f.ast()
        self.assertEqual(ast["pagination"]["limit"], 50)
        self.assertEqual(ast["pagination"]["offset"], 0)


class TestQueryBuilderExecute(unittest.TestCase):
    """Tests for execute() without executor."""

    def test_execute_without_executor_raises(self):
        import asyncio
        qb = QueryBuilder()
        with self.assertRaises(RuntimeError):
            asyncio.get_event_loop().run_until_complete(qb.execute())


class TestQueryBuilderDeepCopy(unittest.TestCase):
    """Tests that build_ast returns independent copies."""

    def test_modifying_ast_does_not_affect_builder(self):
        qb = QueryBuilder().where(type="risk")
        ast1 = qb.build_ast()
        ast1["filters"]["type"]["$eq"] = "modified"
        ast2 = qb.build_ast()
        self.assertEqual(ast2["filters"]["type"]["$eq"], "risk")


if __name__ == "__main__":
    unittest.main()
