#!/usr/bin/env python3
"""Validate the repository's native Codex plugin payload."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path
from typing import Any


SEMVER_IDENTIFIER = r"(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
SEMVER = re.compile(
    r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    rf"(?:-{SEMVER_IDENTIFIER}(?:\.{SEMVER_IDENTIFIER})*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
UNSUPPORTED_MANIFEST_FIELDS = {"apps", "mcpServers", "hooks"}


def load_json(path: Path, errors: list[str]) -> dict[str, Any] | None:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        errors.append(f"missing required file: {path}")
        return None
    except (OSError, json.JSONDecodeError) as exc:
        errors.append(f"cannot read JSON {path}: {exc}")
        return None
    if not isinstance(value, dict):
        errors.append(f"expected JSON object: {path}")
        return None
    return value


def require_string(value: Any, field: str, errors: list[str]) -> str:
    if not isinstance(value, str) or not value.strip():
        errors.append(f"{field} must be a non-empty string")
        return ""
    return value


def check_relative_path(root: Path, value: Any, field: str, errors: list[str]) -> None:
    path_value = require_string(value, field, errors)
    if not path_value:
        return
    if not path_value.startswith("./"):
        errors.append(f"{field} must start with ./")
        return
    target = (root / path_value[2:]).resolve()
    try:
        target.relative_to(root.resolve())
    except ValueError:
        errors.append(f"{field} escapes the plugin root")
        return
    if not target.exists():
        errors.append(f"{field} does not exist: {path_value}")


def validate_manifest(root: Path, manifest: dict[str, Any], errors: list[str]) -> None:
    expected = {
        "name": "basecamp",
        "homepage": "https://basecamp.com/cli",
        "repository": "https://github.com/basecamp/basecamp-cli",
        "license": "MIT",
        "skills": "./skills/",
    }
    for field, expected_value in expected.items():
        if manifest.get(field) != expected_value:
            errors.append(f"manifest {field} must be {expected_value!r}")

    require_string(manifest.get("description"), "manifest description", errors)
    version = require_string(manifest.get("version"), "manifest version", errors)
    if version and not SEMVER.fullmatch(version):
        errors.append(f"manifest version is not strict semver: {version}")

    author = manifest.get("author")
    if not isinstance(author, dict) or author.get("name") != "37signals":
        errors.append("manifest author.name must be '37signals'")

    unsupported = UNSUPPORTED_MANIFEST_FIELDS.intersection(manifest)
    if unsupported:
        errors.append("unsupported manifest fields: " + ", ".join(sorted(unsupported)))

    interface = manifest.get("interface")
    if not isinstance(interface, dict):
        errors.append("manifest interface must be an object")
        return
    required_interface = (
        "displayName",
        "shortDescription",
        "longDescription",
        "developerName",
        "category",
    )
    for field in required_interface:
        require_string(interface.get(field), f"interface.{field}", errors)
    if interface.get("displayName") != "Basecamp":
        errors.append("interface.displayName must be 'Basecamp'")
    if interface.get("developerName") != "37signals":
        errors.append("interface.developerName must be '37signals'")
    if interface.get("category") != "Productivity":
        errors.append("interface.category must be 'Productivity'")
    if interface.get("capabilities") != ["Interactive", "Read", "Write"]:
        errors.append("interface.capabilities must be Interactive, Read, Write")

    prompts = interface.get("defaultPrompt")
    if not isinstance(prompts, list) or not all(isinstance(item, str) for item in prompts):
        errors.append("interface.defaultPrompt must be an array of strings")
    else:
        if len(prompts) > 3:
            errors.append("interface.defaultPrompt must contain at most 3 prompts")
        for index, prompt in enumerate(prompts):
            if len(prompt) > 128:
                errors.append(f"interface.defaultPrompt[{index}] exceeds 128 characters")

    check_relative_path(root, manifest.get("skills"), "manifest skills", errors)
    for field in ("composerIcon", "logo"):
        check_relative_path(root, interface.get(field), f"interface.{field}", errors)


def validate_hooks(root: Path, errors: list[str]) -> None:
    hook_path = root / "hooks" / "hooks.json"
    payload = load_json(hook_path, errors)
    if payload is None:
        return
    hooks = payload.get("hooks")
    if not isinstance(hooks, dict):
        errors.append("hooks/hooks.json must contain a hooks object")
        return
    expected = {
        "SessionStart": (
            "startup|resume|clear|compact",
            "basecamp codex-hook session-start",
        ),
        "PostToolUse": ("^Bash$", "basecamp codex-hook post-commit-check"),
    }
    if set(hooks) != set(expected):
        errors.append("hooks/hooks.json must define only SessionStart and PostToolUse")
    for event, (matcher, command) in expected.items():
        groups = hooks.get(event)
        if not isinstance(groups, list) or len(groups) != 1 or not isinstance(groups[0], dict):
            errors.append(f"{event} must contain exactly one hook group")
            continue
        group = groups[0]
        if group.get("matcher") != matcher:
            errors.append(f"{event} matcher must be {matcher!r}")
        commands = group.get("hooks")
        if not isinstance(commands, list) or len(commands) != 1 or not isinstance(commands[0], dict):
            errors.append(f"{event} must contain exactly one command hook")
            continue
        hook = commands[0]
        if hook.get("type") != "command" or hook.get("command") != command:
            errors.append(f"{event} command must be {command!r}")
        if hook.get("timeout") != 5:
            errors.append(f"{event} timeout must be 5 seconds")
        require_string(hook.get("statusMessage"), f"{event} statusMessage", errors)


def validate_repository_contract(root: Path, errors: list[str]) -> None:
    for relative in (
        "skills/basecamp/SKILL.md",
        "skills/basecamp-doctor/SKILL.md",
        "assets/bc5-snowglobe.png",
    ):
        if not (root / relative).is_file():
            errors.append(f"missing required plugin path: {relative}")

    command_file = root / "internal" / "commands" / "codex_hook.go"
    root_file = root / "internal" / "cli" / "root.go"
    command_read = True
    try:
        command_source = command_file.read_text(encoding="utf-8")
    except OSError as exc:
        errors.append(f"cannot read hidden command source: {exc}")
        command_source = ""
        command_read = False
    root_read = True
    try:
        root_source = root_file.read_text(encoding="utf-8")
    except OSError as exc:
        errors.append(f"cannot read command registration: {exc}")
        root_source = ""
        root_read = False
    command_patterns = {
        r'\bUse\s*:\s*"codex-hook"': 'Use: "codex-hook"',
        r"\bHidden\s*:\s*true\b": "Hidden: true",
        r'\bUse\s*:\s*"session-start"': 'Use: "session-start"',
        r'\bUse\s*:\s*"post-commit-check"': 'Use: "post-commit-check"',
    }
    if command_read:
        for pattern, description in command_patterns.items():
            if re.search(pattern, command_source) is None:
                errors.append(f"hidden command source missing {description!r}")
    if root_read and re.search(r"\bcommands\s*\.\s*NewCodexHookCmd\s*\(\s*\)", root_source) is None:
        errors.append("Codex hook command is not registered in internal/cli/root.go")


def validate_version_parity(root: Path, codex: dict[str, Any], errors: list[str]) -> None:
    claude = load_json(root / ".claude-plugin" / "plugin.json", errors)
    if claude is not None and codex.get("version") != claude.get("version"):
        errors.append(
            "Claude and Codex manifest versions differ: "
            f"{claude.get('version')!r} != {codex.get('version')!r}"
        )


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else Path(__file__).resolve().parent.parent).resolve()
    errors: list[str] = []
    manifest = load_json(root / ".codex-plugin" / "plugin.json", errors)
    if manifest is not None:
        validate_manifest(root, manifest, errors)
        validate_version_parity(root, manifest, errors)
    validate_hooks(root, errors)
    validate_repository_contract(root, errors)

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("Codex plugin check passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
