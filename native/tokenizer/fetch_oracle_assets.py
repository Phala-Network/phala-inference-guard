#!/usr/bin/env python3
import argparse
import hashlib
import json
import os
from pathlib import Path
from urllib.parse import quote
from urllib.request import Request, urlopen


ASSETS = ("tokenizer.json", "tokenizer_config.json", "chat_template.jinja")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    args.output.mkdir(parents=True, exist_ok=True)
    token = os.environ.get("HF_TOKEN", "")
    manifest = {
        "repository": args.repository,
        "revision": args.revision,
        "assets": {},
    }
    for name in ASSETS:
        url = (
            "https://huggingface.co/"
            f"{quote(args.repository, safe='/')}/resolve/{quote(args.revision, safe='')}/{quote(name)}"
        )
        headers = {"Authorization": f"Bearer {token}"} if token else {}
        with urlopen(Request(url, headers=headers), timeout=120) as response:
            payload = response.read()
        target = args.output / name
        target.write_bytes(payload)
        manifest["assets"][name] = {
            "bytes": len(payload),
            "sha256": hashlib.sha256(payload).hexdigest(),
        }
    print(json.dumps(manifest, indent=2, sort_keys=True))


if __name__ == "__main__":
    main()
