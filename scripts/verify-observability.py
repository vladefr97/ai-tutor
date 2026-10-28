#!/usr/bin/env python3
"""Checks AC-12/AC-13 against a running stack (make verify-observability).

Sends control traffic with a known number of 2xx/4xx/5xx responses, a slow
request and a real failing model call, then verifies through Prometheus,
Tempo, Loki and Grafana that the data arrived. Exits 1 on any mismatch.

The backend must run with OBSERVABILITY_TEST_ROUTES=1 (5xx and slow routes)
and MODEL_PLANNER set to a model that does not exist (real external error).
"""
import http.cookiejar
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

BASE = os.environ.get("BASE_URL", "http://localhost:8080")
PROM = os.environ.get("PROMETHEUS_URL", "http://localhost:9090")
TEMPO = os.environ.get("TEMPO_URL", "http://localhost:3200")
LOKI = os.environ.get("LOKI_URL", "http://localhost:3100")
GRAFANA = os.environ.get("GRAFANA_URL", "http://localhost:3000")
MAILPIT = os.environ.get("MAILPIT_URL", "http://localhost:8025")
DASHBOARD = os.path.join(os.path.dirname(__file__), "..", "infra/observability/grafana/dashboards/tutor-backend.json")

failures = []


def check(ok, what):
    print(("OK   " if ok else "FAIL ") + what, flush=True)
    if not ok:
        failures.append(what)


def request(url, method="GET", body=None, opener=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method, headers={"Content-Type": "application/json"} if data else {})
    try:
        resp = (opener or urllib.request.build_opener()).open(req, timeout=30)
        return resp.status, resp.headers, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


def get_json(url):
    status, _, body = request(url)
    if status != 200:
        raise RuntimeError(f"{url}: {status} {body[:200]!r}")
    return json.loads(body)


def prom_value(expr):
    data = get_json(PROM + "/api/v1/query?" + urllib.parse.urlencode({"query": expr}))["data"]["result"]
    return sum(float(r["value"][1]) for r in data)


def wait_scrape():
    time.sleep(12)  # scrape interval is 5s


def login(email):
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    st, _, _ = request(BASE + "/api/auth/code", "POST", {"email": email}, opener)
    assert st == 204, st
    code = ""
    for _ in range(30):
        found = get_json(MAILPIT + "/api/v1/search?" + urllib.parse.urlencode({"query": "to:" + email}))["messages"]
        if found:
            text = get_json(MAILPIT + "/api/v1/message/" + found[0]["ID"])["Text"]
            code = re.search(r"\b\d{6}\b", text).group(0)
            break
        time.sleep(0.5)
    st, _, _ = request(BASE + "/api/auth/verify", "POST", {"email": email, "code": code}, opener)
    assert st == 200, st
    return opener, code


