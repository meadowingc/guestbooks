"""Deploy a committed release to the existing Guestbooks systemd VPS layout."""

import argparse
from contextlib import closing
import fcntl
import hashlib
from html.parser import HTMLParser
from http.cookies import SimpleCookie
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shlex
import shutil
import signal
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time
import traceback
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener
import uuid


class DeploymentError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise DeploymentError(message)


def checksum(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def extract(archive, destination):
    seen = set()
    with tarfile.open(archive) as bundle:
        for member in bundle:
            path = PurePosixPath(member.name)
            require(not path.is_absolute() and ".." not in path.parts and path.parts,
                    "Archive contains an unsafe path")
            require(member.name not in seen and (member.isdir() or member.isfile()),
                    "Archive contains duplicate paths, links or special files")
            seen.add(member.name)
            target = destination.joinpath(*path.parts)
            target.parent.mkdir(parents=True, exist_ok=True)
            if member.isdir():
                target.mkdir(exist_ok=True)
            else:
                with bundle.extractfile(member) as source, target.open("xb") as output:
                    shutil.copyfileobj(source, output)


def database_digest(connection):
    require(connection.execute("PRAGMA quick_check").fetchone() == ("ok",),
            "SQLite integrity check failed")
    digest = hashlib.sha256()
    connection.text_factory = bytes
    try:
        schema = connection.execute(
            "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name"
        ).fetchall()
        digest.update(repr(schema).encode("ascii"))
        for (name,) in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
        ).fetchall():
            identifier = '"' + name.decode("utf-8").replace('"', '""') + '"'
            columns = connection.execute(f"PRAGMA table_info({identifier})").fetchall()
            keys = sorted((column[5], column[1]) for column in columns if column[5])
            order = ", ".join('"' + name.decode("utf-8").replace('"', '""') + '"' for _, name in keys) or "rowid"
            for row in connection.execute(f"SELECT * FROM {identifier} ORDER BY {order}"):
                digest.update(repr(row).encode("ascii"))
        for pragma in ("user_version", "application_id"):
            digest.update(repr(connection.execute(f"PRAGMA {pragma}").fetchone()).encode("ascii"))
    finally:
        connection.text_factory = str
    return digest.hexdigest()


def read_database_digest(path):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as connection:
        connection.execute("BEGIN")
        return database_digest(connection)


def backup_database(source, destination):
    deadline = time.monotonic() + 60
    def progress(status, remaining, total):
        require(time.monotonic() < deadline, "SQLite backup did not finish within 60 seconds")
    with closing(sqlite3.connect(source.as_uri() + "?mode=ro", uri=True)) as live:
        with closing(sqlite3.connect(destination)) as backup:
            live.backup(backup, pages=256, progress=progress, sleep=0.05)
            return database_digest(backup)


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, new_url):
        return None


def request(url, fields=None, headers=None):
    body = None if fields is None else urlencode(fields).encode("ascii")
    outgoing = Request(url, data=body, headers=headers or {})
    try:
        response = build_opener(ProxyHandler({}), NoRedirect()).open(outgoing, timeout=10)
    except HTTPError as error:
        response = error
    with response:
        content = response.read(1024 * 1024 + 1)
        require(len(content) <= 1024 * 1024, "Unexpectedly large smoke-check response")
        return response.status, response.headers, content


class TokenParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.tokens = []

    def handle_starttag(self, tag, attrs):
        fields = dict(attrs)
        if tag == "input" and fields.get("name") == "gorilla.csrf.Token":
            self.tokens.append(fields.get("value"))


def check_forms(public_url, local_url="http://127.0.0.1:6235"):
    status, _, body = request(public_url + "/healthz")
    require(status == 200 and body.strip() == b"ok", "Public health check failed")
    fields = {"username": "__deployment_probe_" + uuid.uuid4().hex, "password": uuid.uuid4().hex}
    for origin in (local_url, public_url):
        status, headers, body = request(origin + "/admin/signin")
        parser = TokenParser()
        parser.feed(body.decode("utf-8"))
        token = headers.get("X-CSRF-Token")
        require(status == 200 and token and token in parser.tokens, "Sign-in form/token check failed")
        cookies = SimpleCookie()
        for value in headers.get_all("Set-Cookie", []):
            cookies.load(value)
        cookie = cookies.get("_gorilla_csrf")
        require(cookie and cookie["secure"] and cookie["httponly"], "Secure CSRF cookie is missing")
        outgoing = {"Cookie": cookie.OutputString(attrs=[]), "Origin": public_url,
                    "Referer": public_url + "/admin/signin"}
        fields["gorilla.csrf.Token"] = token
        status, _, body = request(public_url + "/admin/signin", fields, outgoing)
        # A token minted locally must also work through the public proxy route.
        require(status == 401 and b"Invalid username or password" in body,
                "Public sign-in POST did not reach authentication")
    status, _, _ = request(public_url + "/admin/signin",
                           {key: value for key, value in fields.items() if key != "gorilla.csrf.Token"}, outgoing)
    require(status == 403, "Missing-token CSRF rejection failed")
    status, _, _ = request(public_url + "/admin/signin", fields,
                           {**outgoing, "Origin": "https://foreign-origin.invalid"})
    require(status == 403, "Foreign-origin CSRF rejection failed")


