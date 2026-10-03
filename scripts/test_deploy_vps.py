"""Deployment safety tests use only temporary files and loopback HTTP."""

import argparse
from contextlib import closing, redirect_stdout
from email.message import Message
from http.server import BaseHTTPRequestHandler, HTTPServer
import io
import json
import os
from pathlib import Path
import platform
import sqlite3
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch

import deploy_vps as deploy


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        output = redirect_stdout(io.StringIO())
        output.__enter__()
        self.addCleanup(output.__exit__, None, None, None)
        previous_umask = os.umask(0o077)
        self.addCleanup(os.umask, previous_umask)
        directory = tempfile.TemporaryDirectory(prefix="guestbooks-deployment-test-")
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.stage = self.root / "stage"
        self.stage.mkdir()
        self.installer = deploy.Installer(self.stage, self.root)
        self.installer.data.mkdir(parents=True)
        self.installer.config.parent.mkdir(parents=True)
        self.installer.unit.parent.mkdir(parents=True)
        self.installer.caddy.parent.mkdir(parents=True)
        self.installer.config.write_text("synthetic configuration, never replaced")
        self.installer.unit.write_text("synthetic systemd unit, never replaced")
        self.installer.caddy.write_text("unrelated sites, never replaced")
        self.previous = self.installer.root / "releases/previous"
        self.previous.mkdir(parents=True)
        (self.previous / "guestbooks").write_text("previous binary")
        self.installer.current.symlink_to(self.previous)
        for name in ("assets", "templates"):
            (self.previous / name).mkdir()
            (self.installer.data / name).symlink_to(self.installer.current / name)
        (self.installer.data / "config.yaml").symlink_to(self.installer.config)
        with closing(sqlite3.connect(self.installer.database)) as connection:
            connection.executescript("""
                CREATE TABLE messages (id INTEGER PRIMARY KEY, text TEXT);
                INSERT INTO messages VALUES (1, CAST(X'ff00fe' AS TEXT));
            """)
        self.original = deploy.read_database_digest(self.installer.database)
        self.operations = []
        self.on_start = lambda: None
        self.addCleanup(patch.stopall)
        patch("deploy_vps.os.geteuid", return_value=0).start()
        patch.object(self.installer, "run", side_effect=self.fake_run).start()
        patch.object(self.installer, "local_health").start()
        self.process = patch.object(self.installer, "check_process").start()
        self.forms = patch("deploy_vps.check_forms").start()
        self.release = self.bundle()

    def fake_run(self, *args, cwd=None):
        self.assertEqual(args[0], "systemctl")
        action = args[1]
        if action == "show":
            return (f"User=guestbooks\nWorkingDirectory={self.installer.data}\n"
                    f"ExecStart={{ path={self.installer.current / 'guestbooks'} ; argv[]=guestbooks ; }}")
        if action in ("start", "stop"):
            self.operations.append(action)
        if action == "start":
            self.on_start()
        return ""

    def bundle(self):
        payload = self.root / "payload"
        (payload / "assets").mkdir(parents=True)
        (payload / "templates/admin").mkdir(parents=True)
        (payload / "guestbooks").write_text("new binary")
        (payload / "assets/script.js").write_text("synthetic asset")
        (payload / "templates/admin/signin.html").write_text("synthetic template")
        manifest = {"revision": "a" * 40, "architecture": platform.machine(),
                    "files": deploy.release_files(payload)}
        (payload / "release.json").write_text(json.dumps(manifest))
        with tarfile.open(self.stage / "release.tar", "w") as archive:
            for child in payload.iterdir():
                archive.add(child, arcname=child.name)
        (self.stage / "request.json").write_text(json.dumps({
            "public_url": "https://guestbooks.example.test",
            "archive_sha256": deploy.checksum(self.stage / "release.tar"),
        }))
        return self.installer.root / "releases" / ("a" * 40 + "-" + manifest["files"]["guestbooks"][:12])

    def receipt(self):
        return json.loads((self.stage / "deployment.json").read_text())

    def test_success_preserves_data_configuration_and_readable_release(self):
        self.installer.install()
        self.assertEqual(self.operations, ["stop", "start"])
        self.assertEqual(self.installer.current.resolve(), self.release)
        self.assertEqual(deploy.read_database_digest(self.installer.database), self.original)
        self.assertEqual(deploy.read_database_digest(self.stage / "before.db"), self.original)
        self.assertEqual(self.receipt()["status"], "healthy")
        for source, name in ((self.installer.config, "config.yaml"), (self.installer.unit, "guestbooks.service"),
                             (self.installer.caddy, "Caddyfile")):
            self.assertEqual(source.read_bytes(), (self.stage / name).read_bytes())
        for path in (self.release, self.release / "assets", self.release / "guestbooks"):
            self.assertEqual(path.stat().st_mode & 0o777, 0o755)
        self.assertEqual((self.release / "templates/admin/signin.html").stat().st_mode & 0o777, 0o644)
        self.assertEqual((self.stage / "before.db").stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.forms.call_count, 2)
        self.process.assert_called_with(self.release)

    def fail_after_start(self, mutation=None):
        def failure():
            self.on_start = lambda: None
            if mutation:
                with closing(sqlite3.connect(self.installer.database)) as connection:
                    connection.executescript(mutation)
            raise deploy.DeploymentError("synthetic startup failure")
        self.on_start = failure

    def test_start_failure_rolls_back_binary_without_restoring_database(self):
        self.fail_after_start()
        with self.assertRaisesRegex(deploy.DeploymentError, "synthetic startup failure"):
            self.installer.install()
        self.assertEqual(self.installer.current.resolve(), self.previous)
        self.assertEqual(self.operations, ["stop", "start", "stop", "start"])
        self.assertEqual(deploy.read_database_digest(self.installer.database), self.original)
        self.assertEqual(self.receipt()["status"], "rolled_back")

    def test_new_writes_prevent_automatic_rollback_or_data_restoration(self):
        self.fail_after_start("INSERT INTO messages VALUES (2, 'accepted after activation');")
        with self.assertRaisesRegex(deploy.DeploymentError, "database changed.*STOPPED"):
            self.installer.install()
        self.assertEqual(self.operations, ["stop", "start", "stop"])
        self.assertEqual(self.installer.current.resolve(), self.release)
        self.assertNotEqual(deploy.read_database_digest(self.installer.database), self.original)
        self.assertEqual(deploy.read_database_digest(self.stage / "before.db"), self.original)
        self.assertEqual(self.receipt()["status"], "needs_recovery")
        with closing(sqlite3.connect(self.installer.database)) as connection:
            self.assertEqual(connection.execute("SELECT text FROM messages WHERE id=2").fetchone()[0],
                             "accepted after activation")

    def test_schema_migration_prevents_automatic_downgrade(self):
        self.fail_after_start("ALTER TABLE messages ADD COLUMN new_field TEXT;")
        with self.assertRaisesRegex(deploy.DeploymentError, "database changed"):
            self.installer.install()
        self.assertEqual(self.operations[-1], "stop")
        self.assertEqual(self.receipt()["status"], "needs_recovery")

    def test_failed_backup_restarts_previous_release_without_switching(self):
        with patch("deploy_vps.backup_database", side_effect=sqlite3.OperationalError("synthetic backup failure")):
            with self.assertRaisesRegex(sqlite3.OperationalError, "backup failure"):
                self.installer.install()
        self.assertEqual(self.operations, ["stop", "stop", "start"])
        self.assertEqual(self.installer.current.resolve(), self.previous)
        self.assertEqual(deploy.read_database_digest(self.installer.database), self.original)

    def test_failed_public_check_after_activation_rolls_back(self):
        self.forms.side_effect = [None, deploy.DeploymentError("synthetic public failure")]
        with self.assertRaisesRegex(deploy.DeploymentError, "public failure"):
            self.installer.install()
        self.assertEqual(self.installer.current.resolve(), self.previous)
        self.assertEqual(self.receipt()["status"], "rolled_back")

    def test_upload_and_layout_errors_never_stop_service(self):
        with (self.stage / "release.tar").open("ab") as stream:
            stream.write(b"corrupt upload")
        with self.assertRaisesRegex(deploy.DeploymentError, "checksum mismatch"):
            self.installer.install()
        self.assertEqual(self.operations, [])
        self.assertEqual(self.installer.current.resolve(), self.previous)

    def test_same_release_is_an_idempotent_no_restart(self):
        self.installer.install()
        next_stage = self.root / "next-stage"
        next_stage.mkdir()
        for name in ("request.json", "release.tar"):
            (next_stage / name).write_bytes((self.stage / name).read_bytes())
        self.installer.stage = next_stage
        self.installer.log = next_stage / "install.log"
        self.operations.clear()
        self.installer.install()
        self.assertEqual(self.operations, [])
        self.assertEqual(json.loads((next_stage / "deployment.json").read_text())["status"], "already_current")
        self.assertFalse((next_stage / "before.db").exists())

    def test_incompatible_binary_fails_before_service_stop(self):
        self.installer.local_health.side_effect = deploy.DeploymentError("synthetic libc mismatch")
        with self.assertRaisesRegex(deploy.DeploymentError, "libc mismatch"):
            self.installer.install()
        self.assertEqual(self.operations, [])
        self.assertFalse((self.stage / "before.db").exists())

    def test_deployment_lock_excludes_another_installer(self):
        with (self.installer.root / ".deploy.lock").open("a") as lock:
            deploy.fcntl.flock(lock, deploy.fcntl.LOCK_EX | deploy.fcntl.LOCK_NB)
            with self.assertRaisesRegex(deploy.DeploymentError, "Another Guestbooks deployment"):
                self.installer.install()
        self.assertEqual(self.operations, [])

    def test_snapshot_includes_committed_wal_and_invalid_utf8(self):
        with closing(sqlite3.connect(self.installer.database)) as writer:
            writer.execute("PRAGMA journal_mode=WAL")
            writer.execute("INSERT INTO messages VALUES (2, 'in WAL')")
            writer.commit()
            backup = self.stage / "wal-snapshot.db"
            self.assertEqual(deploy.backup_database(self.installer.database, backup),
                             deploy.read_database_digest(self.installer.database))
            self.assertNotEqual(deploy.read_database_digest(backup), self.original)


