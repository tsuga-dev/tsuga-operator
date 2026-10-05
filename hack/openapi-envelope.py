#!/usr/bin/env python3
"""Blank the opaque payload subtrees before an OpenAPI drift diff.

The operator's real contract with the Tsuga API is narrow: it sends an opaque
payload envelope and reads only `data.id` back. Everything else is passthrough:

  - Request: graph visualizations, monitor configuration, and both SLO
    configuration fields (the SLI config and each alert's config) are opaque
    JSON (see api/v1alpha1: Visualization/Configuration are Schemaless), so
    churn inside those discriminator unions is not a contract break for us.
  - Response: the operator reads only `data.id`; the rest of the returned
    Dashboard/Monitor/Slo, and the create envelope's `requestId`, are never
    consumed. On a failure it branches on the HTTP status code, and reads the
    error envelope's `requestId`, `error.message` and `error.code` for logs and
    status messages (falling back to the raw body), so only those fields of the
    error envelopes are part of the contract.

oasdiff has no notion of an opaque subtree, so we reduce both specs to that
contract before diffing: blank the request unions, and project the response
Dashboard/Monitor/Slo models, create envelopes and error envelopes onto the
fields the operator reads (so a removal of `data.id` or `error.message` is
still caught, but other response body evolution is not). What remains to
diff is the request envelope (required fields, the enums the operator emits:
timePreset, priority, permissions, timeframeDays) plus those response fields.

Reads a spec on stdin, writes the filtered spec to stdout. If a target node is
absent (e.g. the spec restructured), it is left untouched, so the drift gate
starts reporting union noise again — a visible signal to update this filter.
"""
import json
import sys

OPAQUE = {"type": "object", "description": "opaque; operator passthrough"}

# (create path, dotted location of the opaque node within its request schema)
REQUEST_TARGETS = [
    ("/v1/dashboards", ["properties", "graphs", "items", "properties", "visualization"]),
    ("/v1/monitors", ["properties", "configuration"]),
    ("/v1/slos", ["properties", "configuration"]),
    ("/v1/slos", ["properties", "alerts", "items", "properties", "configuration"]),
]

# Response schemas reduced to the properties the operator reads. Projecting
# the real schema (rather than substituting a fixed shell) keeps a removal of
# one of these properties visible to the diff.
RESPONSE_FIELDS = {
    "Dashboard": ["id"],
    "Monitor": ["id"],
    "Slo": ["id"],
    # Create envelopes: `data` only, never `requestId`.
    "CreateDashboardResponse": ["data"],
    "CreateMonitorResponse": ["data"],
    "CreateSloResponse": ["data"],
    # Error envelopes feed logs and status messages.
    "ClientErrorEnvelope": ["requestId", "error"],
    "ServerErrorEnvelope": ["requestId", "error"],
    "ClientErrorResponse": ["message", "code"],
    "ServerErrorResponse": ["message", "code"],
}


def project(schema, fields):
    properties = {k: v for k, v in schema.get("properties", {}).items() if k in fields}
    # `code` is shown as free text, so new error codes are not breaks.
    if "code" in properties:
        properties["code"] = {k: v for k, v in properties["code"].items() if k != "enum"}
    out = {"type": "object", "properties": properties}
    required = [k for k in schema.get("required", []) if k in fields]
    if required:
        out["required"] = required
    return out


def request_schema(spec, path):
    return (
        spec.get("paths", {})
        .get(path, {})
        .get("post", {})
        .get("requestBody", {})
        .get("content", {})
        .get("application/json", {})
        .get("schema")
    )


def blank(node, keys):
    for key in keys[:-1]:
        if not isinstance(node, dict) or key not in node:
            return False
        node = node[key]
    if isinstance(node, dict) and keys[-1] in node:
        node[keys[-1]] = dict(OPAQUE)
        return True
    return False


def main():
    spec = json.load(sys.stdin)
    for path, keys in REQUEST_TARGETS:
        schema = request_schema(spec, path)
        if schema is None or not blank(schema, keys):
            print(f"openapi-envelope: warning: {path} {'.'.join(keys)} not found", file=sys.stderr)
    schemas = spec.get("components", {}).get("schemas", {})
    for model, fields in RESPONSE_FIELDS.items():
        if model in schemas:
            schemas[model] = project(schemas[model], fields)
        else:
            print(f"openapi-envelope: warning: component {model} not found", file=sys.stderr)
    json.dump(spec, sys.stdout)


if __name__ == "__main__":
    main()
