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


def measure(
    binary: Path,
    tokenizer: Path,
    text: str,
    mode: str,
    warmup: int,
    iterations: int,
    block_size: int,
) -> dict:
    command = [
        str(binary),
        mode,
        str(tokenizer),
        "true",
        str(warmup),
        str(iterations),
    ]
    if mode == "analyze-bench":
        command.append(str(block_size))
    completed = subprocess.run(
        command,
        input=text,
        text=True,
        capture_output=True,
        check=True,
    )
    return json.loads(completed.stdout)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tokenizer", type=Path, required=True)
    parser.add_argument("--native-binary", type=Path, required=True)
    parser.add_argument("--block-size", type=int, default=64)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    if args.block_size <= 0:
        raise ValueError("block size must be positive")

    results = []
    for name, seed, repeat, warmup, iterations in CASES:
        text = seed * repeat
        vec_ids = measure(
            args.native_binary,
            args.tokenizer,
            text,
            "bench",
            warmup,
            iterations,
            args.block_size,
        )
        block_analysis = measure(
            args.native_binary,
            args.tokenizer,
            text,
            "analyze-bench",
            warmup,
            iterations,
            args.block_size,
        )
        if vec_ids["tokens"] != block_analysis["tokens"]:
            raise RuntimeError(f"token-count mismatch for {name}")
        results.append(
            {
                "name": name,
                "input_sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
                "vec_ids": vec_ids,
                "block_analysis": block_analysis,
                "analysis_to_vec_ratio": {
                    quantile: block_analysis[quantile] / vec_ids[quantile]
                    for quantile in ("p50_us", "p95_us", "p99_us")
                },
            }
        )

    report = {
        "scope": (
            "same Rust binary and tokenizer fixture; each path has its own process load and "
            "warmup; block_analysis returns no token IDs and includes input SHA-256 plus "
            "keyed chained full/partial block digests"
        ),
        "block_size": args.block_size,
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
