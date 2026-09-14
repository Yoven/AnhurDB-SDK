"""The twelve responses that used to be ``Dict[str, Any]`` — decoded and audited.

One responsibility: prove the RESPONSE side of the 3.0.0 parity round. The
sibling ``test_parity_wire_offline.py`` proves the request side.

Two assertions carry the weight, and neither can be satisfied by a model that
merely "looks right":

1. **No key is dropped** — the model's declared fields must be a SUPERSET of
   every key the route can emit. A test that only checks the fields it already
   knows about cannot catch a field the server added.
2. **No field is a phantom** — every declared field must correspond to a wire
   key, unless it is justified in writing in ``live_field_sets.py``. A declared
   field the server never sends reads to the next maintainer like a safety net
   and produces unreachable branches (Go's ``UploadStatusResult.Error`` and
   TypeScript's ``UploadResult.id`` were exactly that).
"""

from __future__ import annotations

import pytest

from anhurdb.models import (
    ContextResult,
    EntitiesPage,
    EntityGraphResult,
    EntityTimelineResult,
    GroundingResult,
    ManifestResult,
    ProfileResult,
    SessionStats,
    SmartSearchResponse,
    UploadResult,
    UploadStatusResult,
    WalkResult,
)

from live_field_sets import (
    JUSTIFIED_EXTRA_FIELDS,
    LIVE_SAMPLES,
    WIRE_CONTRACTS,
)


# ── the two structural audits ────────────────────────────────────────────


@pytest.mark.parametrize(
    "model, wire_keys, anchor",
    WIRE_CONTRACTS,
    ids=[contract[0].__name__ for contract in WIRE_CONTRACTS],
)
def test_model_declares_every_key_the_server_can_send(model, wire_keys, anchor):
    """Superset check — a missing field is a SILENT drop.

    ``extra="ignore"`` is what makes this dangerous: a key the model does not
    declare is discarded without a word, so the caller sees a default and has
    no way to tell it apart from a value the server genuinely sent as zero.
    """
    declared = set(model.model_fields)
    missing = set(wire_keys) - declared
    assert not missing, (
        f"{model.__name__} silently drops {sorted(missing)} "
        f"(server truth: {anchor})"
    )


@pytest.mark.parametrize(
    "model, wire_keys, anchor",
    WIRE_CONTRACTS,
    ids=[contract[0].__name__ for contract in WIRE_CONTRACTS],
)
def test_model_declares_no_phantom_field(model, wire_keys, anchor):
    """Subset check — a field the server never sends is worse than a missing one."""
    declared = set(model.model_fields)
    justified = set(JUSTIFIED_EXTRA_FIELDS.get(model.__name__, {}))
    phantom = declared - set(wire_keys) - justified
    assert not phantom, (
        f"{model.__name__} declares {sorted(phantom)}, which the server never "
        f"sends (server truth: {anchor}). Delete it, or justify it in "
        f"live_field_sets.JUSTIFIED_EXTRA_FIELDS."
    )


# ── decoding the captured live envelopes ─────────────────────────────────