def main():
    for _ in range(30):
        try:
            if any(t["health"] == "up" for t in get_json(PROM + "/api/v1/targets")["data"]["activeTargets"]):
                break
        except Exception:
            pass
        time.sleep(2)
    else:
        check(False, "Prometheus scrapes the backend")
        return

    me = 'http_requests_total{method="GET",route="/api/me",code="%s"}'
    status_route = 'http_requests_total{route="/api/observability-test/status/{code}",code="%s"}'
    slow = 'http_request_duration_seconds_bucket{route="/api/observability-test/slow",code="200",le="%s"}'
    ext_err = 'external_calls_total{service="model",operation="planner",outcome="error"}'
    series = {"2xx /api/me": me % "200", "401 /api/me": me % "401", "503": status_route % "503",
              "500": status_route % "500", "slow le=1": slow % "1", "slow le=2.5": slow % "2.5",
              "model errors": ext_err}
    wait_scrape()
    before = {k: prom_value(v) for k, v in series.items()}

    email = f"observability-{int(time.time())}@example.test"
    opener, code = login(email)
    for _ in range(5):
        request(BASE + "/api/me", opener=opener)
    for _ in range(3):
        request(BASE + "/api/me")  # no session → 401
    for _ in range(2):
        request(BASE + "/api/observability-test/status/503")
    request(BASE + "/api/observability-test/status/500")
    request(BASE + "/api/observability-test/slow")

    # Real external call that fails: the planner model does not exist.
    st, headers, body = request(BASE + "/api/courses", "POST", {"direction": "pm"}, opener)
    check(st == 201, f"course created ({st})")
    trace_id = headers.get("X-Trace-Id", "")
    op = json.loads(body)["operation"]["id"]
    for _ in range(90):
        _, _, b = request(BASE + "/api/operations/" + op, opener=opener)
        status = json.loads(b)["status"]
        if status in ("succeeded", "failed"):
            break
        time.sleep(2)
    check(status == "failed", f"model call with a nonexistent model fails the operation ({status})")

    wait_scrape()
    delta = {k: prom_value(v) - before[k] for k, v in series.items()}
    for name, want in [("2xx /api/me", 5), ("401 /api/me", 3), ("503", 2), ("500", 1), ("slow le=1", 0), ("slow le=2.5", 1), ("model errors", 1)]:
        check(delta[name] == want, f"Prometheus delta {name}: {delta[name]:g}, want {want}")
    labels = get_json(PROM + "/api/v1/label/route/values")["data"]
    check(all(not re.search(r"[0-9a-f]{8}-[0-9a-f]{4}", r) for r in labels), "route labels are patterns without IDs")

    # Trace of the request, background operation and model call.
    spans = []
    for _ in range(20):
        st, _, b = request(TEMPO + "/api/traces/" + trace_id)
        if st == 200:
            doc = json.loads(b)
            spans = [s for batch in doc.get("batches", doc.get("resourceSpans", []))
                     for ss in batch.get("scopeSpans", batch.get("instrumentationLibrarySpans", [])) for s in ss["spans"]]
            if any(s["name"] == "model planner" for s in spans):
                break
        time.sleep(3)
    names = {s["name"] for s in spans}
    check({"POST /api/courses", "operation mentor_reply", "model planner"} <= names, f"Tempo trace {trace_id}: {sorted(names)}")
    model = [s for s in spans if s["name"] == "model planner"]
    check(bool(model) and model[0].get("status", {}).get("code") in ("STATUS_CODE_ERROR", 2), "model span is marked as error")

    # Logs linked by trace_id.
    def loki(query):
        params = {"query": query, "start": str(int((time.time() - 3600) * 1e9)), "end": str(int(time.time() * 1e9)), "limit": "100"}
        res = get_json(LOKI + "/loki/api/v1/query_range?" + urllib.parse.urlencode(params))["data"]["result"]
        return [line for stream in res for _, line in stream["values"]]

    lines = []
    for _ in range(20):
        lines = loki('{service_name="tutor-backend"} | trace_id="%s"' % trace_id)
        if any("operation failed" in l for l in lines):
            break
        time.sleep(3)
    check(any("operation failed" in l for l in lines), f"Loki has the failure log for trace {trace_id} ({len(lines)} lines)")
    check(any("http request" in l for l in lines), "Loki has the request log for the same trace")
    check(not loki('{service_name="tutor-backend"} |= "%s"' % code), "login code is not in Loki")
    key = os.environ.get("OPENAI_API_KEY", "")
    if key:
        check(not loki('{service_name="tutor-backend"} |= "%s"' % key[-12:]), "API key is not in Loki")

    # Every dashboard panel returns data through Grafana's datasource proxy.
    dash = json.load(open(DASHBOARD))
    found = get_json(GRAFANA + "/api/search?query=" + urllib.parse.quote(dash["title"]))
    check(any(d.get("uid") == dash["uid"] for d in found), "dashboard is provisioned in Grafana")
    for panel in dash["panels"]:
        for t in panel["targets"]:
            uid = t["datasource"]["uid"]
            if uid == "prometheus":
                url = f"{GRAFANA}/api/datasources/proxy/uid/prometheus/api/v1/query?" + urllib.parse.urlencode({"query": t["expr"]})
                result = get_json(url)["data"]["result"]
            else:
                params = {"query": t["expr"], "start": str(int((time.time() - 3600) * 1e9)), "limit": "5"}
                result = get_json(f"{GRAFANA}/api/datasources/proxy/uid/loki/loki/api/v1/query_range?" + urllib.parse.urlencode(params))["data"]["result"]
            check(bool(result), f"panel «{panel['title']}» {t['refId']} has data")

    print()
    if failures:
        print(f"{len(failures)} check(s) failed")
        sys.exit(1)
    print("all observability checks passed")


if __name__ == "__main__":
    main()