class InputAndProbeTests(unittest.TestCase):
    def test_dirty_worktree_refuses_before_build_or_ssh(self):
        with patch("deploy_vps.platform.system", return_value="Linux"), \
                patch("deploy_vps.shutil.which", return_value="/synthetic/tool"), \
                patch("deploy_vps.subprocess.check_output", return_value=" M edited.go\n"), \
                patch("deploy_vps.ssh") as ssh, patch("deploy_vps.subprocess.run") as run:
            with self.assertRaisesRegex(deploy.DeploymentError, "worktree must be clean"):
                deploy.deploy(argparse.Namespace(host="example.test", public_url="https://example.test", yes=True))
            ssh.assert_not_called()
            run.assert_not_called()

    def test_local_flow_uses_committed_source_and_stages_a_complete_release(self):
        calls = []
        def run(command, **kwargs):
            calls.append(command)
            if command[0] == "git":
                archive_path = Path(command[command.index("-o") + 1])
                with tarfile.open(archive_path, "w") as archive:
                    for name, content in {
                        "scripts/deploy_vps.py": b"committed installer",
                        "assets/app.js": b"matching asset",
                        "templates/admin/signin.html": b"matching template",
                    }.items():
                        member = tarfile.TarInfo(name)
                        member.size = len(content)
                        archive.addfile(member, io.BytesIO(content))
            elif command[:2] == ["go", "build"]:
                Path(command[command.index("-o") + 1]).write_bytes(b"compiled binary")
                self.assertEqual(kwargs["env"]["CGO_ENABLED"], "1")
                self.assertEqual(kwargs["env"]["GOARCH"], "amd64")
            elif command[0] == "scp":
                paths = [Path(value) for value in command[2:-1]]
                self.assertEqual(paths[-1].read_bytes(), b"committed installer")
                metadata = json.loads(paths[1].read_text())
                self.assertEqual(metadata["archive_sha256"], deploy.checksum(paths[0]))
                with tarfile.open(paths[0]) as archive:
                    manifest = json.load(archive.extractfile("release.json"))
                    self.assertEqual(manifest["revision"], "b" * 40)
                    self.assertEqual(set(manifest["files"]),
                                     {"guestbooks", "assets/app.js", "templates/admin/signin.html"})
            return argparse.Namespace(returncode=0)
        with patch("deploy_vps.platform.system", return_value="Linux"), \
                patch("deploy_vps.platform.machine", return_value="x86_64"), \
                patch("deploy_vps.shutil.which", return_value="/synthetic/tool"), \
                patch("deploy_vps.subprocess.check_output", side_effect=["", "b" * 40]), \
                patch("deploy_vps.subprocess.run", side_effect=run), \
                patch("deploy_vps.ssh", return_value=argparse.Namespace(
                    stdout="/root/guestbooks-deploy-backups/update-Synthetic\n")) as ssh, \
                redirect_stdout(io.StringIO()):
            deploy.deploy(argparse.Namespace(host="example.test", public_url="https://example.test", yes=True))
        go_tests = [command for command in calls if command[:2] == ["go", "test"]]
        self.assertEqual([command[command.index("-tags") + 1] for command in go_tests], ["", "release"])
        remote = ssh.call_args.args
        self.assertIn("systemd-run", remote)
        self.assertIn("--wait", remote)
        self.assertIn("--install", remote)
        self.assertIn("--property=TimeoutStartSec=infinity", remote)

    def test_origins_are_strict_https(self):
        self.assertEqual(deploy.public_origin("https://example.test/"), "https://example.test")
        for value in ("http://example.test", "https://user:secret@example.test", "https://example.test/path",
                      "https://example.test?query", "https://example.test#fragment", "https://example.test:99999"):
            with self.subTest(value=value), self.assertRaises((deploy.DeploymentError, ValueError)):
                deploy.public_origin(value)

    def test_unsafe_archives_are_rejected(self):
        for name, kind in (("../escape", tarfile.REGTYPE), ("/absolute", tarfile.REGTYPE),
                           ("link", tarfile.SYMTYPE), ("hardlink", tarfile.LNKTYPE)):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                archive_path = root / "unsafe.tar"
                member = tarfile.TarInfo(name)
                member.type = kind
                member.linkname = "/etc/passwd"
                with tarfile.open(archive_path, "w") as archive:
                    archive.addfile(member, io.BytesIO())
                destination = root / "extracted"
                destination.mkdir()
                with self.assertRaises(deploy.DeploymentError):
                    deploy.extract(archive_path, destination)
                self.assertEqual(list(destination.iterdir()), [])

    def test_form_probe_checks_both_local_and_public_tokens_and_rejections(self):
        calls = []
        def respond(url, fields=None, headers=None):
            calls.append((url, dict(fields or {}), headers))
            if url.endswith("/healthz"):
                return 200, Message(), b"ok\n"
            if fields is None:
                token = "local-token" if url.startswith("http://") else "public-token"
                response = Message()
                response["X-CSRF-Token"] = token
                response["Set-Cookie"] = "_gorilla_csrf=synthetic; Secure; HttpOnly; SameSite=Lax"
                return 200, response, f'<input name="gorilla.csrf.Token" value="{token}">'.encode()
            self.assertEqual(url, "https://example.test/admin/signin")
            if "gorilla.csrf.Token" not in fields or headers["Origin"] != "https://example.test":
                return 403, Message(), b"Forbidden"
            return 401, Message(), b"Invalid username or password"
        with patch("deploy_vps.request", side_effect=respond):
            deploy.check_forms("https://example.test")
        posted = [fields for _, fields, _ in calls if fields]
        self.assertEqual([fields.get("gorilla.csrf.Token") for fields in posted],
                         ["local-token", "public-token", None, "public-token"])

    def test_http_does_not_follow_redirects_or_forward_tokens(self):
        visits = []
        agents = []
        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                visits.append(self.path)
                agents.append(self.headers.get("User-Agent"))
                self.send_response(302)
                self.send_header("Location", "/must-not-follow")
                self.end_headers()

            def log_message(self, *args):
                pass

        with HTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever)
            thread.start()
            try:
                status, _, _ = deploy.request(f"http://127.0.0.1:{server.server_port}/signin",
                                               {"gorilla.csrf.Token": "synthetic"})
                self.assertEqual(status, 302)
                self.assertEqual(visits, ["/signin"])
                self.assertEqual(agents, ["GuestbooksDeploymentCheck/1.0"])
            finally:
                server.shutdown()
                thread.join()

    def test_health_failure_identifies_edge_challenges_without_logging_body(self):
        headers = Message()
        headers["cf-mitigated"] = "challenge"
        with patch("deploy_vps.request", return_value=(403, headers, b"private diagnostic body")):
            with self.assertRaisesRegex(deploy.DeploymentError, r"HTTP 403 \(Cloudflare challenge\)") as raised:
                deploy.check_forms("https://example.test")
        self.assertNotIn("private diagnostic body", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
