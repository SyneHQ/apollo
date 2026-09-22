"""Build a separate local fixture image without registry resolution or packages."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import uuid


IMAGE_ID = re.compile(r"sha256:[a-f0-9]{64}\Z")
FILES = ("__init__.py", "provider.py", "server.py", "sitecustomize.py", "transport.py")
LABELS = {"syne.fixture": "shopify-refund-acceptance", "syne.fixture.boundary": "provider DNS/TLS substituted; not a production worker"}


def checked(condition, message):
    if not condition:
        raise RuntimeError(message)


def docker(args, env, *, timeout=30, output=None):
    try:
        result = subprocess.run(["docker", *args], env=env, text=True, stdout=output or subprocess.PIPE,
                                stderr=output or subprocess.PIPE, timeout=timeout, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise RuntimeError("Local Docker command unavailable or timed out") from error
    checked(result.returncode == 0, "Local Docker operation failed; no registry fallback is permitted")
    return result.stdout.strip() if result.stdout is not None else ""


def inspect(reference, env):
    images = json.loads(docker(["image", "inspect", reference], env))
    checked(len(images) == 1 and IMAGE_ID.fullmatch(images[0].get("Id", "")), "Expected exactly one installed image")
    return images[0]


def local_environment():
    env = dict(os.environ)
    if env.get("DOCKER_HOST") and not env.get("DOCKER_CONTEXT"):
        endpoint = env["DOCKER_HOST"]
    else:
        endpoint = json.loads(docker(["context", "inspect", "--format", "{{json .Endpoints.docker.Host}}"], env))
    checked(isinstance(endpoint, str) and endpoint.startswith("unix:///"), "Select a local Unix-socket Docker engine")
    env.pop("DOCKER_CONTEXT", None)
    env["DOCKER_HOST"] = endpoint
    # The legacy builder resolves this inspected alias from the daemon's image
    # store. BuildKit may consult registry metadata even with --pull=false.
    env["DOCKER_BUILDKIT"] = "0"
    return env


def reviewed_context(source, destination):
    checked((source / "Dockerfile").is_file() and not (source / "Dockerfile").is_symlink(), "Dockerfile must be a regular local file")
    raw = (source / "Dockerfile").read_text()
    checked(not re.search(r"^\s*#\s*syntax\s*=", raw, re.MULTILINE | re.IGNORECASE), "Remote Dockerfile frontends are not allowed")
    instructions = [" ".join(line.split()) for line in re.sub(r"\\\n\s*", " ", raw).splitlines()
                    if line.strip() and not line.lstrip().startswith("#")]
    checked(instructions == [
        "ARG BASE_WORKER_IMAGE", "FROM ${BASE_WORKER_IMAGE}",
        'LABEL syne.fixture="shopify-refund-acceptance" syne.fixture.boundary="provider DNS/TLS substituted; not a production worker"',
        "COPY tests/shopify_fixture /opt/syne-shopify-fixture/shopify_fixture",
        "COPY tests/shopify_fixture/sitecustomize.py /opt/syne-shopify-fixture/sitecustomize.py",
        "ENV PYTHONPATH=/opt/syne-shopify-fixture",
    ], "Fixture Dockerfile changed; review the offline build contract before building")
    target = destination / "tests" / "shopify_fixture"
    target.mkdir(parents=True)
    hashes = {"Dockerfile": hashlib.sha256(raw.encode()).hexdigest()}
    (destination / "Dockerfile").write_text(raw)
    for name in FILES:
        path = source / name
        checked(path.is_file() and not path.is_symlink(), "Fixture source must be a regular local file")
        content = path.read_bytes()
        checked(len(content) <= 128 * 1024, "Fixture source exceeds the build bound")
        (target / name).write_bytes(content)
        hashes[name] = hashlib.sha256(content).hexdigest()
    return hashes


def verify_image(base, result):
    checked(result["Id"] != base["Id"], "Fixture build did not create a derived image")
    for key in ("Os", "Architecture", "Variant"):
        checked(result.get(key) == base.get(key), "Fixture platform differs from its inspected base")
    layers = base.get("RootFS", {}).get("Layers", [])
    derived = result.get("RootFS", {}).get("Layers", [])
    checked(layers and len(derived) > len(layers) and derived[:len(layers)] == layers, "Fixture layers do not descend from the inspected base")
    original, config = base["Config"], result["Config"]
    checked(original.get("User") and original["User"].split(":")[0] not in {"0", "root"}, "Base worker must already run as non-root")
    for key in ("User", "Entrypoint", "Cmd", "WorkingDir", "StopSignal", "Healthcheck", "ExposedPorts", "Volumes", "Shell", "OnBuild", "ArgsEscaped"):
        checked(config.get(key) == original.get(key), "Fixture changed inherited runtime configuration")
    environment = lambda values: dict(value.split("=", 1) for value in values or [])
    expected = environment(original.get("Env"))
    expected["PYTHONPATH"] = "/opt/syne-shopify-fixture"
    checked(environment(config.get("Env")) == expected, "Fixture changed an unexpected environment setting")
    checked(config.get("Labels") == {**(original.get("Labels") or {}), **LABELS}, "Fixture labels do not match the acceptance boundary")
    return {"platform": f'{result["Os"]}/{result["Architecture"]}', "baseLayers": len(layers), "fixtureLayers": len(derived),
            "inheritedNonRootRuntime": True, "verifiedBaseLayerPrefix": True}


def build(base_id, source, env):
    checked(IMAGE_ID.fullmatch(base_id), "Supply the full installed immutable sha256 image ID")
    base = inspect(base_id, env)
    checked(base["Id"] == base_id, "Installed base identity changed")
    checked(not base["Config"].get("OnBuild"), "Base image must not have hidden ONBUILD instructions")
    checked(env.get("DOCKER_BUILDKIT") == "0", "The local legacy builder is required; no BuildKit fallback is permitted")
    # A persistent pre-existing tag protects the base when our temporary alias is
    # removed, including when the build fails before producing a child image.
    original_tags = set(base.get("RepoTags") or [])
    checked(original_tags, "Base needs a pre-existing persistent tag; the helper will not remove its last image reference")
    alias = "syne-shopify-fixture-base-" + uuid.uuid4().hex + ":local"
    checked(not docker(["image", "ls", "--quiet", "--filter", "reference=" + alias], env), "Temporary alias already exists; refusing to overwrite it")
    created = False
    try:
        docker(["image", "tag", base_id, alias], env)
        created = True
        checked(inspect(alias, env)["Id"] == base_id, "Task base alias does not match its immutable ID")
        with tempfile.TemporaryDirectory(prefix="syne-shopify-fixture-build-") as temporary:
            context = Path(temporary) / "context"
            context.mkdir()
            hashes = reviewed_context(source, context)
            iid = Path(temporary) / "result.iid"
            with (Path(temporary) / "build.log").open("w") as log:
                docker(["build", "--pull=false", "--network=none", "--rm=true", "--force-rm=true",
                        "--build-arg", "BASE_WORKER_IMAGE=" + alias, "--iidfile", str(iid),
                        "-f", str(context / "Dockerfile"), str(context)], env, timeout=180, output=log)
            result_id = iid.read_text().strip()
            checked(IMAGE_ID.fullmatch(result_id), "Build did not return an immutable image ID")
            checked(inspect(alias, env)["Id"] == base_id and inspect(base_id, env)["Id"] == base_id, "Base identity changed during build")
            verified = verify_image(base, inspect(result_id, env))
            evidence = {"baseImage": base_id, "fixtureImage": result_id, "builder": "local legacy Docker builder; registry fallback disabled",
                        "sourceSha256": hashes, **verified}
    finally:
        if created:
            current = inspect(alias, env)
            checked(current["Id"] == base_id, "Task alias changed; refusing to remove an unrelated image reference")
            checked(set(current.get("RepoTags") or []) & original_tags, "Original base tags disappeared; retaining the task alias to preserve the base")
            docker(["image", "rm", "--no-prune", alias], env)
            checked(inspect(base_id, env)["Id"] == base_id, "Base image was not preserved after alias cleanup")
    evidence["temporaryBaseTagRemoved"] = True
    return evidence


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-image", required=True)
    parser.add_argument("--evidence-file", type=Path)
    args = parser.parse_args()
    try:
        checked(IMAGE_ID.fullmatch(args.base_image), "Supply the full installed immutable sha256 image ID")
        if args.evidence_file:
            checked(args.evidence_file.is_absolute(), "Evidence path must be absolute")
            checked(args.evidence_file.parent.is_dir() and os.access(args.evidence_file.parent, os.W_OK), "Evidence directory must already exist and be writable")
            checked(not args.evidence_file.exists() and not args.evidence_file.is_symlink(), "Evidence file already exists; choose a new path")
        result = build(args.base_image, Path(__file__).resolve().parent, local_environment())
        if args.evidence_file:
            with args.evidence_file.open("x") as output:
                json.dump(result, output, indent=2)
                output.write("\n")
        print(json.dumps(result, indent=2))
    except (RuntimeError, ValueError, OSError) as error:
        parser.exit(1, str(error) + "\n")


if __name__ == "__main__":
    main()
