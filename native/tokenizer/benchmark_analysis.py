#!/usr/bin/env python3
import argparse
import hashlib
import json
import subprocess
import tempfile
from pathlib import Path


CASES = (
    ("short", "The quick brown fox calls tool_17 with JSON. ", 4, 200, 5000),
    ("medium", "predictive admission token ", 1024, 50, 1000),
    ("long", "predictive admission token ", 8192, 20, 200),
    ("about_64k_tokens", "predictive ", 65536, 3, 30),
    ("about_128k_tokens", "predictive ", 131072, 2, 15),
)


def measure(
    binary: Path,
    tokenizer: Path,
    text: str,
    mode: str,
    warmup: int,
    iterations: int,
    add_special_tokens: str,
) -> dict:
    command = [
        str(binary),
        mode,
        str(tokenizer),
        add_special_tokens,
        str(warmup),
        str(iterations),
    ]
    with tempfile.NamedTemporaryFile() as timing:
        completed = subprocess.run(
            ["/usr/bin/time", "-f", "%M", "-o", timing.name, *command],
            input=text,
            text=True,
            capture_output=True,
            check=True,
        )
        timing.seek(0)
        peak_rss_kib = int(timing.read().decode("ascii").strip())
    result = json.loads(completed.stdout)
    result["peak_rss_kib"] = peak_rss_kib
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tokenizer", type=Path, required=True)
    parser.add_argument("--native-binary", type=Path, required=True)
    parser.add_argument("--add-special-tokens", choices=("true", "false"), default="false")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    results = []
    for name, seed, repeat, warmup, iterations in CASES:
        text = seed * repeat
        count_only = measure(
            args.native_binary,
            args.tokenizer,
            text,
            "count-bench",
            warmup,
            iterations,
            args.add_special_tokens,
        )
        vec_ids = measure(
            args.native_binary,
            args.tokenizer,
            text,
            "bench",
            warmup,
            iterations,
            args.add_special_tokens,
        )
        if count_only["tokens"] != vec_ids["tokens"]:
            raise RuntimeError(f"token-count mismatch for {name}")
        results.append(
            {
                "name": name,
                "input_sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
                "count_only": count_only,
                "vec_ids": vec_ids,
                "count_to_vec_ratio": {
                    quantile: count_only[quantile] / vec_ids[quantile]
                    for quantile in ("p50_us", "p95_us", "p99_us")
                },
            }
        )

    report = {
        "scope": (
            "same Rust binary and tokenizer; each path has its own process load and "
            "warmup; report-level input_sha256 identifies only the fixed synthetic fixture; "
            "count_only returns only a count; vec_ids clones IDs for the exact-ID comparator; "
            "peak RSS includes tokenizer load"
        ),
        "add_special_tokens": args.add_special_tokens == "true",
        "tokenizer_sha256": hashlib.sha256(args.tokenizer.read_bytes()).hexdigest(),
        "cases": results,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(report, sort_keys=True))


if __name__ == "__main__":
    main()
