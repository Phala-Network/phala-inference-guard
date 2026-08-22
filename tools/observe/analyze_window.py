#!/usr/bin/env python3
"""CLI for reset-aware PIG serving-window analysis."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

from window_analysis import ObservationWindow, analyze


def arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("samples", type=Path, help="fixed-interval observer CSV")
    parser.add_argument("output", type=Path, help="JSON output path")
    parser.add_argument(
        "--horizon",
        required=True,
        choices=("release", "stability", "delayed"),
        help="formal checkpoint contract to evaluate",
    )
    return parser.parse_args()


def main() -> int:
    args = arguments()
    result = analyze(ObservationWindow.from_csv(args.samples), horizon=args.horizon)
    result["artifact"] = {
        "samples_file": args.samples.name,
        "samples_sha256": hashlib.sha256(args.samples.read_bytes()).hexdigest(),
    }
    encoded = json.dumps(result, indent=2, sort_keys=True, allow_nan=False) + "\n"
    args.output.write_text(encoded, encoding="utf-8")
    digest = hashlib.sha256(encoded.encode("utf-8")).hexdigest()
    args.output.with_suffix(args.output.suffix + ".sha256").write_text(
        f"{digest}  {args.output.name}\n",
        encoding="utf-8",
    )
    print(encoded, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
