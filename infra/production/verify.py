#!/usr/bin/env python3
"""Verify the public vitrine and private console without creating application data."""

import argparse
import base64
import json
import re
import subprocess
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("public_url")
    parser.add_argument("--private-url", default="http://localhost:18080")
    parser.add_argument("--credentials", type=Path, required=True)
    parser.add_argument("--public-connect-to", help="Optional curl connect-to mapping for private staging")
    args = parser.parse_args()
    credentials = json.loads(args.credentials.read_text())
    encoded = base64.b64encode(
        f"{credentials['username']}:{credentials['password']}".encode()
    ).decode()

    def request(base, path, *, authenticated=False, method="GET", headers=None):
        command = [
            "curl", "--silent", "--show-error", "--noproxy", "*",
            "--max-time", "20", "--path-as-is", "--request", method,
            "--dump-header", "-", "--write-out", "\n%{http_code}", "--config", "-",
        ]
        if base == args.public_url and args.public_connect_to:
            command += ["--connect-to", args.public_connect_to]
        if method == "POST":
            command += ["--data-binary", "{}"]
        for name, value in (headers or {}).items():
            command += ["--header", name + ": " + value]
        command += [base.rstrip("/") + path]
        # Credentials go through stdin, never process arguments or printed output.
        config = f'header = "Authorization: Basic {encoded}"\n' if authenticated else ""
        result = subprocess.run(command, input=config.encode(), capture_output=True)
        if result.returncode:
            raise RuntimeError(result.stderr.decode(errors="replace").strip())
        raw, status = result.stdout.rsplit(b"\n", 1)
        head, body = raw.split(b"\r\n\r\n", 1)
        response_headers = {}
        for line in head.decode().splitlines()[1:]:
            name, _, value = line.partition(":")
            response_headers[name.lower()] = value.strip()
        return int(status), response_headers, body

    def check(name, expected, base, path, **kwargs):
        status, headers, body = request(base, path, **kwargs)
        assert status in expected, f"{name}: HTTP {status}, expected {expected}"
        print(f"PASS {name}: HTTP {status}")
        return headers, body

    public, private = args.public_url, args.private_url
    headers, body = check("vitrine without login", {200}, public, "/")
    assert "www-authenticate" not in headers
    assert b"Your agents." in body and b'id="root"' not in body
    assert "default-src 'none'" in headers.get("content-security-policy", "")
    _, with_login = check("owner login still sees public vitrine", {200}, public, "/", authenticated=True)
    assert body == with_login
    for path in ("/site.css", "/icon.svg", "/fonts/geist-latin-wght-normal.woff2", "/robots.txt", "/sitemap.xml"):
        check("public asset " + path, {200}, public, path)
    for path in ("/api", "/api/v1/health", "/api/v1/projects", "/api/v1/runs", "/mcp", "/mcp/read-only", "/openapi.json", "/setup", "/requests", "/app", "/.env", "/owner.users"):
        check("public cannot reach " + path, {404}, public, path, authenticated=True)
    for host in ("localhost:18080", "localhost:8080", "127.0.0.1:8080"):
        status, _, body = request(public, "/api/v1/health", authenticated=True, headers={"Host": host})
        assert status in (404, 421) or (status == 200 and not body), "Host header exposed private API"
        print("PASS forged Host cannot reach private API:", host)
    for path in ("/", "/api/v1/projects", "/mcp", "/mcp/read-only"):
        headers, _ = check("private password required " + path, {401}, private, path)
        assert "Basic" in headers.get("www-authenticate", "")
    check("wrong private password rejected", {401}, private, "/", headers={"Authorization": "Basic b3duZXI6d3Jvbmc="})
    _, body = check("private console", {200}, private, "/", authenticated=True)
    assert b'<div id="root">' in body
    for path in re.findall(rb'(?:src|href)="(/assets/[^\"]+)"', body):
        path = path.decode()
        check("private console asset", {200}, private, path, authenticated=True)
        check("console asset not public", {404}, public, path, authenticated=True)
    _, body = check("private API health", {200}, private, "/api/v1/health", authenticated=True)
    assert json.loads(body) == {"status": "ok"}
    _, body = check("private MCP address", {200}, private, "/api/v1/mcp/connection", authenticated=True)
    assert json.loads(body)["url"] == private.rstrip("/") + "/mcp"
    check("private project data", {200}, private, "/api/v1/projects", authenticated=True)
    for origin in (public, "https://example.invalid", "null"):
        check("foreign write blocked: " + origin, {403}, private, "/api/v1/health", authenticated=True, method="POST", headers={"Origin": origin})
    for site in ("cross-site", "same-site"):
        check("browser write blocked: " + site, {403}, private, "/api/v1/health", authenticated=True, method="POST", headers={"Sec-Fetch-Site": site})
    for headers in ({"Origin": private}, {}):
        check("owner write reaches method guard", {405}, private, "/api/v1/health", authenticated=True, method="POST", headers=headers)
    for provider in ("github", "linear"):
        headers, _ = check("unsigned webhook rejected: " + provider, {400, 401, 403, 415, 503}, public, "/webhooks/" + provider, method="POST", headers={"Content-Type": "application/json"})
        assert "www-authenticate" not in headers
        check("webhook method guard: " + provider, {405}, public, "/webhooks/" + provider)
    for path in ("/webhooks/github/anything", "/webhooks/api/v1/projects", "/webhooks/github/../api/v1/projects"):
        check("webhook prefix exposes no app route", {404}, public, path, authenticated=True)
    print("Public/private separation verified; no runs or provider messages created.")


if __name__ == "__main__":
    main()
