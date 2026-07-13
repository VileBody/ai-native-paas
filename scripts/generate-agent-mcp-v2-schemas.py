#!/usr/bin/env python3
"""Generate the closed Project MCP v2 schemas and compatibility metadata."""

import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "contracts" / "mcp" / "v2"
OPENAPI_OUT = ROOT / "contracts" / "openapi" / "agent-v2.openapi.json"
BASE = "https://platform.example.com/contracts/mcp/v2/"
DRAFT = "https://json-schema.org/draft/2020-12/schema"


def obj(properties=None, required=()):
    value = {"type": "object", "additionalProperties": False, "properties": properties or {}}
    if required:
        value["required"] = list(required)
    return value


def string(*, minimum=1, maximum=4096, pattern=None, enum=None):
    value = {"type": "string", "minLength": minimum, "maxLength": maximum}
    if pattern:
        value["pattern"] = pattern
    if enum:
        value["enum"] = enum
    return value


ID = string(maximum=63, pattern=r"^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$")
NAME = string(maximum=128)
PATH = string(maximum=4096, pattern=r"^(?!/)(?!.*(?:^|/)\.\.(?:/|$))(?!.*\\).+$")
DIGEST = string(minimum=71, maximum=71, pattern=r"^sha256:[0-9a-f]{64}$")
SHA = string(minimum=40, maximum=64, pattern=r"^[0-9a-f]{40,64}$")
URL = string(maximum=4096, pattern=r"^https://")
INT = {"type": "integer", "minimum": 0}
BOOL = {"type": "boolean"}
STRING_MAP = {"type": "object", "maxProperties": 128, "additionalProperties": string(minimum=0, maximum=4096)}

tools = {}


def add(group, name, properties=None, required=(), read_only=False):
    tools[name] = {
        "group": group,
        "readOnly": read_only,
        "requiredScope": "agent.tool:" + name,
        "schema": obj(properties, required),
    }


PROJECT = "project_repository"
WORKSPACE = "workspace"
INFRA = "infrastructure"
BUILD = "build"
GITOPS = "gitops_runtime"
PROVIDER = "provider_capability"
GOVERNANCE = "governance"

add(PROJECT, "project_create", {"name": NAME, "platform_contract": string(maximum=1048576)}, ("name",))
add(PROJECT, "project_get", read_only=True)
add(PROJECT, "project_get_policy", read_only=True)
add(PROJECT, "repository_status", read_only=True)
add(PROJECT, "repository_diff", {"base_sha": SHA, "target_sha": SHA}, read_only=True)
add(PROJECT, "repository_apply_patch", {"base_sha": SHA, "patch": string(maximum=1048576)}, ("base_sha", "patch"))
add(PROJECT, "repository_create_branch", {"branch": NAME, "base_sha": SHA}, ("branch", "base_sha"))
add(PROJECT, "repository_commit", {"branch": NAME, "base_sha": SHA, "message": string(maximum=500), "paths": {"type": "array", "minItems": 1, "maxItems": 1000, "items": PATH}}, ("branch", "base_sha", "message", "paths"))
add(PROJECT, "repository_push", {"branch": NAME, "commit_sha": SHA}, ("branch", "commit_sha"))
add(PROJECT, "repository_create_merge_request", {"source_branch": NAME, "target_branch": NAME, "title": string(maximum=500)}, ("source_branch", "target_branch", "title"))

add(WORKSPACE, "workspace_create", {"task_id": ID, "image_digest": DIGEST, "cpu_millis": INT, "memory_mib": INT, "ttl_seconds": INT}, ("task_id", "image_digest"))
add(WORKSPACE, "workspace_get", {"workspace_id": ID}, ("workspace_id",), True)
add(WORKSPACE, "workspace_exec", {"workspace_id": ID, "argv": {"type": "array", "minItems": 1, "maxItems": 128, "items": string(minimum=0, maximum=4096)}, "working_dir": PATH, "timeout_seconds": INT, "environment_refs": STRING_MAP}, ("workspace_id", "argv", "timeout_seconds"))
add(WORKSPACE, "workspace_upload_artifact", {"workspace_id": ID, "path": PATH, "content_digest": DIGEST}, ("workspace_id", "path", "content_digest"))
add(WORKSPACE, "workspace_cancel_command", {"workspace_id": ID, "command_id": ID}, ("workspace_id", "command_id"))
add(WORKSPACE, "workspace_destroy", {"workspace_id": ID, "reason": string(maximum=500)}, ("workspace_id",))

