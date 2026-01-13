#!/usr/bin/env python3
import argparse
import os
import re
import sys
from pathlib import Path


DOC_ENDPOINT_RE = re.compile(r"\b(GET|POST|PUT|PATCH|DELETE)\s+(/\S+)", re.I)
API_CALL_STR_RE = re.compile(
    r"api_(get|post|put|patch|delete|upload)\s+(\"([^\"]+)\"|'([^']+)')"
)
API_CALL_VAR_RE = re.compile(
    r"api_(get|post|put|patch|delete|upload)\s+\"?(\$[A-Za-z_][A-Za-z0-9_]*|\$\{[A-Za-z_][A-Za-z0-9_]*\})\"?"
)
ASSIGN_RE = re.compile(r"^\s*(?:local\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([\"'])(.+?)\2")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Compare bcq API usage to bc3-api documentation coverage."
    )
    parser.add_argument(
        "--bcq-root",
        default=str(Path(__file__).resolve().parents[1]),
        help="Path to bcq repo root (default: repo root).",
    )
    parser.add_argument(
        "--bc3-api",
        default=os.environ.get("BC3_API_PATH", ""),
        help="Path to bc3-api repo root (default: ../bc3-api).",
    )
    parser.add_argument(
        "--show-missing",
        action="store_true",
        help="List missing endpoints per partial/zero section.",
    )
    return parser.parse_args()


def resolve_bc3_api_root(bcq_root: Path, provided: str) -> Path:
    if provided:
        return Path(provided).expanduser().resolve()
    candidate = (bcq_root.parent / "bc3-api").resolve()
    return candidate


def load_doc_endpoints(sections_dir: Path) -> dict[str, list[tuple[str, str]]]:
    sections: dict[str, list[tuple[str, str]]] = {}
    for md_file in sorted(sections_dir.glob("*.md")):
        text = md_file.read_text(encoding="utf-8")
        seen = set()
        endpoints: list[tuple[str, str]] = []
        for match in DOC_ENDPOINT_RE.finditer(text):
            method = match.group(1).upper()
            path = match.group(2).rstrip("`,.")
            key = (method, path)
            if key in seen:
                continue
            seen.add(key)
            endpoints.append(key)
        if endpoints:
            sections[md_file.name] = endpoints
    return sections


def load_bcq_endpoints(commands_dir: Path) -> set[tuple[str, str]]:
    endpoints: set[tuple[str, str]] = set()

    for script in commands_dir.rglob("*.sh"):
        text = script.read_text(encoding="utf-8")
        assignments: dict[str, set[str]] = {}
        for line in text.splitlines():
            match = ASSIGN_RE.match(line)
            if not match:
                continue
            var_name = match.group(1)
            value = match.group(3)
            if not value.startswith("/"):
                continue
            assignments.setdefault(var_name, set()).add(value)

        for match in API_CALL_STR_RE.finditer(text):
            method = match.group(1).upper()
            if method == "UPLOAD":
                method = "POST"
            path = match.group(3) or match.group(4)
            if re.fullmatch(r"\$\{?[A-Za-z_][A-Za-z0-9_]*\}?", path):
                continue
            endpoints.add((method, path))

        for match in API_CALL_VAR_RE.finditer(text):
            method = match.group(1).upper()
            if method == "UPLOAD":
                method = "POST"
            var_token = match.group(2)
            var_name = var_token.strip("${}").lstrip("$")
            for path in assignments.get(var_name, set()):
                endpoints.add((method, path))

    return endpoints


def segmentize(path: str, wildcard_vars: bool, wildcard_numbers: bool) -> list[str]:
    path = path.split("?", 1)[0]
    path = path.replace("(.:format)", "")
    path = path.replace("(", "").replace(")", "")
    segments: list[str] = []
    for segment in path.strip("/").split("/"):
        if not segment:
            continue
        segment = segment.replace(".json", "")
        is_wildcard = False
        if wildcard_vars and ("$" in segment or segment.startswith(":") or segment.startswith("{")):
            is_wildcard = True
        if wildcard_numbers and segment.isdigit():
            is_wildcard = True
        if ":" in segment:
            is_wildcard = True
        if is_wildcard:
            segments.append("*")
        else:
            segments.append(segment)
    return segments


def endpoints_match(doc_endpoint: tuple[str, str], bcq_endpoint: tuple[str, str]) -> bool:
    doc_method, doc_path = doc_endpoint
    bcq_method, bcq_path = bcq_endpoint
    if doc_method != bcq_method:
        return False
    doc_segs = segmentize(doc_path, wildcard_vars=True, wildcard_numbers=True)
    bcq_segs = segmentize(bcq_path, wildcard_vars=True, wildcard_numbers=False)
    if len(doc_segs) != len(bcq_segs):
        return False
    for doc_seg, bcq_seg in zip(doc_segs, bcq_segs):
        if doc_seg == "*" or bcq_seg == "*":
            continue
        if doc_seg != bcq_seg:
            return False
    return True


def main() -> int:
    args = parse_args()
    bcq_root = Path(args.bcq_root).resolve()
    bc3_api_root = resolve_bc3_api_root(bcq_root, args.bc3_api)
    sections_dir = bc3_api_root / "sections"

    if not sections_dir.exists():
        print(f"error: sections dir not found: {sections_dir}", file=sys.stderr)
        return 1

    doc_sections = load_doc_endpoints(sections_dir)
    bcq_endpoints = load_bcq_endpoints(bcq_root / "lib" / "commands")

    unique_docs = []
    doc_seen = set()
    for endpoints in doc_sections.values():
        for endpoint in endpoints:
            if endpoint in doc_seen:
                continue
            doc_seen.add(endpoint)
            unique_docs.append(endpoint)

    matched = 0
    section_stats = {}
    section_missing = {}
    for section_name, endpoints in doc_sections.items():
        total = len(endpoints)
        section_matched = 0
        missing = []
        for endpoint in endpoints:
            if any(endpoints_match(endpoint, bcq_ep) for bcq_ep in bcq_endpoints):
                section_matched += 1
            else:
                missing.append(f"{endpoint[0]} {endpoint[1]}")
        section_stats[section_name] = (section_matched, total)
        if missing:
            section_missing[section_name] = missing

    for endpoint in unique_docs:
        if any(endpoints_match(endpoint, bcq_ep) for bcq_ep in bcq_endpoints):
            matched += 1

    total = len(unique_docs)
    pct = (matched / total * 100) if total else 0.0
    print(f"overall: {matched}/{total} ({pct:.1f}%)")

    full = sorted([s for s, (m, t) in section_stats.items() if m == t])
    partial = sorted([s for s, (m, t) in section_stats.items() if 0 < m < t])
    zero = sorted([s for s, (m, t) in section_stats.items() if m == 0])

    print(f"full: {len(full)}")
    print(f"partial: {len(partial)}")
    print(f"zero: {len(zero)}")

    if args.show_missing:
        for section in partial + zero:
            missing = section_missing.get(section, [])
            if not missing:
                continue
            print(f"\n{section}")
            for item in missing:
                print(f"  {item}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
