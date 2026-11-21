"""Wire-contract tests for EntityModel (entity_type, not bare type)."""

from anhurdb.models import EntityModel


def test_entity_model_reads_entity_type_wire_key():
    entity = EntityModel.model_validate(
        {
            "id": 42,
            "name": "chrome",
            "entity_type": "product",
            "mention_count": 7,
            "weight": 1.0,
        }
    )
    assert entity.entity_type == "product"
    assert entity.name == "chrome"
    assert entity.mention_count == 7


def test_entity_model_ignores_removed_type_alias():
    """4.0.0 removal: ``type`` is no longer read as ``entity_type`` — the
    server emits only ``entity_type`` (handler/entity.go:89, :438-441)."""
    entity = EntityModel.model_validate(
        {"id": 1, "name": "chrome", "type": "product"}
    )
    assert entity.entity_type == ""