def public_origin(value):
    parsed = urlsplit(value)
    require(parsed.scheme == "https" and parsed.hostname and not parsed.username and not parsed.password
            and parsed.path in ("", "/") and not parsed.query and not parsed.fragment,
            "--public-url must be an HTTPS origin without credentials, a path, query or fragment")
    require(parsed.port is None or 1 <= parsed.port <= 65535, "Invalid public URL port")
    return value.rstrip("/")


def release_files(directory):
    paths = list(directory.rglob("*"))
    require(not directory.is_symlink() and not any(path.is_symlink() for path in paths),
            "Release must not contain symbolic links")
    return {str(path.relative_to(directory)): checksum(path)
            for path in paths if path.is_file() and path != directory / "release.json"}


class Installer:
    def __init__(self, stage, system_root=Path("/")):
        self.stage = stage
        self.root = system_root / "opt/guestbooks"
        self.data = system_root / "var/lib/guestbooks"
        self.config = system_root / "etc/guestbooks/config.yaml"
        self.unit = system_root / "etc/systemd/system/guestbooks.service"
        self.caddy = system_root / "etc/caddy/Caddyfile"
        self.current = self.root / "current"
        self.database = self.data / "guestbook.db"
        self.log = stage / "install.log"

    def run(self, *args, cwd=None):
        with self.log.open("ab") as log:
            result = subprocess.run(args, cwd=cwd, stdout=subprocess.PIPE, stderr=log, timeout=90)
            if result.returncode:
                log.write(result.stdout)
                raise DeploymentError(f"{args[0]} failed; see {self.log}")
        return result.stdout.decode("utf-8").strip()

    def service(self, action):
        return self.run("systemctl", action, "guestbooks.service")

    def check_process(self, release):
        self.service("is-active")
        pid = self.run("systemctl", "show", "guestbooks.service", "--property=MainPID", "--value")
        require(pid.isdecimal() and int(pid) > 0
                and Path(f"/proc/{pid}/exe").resolve() == release / "guestbooks",
                "systemd is not running the expected release binary")

    def local_health(self, release):
        self.run("runuser", "-u", "guestbooks", "--", str(release / "guestbooks"), "healthcheck", cwd=self.data)

    def restore_owner(self, owner):
        for path in (self.database, Path(str(self.database) + "-wal"), Path(str(self.database) + "-shm")):
            if path.exists():
                os.chown(path, owner.st_uid, owner.st_gid)

    def switch(self, release):
        temporary = self.root / (".current-" + uuid.uuid4().hex)
        try:
            temporary.symlink_to(release)
            os.replace(temporary, self.current)
        finally:
            temporary.unlink(missing_ok=True)

    def record(self, status, **fields):
        path = self.stage / "deployment.json"
        receipt = json.loads(path.read_text()) if path.exists() else {}
        receipt.update(status=status, **fields)
        temporary = path.with_suffix(".tmp")
        temporary.write_text(json.dumps(receipt, indent=2) + "\n")
        os.replace(temporary, path)

    def activate(self, release, url):
        previous = self.current.resolve()
        owner = self.database.stat()
        backup = self.stage / "before.db"
        original_digest = None
        try:
            self.service("stop")
            self.record("stopped")
            original_digest = backup_database(self.database, backup)
            self.record("backed_up", database_sha256=checksum(backup))
            self.restore_owner(owner)
            self.switch(release)
            self.service("start")
            self.record("checking")
            for attempt in range(20):
                try:
                    self.local_health(release)
                    break
                except DeploymentError:
                    if attempt == 19:
                        raise
                    time.sleep(0.5)
            self.check_process(release)
            check_forms(url)
            self.check_process(release)
        except BaseException:
            with self.log.open("a") as log:
                log.write(traceback.format_exc())
            self.service("stop")
            self.record("needs_recovery")
            # Never discard writes or guess whether an altered schema can be downgraded.
            changed = original_digest is not None and read_database_digest(self.database) != original_digest
            self.restore_owner(owner)
            if changed:
                raise DeploymentError(
                    f"Deployment failed and the database changed. Service left STOPPED; no data restored. "
                    f"Preserve current data and review {backup} and {self.log} before recovery."
                )
            self.switch(previous)
            self.service("start")
            self.local_health(previous)
            self.record("rolled_back")
            print("Deployment failed; previous release restarted without restoring the database.", flush=True)
            raise

    def install(self):
        require(os.geteuid() == 0, "The remote installer must run as root")
        os.umask(0o077)
        require(self.root.is_dir() and self.current.is_symlink(), "Existing release layout is missing")
        with (self.root / ".deploy.lock").open("a") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as error:
                raise DeploymentError("Another Guestbooks deployment is running") from error
            require(not (self.stage / "before.db").exists(), "Never reuse a deployment staging directory")
            metadata = json.loads((self.stage / "request.json").read_text())
            url = public_origin(metadata["public_url"])
            require(checksum(self.stage / "release.tar") == metadata["archive_sha256"], "Upload checksum mismatch")
            self.service("is-active")
            self.service("is-enabled")
            require(self.current.resolve().parent == self.root / "releases", "Unexpected current release location")
            require(self.database.is_file() and not self.database.is_symlink(), "Live database is missing or redirected")
            require((self.data / "config.yaml").resolve() == self.config, "Unexpected live configuration link")
            for name in ("assets", "templates"):
                require((self.data / name).is_symlink()
                        and os.readlink(self.data / name) == str(self.current / name),
                        f"Live {name} must follow the current release")
            properties = self.run("systemctl", "show", "guestbooks.service",
                                  "--property=User,WorkingDirectory,ExecStart")
            require("User=guestbooks\n" in properties + "\n"
                    and f"WorkingDirectory={self.data}\n" in properties + "\n"
                    and f"path={self.current / 'guestbooks'} ;" in properties,
                    "systemd unit does not use the supported Guestbooks layout")
            incoming = self.stage / "unpacked"
            incoming.mkdir()
            extract(self.stage / "release.tar", incoming)
            manifest = json.loads((incoming / "release.json").read_text())
            revision = manifest["revision"]
            require(re.fullmatch("[0-9a-f]{40}", revision), "Invalid release revision")
            require(manifest["architecture"] == platform.machine(), "Local and VPS architectures differ")
            files = release_files(incoming)
            require(files == manifest["files"] and "guestbooks" in files
                    and "templates/admin/signin.html" in files and any(name.startswith("assets/") for name in files),
                    "Release contents do not match the manifest")
            require(all(name == "guestbooks" or name.startswith(("assets/", "templates/")) for name in files),
                    "Unexpected runtime release contents")
            release = self.root / "releases" / (revision + "-" + files["guestbooks"][:12])
            if release.exists():
                require(release_files(release) == files
                        and json.loads((release / "release.json").read_text()) == manifest,
                        "Existing release differs from the upload; it was not overwritten")
            else:
                shutil.copytree(incoming, release)
            release.chmod(0o755)
            for path in release.rglob("*"):
                path.chmod(0o755 if path.is_dir() or path == release / "guestbooks" else 0o644)
            # Executing healthcheck verifies native/CGO compatibility and configuration,
            # without starting another server or migrating the live database.
            self.local_health(release)
            check_forms(url)
            if self.current.resolve() == release:
                self.check_process(release)
                self.record("already_current", release=str(release), revision=revision)
                print(f"Release {revision} is already current and healthy; no restart needed.", flush=True)
                return
            for source, name in ((self.config, "config.yaml"), (self.unit, "guestbooks.service"), (self.caddy, "Caddyfile")):
                shutil.copyfile(source, self.stage / name)
            receipt = {"previous_release": str(self.current.resolve()), "release": str(release),
                       "revision": revision, "database": str(self.database), "public_url": url}
            self.record("prepared", **receipt)
            self.activate(release, url)
            self.record("healthy")
            print(f"Deployed {revision}. Backups and receipt: {self.stage}", flush=True)


