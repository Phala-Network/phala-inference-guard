#!/usr/bin/env python3
import argparse
import hashlib
import json
import subprocess
from pathlib import Path


CASES = (
    ("small", "The quick brown fox calls tool_17 with JSON. ", 4, 200, 5000),
    ("64kib", "0123456789abcdef", 4096, 20, 200),
    ("2mib", "0123456789abcdef", 131072, 2, 20),
)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tokenizer", type=Path, required=True)
    parser.add_argument("--native-binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    results = []
    for name, seed, repeat, warmup, iterations in CASES:
        text = seed * repeat
        completed = subprocess.run(
            [
                str(args.native_binary),
                "bench",
                str(args.tokenizer),
                "true",
                str(warmup),
                str(iterations),
            ],
            input=text,
            text=True,
            capture_output=True,
            check=True,
        )
        measured = json.loads(completed.stdout)
        measured["name"] = name
        measured["input_sha256"] = hashlib.sha256(text.encode("utf-8")).hexdigest()
        results.append(measured)
    report = {
        "scope": "Rust tokenizer core in one process; excludes process startup and Go/CGO",
        "tokenizer_sha256": hashlib.sha256(args.tokenizer.read_bytes()).hexdigest(),
        "cases": results,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps(report, sort_keys=True))


if __name__ == "__main__":
    main()