add(INFRA, "infra_init", {"workspace_id": ID, "root": PATH}, ("workspace_id", "root"))
add(INFRA, "infra_validate", {"workspace_id": ID, "root": PATH}, ("workspace_id", "root"))
add(INFRA, "infra_plan", {"workspace_id": ID, "root": PATH, "source_sha": SHA, "variables": STRING_MAP, "destroy": BOOL}, ("workspace_id", "root", "source_sha"))
add(INFRA, "infra_get_plan", {"plan_id": ID}, ("plan_id",), True)
add(INFRA, "infra_apply", {"plan_id": ID, "plan_hash": DIGEST, "estimate_version": ID, "reservation_id": ID, "approval_grant_id": ID}, ("plan_id", "plan_hash", "estimate_version", "reservation_id"))
add(INFRA, "infra_destroy", {"plan_id": ID, "plan_hash": DIGEST, "estimate_version": ID, "reservation_id": ID, "approval_grant_id": ID}, ("plan_id", "plan_hash", "estimate_version", "reservation_id", "approval_grant_id"))
add(INFRA, "infra_state_list", {"state_generation": INT}, read_only=True)
add(INFRA, "infra_import", {"workspace_id": ID, "address": NAME, "external_id": NAME}, ("workspace_id", "address", "external_id"))

build_spec = obj({"source_sha": SHA, "driver": string(enum=["dockerfile", "buildpacks", "nix", "custom-approved"]), "definition_path": PATH, "platforms": {"type": "array", "minItems": 1, "maxItems": 8, "items": string(enum=["linux/amd64", "linux/arm64"])}, "secret_refs": {"type": "array", "maxItems": 128, "items": ID}, "timeout_seconds": INT}, ("source_sha", "driver", "platforms", "timeout_seconds"))
add(BUILD, "build_execute", {"workspace_id": ID, "spec": build_spec}, ("workspace_id", "spec"))
add(BUILD, "build_get", {"build_id": ID}, ("build_id",), True)
add(BUILD, "artifact_get", {"artifact_id": ID}, ("artifact_id",), True)
add(BUILD, "artifact_verify", {"artifact_digest": DIGEST, "policy_version": ID}, ("artifact_digest", "policy_version"), True)

