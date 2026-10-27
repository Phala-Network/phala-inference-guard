#!/usr/bin/env python3
"""Exercise the built Linux image against a loopback-only mock backend."""

import argparse
import http.server
import json
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


LABELS = 'model_name="image-contract",engine_type="unified",dp_rank="0"'
METRICS = "\n".join(
    f"# TYPE {name} {kind}\n{name}{{{LABELS}{extra}}} {value}"
    for name, kind, value, extra in [
        ("sglang:max_total_num_tokens", "gauge", 65536, ""),
        ("sglang:page_size", "gauge", 1, ""),
        ("sglang:num_pages", "gauge", 65536, ""),
        ("sglang:kv_available_tokens", "gauge", 65536, ""),
        ("sglang:kv_evictable_tokens", "gauge", 0, ""),
        ("sglang:kv_used_tokens", "gauge", 0, ""),
        ("sglang:num_running_reqs", "gauge", 0, ""),
        ("sglang:num_queue_reqs", "gauge", 0, ""),
        ("sglang:num_retracted_requests_total", "counter", 0, ""),
        ("sglang:realtime_tokens_total", "counter", 0, ',mode="decode"'),
    ]
) + "\n"


class Backend(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    saw_chunked = False

    def log_message(self, *_):
        pass

    def send(self, status, body, content_type="application/json"):
        if not isinstance(body, bytes):
            body = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/metrics":
            self.send(200, METRICS.encode(), "text/plain; version=0.0.4")
        elif self.path == "/server_info":
            self.send(200, {
                "max_running_requests": 48,
                "chunked_prefill_size": 8192,
                "enable_priority_scheduling": True,
                "schedule_low_priority_values_first": False,
                "disable_priority_preemption": True,
                "default_priority_value": 1,
                "internal_states": [{"effective_max_running_requests_per_dp": 43}],
            })
        else:
            self.send(200, {"data": [{"id": "image-contract"}]})

    def do_POST(self):
        try:
            if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
                Backend.saw_chunked = True
                chunks = []
                while True:
                    size = int(self.rfile.readline().split(b";", 1)[0], 16)
                    if size == 0:
                        while self.rfile.readline().strip():
                            pass
                        break
                    chunks.append(self.rfile.read(size))
                    if self.rfile.read(2) != b"\r\n":
                        raise ValueError("invalid chunk framing")
                body = b"".join(chunks)
            else:
                body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            request = json.loads(body)
            priority = request["priority"]
        except (ValueError, KeyError):
            self.send(400, {"error": {"message": "invalid request"}})
            return
        usage = {"prompt_tokens": 1024, "completion_tokens": 1,
                 "prompt_tokens_details": {"cached_tokens": 0}}
        if request.get("stream"):
            events = [
                {"choices": [{"delta": {"content": "ok"}}], "smoke_priority": priority},
                {"choices": [], "usage": usage},
            ]
            payload = b"".join(b"data: " + json.dumps(event).encode() + b"\n\n" for event in events)
            self.send(200, payload + b"data: [DONE]\n\n", "text/event-stream")
        else:
            self.send(200, {"choices": [{"message": {"content": "ok"}}],
                            "usage": usage, "smoke_priority": priority})


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    parser.add_argument("--expected-version", required=True)
    args = parser.parse_args()
    image_id = subprocess.check_output(["docker", "image", "inspect", "--format", "{{.Id}}", args.image], text=True).strip()
    backend = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Backend)
    threading.Thread(target=backend.serve_forever, daemon=True).start()
    with socket.socket() as port_socket:
        port_socket.bind(("127.0.0.1", 0))
        port = port_socket.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    upstream = f"http://127.0.0.1:{backend.server_port}"
    # A fixed disposable fixture credential; never reads operator credentials.
    fixture_token = "pig-disposable-image-contract"
    container = f"pig-image-contract-{port}"
    command = ["docker", "run", "--rm", "--pull=never", "--network=host", "--name", container,
               "--cpus=2", "--memory=512m", "--pids-limit=128",
               "--label", "codex.test=pig-image-contract"]
    for name, value in {
        "LISTEN": f"127.0.0.1:{port}", "UPSTREAM": upstream,
        "PREDICTIVE_METRICS_URL": upstream + "/metrics", "TOKEN": fixture_token,
        "ATTESTATION_ENABLED": "false", "GOMAXPROCS": "2",
    }.items():
        command.extend(["-e", f"{name}={value}"])
    command.append(image_id)

    def request(path, body=None, tier=None, authorized=True):
        headers = {"Content-Type": "application/json"}
        if authorized:
            headers["Authorization"] = "Bearer " + fixture_token
        if tier is not None:
            headers["X-User-Tier"] = tier
        req = urllib.request.Request(base + path, data=body, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=15) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as error:
            return error.code, error.read()

    with tempfile.TemporaryFile(mode="w+") as runtime_log:
        process = subprocess.Popen(command, stdout=runtime_log, stderr=subprocess.STDOUT, text=True)
        try:
            deadline = time.monotonic() + 60
            while True:
                if process.poll() is not None:
                    raise RuntimeError("final image exited before readiness")
                try:
                    if request("/readyz")[0] == 200:
                        break
                except (OSError, urllib.error.URLError):
                    pass
                if time.monotonic() >= deadline:
                    raise RuntimeError("final image did not become ready")
                time.sleep(0.2)
            assert request("/v1/models", authorized=False)[0] == 401
            assert request("/v1/models")[0] == 200
            for tier, expected in [("priority", 2), ("default", 1), ("flex", 0), ("unknown", 1), (None, 1)]:
                body = json.dumps({"messages": [{"role": "user", "content": "hello"}], "priority": 99}).encode()
                status, payload = request("/v1/chat/completions", body, tier)
                assert status == 200 and json.loads(payload)["smoke_priority"] == expected
            status, payload = request("/v1/chat/completions", b'{"messages":[],"stream":true,"priority":99}', "priority")
            assert status == 200 and b'"smoke_priority": 2' in payload and b"[DONE]" in payload
            large = b"{" + b" " * (4 * 1024 * 1024) + b'"messages":[],"priority":99}'
            status, payload = request("/v1/chat/completions", large, "flex")
            assert status == 200 and json.loads(payload)["smoke_priority"] == 0 and Backend.saw_chunked
            invalid = b"{" + b" " * (4 * 1024 * 1024) + b'"messages":[],}'
            assert 400 <= request("/v1/chat/completions", invalid, "default")[0] < 500
            status, payload = request("/v1/metrics")
            metrics = payload.decode()
            expected = args.expected_version.removeprefix("v")
            assert status == 200 and f'pig_info{{version="PIG-v{expected}"}} 1' in metrics
            assert "pig_predictive_backend_tier_scheduling_enabled 1\n" in metrics
            assert "pig_predictive_running_limit 43\n" in metrics
            assert "pig_predictive_tps_warm_prompt_tokens 8192\n" in metrics
            print(f"PIG_FINAL_IMAGE_SMOKE_OK image={image_id} version={expected} auth=pass tiers=pass stream=pass oversized_chunked=pass effective_pool=43")
        except Exception:
            runtime_log.seek(0)
            print("".join(runtime_log.readlines()[-20:]))
            raise
        finally:
            subprocess.run(["docker", "rm", "-f", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            process.wait(timeout=20)
            backend.shutdown()
            backend.server_close()


if __name__ == "__main__":
    main()
