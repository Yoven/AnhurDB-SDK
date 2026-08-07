#!/usr/bin/env python3
"""Compare the Go and Python observations against the shared scenarios.

This is the only component that decides pass/fail. The two probes just observe,
so a divergence is reported once, in one place, in one voice.

Usage (normally called by ``run_parity.sh``)::

    python3 compare_parity.py <scenarios_dir> <go.json> <python.json>

Exit codes — a verifier that cannot tell "did not run" from "passed" is worse
than no verifier at all, so the two get different codes:

    0  every scenario identical in both implementations
    1  PARITY BROKEN — at least one scenario diverges from its expectation
    2  the comparison could not be made (missing file, missing scenario,
       unclassified field) — nothing was proven either way

Standard library only.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any, Dict, List, NoReturn

# Field classes a scenario may declare. Every observed field MUST fall into
# exactly one of them — see check_scenario for why an unclassified field is a
# hard error rather than a warning.
CLASS_EXPECT = "expect"
CLASS_DIVERGENT = "divergent"
CLASS_NOT_COMPARED = "not_compared"


def harness_error(message: str) -> NoReturn:
    """Abort with exit code 2 — "could not compare", never "compared and failed".

    Junior Tip [why this exists instead of `raise SystemExit(message)`, 2026-07-30]:
    SystemExit with a STRING argument prints the message and exits with code **1**,
    which is this tool's code for PARITY BROKEN. A missing observation file would
    then be indistinguishable from a real divergence — a verifier lying about which
    kind of bad it is, which is the same family of bug as a verifier saying OK when
    it is wrong. Caught by the harness's own negative test.
    """
    print(message, file=sys.stderr)
    sys.exit(2)


def render_value(value: Any) -> str:
    """Compact one-line rendering, so a diff line stays readable."""
    return json.dumps(value, ensure_ascii=False)


class ScenarioResult:
    """Outcome of one scenario: failures, declared divergences, skipped fields."""

    def __init__(self, name: str, rule: str, source_path: Path, on_failure: str) -> None:
        self.name = name
        self.rule = rule
        # Kept on the result so the report never has to guess a scenario's file
        # name from its scenario name — a mismatch there would silently drop the
        # "what to do" guidance exactly when somebody needs it.
        self.source_path = source_path
        self.on_failure = on_failure
        self.failures: List[str] = []
        self.declared_divergences: List[str] = []
        self.not_compared: List[str] = []
        self.compared_field_count = 0

    @property
    def passed(self) -> bool:
        return not self.failures


def check_scenario(
    scenario: Dict[str, Any],
    scenario_path: Path,
    go_observation: Dict[str, Any],
    python_observation: Dict[str, Any],
) -> ScenarioResult:
    """Compare one scenario's observations against its declared expectations."""
    name = scenario["name"]
    result = ScenarioResult(
        name,
        scenario.get("rule", "?"),
        scenario_path,
        str(scenario.get("on_failure") or "(no guidance written in the scenario file)"),
    )

    expected_fields: Dict[str, Any] = scenario.get(CLASS_EXPECT) or {}
    divergent_fields: Dict[str, Any] = scenario.get(CLASS_DIVERGENT) or {}
    not_compared_fields: Dict[str, Any] = scenario.get(CLASS_NOT_COMPARED) or {}

    # 1. Every field either implementation reported must be classified. Without
    #    this rule, adding an observation field and forgetting to classify it
    #    would create a blind spot that looks exactly like a green run — which
    #    is the failure mode this whole harness exists to eliminate.
    observed_field_names = set(go_observation) | set(python_observation)
    classified_field_names = (
        set(expected_fields) | set(divergent_fields) | set(not_compared_fields)
    )
    for field_name in sorted(observed_field_names - classified_field_names):
        result.failures.append(
            f"field `{field_name}` is observed but not classified in the scenario file "
            f"(add it to `expect`, or to `divergent`/`not_compared` WITH a written reason). "
            f"go={render_value(go_observation.get(field_name))} "
            f"python={render_value(python_observation.get(field_name))}"
        )
    for field_name in sorted(classified_field_names - observed_field_names):
        result.failures.append(
            f"field `{field_name}` is declared in the scenario file but neither probe "
            f"reported it — the scenario and the probes are out of sync"
        )

    # 2. `expect`: both implementations must produce exactly this value.
    for field_name, expected_value in sorted(expected_fields.items()):
        go_value = go_observation.get(field_name)
        python_value = python_observation.get(field_name)
        result.compared_field_count += 1
        if go_value == expected_value and python_value == expected_value:
            continue
        detail = [f"field `{field_name}`", f"      expected : {render_value(expected_value)}"]
        detail.append(
            f"      go       : {render_value(go_value)}"
            + ("" if go_value == expected_value else "   <-- differs")
        )
        detail.append(
            f"      python   : {render_value(python_value)}"
            + ("" if python_value == expected_value else "   <-- differs")
        )
        result.failures.append("\n".join(detail))

    # 3. `divergent`: a KNOWN, written-down disagreement. Each side must still
    #    produce exactly its declared value, so the difference cannot quietly
    #    grow, shrink or flip while nobody is looking.
    for field_name, declaration in sorted(divergent_fields.items()):
        if not isinstance(declaration, dict) or "go" not in declaration or "python" not in declaration:
            result.failures.append(
                f"field `{field_name}` is listed as divergent but the scenario does not "
                f"declare both a `go` and a `python` value"
            )
            continue
        result.compared_field_count += 1
        go_value = go_observation.get(field_name)
        python_value = python_observation.get(field_name)
        go_matches = go_value == declaration["go"]
        python_matches = python_value == declaration["python"]
        if go_matches and python_matches:
            result.declared_divergences.append(
                f"field `{field_name}`\n"
                f"      go       : {render_value(go_value)}\n"
                f"      python   : {render_value(python_value)}\n"
                f"      decision : {declaration.get('decision_needed', '(none written)')}"
            )
            continue
        detail = [
            f"field `{field_name}` no longer matches its DECLARED divergence "
            f"(one side changed behaviour)"
        ]
        detail.append(
            f"      declared go     : {render_value(declaration['go'])}\n"
            f"      observed go     : {render_value(go_value)}"
            + ("" if go_matches else "   <-- changed")
        )
        detail.append(
            f"      declared python : {render_value(declaration['python'])}\n"
            f"      observed python : {render_value(python_value)}"
            + ("" if python_matches else "   <-- changed")
        )
        result.failures.append("\n".join(detail))

    # 4. `not_compared`: representation-only differences (units, file formats).
    #    Never silently dropped — each one carries a written reason and is
    #    listed in the report.
    for field_name, reason in sorted(not_compared_fields.items()):
        if not str(reason).strip():
            result.failures.append(
                f"field `{field_name}` is excluded from the comparison with no reason "
                f"written down — an unexplained exclusion is a blind spot"
            )
            continue
        result.not_compared.append(
            f"field `{field_name}`: go={render_value(go_observation.get(field_name))} "
            f"python={render_value(python_observation.get(field_name))}\n      why: {reason}"
        )
    return result


