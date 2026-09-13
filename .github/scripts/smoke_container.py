#!/usr/bin/env python3
"""Verify the production image with disposable data and loopback HTTP."""

import argparse
import gzip
from html.parser import HTMLParser
import io
import json
import re
import subprocess
import time
from urllib.error import HTTPError, URLError
from urllib.request import ProxyHandler, Request, build_opener


class AssetLinks(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = set()

    def handle_starttag(self, tag, attributes):
        attributes = dict(attributes)
        path = attributes.get("src") if tag == "script" else attributes.get("href") if tag == "link" else None
        if path and re.fullmatch(r"/assets/[^/]+-[A-Za-z0-9_-]{8}\.(js|css)", path):
            self.paths.add(path)


def smoke(image):
    container = subprocess.check_output([
        "docker", "run", "--detach", "--read-only", "--cap-drop=ALL",
        "--security-opt=no-new-privileges", "--memory=512m", "--cpus=2",
        "--tmpfs", "/data:rw,nosuid,nodev,noexec", "--publish", "127.0.0.1::8080",
        "--env", "FILEAMENT_WEB_DIR=/missing-external-ui", image,
    ], text=True).strip()
    try:
        address = subprocess.check_output(["docker", "port", container, "8080/tcp"], text=True).strip()
        assert re.fullmatch(r"127\.0\.0\.1:[0-9]+", address), "expected a loopback port"
        opener = build_opener(ProxyHandler({}))

        def request(path, method="GET", **headers):
            req = Request("http://" + address + path, method=method, headers=headers)
            try:
                response = opener.open(req, timeout=3)
            except HTTPError as error:
                response = error
            with response:
                body = response.read(8 * 1024 * 1024 + 1)
                assert len(body) <= 8 * 1024 * 1024, "unexpectedly large smoke response"
                return response.status, response.headers, body

        deadline = time.monotonic() + 45
        while True:
            try:
                status, _, body = request("/healthz")
                if status == 200 and json.loads(body).get("status") == "ok":
                    break
            except (URLError, TimeoutError, ConnectionError):
                pass
            if time.monotonic() >= deadline:
                raise AssertionError("container did not become healthy")
            time.sleep(0.2)

        status, headers, body = request("/")
        assert status == 200 and headers.get("Cache-Control") == "no-cache", "HTML response failed"
        assets = AssetLinks()
        assets.feed(body.decode("utf-8"))
        assert any(path.endswith(".js") for path in assets.paths), "HTML has no hashed script"
        for path in sorted(assets.paths):
            status, headers, original = request(path, **{"Accept-Encoding": "identity"})
            assert status == 200 and not headers.get("Content-Encoding"), "identity asset failed"
            status, headers, encoded = request(path, **{"Accept-Encoding": "gzip"})
            assert status == 200 and headers.get("Content-Encoding") == "gzip", "gzip asset failed"
            assert headers.get("Cache-Control") == "public, max-age=31536000, immutable", "asset cache policy failed"
            assert headers.get("Vary") == "Accept-Encoding", "asset encoding variation failed"
            with gzip.GzipFile(fileobj=io.BytesIO(encoded)) as compressed:
                decoded = compressed.read(len(original) + 1)
            assert decoded == original and len(encoded) < len(original), "gzip payload failed"
            tag = headers.get("ETag")
            assert tag, "asset validator missing"
            status, head, body = request(path, "HEAD", **{"Accept-Encoding": "gzip"})
            assert status == 200 and not body and int(head.get("Content-Length")) == len(encoded), "HEAD response failed"
            status, _, body = request(path, **{"Accept-Encoding": "gzip", "If-None-Match": tag})
            assert status == 304 and not body, "conditional asset response failed"
            print(f"{path}: identity={len(original)} gzip={len(encoded)} bytes")

        status, headers, _ = request("/api/me")
        assert status == 200 and headers.get("Cache-Control") == "private, no-store", "private API cache policy failed"
        status, headers, _ = request("/s/smoke-test")
        assert status == 200 and headers.get("Cache-Control") == "private, no-store" and headers.get("X-Robots-Tag") == "noindex", "share HTML policy failed"
        print("Production image smoke test passed")
    finally:
        subprocess.run(["docker", "rm", "--force", container], check=True, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    smoke(parser.parse_args().image)
