#!/usr/bin/env python3
import argparse
import hashlib
import json
import subprocess
from pathlib import Path

import tokenizers
import transformers


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--tokenizer-dir", type=Path, required=True)
    parser.add_argument("--native-binary", type=Path, required=True)
    parser.add_argument("--fixtures", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    tokenizer = transformers.AutoTokenizer.from_pretrained(
        args.tokenizer_dir,
        local_files_only=True,
        use_fast=True,
    )
    fixtures = json.loads(args.fixtures.read_text(encoding="utf-8"))
    results = []
    mismatches = 0
    for fixture in fixtures:
        text = fixture["text"] * int(fixture.get("repeat", 1))
        for add_special_tokens in (False, True):
            oracle_ids = tokenizer.encode(text, add_special_tokens=add_special_tokens)
            completed = subprocess.run(
                [
                    str(args.native_binary),
                    "encode",
                    str(args.tokenizer_dir / "tokenizer.json"),
                    str(add_special_tokens).lower(),
                ],
                input=text,
                text=True,
                capture_output=True,
                check=True,
            )
            native_ids = json.loads(completed.stdout)
            equal = native_ids == oracle_ids
            mismatches += int(not equal)
            results.append(
                {
                    "name": fixture["name"],
                    "add_special_tokens": add_special_tokens,
                    "input_sha256": hashlib.sha256(text.encode("utf-8")).hexdigest(),
                    "input_bytes": len(text.encode("utf-8")),
                    "token_count": len(oracle_ids),
                    "exact_ids_equal": equal,
                }
            )

    assets = {}
    for path in sorted(args.tokenizer_dir.iterdir()):
        if path.is_file():
            payload = path.read_bytes()
            assets[path.name] = {
                "bytes": len(payload),
                "sha256": hashlib.sha256(payload).hexdigest(),
            }
    report = {
        "oracle": {
            "transformers": transformers.__version__,
            "tokenizers": tokenizers.__version__,
            "is_fast": bool(tokenizer.is_fast),
        },
        "capability": "pre-rendered text tokenization only",
        "assets": assets,
        "cases": results,
        "mismatches": mismatches,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({"cases": len(results), "mismatches": mismatches}, sort_keys=True))
    if mismatches:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
