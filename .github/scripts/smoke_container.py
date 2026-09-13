#!/usr/bin/env python3
"""Verify the production image with disposable data and loopback HTTP."""

import argparse
from contextlib import contextmanager
import gzip
from html.parser import HTMLParser
from http.cookiejar import CookieJar
import io
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import tarfile
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import HTTPCookieProcessor, ProxyHandler, Request, build_opener
import uuid
import zipfile


PERMISSIONS_IMAGE = "busybox:1.37.0"
DEFAULT_USER = "65532:65532"


def docker(*args, timeout=120):
    return subprocess.check_output(["docker", *args], text=True, timeout=timeout).strip()


@contextmanager
def container(image, mount, user=None):
    options = ["--user", user] if user else []
    identifier = docker(
        "create", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
        "--memory=1g", "--cpus=2", "--pids-limit=128", "--mount", mount,
        "--publish", "127.0.0.1::8080", "--env", "FILEAMENT_WEB_DIR=/missing-external-ui",
        *options, image,
    )
    try:
        settings = json.loads(docker("inspect", identifier))[0]
        host = settings["HostConfig"]
        assert settings["Config"]["User"] == (user or DEFAULT_USER), "runtime user changed"
        assert host["ReadonlyRootfs"] and not host["Privileged"], "runtime filesystem or privilege policy changed"
        assert host["CapDrop"] == ["ALL"] and not host["CapAdd"], "runtime capabilities were not dropped"
        assert "no-new-privileges" in host["SecurityOpt"], "privilege escalation is not disabled"
        assert host["Memory"] > 0 and host["NanoCpus"] > 0 and host["PidsLimit"] > 0, "runtime resource limits are missing"
        docker("start", identifier)
        yield identifier
    finally:
        docker("rm", "--force", identifier)


class Client:
    def __init__(self, address):
        assert re.fullmatch(r"127\.0\.0\.1:[0-9]+", address), "expected a loopback port"
        self.address = address
        self.opener = build_opener(ProxyHandler({}), HTTPCookieProcessor(CookieJar()))

    def request(self, path, method="GET", data=None, **headers):
        req = Request("http://" + self.address + path, data=data, method=method, headers=headers)
        try:
            response = self.opener.open(req, timeout=5)
        except HTTPError as error:
            response = error
        with response:
            body = response.read(8 * 1024 * 1024 + 1)
            assert len(body) <= 8 * 1024 * 1024, "unexpectedly large smoke response"
            return response.status, response.headers, body

    def json(self, path, method="GET", payload=None, expected=200):
        data = json.dumps(payload).encode() if payload is not None else None
        status, _, body = self.request(path, method, data, **{"Content-Type": "application/json"})
        assert status == expected, f"{method} {path}: expected {expected}, got {status}"
        return json.loads(body) if body else None

    def wait_healthy(self):
        deadline = time.monotonic() + 45
        while True:
            try:
                status, _, body = self.request("/healthz")
                if status == 200 and json.loads(body).get("status") == "ok":
                    return
            except (URLError, TimeoutError, ConnectionError):
                pass
            assert time.monotonic() < deadline, "container did not become healthy"
            time.sleep(0.2)


def client_for(identifier):
    client = Client(docker("port", identifier, "8080/tcp"))
    client.wait_healthy()
    return client