def load_observations(path: Path, label: str) -> Dict[str, Any]:
    """Read a probe's observation file, failing loudly and specifically."""
    if not path.is_file():
        harness_error(
            f"HARNESS ERROR: the {label} probe wrote no observation file ({path}). "
            f"Nothing was compared — this is NOT a pass."
        )
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except ValueError as decode_error:
        harness_error(f"HARNESS ERROR: {label} observations are not valid JSON: {decode_error}")
    scenarios = document.get("scenarios")
    if not isinstance(scenarios, dict) or not scenarios:
        harness_error(f"HARNESS ERROR: {label} observations contain no scenarios")
    return scenarios


def main(argv: List[str]) -> int:
    if len(argv) != 4:
        print(f"usage: {argv[0]} <scenarios_dir> <go.json> <python.json>", file=sys.stderr)
        return 2

    scenarios_dir = Path(argv[1]).resolve()
    go_scenarios = load_observations(Path(argv[2]).resolve(), "Go")
    python_scenarios = load_observations(Path(argv[3]).resolve(), "Python")

    scenario_files = sorted(scenarios_dir.glob("*.json"))
    if not scenario_files:
        harness_error(f"HARNESS ERROR: no scenarios in {scenarios_dir}")

    results: List[ScenarioResult] = []
    setup_errors: List[str] = []

    for scenario_file in scenario_files:
        scenario = json.loads(scenario_file.read_text(encoding="utf-8"))
        name = scenario.get("name", scenario_file.stem)
        missing_from = [
            label
            for label, observations in (("Go", go_scenarios), ("Python", python_scenarios))
            if name not in observations
        ]
        if missing_from:
            setup_errors.append(
                f"{name}: not observed by {' and '.join(missing_from)} — that probe never "
                f"ran this scenario, so it proves nothing"
            )
            continue
        results.append(
            check_scenario(scenario, scenario_file, go_scenarios[name], python_scenarios[name])
        )

    # ── report ───────────────────────────────────────────────────────────────
    print()
    print("AnhurDB memory plugins — cross-language parity")
    print("  Go     : AnhurDB-SDK/v2/plugins/core           (anhur-claude-memory, anhur-hermes-memory)")
    print("  Python : AnhurDB-SDK/v2/plugins/hermes-agent   (Hermes Agent memory provider)")
    print()

    for result in results:
        status = "ok      " if result.passed else "DIVERGE "
        print(f"  [{status}] {result.name}  ({result.rule}, {result.compared_field_count} fields compared)")
        for divergence in result.declared_divergences:
            print(f"      note: declared divergence — {divergence.splitlines()[0]}")
        for failure in result.failures:
            print(f"      !! {failure}")

    failed_results = [result for result in results if not result.passed]
    declared_total = sum(len(result.declared_divergences) for result in results)
    not_compared_total = sum(len(result.not_compared) for result in results)

    if declared_total:
        print()
        print("DECLARED DIVERGENCES — written down, pinned, and still waiting for your decision")
        print("(these are NOT failures: both sides behave exactly as the scenario says they do)")
        for result in results:
            for divergence in result.declared_divergences:
                print(f"  {result.name}:")
                print(f"      {divergence}")
    if not_compared_total:
        print()
        print(f"NOT COMPARED — {not_compared_total} representation-only field(s), each with a written reason:")
        for result in results:
            for skipped in result.not_compared:
                print(f"  {result.name}: {skipped}")

    print()
    print("=" * 78)
    if setup_errors:
        print("VERDICT: COULD NOT COMPARE — the harness did not get what it needed")
        for setup_error in setup_errors:
            print(f"  - {setup_error}")
        print()
        print("WHAT TO DO: re-run ./run_parity.sh and read the probe output above the report.")
        print("=" * 78)
        return 2

    if failed_results:
        print(f"VERDICT: PARITY BROKEN — {len(failed_results)} of {len(results)} scenarios diverge")
        print()
        print("This does NOT mean your memory is down right now: it means the Go plugin and the")
        print("Python provider disagree about a rule, so one of them is behaving differently from")
        print("the behaviour you reviewed. (To check whether memory is actually live, run the")
        print("per-plugin verifiers — see the root README, section `Agent memory`.)")
        print()
        print("WHAT TO DO, scenario by scenario:")
        for result in failed_results:
            print()
            print(f"  {result.name}  ({result.rule})")
            print(f"    fixture : {result.source_path}")
            print(f"    fix     : {result.on_failure}")
        print("=" * 78)
        return 1

    print(f"VERDICT: PARITY OK — {len(results)}/{len(results)} scenarios behave identically in Go and Python")
    if declared_total:
        print(f"         ({declared_total} declared divergence(s) above still need your decision)")
    print("=" * 78)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
