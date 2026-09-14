"""The LIVE field sets every 3.0.0 response model must cover.

Captured 2026-09-14 against ``https://anhurdb.yoven.ai`` (read-only, owner
key) and cross-checked against the handler that builds each map. This module
is DATA, not a test: ``test_parity_models_offline.py`` decodes it offline and
``test_parity_live_contract.py`` re-proves it against the running server.

Junior Tip [why the field set lives in one shared module]: the offline test and
the live test must assert against the SAME list, or the offline one starts
"passing" against a shape the server abandoned. One list, two consumers — the
day the server adds a key, the live test fails on the SUPERSET assertion and
the fix is a single edit here.

Each entry is ``(model, wire_keys, handler_anchor)``. ``wire_keys`` is the
COMPLETE set of keys the route can emit, ``omitempty`` ones included: a model
is correct when its declared fields are a SUPERSET of this, because a field a
model declares and the server never sends is a phantom, and a key the server
sends that the model does not declare is a silent drop.
"""

from __future__ import annotations

from typing import Any, Dict, List, Tuple

from anhurdb.models import (
    AddResult,
    ContextResult,
    EntitiesPage,
    EntityGraphEdge,
    EntityGraphNode,
    EntityGraphResult,
    EntityTimelineResult,
    GroundingAnchor,
    GroundingConsolidation,
    GroundingResult,
    GroundingTarget,
    ManifestResult,
    ProfileDynamic,
    ProfileResult,
    ProfileStatic,
    ProfileStats,
    RecordSummary,
    SessionStats,
    SmartSearchHit,
    SmartSearchResponse,
    UploadResult,
    UploadStatusResult,
    WalkEdge,
    WalkResult,
)

# (model, wire keys the server can emit, where that is anchored)
WIRE_CONTRACTS: List[Tuple[Any, List[str], str]] = [
    (AddResult, ["session_id", "records", "mode", "id", "status"],
     "SDK-normalised; /ingest sends session_id+records, /records sends id+status"),
    (RecordSummary, ["id", "type", "summary"], "golang client.RecordSummary"),

    (ProfileResult, ["static", "dynamic", "stats"], "handler/profile.go:27-31"),
    (ProfileStatic, ["facts", "preferences", "decisions", "risks", "emotions",
                     "highlight"], "handler/profile.go:34-41"),
    (ProfileDynamic, ["recent_tasks", "recent_topics"], "handler/profile.go:43-46"),
    (ProfileStats, ["total_records", "sessions", "last_active"],
     "handler/profile.go:48-52 — last_actIVE here, last_actIVITY on the session row"),

    (WalkResult, ["nodes", "edges", "truncated"],
     "handler/record_search_graph.go:220-222 — no start_id, no depth"),
    (WalkEdge, ["source", "target"],
     "handler/record_search_graph.go:325-334 — no edge type, ever"),

    (ContextResult, ["target", "neighbors"], "handler/record_graph.go:270-273"),

    (GroundingResult, ["target", "anchors", "consolidations", "depth_used",
                       "max_depth", "found_count", "anchors_capped",
                       "consolidations_capped"],
     "service/record_grounding.go:141-150"),
    (GroundingTarget, ["id", "uuid", "type", "summary"],
     "service/record_grounding.go GroundingTargetView"),
    (GroundingAnchor, ["id", "type", "uuid", "summary", "content",
                       "hops_from_target", "session_position"],
     "service/record_grounding.go:109-117"),
    (GroundingConsolidation, ["id", "uuid", "summary", "hops_from_target"],
     "service/record_grounding.go:125-130"),

    (SessionStats, ["uuid", "record_count", "types", "last_activity", "summary"],
     "database/list_sessions.go:38 — the key is last_actIVITY"),

    (ManifestResult, ["records", "count", "limit", "offset", "has_more"],
     "handler/search_aux.go:225-229 and handler/record_session.go:307-311 — NO next_offset"),

    (UploadResult, ["message", "record_id", "uuid", "filename", "mime",
                    "mime_detected", "extension", "size_bytes", "status"],
     "handler/upload.go:109-119 — there is no id"),
    (UploadStatusResult, ["record_id", "uuid", "status", "type", "summary",
                          "metadata", "completed"],
     "handler/upload.go:220-236 — a fixed 7-key map, no error key"),

    (EntitiesPage, ["entities", "count", "total", "limit", "offset", "has_more",
                    "next_offset"],
     "handler/entity.go:201-207 — this route DOES carry a cursor"),
    (EntityGraphResult, ["entity_id", "depth", "node_count", "nodes"],
     "handler/entity.go:305-310"),
    (EntityGraphNode, ["entity", "edges"], "handler/entity.go:299-302"),
    (EntityGraphEdge, ["id", "source_id", "target_id", "relation", "confidence",
                       "weight", "event_time", "ingested_at", "valid_until",
                       "source_record_id"],
     "database/entity.go:40-51"),
    (EntityTimelineResult, ["entity", "timeline", "record_ids", "edge_count"],
     "handler/entity.go:363-368"),

    (SmartSearchResponse, ["results", "count", "scope", "bundle_hash",
                           "bundle_ordering"],
     "handler/search_smart.go:216-224"),
    (SmartSearchHit, ["id", "uuid", "type", "summary", "metadata", "score",
                      "weight", "status", "relevance", "bm25", "created_at",
                      "updated_at", "provenance", "scope", "leg_relevance"],
     "live row 2026-09-14 + handler/search_scope_smart_merge.go:17-32"),
]


