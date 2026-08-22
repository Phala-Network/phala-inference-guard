#!/usr/bin/env python3

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sys

from paired_snapshot_analysis import analyze_paired_captures, load_capture


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Analyze immutable start/end PIG and backend captures."
    )
    parser.add_argument("start_capture", type=Path)
    parser.add_argument("end_capture", type=Path)
    parser.add_argument("output_json", type=Path)
    args = parser.parse_args()

    result = analyze_paired_captures(
        load_capture(args.start_capture), load_capture(args.end_capture)
    )
    encoded = (
        json.dumps(result, indent=2, sort_keys=True, allow_nan=False) + "\n"
    ).encode("utf-8")
    args.output_json.parent.mkdir(parents=True, exist_ok=True)
    args.output_json.write_bytes(encoded)
    digest = hashlib.sha256(encoded).hexdigest()
    sidecar = args.output_json.with_name(f"{args.output_json.name}.sha256")
    sidecar.write_text(
        f"{digest}  {args.output_json.name}\n", encoding="utf-8"
    )
    json.dump(
        {
            "schema_version": result["schema_version"],
            "comparison_eligible": result["evidence"][
                "comparison_eligible"
            ],
            "errors": result["evidence"]["errors"],
            "output": str(args.output_json),
            "output_sha256": digest,
        },
        sys.stdout,
        sort_keys=True,
    )
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
