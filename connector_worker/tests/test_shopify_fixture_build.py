"""Offline fixture-builder guards; no Docker calls or service startup."""
import copy
import json
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from shopify_fixture import build_image as builder

BASE = "sha256:" + "a" * 64
RESULT = "sha256:" + "b" * 64
SOURCE = Path(builder.__file__).parent


def base_image():
    return {"Id": BASE, "Os": "linux", "Architecture": "arm64", "RepoTags": ["existing-worker:local"],
            "RootFS": {"Layers": ["sha256:layer1", "sha256:layer2"]},
            "Config": {"User": "10001:10001", "WorkingDir": "/app", "Cmd": ["syne-connector-worker"], "Env": ["EXISTING=kept"]}}


def fixture_image():
    image = base_image()
    image["Id"] = RESULT
    image["RootFS"]["Layers"].append("sha256:fixture")
    image["Config"]["Env"].append("PYTHONPATH=/opt/syne-shopify-fixture")
    image["Config"]["Labels"] = dict(builder.LABELS)
    return image


class BuildGuards(unittest.TestCase):
    def test_verified_layer_ancestry_and_inherited_runtime(self):
        self.assertTrue(builder.verify_image(base_image(), fixture_image())["inheritedNonRootRuntime"])
        for key, value in (("User", "0"), ("Cmd", ["sh"]), ("WorkingDir", "/other"), ("Env", ["PYTHONPATH=/other"]), ("Labels", {})):
            image = fixture_image()
            image["Config"][key] = value
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                builder.verify_image(base_image(), image)
        image = fixture_image()
        image["RootFS"]["Layers"][0] = "another-base"
        with self.assertRaisesRegex(RuntimeError, "descend"):
            builder.verify_image(base_image(), image)

    def test_context_contains_only_reviewed_fixture_files(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            hashes = builder.reviewed_context(SOURCE, target)
            self.assertEqual(set(hashes), {"Dockerfile", *builder.FILES})
            self.assertEqual({str(p.relative_to(target)) for p in target.rglob("*") if p.is_file()},
                             {"Dockerfile", *("tests/shopify_fixture/" + name for name in builder.FILES)})
            raw = (target / "Dockerfile").read_text()
            (target / "Dockerfile").write_text("# syntax=example.invalid/frontend\n" + raw)
            with self.assertRaisesRegex(RuntimeError, "frontends"):
                builder.reviewed_context(target, target / "rejected")

    def test_local_endpoint_is_pinned_and_buildkit_disabled(self):
        with patch.dict(builder.os.environ, {"DOCKER_HOST": "unix:///tmp/task.sock", "DOCKER_CONTEXT": ""}, clear=True):
            env = builder.local_environment()
        self.assertEqual(env["DOCKER_HOST"], "unix:///tmp/task.sock")
        self.assertEqual(env["DOCKER_BUILDKIT"], "0")
        self.assertNotIn("DOCKER_CONTEXT", env)
        with patch.dict(builder.os.environ, {"DOCKER_HOST": "tcp://remote:2375"}, clear=True):
            with self.assertRaisesRegex(RuntimeError, "local Unix"):
                builder.local_environment()

    def test_hidden_base_instructions_and_mutable_ids_fail_before_tagging(self):
        base = base_image()
        base["Config"]["OnBuild"] = ["RUN download-command"]
        with patch.object(builder, "inspect", return_value=base), patch.object(builder, "docker") as command:
            with self.assertRaisesRegex(RuntimeError, "ONBUILD"):
                builder.build(BASE, SOURCE, {"DOCKER_BUILDKIT": "0"})
            command.assert_not_called()
        with patch.object(builder, "inspect") as inspect:
            with self.assertRaisesRegex(RuntimeError, "immutable"):
                builder.build("worker:latest", SOURCE, {"DOCKER_BUILDKIT": "0"})
            inspect.assert_not_called()

    def test_evidence_path_fails_before_building(self):
        with patch("sys.argv", ["build_image.py", "--base-image", BASE, "--evidence-file", "relative.json"]), \
                patch.object(builder, "local_environment") as environment, patch.object(builder, "build") as build, patch("sys.stderr", io.StringIO()):
            with self.assertRaises(SystemExit) as error:
                builder.main()
            self.assertEqual(error.exception.code, 1)
            environment.assert_not_called()
            build.assert_not_called()

    def simulate(self, fail_build=False, original_tags=True):
        base = base_image()
        if not original_tags:
            base["RepoTags"] = []
        calls, alias = [], None

        def docker(args, _env, **kwargs):
            nonlocal alias
            calls.append(args)
            if args[:2] == ["image", "inspect"]:
                image = fixture_image() if args[2] == RESULT else copy.deepcopy(base)
                if alias and args[2] != RESULT:
                    image["RepoTags"] = [*base["RepoTags"], alias]
                return json.dumps([image])
            if args[:2] == ["image", "tag"]:
                self.assertEqual(args[2], BASE)
                alias = args[3]
            if args[0] == "build":
                self.assertIn("--pull=false", args)
                self.assertIn("--network=none", args)
                self.assertIn("BASE_WORKER_IMAGE=" + alias, args)
                self.assertNotIn("BASE_WORKER_IMAGE=" + BASE, args)
                if fail_build:
                    raise RuntimeError("simulated unavailable legacy builder")
                Path(args[args.index("--iidfile") + 1]).write_text(RESULT)
            return ""

        with patch.object(builder, "docker", side_effect=docker):
            try:
                result = builder.build(BASE, SOURCE, {"DOCKER_BUILDKIT": "0"})
            except RuntimeError:
                if not (fail_build or not original_tags):
                    raise
                result = None
        return calls, alias, result

    def test_unique_temporary_alias_removed_after_verified_build(self):
        calls, alias, result = self.simulate()
        self.assertRegex(alias, r"^syne-shopify-fixture-base-[a-f0-9]{32}:local$")
        self.assertIn(["image", "rm", "--no-prune", alias], calls)
        self.assertEqual(result["fixtureImage"], RESULT)
        self.assertTrue(result["temporaryBaseTagRemoved"])
        self.assertFalse(any(args[0] in {"pull", "push", "run"} for args in calls))

    def test_failed_build_cleans_only_task_alias_without_fallback(self):
        calls, alias, result = self.simulate(fail_build=True)
        self.assertIsNone(result)
        self.assertIn(["image", "rm", "--no-prune", alias], calls)
        self.assertEqual(sum(args[0] == "build" for args in calls), 1)
        self.assertNotIn(["image", "rm", BASE], calls)

    def test_last_base_reference_is_never_removed(self):
        calls, alias, result = self.simulate(original_tags=False)
        self.assertIsNone(result)
        self.assertIsNone(alias)
        self.assertTrue(all(args[:2] == ["image", "inspect"] for args in calls))


if __name__ == "__main__":
    unittest.main()