class AssetLinks(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = set()

    def handle_starttag(self, tag, attributes):
        attributes = dict(attributes)
        path = attributes.get("src") if tag == "script" else attributes.get("href") if tag == "link" else None
        if path and re.fullmatch(r"/assets/[^/]+-[A-Za-z0-9_-]{8}\.(js|css)", path):
            self.paths.add(path)


def check_assets(client):
    status, headers, body = client.request("/")
    assert status == 200 and headers.get("Cache-Control") == "no-cache", "HTML response failed"
    assets = AssetLinks()
    assets.feed(body.decode("utf-8"))
    assert any(path.endswith(".js") for path in assets.paths), "HTML has no hashed script"
    for path in sorted(assets.paths):
        status, headers, original = client.request(path, **{"Accept-Encoding": "identity"})
        assert status == 200 and not headers.get("Content-Encoding"), "identity asset failed"
        status, headers, encoded = client.request(path, **{"Accept-Encoding": "gzip"})
        assert status == 200 and headers.get("Content-Encoding") == "gzip", "gzip asset failed"
        assert headers.get("Cache-Control") == "public, max-age=31536000, immutable", "asset cache policy failed"
        assert headers.get("Vary") == "Accept-Encoding", "asset encoding variation failed"
        with gzip.GzipFile(fileobj=io.BytesIO(encoded)) as compressed:
            decoded = compressed.read(len(original) + 1)
        assert decoded == original and len(encoded) < len(original), "gzip payload failed"
        tag = headers.get("ETag")
        assert tag, "asset validator missing"
        status, head, body = client.request(path, "HEAD", **{"Accept-Encoding": "gzip"})
        assert status == 200 and not body and int(head.get("Content-Length")) == len(encoded), "HEAD response failed"
        status, _, body = client.request(path, **{"Accept-Encoding": "gzip", "If-None-Match": tag})
        assert status == 304 and not body, "conditional asset response failed"
        print(f"{path}: identity={len(original)} gzip={len(encoded)} bytes")
    status, headers, _ = client.request("/api/me")
    assert status == 200 and headers.get("Cache-Control") == "private, no-store", "private API cache policy failed"
    status, headers, _ = client.request("/s/smoke-test")
    assert status == 200 and headers.get("Cache-Control") == "private, no-store" and headers.get("X-Robots-Tag") == "noindex", "share HTML policy failed"


def create_catalog(client):
    password = secrets.token_urlsafe(24)
    client.json("/api/auth/setup", "POST", {"password": password}, expected=201)
    client.json("/api/auth/login", "POST", {"password": password})
    mesh = b"solid p\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendloop\nendfacet\nendsolid p\n"
    boundary = uuid.uuid4().hex
    upload = (
        f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="smoke.stl"\r\n'
        'Content-Type: application/octet-stream\r\n\r\n'
    ).encode() + mesh + f"\r\n--{boundary}--\r\n".encode()
    status, _, body = client.request("/api/models", "POST", upload, **{"Content-Type": f"multipart/form-data; boundary={boundary}"})
    assert status == 201, f"mesh upload failed: {status}"
    model_id = json.loads(body)["id"]
    client.json(f"/api/models/{model_id}", "PATCH", {"title": "Persistent smoke model", "tags": ["smoke"]})
    collection = client.json("/api/collections", "POST", {"name": "Smoke collection"}, expected=201)
    collection_id = collection["id"]
    client.json(f"/api/collections/{collection_id}/models/{model_id}", "PUT", expected=204)
    deadline = time.monotonic() + 30
    while True:
        model = client.json(f"/api/models/{model_id}")
        if model.get("primaryThumb"):
            break
        assert time.monotonic() < deadline, "thumbnail was not generated with the hardened runtime"
        assert not any(job["status"] == "failed" for job in model.get("thumbnailJobs", [])), "thumbnail generation failed"
        time.sleep(0.2)
    status, _, body = client.request(f'/thumbs/{model_id}/{model["primaryThumb"]}')
    assert status == 200 and body.startswith(b"\x89PNG\r\n\x1a\n"), "thumbnail could not be read"
    status, _, body = client.request(f'/files/{model_id}/{model["files"][0]["id"]}')
    assert status == 200 and body == mesh, "mesh download changed"
    status, _, body = client.request("/api/backups", "POST")
    assert status == 200, f"backup creation failed: {status}"
    with zipfile.ZipFile(io.BytesIO(body)) as archive:
        assert f"data/models/{model_id}/model.json" in archive.namelist(), "backup lacks model sidecar"
        assert "data/collections.json" in archive.namelist(), "backup lacks collections"
    return password, model_id, collection_id


def check_catalog(client, fixture):
    password, model_id, collection_id = fixture
    client.json("/api/auth/login", "POST", {"password": password})
    model = client.json(f"/api/models/{model_id}")
    assert model["title"] == "Persistent smoke model" and model["tags"] == ["smoke"], "metadata did not survive restart"
    assert model.get("primaryThumb"), "thumbnail did not survive restart"
    collection = client.json(f"/api/collections/{collection_id}")
    assert collection["modelIds"] == [model_id], "collection membership did not survive restart"


def check_data_owner(identifier, uid, gid):
    # Read archive headers only; never extract or print the disposable credentials.
    with tempfile.TemporaryFile() as snapshot:
        subprocess.run(["docker", "cp", identifier + ":/data/.", "-"], stdout=snapshot, check=True, timeout=45)
        snapshot.seek(0)
        with tarfile.open(fileobj=snapshot) as archive:
            names = set()
            for item in archive:
                assert (item.uid, item.gid) == (uid, gid), f"incorrect data ownership: {item.name}"
                names.add(item.name.removeprefix("./").removeprefix("data/"))
            assert "fileament.db" in names and "collections.json" in names, "persistent files are missing"


def change_volume_owner(volume, owner):
    docker("run", "--rm", "--network", "none", "--read-only", "--user", "0:0",
           "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE",
           "--mount", f"type=volume,src={volume},dst=/data", PERMISSIONS_IMAGE,
           "chown", "-R", owner, "/data")


def smoke(image):
    config = json.loads(docker("image", "inspect", image))[0]["Config"]
    assert config["User"] == DEFAULT_USER, "production image must default to UID/GID 65532"
    assert config["Entrypoint"] == ["/fileament"], "standalone entrypoint changed"
    assert config["WorkingDir"] == "/", "custom users need an accessible working directory"
    volume = docker("volume", "create", "fileament-smoke-" + uuid.uuid4().hex)
    try:
        with container(image, f"type=volume,src={volume},dst=/data") as identifier:
            client = client_for(identifier)
            check_assets(client)
            fixture = create_catalog(client)
            docker("stop", "--time", "10", identifier)
            check_data_owner(identifier, 65532, 65532)
            docker("start", identifier)
            check_catalog(client_for(identifier), fixture)
            docker("stop", "--time", "10", identifier)
            change_volume_owner(volume, "0:0")
            docker("start", identifier)
            assert int(docker("wait", identifier, timeout=20)) != 0, "root-owned private data unexpectedly accepted"
            change_volume_owner(volume, DEFAULT_USER)
            docker("start", identifier)
            check_catalog(client_for(identifier), fixture)
            docker("stop", "--time", "10", identifier)
            check_data_owner(identifier, 65532, 65532)
            print("Named volume, restart, ownership rejection and migration passed")
    finally:
        docker("volume", "rm", volume)

    with tempfile.TemporaryDirectory(prefix="fileament-smoke-bind-") as directory:
        uid, gid = (os.getuid(), os.getgid()) if os.getuid() else (10001, 10001)
        os.chown(directory, uid, gid)
        with container(image, f"type=bind,src={directory},dst=/data", f"{uid}:{gid}") as identifier:
            fixture = create_catalog(client_for(identifier))
            docker("stop", "--time", "10", identifier)
            check_data_owner(identifier, uid, gid)
            docker("start", identifier)
            check_catalog(client_for(identifier), fixture)
            print("Custom non-root bind-mount user and restart passed")
    print("Production image smoke test passed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    args = parser.parse_args()
    subprocess.run(["docker", "compose", "--env-file", os.devnull, "-f",
                    str(Path(__file__).resolve().parents[2] / "compose.yaml"), "config", "--quiet"], check=True)
    smoke(args.image)
