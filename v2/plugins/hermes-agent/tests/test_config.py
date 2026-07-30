"""Configuration loading — the exact layer that failed for 12.8 days.

Every test here maps to a concrete way the 2026-07-18→07-30 blackout became
invisible: a file the process could not read, a variable that only existed in
some shell, and a log that never said which of the two had happened.
"""

from __future__ import annotations

from pathlib import Path


def test_env_file_accepts_export_quotes_and_comments(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    env_file = tmp_path / "env"
    env_file.write_text(
        "\n".join(
            [
                "# a comment",
                "",
                "export ANHUR_API_KEY=anhur_from_file",
                'ANHUR_URL="https://quoted.example"',
                "  export   ANHUR_CONTAINER = 'spaced'  ",
                "this line has no equals sign and must be skipped",
                "ANHUR_RECALL_LIMIT=3",
            ]
        ),
        encoding="utf-8",
    )

    values, error = config_module.read_env_file(env_file)

    assert error == ""
    assert values["ANHUR_API_KEY"] == "anhur_from_file"
    assert values["ANHUR_URL"] == "https://quoted.example"
    assert values["ANHUR_CONTAINER"] == "spaced"
    # The malformed line must not have discarded the key that follows it.
    assert values["ANHUR_RECALL_LIMIT"] == "3"


def test_missing_env_file_is_not_an_error(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    values, error = config_module.read_env_file(tmp_path / "does-not-exist")
    assert values == {}
    assert error == ""


def test_unreadable_env_file_is_recorded_not_raised(plugin_modules, tmp_path):
    """A directory where the file should be: report it, never crash."""
    config_module = plugin_modules["config"]
    (tmp_path / "env").mkdir()

    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path)}
    )

    assert config.env_file_error != ""
    assert config.key_source == config_module.KEY_SOURCE_MISSING
    assert "could not be read" in config.missing_key_diagnostic()


def test_environment_wins_over_file_and_source_is_recorded(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    (tmp_path / "env").write_text("export ANHUR_API_KEY=from_file\n", encoding="utf-8")

    config = config_module.load_plugin_config(
        environment={
            "ANHUR_STATE_DIR": str(tmp_path),
            "ANHUR_API_KEY": "from_environment",
        }
    )

    assert config.api_key == "from_environment"
    assert config.key_source == config_module.KEY_SOURCE_ENVIRONMENT


def test_file_is_used_when_environment_has_no_key(plugin_modules, tmp_path):
    """The regression itself: the value lives ONLY in the file."""
    config_module = plugin_modules["config"]
    (tmp_path / "env").write_text("ANHUR_API_KEY=only_in_file\n", encoding="utf-8")

    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path)}
    )

    assert config.api_key == "only_in_file"
    assert config.key_source == config_module.KEY_SOURCE_FILE
    assert config.has_api_key is True


def test_empty_environment_variable_does_not_mask_the_file(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    (tmp_path / "env").write_text("ANHUR_API_KEY=only_in_file\n", encoding="utf-8")

    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path), "ANHUR_API_KEY": "   "}
    )

    assert config.api_key == "only_in_file"
    assert config.key_source == config_module.KEY_SOURCE_FILE


def test_explicit_env_file_path_overrides_the_state_dir(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    elsewhere = tmp_path / "elsewhere.env"
    elsewhere.write_text("ANHUR_API_KEY=from_explicit_path\n", encoding="utf-8")

    config = config_module.load_plugin_config(
        environment={
            "ANHUR_STATE_DIR": str(tmp_path / "state"),
            "ANHUR_ENV_FILE": str(elsewhere),
        }
    )

    assert config.env_file_path == elsewhere
    assert config.api_key == "from_explicit_path"


def test_missing_key_produces_actionable_loud_diagnostic(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    (tmp_path / "env").write_text("ANHUR_URL=https://example\n", encoding="utf-8")

    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path)}
    )
    diagnostic = config.missing_key_diagnostic()

    assert config.key_source == config_module.KEY_SOURCE_MISSING
    assert config.has_api_key is False
    assert "ANHUR_API_KEY is not set" in diagnostic
    assert str(config.env_file_path) in diagnostic
    assert "1 variable(s)" in diagnostic
    assert "hermes memory setup" in diagnostic


def test_state_dir_expands_shell_style_variables(plugin_modules, tmp_path, monkeypatch):
    """`$HOME/...` in the file must not create a literal `$HOME` directory."""
    config_module = plugin_modules["config"]
    monkeypatch.setenv("HOME", str(tmp_path))
    (tmp_path / "env").write_text(
        "export ANHUR_STATE_DIR=$HOME/state-from-file\n", encoding="utf-8"
    )

    config = config_module.load_plugin_config(
        environment={"ANHUR_ENV_FILE": str(tmp_path / "env")}
    )

    assert config.state_dir == tmp_path / "state-from-file"
    assert "$HOME" not in str(config.state_dir)


def test_api_key_with_dollar_sign_is_never_expanded(plugin_modules, tmp_path):
    """Expansion applies to paths only — a key is a literal."""
    config_module = plugin_modules["config"]
    (tmp_path / "env").write_text("ANHUR_API_KEY=anhur_$HOME_literal\n", encoding="utf-8")

    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path)}
    )

    assert config.api_key == "anhur_$HOME_literal"


def test_numeric_settings_fall_back_when_garbage(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]

    config = config_module.load_plugin_config(
        environment={
            "ANHUR_STATE_DIR": str(tmp_path),
            "ANHUR_RECALL_LIMIT": "not-a-number",
            "ANHUR_MAX_CHUNK_CHARS": "-5",
            "ANHUR_HTTP_TIMEOUT": "2.5",
        }
    )

    assert config.recall_limit == config_module.DEFAULT_RECALL_LIMIT
    assert config.max_chunk_chars == config_module.DEFAULT_MAX_CHUNK_CHARS
    assert config.http_timeout_seconds == 2.5


def test_describe_and_diagnostics_never_leak_the_api_key(
    plugin_modules, tmp_path
):
    """Non-negotiable: the key is never printed, logged, or written."""
    config_module = plugin_modules["config"]
    secret = "anhur_super_secret_value"

    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path), "ANHUR_API_KEY": secret}
    )
    config_module.log_diagnostic(config, 40, f"config: {config.describe()}")

    assert secret not in config.describe()
    assert secret not in config.missing_key_diagnostic()
    log_text = Path(config.log_path).read_text(encoding="utf-8")
    assert secret not in log_text
    assert "key source=environment" in log_text


def test_state_dir_and_log_are_owner_only(plugin_modules, tmp_path):
    config_module = plugin_modules["config"]
    config = config_module.load_plugin_config(
        environment={"ANHUR_STATE_DIR": str(tmp_path / "state")}
    )

    assert config_module.ensure_state_dir(config) is True
    config_module.log_diagnostic(config, 20, "hello")

    assert (config.state_dir.stat().st_mode & 0o077) == 0
    assert (config.log_path.stat().st_mode & 0o077) == 0
