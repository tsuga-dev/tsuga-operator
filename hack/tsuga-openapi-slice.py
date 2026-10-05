#!/usr/bin/env python3
"""Extract the Tsuga-backed operator API and flatten pass-through unions."""

import argparse
import copy
import json
from pathlib import Path


RESOURCES = ("dashboards", "monitors", "slos")
PATHS = {f"/v1/{resource}{suffix}" for resource in RESOURCES for suffix in ("", "/{id}", "/query")}
OPAQUE = {
    "type": "object",
    "additionalProperties": True,
    "description": "Opaque Tsuga configuration passed through by the operator",
}


def flatten_opaque_fields(spec):
    for resource in RESOURCES:
        for suffix, method in (("", "post"), ("/{id}", "put")):
            properties = spec["paths"][f"/v1/{resource}{suffix}"][method]["requestBody"]["content"]["application/json"]["schema"]["properties"]
            if resource == "dashboards":
                properties["graphs"]["items"]["properties"]["visualization"] = copy.deepcopy(OPAQUE)
            else:
                properties["configuration"] = copy.deepcopy(OPAQUE)
            if resource == "slos":
                properties["alerts"]["items"]["properties"]["configuration"] = copy.deepcopy(OPAQUE)

    # The current operator reads only data.id (and data[].tags on queries, to
    # verify ownership before adoption) from successful responses and branches
    # on the HTTP status for errors. Reduce response unions accordingly.
    for name in ("Dashboard", "Monitor", "Slo"):
        tags = spec["components"]["schemas"][name]["properties"]["tags"]
        spec["components"]["schemas"][name] = {
            "type": "object",
            "required": ["id"],
            "properties": {"id": {"type": "string"}, "tags": tags},
        }
    for name in ("ClientErrorEnvelope", "ServerErrorEnvelope"):
        spec["components"]["schemas"][name] = copy.deepcopy(OPAQUE)


def referenced_components(spec):
    available = spec["components"]
    selected = {}
    queue = []

    def collect(value):
        if isinstance(value, dict):
            ref = value.get("$ref")
            if isinstance(ref, str) and ref.startswith("#/components/"):
                queue.append(ref)
            for child in value.values():
                collect(child)
        elif isinstance(value, list):
            for child in value:
                collect(child)

    collect(spec["paths"])
    # Security requirements name their schemes instead of using $ref.
    requirements = list(spec.get("security", []))
    for operations in spec["paths"].values():
        for operation in operations.values():
            if isinstance(operation, dict):
                requirements.extend(operation.get("security", []))
    for requirement in requirements:
        for name in requirement:
            queue.append(f"#/components/securitySchemes/{name}")
    seen = set()
    while queue:
        ref = queue.pop()
        if ref in seen:
            continue
        seen.add(ref)
        _, _, section, name = ref.split("/", 3)
        value = available[section][name]
        selected.setdefault(section, {})[name] = value
        collect(value)
    return selected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("--opaque", action="store_true", help="replace the operator's pass-through unions with JSON objects")
    args = parser.parse_args()

    spec = json.loads(args.source.read_text())
    missing = PATHS - spec["paths"].keys()
    if missing:
        parser.error(f"missing expected paths: {sorted(missing)}")
    spec["paths"] = {path: spec["paths"][path] for path in sorted(PATHS)}
    if args.opaque:
        flatten_opaque_fields(spec)
    spec["components"] = referenced_components(spec)
    args.output.write_text(json.dumps(spec, indent=2, sort_keys=True) + "\n")
    print(f"wrote {args.output}: {len(spec['paths'])} paths, {len(spec['components'].get('schemas', {}))} schemas")


if __name__ == "__main__":
    main()