class TestDecodesTheLiveEnvelopes:
    def test_profile(self) -> None:
        profile = ProfileResult.model_validate(LIVE_SAMPLES["profile"])
        assert profile.static.facts == ["runs AnhurDB"]
        assert profile.dynamic.recent_topics == ["raft"]
        assert profile.stats.total_records == 3577
        assert profile.stats.sessions == 31
        # `last_active` on the PROFILE, `last_activity` on the session row.
        assert profile.stats.last_active == "2026-09-14T10:00:00Z"

    def test_walk_nodes_are_full_records_not_id_stubs(self) -> None:
        walk = WalkResult.model_validate(LIVE_SAMPLES["walk"])
        assert walk.truncated is False
        assert walk.edges[0].source == 18 and walk.edges[0].target == 19
        node = walk.nodes[0]
        # A projection type would have thrown these five away.
        assert node.uuid == "u-18"
        assert node.related_ids == [19]
        assert node.status == "saved"
        assert node.weight == 0.5
        assert node.metadata == "{}"

    def test_walk_semantic_leaves_truncated_unknown(self) -> None:
        """``/walk/semantic`` sends ``{nodes, edges}`` and makes no completeness claim.

        Defaulting ``truncated`` to ``False`` would invent a guarantee the
        endpoint never gave.
        """
        walk = WalkResult.model_validate(LIVE_SAMPLES["walk_semantic"])
        assert walk.truncated is None

    def test_topology(self) -> None:
        context = ContextResult.model_validate(LIVE_SAMPLES["topology"])
        assert context.target is not None and context.target.id == 18
        assert [neighbor.id for neighbor in context.neighbors] == [19]

    def test_grounding(self) -> None:
        grounding = GroundingResult.model_validate(LIVE_SAMPLES["grounding"])
        assert grounding.target.id == 42
        assert grounding.depth_used == 1 and grounding.max_depth == 3
        assert grounding.found_count == 1
        anchor = grounding.anchors[0]
        assert anchor.hops_from_target == 1
        # content arrives PARSED, not as a JSON string to unmarshal twice.
        assert anchor.content == {"user": "hi", "assistant": "hello"}
        assert grounding.consolidations[0].hops_from_target == 1

    def test_session_stats_uses_last_activity(self) -> None:
        session = SessionStats.model_validate(LIVE_SAMPLES["sessions_stats_row"])
        assert session.last_activity == "2026-09-14T10:00:00Z"
        assert session.types == {"episodic": 9, "fact": 3}
        assert session.summary == "a session"

    def test_manifest_has_no_cursor(self) -> None:
        manifest = ManifestResult.model_validate(LIVE_SAMPLES["manifest"])
        assert manifest.has_more is False
        assert manifest.count == 1
        assert not hasattr(manifest, "next_offset"), (
            "the manifest routes send no cursor; modelling one gives every "
            "caller a field that is permanently 0 and reads like page one"
        )

    def test_upload_accept_has_record_id_and_no_id(self) -> None:
        upload = UploadResult.model_validate(LIVE_SAMPLES["upload_accept"])
        assert upload.record_id == 900
        assert upload.mime_detected == "application/pdf"
        assert upload.size_bytes == 1234
        assert not hasattr(upload, "id"), (
            "there is no `id` on this response — polling with it 404s for a "
            "file that uploaded perfectly"
        )

    def test_upload_status_has_no_error_field(self) -> None:
        status = UploadStatusResult.model_validate(LIVE_SAMPLES["upload_status"])
        assert status.record_id == 900 and status.completed is True
        assert status.type == "file"
        assert not hasattr(status, "error"), (
            "a failed ingest is reported through `status` and ONLY through "
            "`status`; an `error` field would be an unreachable branch"
        )
        assert not hasattr(status, "filename")

    def test_entities_page_carries_the_cursor(self) -> None:
        page = EntitiesPage.model_validate(LIVE_SAMPLES["entities_page"])
        assert page.next_offset == 0 and page.total == 1
        entity = page.entities[0]
        assert entity.entity_type == "organization"
        assert entity.attributes == {"hq": "MTV"}
        assert entity.mention_count == 7

    def test_entity_graph_edges_keep_id_weight_and_ingested_at(self) -> None:
        graph = EntityGraphResult.model_validate(LIVE_SAMPLES["entity_graph"])
        assert graph.depth == 1 and graph.node_count == 1
        edge = graph.nodes[0].edges[0]
        # These three are exactly what the request-side EntityEdge lacks.
        assert edge.id == 3
        assert edge.weight == 0.9
        assert edge.ingested_at == "2026-01-01"
        assert graph.nodes[0].entity.name == "Ana"

    def test_entity_timeline(self) -> None:
        timeline = EntityTimelineResult.model_validate(LIVE_SAMPLES["entity_timeline"])
        assert timeline.edge_count == 1
        assert timeline.record_ids == [42]
        assert timeline.timeline[0].relation == "works_at"

    def test_smart_search_hits(self) -> None:
        response = SmartSearchResponse.model_validate(LIVE_SAMPLES["smart_search_hits"])
        assert response.count == 1
        assert response.bundle_ordering == "smart_relevance"
        hit = response.results[0]
        assert hit.relevance == 1.75
        assert hit.bm25 == 3.2
        # metadata is the RAW column, not a parsed object.
        assert hit.metadata == '{"container_tag":"x"}'
        # shared-plane-only keys are absent on a session search.
        assert hit.provenance is None and hit.leg_relevance is None

    def test_smart_search_null_results_stays_none(self) -> None:
        """``results`` is genuinely JSON ``null`` on the ordinary no-match answer.

        The handler marshals a nil Go slice. Declaring a non-null list would
        turn "the search ran and found nothing" into "[]", erasing the
        difference from "the key was absent" — the same ``None != []``
        discipline ``SearchResponse.leg_scores`` already keeps.
        """
        response = SmartSearchResponse.model_validate(LIVE_SAMPLES["smart_search_empty"])
        assert response.results is None
        assert response.results != []
        assert response.count == 0


class TestUnknownServerFieldsNeverBreakACaller:
    """``extra="ignore"`` — never ``extra="forbid"``.

    A server that adds a field must not break every existing caller. This is
    the counterweight to the phantom audit above: the model may be behind the
    server, it may never be ahead of it.
    """

    @pytest.mark.parametrize(
        "model, sample",
        [
            (ProfileResult, "profile"),
            (WalkResult, "walk"),
            (GroundingResult, "grounding"),
            (ManifestResult, "manifest"),
            (UploadResult, "upload_accept"),
            (UploadStatusResult, "upload_status"),
            (EntitiesPage, "entities_page"),
            (EntityGraphResult, "entity_graph"),
            (EntityTimelineResult, "entity_timeline"),
            (SmartSearchResponse, "smart_search_hits"),
        ],
    )
    def test_a_new_server_key_is_ignored_not_fatal(self, model, sample) -> None:
        payload = dict(LIVE_SAMPLES[sample])
        payload["a_field_from_the_future"] = {"anything": True}
        decoded = model.model_validate(payload)
        assert decoded is not None