# Fields a model may declare that the server does NOT send, with the reason.
# Anything outside this map is a phantom and the offline test fails on it.
#
# Junior Tip [why an allow-list and not "extra fields are fine"]: the whole
# point of this round was that Go declared UploadStatusResult.Error, TypeScript
# declared UploadResult.id, and both read like safety nets for a signal that
# never arrives. A model field with no wire key behind it must be justified in
# writing, here, or deleted.
JUSTIFIED_EXTRA_FIELDS: Dict[str, Dict[str, str]] = {
    "AddResult": {},
    "SmartSearchHit": {},
}


# The live envelopes as captured, for the offline decode test. Trimmed to one
# representative row each; the KEY SETS are complete.
LIVE_SAMPLES: Dict[str, Any] = {
    "profile": {
        "static": {
            "facts": ["runs AnhurDB"],
            "preferences": ["prefers Go"],
            "decisions": ["ship 3.0.0"],
            "risks": [],
            "emotions": [],
            "highlight": ["parity round"],
        },
        "dynamic": {"recent_tasks": ["sdk parity"], "recent_topics": ["raft"]},
        "stats": {"total_records": 3577, "sessions": 31,
                  "last_active": "2026-09-14T10:00:00Z"},
    },
    "walk": {
        "nodes": [{
            "archived": False, "consolidated": False,
            "created_at": "2026-09-01T00:00:00Z", "id": 18, "main_ids": [],
            "metadata": "{}", "related_ids": [19], "score": 5, "status": "saved",
            "summary": "seed", "type": "episodic",
            "updated_at": "2026-09-01T00:00:00Z", "uuid": "u-18", "weight": 0.5,
        }],
        "edges": [{"source": 18, "target": 19}],
        "truncated": False,
    },
    "walk_semantic": {
        "nodes": [],
        "edges": [],
    },
    "topology": {
        "target": {"id": 18, "uuid": "u-18", "type": "episodic", "summary": "t"},
        "neighbors": [{"id": 19, "uuid": "u-19", "type": "fact", "summary": "n"}],
    },
    "grounding": {
        "target": {"id": 42, "uuid": "u-42", "type": "fact", "summary": "the fact"},
        "anchors": [{
            "id": 18, "type": "episodic", "uuid": "u-18", "summary": "turn",
            "content": {"user": "hi", "assistant": "hello"},
            "hops_from_target": 1, "session_position": 3,
        }],
        "consolidations": [{"id": 30, "uuid": "u-30", "summary": "star",
                            "hops_from_target": 1}],
        "depth_used": 1, "max_depth": 3, "found_count": 1,
    },
    "sessions_stats_row": {
        "uuid": "s-1", "record_count": 12, "types": {"episodic": 9, "fact": 3},
        "last_activity": "2026-09-14T10:00:00Z", "summary": "a session",
    },
    "manifest": {
        "records": [{"id": 1, "uuid": "u-1", "type": "fact", "summary": "s"}],
        "count": 1, "limit": 50, "offset": 0, "has_more": False,
    },
    "upload_accept": {
        "message": "File accepted for processing", "record_id": 900,
        "uuid": "sess-1", "filename": "report.pdf", "mime": "application/pdf",
        "mime_detected": "application/pdf", "extension": ".pdf",
        "size_bytes": 1234, "status": "processing",
    },
    "upload_status": {
        "record_id": 900, "uuid": "sess-1", "status": "completed",
        "type": "file", "summary": "report.pdf", "metadata": "{}",
        "completed": True,
    },
    "entities_page": {
        "entities": [{
            "id": 5, "name": "Google", "entity_type": "organization",
            "summary": "search company", "attributes": {"hq": "MTV"},
            "dimension": 0, "first_seen": "2026-01-01", "last_seen": "2026-09-01",
            "mention_count": 7, "weight": 0.8,
        }],
        "count": 1, "total": 1, "limit": 200, "offset": 0,
        "has_more": False, "next_offset": 0,
    },
    "entity_graph": {
        "entity_id": 1, "depth": 1, "node_count": 1,
        "nodes": [{
            "entity": {"id": 1, "name": "Ana", "entity_type": "person",
                       "summary": "", "attributes": {}, "dimension": 0,
                       "first_seen": "", "last_seen": "", "mention_count": 1,
                       "weight": 0.5},
            "edges": [{"id": 3, "source_id": 1, "target_id": 2,
                       "relation": "works_at", "confidence": 0.95,
                       "weight": 0.9, "event_time": "2020-01-01",
                       "ingested_at": "2026-01-01", "valid_until": "",
                       "source_record_id": 42}],
        }],
    },
    "entity_timeline": {
        "entity": {"id": 1, "name": "Ana", "entity_type": "person", "summary": "",
                   "attributes": {}, "dimension": 0, "first_seen": "",
                   "last_seen": "", "mention_count": 1, "weight": 0.5},
        "timeline": [{"id": 3, "source_id": 1, "target_id": 2,
                      "relation": "works_at", "confidence": 0.95, "weight": 0.9,
                      "event_time": "2020-01-01", "ingested_at": "2026-01-01"}],
        "record_ids": [42], "edge_count": 1,
    },
    "smart_search_hits": {
        "results": [{
            "id": 7, "uuid": "u-7", "type": "fact", "summary": "a hit",
            "metadata": "{\"container_tag\":\"x\"}", "score": 5.0, "weight": 0.5,
            "status": "saved", "relevance": 1.75, "bm25": 3.2,
            "created_at": "2026-09-01T00:00:00Z",
            "updated_at": "2026-09-01T00:00:00Z",
        }],
        "count": 1, "scope": "sessions", "bundle_hash": "abc123",
        "bundle_ordering": "smart_relevance",
    },
    "smart_search_empty": {
        "results": None, "count": 0, "scope": "sessions",
        "bundle_hash": "", "bundle_ordering": "smart_relevance",
    },
}
