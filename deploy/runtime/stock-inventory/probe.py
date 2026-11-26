"""Authenticated empty inventory against one fresh networkless stock daemon."""
import http.client
import json
import re
import secrets
import ssl
import sys
import time

ORIGIN = "https://127.0.0.1:9998"
API = "/api/v1"
LISTS = ("instances", "pools", "providers")
BODY_LIMIT = 64 * 1024
stage = "transport"


def request(context, method, path, payload=None, token=None):
    headers = {"Accept": "application/json"}
    body = None
    if payload is not None:
        body = json.dumps(payload).encode()
        headers["Content-Type"] = "application/json"
    if token is not None:
        headers["Authorization"] = "Bearer " + token
    connection = http.client.HTTPSConnection("127.0.0.1", 9998, context=context, timeout=3)
    try:
        connection.request(method, API + path, body, headers)
        response = connection.getresponse()
        raw = response.read(BODY_LIMIT + 1)
        if len(raw) > BODY_LIMIT:
            raise ValueError("response too large")
        return response.status, json.loads(raw)
    finally:
        connection.close()


def check(condition):
    if not condition:
        raise ValueError("unexpected stock API response")


def probe(call, max_wait=35):
    global stage
    stage = "verify-fresh-stock"
    deadline = time.monotonic() + max_wait
    while True:
        try:
            status, body = call("GET", "/controller")
        except (ConnectionRefusedError, ConnectionResetError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.25)
            continue
        check(status == 409 and isinstance(body, dict) and body.get("error") == "init_required")
        break

    # Only the genuine local bootstrap/login API can mint the bearer. Nothing
    # is supplied by the runner, stored in a file, or passed in process argv.
    stage = "disposable-bootstrap"
    username = "stockfixture"
    password = secrets.token_urlsafe(36) + "a9!"
    status, user = call("POST", "/first-run", {
        "username": username, "email": "stockfixture@example.invalid", "password": password,
    })
    check(status == 200 and isinstance(user, dict) and user.get("username") == username
          and user.get("enabled") is True and user.get("is_admin") is True and "password" not in user)

    stage = "disposable-login"
    status, login = call("POST", "/auth/login", {"username": username, "password": password})
    del password
    check(status == 200 and isinstance(login, dict) and set(login) == {"token"})
    token = login["token"]
    check(isinstance(token, str) and len(token) <= 8192
          and re.fullmatch(r"[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", token) is not None)

    stage = "loopback-controller-settings"
    expected_urls = {key: ORIGIN for key in ("metadata_url", "callback_url", "agent_url", "webhook_url")}
    status, controller = call("PUT", "/controller", {**expected_urls, "enable_agent_tools_sync": False}, token)
    check(status == 200 and isinstance(controller, dict)
          and all(controller.get(key) == value for key, value in expected_urls.items()))

    # Past initialization and URL gates, check genuine authorization denial
    # followed by successful authenticated reads of the canonical stock arrays.
    for route in LISTS:
        stage = "unauthenticated-" + route
        status, _ = call("GET", "/" + route)
        check(status in (401, 403))
        stage = "authenticated-" + route
        status, rows = call("GET", "/" + route, token=token)
        check(status == 200 and type(rows) is list and not rows)


def main():
    context = ssl.create_default_context(cafile="/fixture-ca.pem")
    context.minimum_version = ssl.TLSVersion.TLSv1_3
    context.verify_flags |= ssl.VERIFY_X509_STRICT
    probe(lambda *args, **kwargs: request(context, *args, **kwargs))
    print("PASS stock GARM v0.2.1 locally authenticated empty instances, pools and providers; anonymous reads denied")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Closed diagnostic names only; never print response bodies, user/token
        # material, exception text or transport addresses.
        print("Stock inventory fixture failed at " + stage, file=sys.stderr)
        raise SystemExit(1)
