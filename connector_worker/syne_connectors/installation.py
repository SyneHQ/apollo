"""Private package admission bound to the coordinator's scoped, trusted handoff."""
from hashlib import sha256
import re
from uuid import UUID

from .manifest import canonical, content_digest, parse_json, require


def validate_installation(env, data, manifest, values):
    trusted = env.get("CONNECTOR_PRIVATE_INSTALLATION")
    received = data.get("installation")
    if trusted is None:
        require("installation" not in data and manifest["publisher"]["id"] == "syne"
                and manifest["id"].startswith("syne/"), "private_installation_required")
        return
    require(env.get("CONNECTOR_PRIVATE_SYNCS_ENABLED") == "true", "private_sync_disabled")
    require(isinstance(trusted, str), "private_installation_invalid")
    expected = parse_json(trusted.encode("utf-8"), 4096)
    require(isinstance(expected, dict) and set(expected) == {
        "id", "policy_digest", "approved_origin", "manifest_digest", "team_id", "binding"
    }, "private_installation_invalid")
    require(all(isinstance(value, str) for value in expected.values()), "private_installation_invalid")
    try:
        valid_id = str(UUID(expected["id"])) == expected["id"]
    except ValueError:
        valid_id = False
    require(valid_id and 1 <= len(expected["team_id"]) <= 100, "private_installation_invalid")
    require(all(re.fullmatch(r"[a-f0-9]{64}", expected[key]) for key in ["policy_digest", "manifest_digest", "binding"]), "private_installation_invalid")
    require(isinstance(received, dict) and set(received) == {"id", "policy_digest", "approved_origin"}
            and received == {key: expected[key] for key in received}, "private_installation_mismatch")
    runtime = manifest["runtime"]
    require(manifest["publisher"]["id"] != "syne" and not manifest["id"].startswith("syne/")
            and runtime["kind"] == "rest" and "subdomainField" not in runtime["origin"]
            and not manifest["permissions"]["localFiles"] and not manifest["capabilities"]["preview"]
            and manifest["capabilities"]["fullRefresh"] and not manifest["capabilities"]["incremental"]
            and manifest["limits"]["concurrency"] == 1
            and all(stream["sync"]["mode"] == "snapshot" for stream in manifest["streams"]), "private_runtime_unsupported")
    require(expected["manifest_digest"] == data["manifest_digest"]
            and expected["approved_origin"] == "https://" + runtime["origin"]["host"], "private_origin_mismatch")
    policy = {"version": 1, "installationId": expected["id"], "teamId": expected["team_id"],
              "manifestDigest": expected["manifest_digest"], "approvedOrigin": expected["approved_origin"]}
    require(sha256(b"syne:connector-install-policy:v1\n" + canonical(policy)).hexdigest() == expected["policy_digest"], "private_policy_mismatch")
    secrets = {field["key"] for field in manifest["configuration"] if field["secret"]}
    # The app stores effective validated nonsecret defaults in its run snapshot.
    nonsecret = {key: value for key, value in values.items() if key not in secrets}
    binding = {"manifestDigest": expected["manifest_digest"], "configuration": nonsecret,
               "fileHash": None, "installationId": expected["id"], "policyDigest": expected["policy_digest"]}
    require(content_digest(binding) == expected["binding"], "private_binding_mismatch")