add(GITOPS, "gitops_validate", {"revision_sha": SHA, "path": PATH, "renderer": string(enum=["helm", "kustomize", "plain"])}, ("revision_sha", "path", "renderer"))
add(GITOPS, "gitops_commit", {"base_sha": SHA, "environment": ID, "artifact_digest": DIGEST, "manifest_digest": DIGEST}, ("base_sha", "environment", "artifact_digest", "manifest_digest"))
add(GITOPS, "argocd_sync", {"application_id": ID, "revision_sha": SHA}, ("application_id", "revision_sha"))
add(GITOPS, "argocd_get_status", {"application_id": ID}, ("application_id",), True)
add(GITOPS, "argocd_rollback", {"application_id": ID, "base_sha": SHA, "target_revision": SHA, "approval_grant_id": ID}, ("application_id", "base_sha", "target_revision"))
add(GITOPS, "deployment_get_logs", {"application_id": ID, "limit": {"type": "integer", "minimum": 1, "maximum": 10000}}, ("application_id",), True)
add(GITOPS, "deployment_get_events", {"application_id": ID, "limit": {"type": "integer", "minimum": 1, "maximum": 10000}}, ("application_id",), True)
add(GITOPS, "deployment_http_probe", {"url": URL, "expected_status": {"type": "integer", "minimum": 100, "maximum": 599}, "timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 60}}, ("url", "expected_status"), True)

add(PROVIDER, "secret_set", {"name": string(maximum=128, pattern=r"^[A-Z][A-Z0-9_]{0,127}$"), "value": string(maximum=65536)}, ("name", "value"))
add(PROVIDER, "secret_list_metadata", read_only=True)
add(PROVIDER, "credential_request", {"provider": ID, "audience": NAME, "scopes": {"type": "array", "minItems": 1, "maxItems": 64, "items": NAME}, "ttl_seconds": {"type": "integer", "minimum": 1, "maximum": 3600}}, ("provider", "audience", "scopes", "ttl_seconds"))
add(PROVIDER, "credential_revoke", {"lease_id": ID}, ("lease_id",))
add(PROVIDER, "recipe_search", {"query": string(minimum=0, maximum=500), "kind": NAME}, read_only=True)
add(PROVIDER, "recipe_get", {"recipe_id": ID, "version": NAME}, ("recipe_id", "version"), True)
add(PROVIDER, "provider_connect", {"provider": ID, "credential_binding_id": ID}, ("provider", "credential_binding_id"))
add(PROVIDER, "provider_plan_resource", {"recipe_id": ID, "recipe_version": NAME, "parameters": {"type": "object", "maxProperties": 256}}, ("recipe_id", "recipe_version", "parameters"))
add(PROVIDER, "capability_bind", {"kind": string(enum=["llm.openrouter-compatible", "parser.apify", "parser.bright-data"]), "budget_id": ID, "rate_limit_id": ID}, ("kind", "budget_id", "rate_limit_id"))
add(PROVIDER, "capability_get_usage", {"binding_id": ID, "period": ID}, ("binding_id", "period"), True)

add(GOVERNANCE, "cost_estimate", {"plan_id": ID, "plan_hash": DIGEST}, ("plan_id", "plan_hash"), True)
add(GOVERNANCE, "approval_request", {"plan_hash": DIGEST, "estimate_version": ID, "target": NAME, "reason": string(maximum=1000)}, ("plan_hash", "estimate_version", "target", "reason"))
add(GOVERNANCE, "approval_get", {"approval_request_id": ID}, ("approval_request_id",), True)
add(GOVERNANCE, "operation_get", {"operation_id": ID}, ("operation_id",), True)
add(GOVERNANCE, "operation_wait", {"operation_id": ID, "timeout_seconds": {"type": "integer", "minimum": 1, "maximum": 60}}, ("operation_id",), True)
add(GOVERNANCE, "operation_cancel", {"operation_id": ID, "reason": string(maximum=500)}, ("operation_id",))
add(GOVERNANCE, "usage_get", {"period": ID}, ("period",), True)

compatibility = {
    "platform_create_project": ["project_create"],
    "platform_get_project": ["project_get"],
    "platform_apply_repository_patch": ["repository_apply_patch"],
    "platform_create_branch": ["repository_create_branch"],
    "platform_create_merge_request": ["repository_create_merge_request"],
    "platform_request_build": ["build_execute"],
    "platform_get_build": ["build_get"],
    "platform_deploy": ["gitops_validate", "gitops_commit", "argocd_sync"],
    "platform_get_deployment": ["argocd_get_status"],
    "platform_rollback": ["argocd_rollback"],
    "platform_set_secret": ["secret_set"],
    "platform_list_secret_metadata": ["secret_list_metadata"],
    "platform_provision_service": ["provider_plan_resource", "infra_plan", "infra_apply"],
    "platform_bind_service": ["provider_connect"],
    "platform_add_domain": ["provider_plan_resource", "infra_plan", "infra_apply"],
    "platform_get_logs": ["deployment_get_logs"],
    "platform_get_usage": ["usage_get"],
    "platform_request_approval": ["approval_request"],
    "platform_get_operation": ["operation_get"],
    "platform_cancel_operation": ["operation_cancel"],
}


def render_outputs():
    rendered = {}
    catalog_tools = []
    for name in sorted(tools):
        metadata = tools[name]
        schema = {
            "$schema": DRAFT,
            "$id": BASE + name + ".schema.json",
            "title": name + " arguments",
            **metadata["schema"],
        }
        rendered[OUT / (name + ".schema.json")] = schema
        catalog_tools.append({
            "name": name,
            "group": metadata["group"],
            "read_only": metadata["readOnly"],
            "required_scope": metadata["requiredScope"],
            "inputSchema": BASE + name + ".schema.json",
            "semanticsVersion": "v2",
        })
    rendered[OUT / "catalog.json"] = {"api_version": "agent.platform.example.com/v2", "semantics_version": "v2", "tools": catalog_tools}
    rendered[OUT / "compatibility-v1.json"] = {
        "api_version": "agent.platform.example.com/v2",
        "routes": [{"v1_tool": name, "v2_targets": targets, "governance_required": True, "deprecation": "MCP v1 compatibility façade; migrate to Project MCP v2"} for name, targets in sorted(compatibility.items())],
    }
    invocation = obj({"api_version": {"const": "agent.platform.example.com/v2"}, "semantics_version": {"const": "v2"}, "task_id": ID, "tool": {"type": "string", "enum": sorted(tools)}, "arguments": {}, "idempotency_key": string(maximum=128), "correlation_id": ID, "approval_grant_id": ID}, ("api_version", "semantics_version", "task_id", "tool", "arguments", "idempotency_key", "correlation_id"))
    rendered[OUT / "invocation-request.schema.json"] = {"$schema": DRAFT, "$id": BASE + "invocation-request.schema.json", "title": "Verified-scope Project MCP invocation", **invocation}
    operation = obj({"operation_id": ID, "tenant_id": ID, "project_id": ID, "state": string(enum=["PENDING", "RUNNING", "WAITING_APPROVAL", "WAITING_DEPENDENCY", "SUCCEEDED", "FAILED", "CANCELED"])}, ("operation_id", "tenant_id", "project_id", "state"))
    response = obj({"api_version": {"const": "agent.platform.example.com/v2"}, "invocation_id": ID, "operation": operation, "result": {}, "error": obj({"code": NAME, "message": string(maximum=1024), "retryable": BOOL}, ("code", "message", "retryable")), "replayed": BOOL}, ("api_version", "invocation_id", "replayed"))
    response["oneOf"] = [{"required": ["operation"], "not": {"required": ["result"]}}, {"required": ["result"], "not": {"required": ["operation"]}}]
    rendered[OUT / "invocation-response.schema.json"] = {"$schema": DRAFT, "$id": BASE + "invocation-response.schema.json", "title": "Project MCP invocation response", **response}
    rendered[OPENAPI_OUT] = {
        "openapi": "3.1.0",
        "info": {"title": "AI-native DevOps Project MCP API", "version": "2.0.0-beta.1"},
        "paths": {
            "/healthz": {"get": {"responses": {"200": {"description": "healthy"}}}},
            "/api/v2/projects": {"post": {"responses": {"201": {"description": "project, Git URL, MCP URL and one-time enrollment token"}}}},
            "/mcp/v2/tools": {"get": {"responses": {"200": {"description": "closed Project MCP catalog"}}}},
            "/mcp/v2/invoke": {"post": {"requestBody": {"required": True, "content": {"application/json": {"schema": {"$ref": "../mcp/v2/invocation-request.schema.json"}}}}, "responses": {"200": {"description": "synchronous result"}, "202": {"description": "asynchronous operation"}}}},
            "/api/v2/agent-enrollments/exchange": {"post": {"responses": {"201": {"description": "scoped refresh credential"}}}},
            "/api/v2/agent-token": {"post": {"responses": {"201": {"description": "15 minute access JWT"}}}},
        },
    }
    return rendered


def serialized(value):
    return json.dumps(value, indent=2, sort_keys=True) + "\n"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    rendered = render_outputs()
    stale = []
    for output, value in rendered.items():
        content = serialized(value)
        if args.check:
            if not output.exists() or output.read_text() != content:
                stale.append(str(output.relative_to(ROOT)))
        else:
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(content)
    if args.check and stale:
        raise SystemExit("stale generated MCP v2 contracts: " + ", ".join(stale))
    if not args.check:
        expected = {path.name for path in rendered if path.parent == OUT}
        for old in OUT.glob("*.json"):
            if old.name not in expected:
                old.unlink()


if __name__ == "__main__":
    main()