def ssh(host, *args, capture=False):
    return subprocess.run(["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", host, shlex.join(args)],
                          check=True, text=True, stdout=subprocess.PIPE if capture else None)


def deploy(args):
    require(platform.system() == "Linux", "Build on Linux with a native CGO toolchain compatible with the VPS")
    require(re.fullmatch(r"[A-Za-z0-9_][A-Za-z0-9_.@-]*", args.host), "Use an SSH alias or user@hostname")
    url = public_origin(args.public_url)
    repository = Path(__file__).resolve().parent.parent
    for tool in ("git", "go", "ssh", "scp"):
        require(shutil.which(tool), f"Required command is missing: {tool}")
    def git(*command):
        return subprocess.check_output(["git", "-C", str(repository), *command], text=True).strip()
    require(not git("status", "--porcelain"), "Commit your changes first; the worktree must be clean")
    revision = git("rev-parse", "HEAD")
    print(f"Deploy {revision} to {args.host} ({url}). The service will stop briefly for its database backup.")
    if not args.yes:
        require(input("Type yes to continue: ").strip() == "yes", "Deployment cancelled; no remote changes made")
    with tempfile.TemporaryDirectory(prefix="guestbooks-deploy-") as temporary:
        directory = Path(temporary)
        source = directory / "source"
        source.mkdir()
        subprocess.run(["git", "-C", str(repository), "archive", "--format=tar",
                        "-o", str(directory / "source.tar"), revision], check=True)
        extract(directory / "source.tar", source)
        environment = {**os.environ, "GOOS": "linux", "CGO_ENABLED": "1"}
        architecture = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine())
        require(architecture, "Supported build architectures are Linux x86_64 and aarch64")
        environment["GOARCH"] = architecture
        for tags in ("", "release"):
            subprocess.run(["go", "test", "-mod=readonly", "-tags", tags, "-count=1", "-timeout", "3m", "./..."],
                           cwd=source, env=environment, check=True)
        subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "scripts", "-p", "test_*.py"],
                       cwd=source, check=True)
        payload = directory / "payload"
        payload.mkdir()
        subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-tags", "release",
                        "-o", str(payload / "guestbooks"), "."], cwd=source, env=environment, check=True)
        for name in ("assets", "templates"):
            shutil.copytree(source / name, payload / name)
        manifest = {"revision": revision, "architecture": platform.machine(),
                    "files": {str(path.relative_to(payload)): checksum(path) for path in payload.rglob("*") if path.is_file()}}
        (payload / "release.json").write_text(json.dumps(manifest, indent=2) + "\n")
        archive = directory / "release.tar"
        with tarfile.open(archive, "w") as bundle:
            for path in sorted(payload.iterdir()):
                bundle.add(path, arcname=path.name)
        metadata = directory / "request.json"
        metadata.write_text(json.dumps({"public_url": url, "archive_sha256": checksum(archive)}) + "\n")
        stage = ssh(args.host, "mktemp", "-d", "/root/guestbooks-deploy-backups/update-XXXXXXXX", capture=True).stdout.strip()
        require(re.fullmatch(r"/root/guestbooks-deploy-backups/update-[A-Za-z0-9]+", stage), "Unexpected remote staging path")
        print(f"Remote backups/logs: {stage}", flush=True)
        subprocess.run(["scp", "-q", str(archive), str(metadata), str(directory / "source.tar"),
                        str(source / "scripts/deploy_vps.py"), f"{args.host}:{stage}/"], check=True)
        unit = "guestbooks-deploy-" + stage.rsplit("/", 1)[1]
        try:
            ssh(args.host, "systemd-run", "--unit=" + unit, "--wait", "--property=Type=oneshot",
                "--property=TimeoutStartSec=infinity",
                "/usr/bin/python3", stage + "/deploy_vps.py", "--install", stage)
        except (OSError, subprocess.SubprocessError):
            print("Deployment was not confirmed. If SSH disconnected, it may still be running; "
                  "inspect the remote journal and receipt before retrying.", flush=True)
            raise
        else:
            print(f"Release {revision} is ready at {url}.", flush=True)
        finally:
            print(f"Deployment journal: ssh {args.host} journalctl -u {unit} --no-pager\n"
                  f"Receipt: {stage}/deployment.json", flush=True)


def main():
    if sys.argv[1:2] == ["--install"]:
        require(len(sys.argv) == 3, "Invalid internal installer arguments")
        stage = Path(sys.argv[2])
        require(re.fullmatch(r"/root/guestbooks-deploy-backups/update-[A-Za-z0-9]+", str(stage)),
                "Invalid internal staging directory")
        def interrupted(signum, frame):
            raise DeploymentError("Remote deployment interrupted")
        signal.signal(signal.SIGTERM, interrupted)
        Installer(stage).install()
        return
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("host", help="Root SSH alias or root@hostname; uses normal SSH keys/config")
    parser.add_argument("--public-url", required=True, help="Public HTTPS origin, e.g. https://guestbooks.example.com")
    parser.add_argument("--yes", action="store_true", help="Confirm deployment without prompting")
    deploy(parser.parse_args())


if __name__ == "__main__":
    try:
        main()
    except (DeploymentError, OSError, ValueError, KeyError, EOFError, tarfile.TarError,
            sqlite3.Error, subprocess.SubprocessError, URLError) as error:
        print(f"Deployment failed: {error}", file=sys.stderr)
        sys.exit(1)
