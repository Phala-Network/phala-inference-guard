#!/usr/bin/env python3
"""Generate the PIG v0.12.18 offline cross-tokenizer oracle manifest.

Tokenizer assets are loaded only in the approved isolated test environment.
The generated manifest records immutable model revisions and token counts, but
neither this tool nor the manifest is used by the production PIG hot path.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import platform
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import tokenizers
import transformers
from transformers import AutoTokenizer


@dataclass(frozen=True)
class TokenizerSpec:
    family: str
    model: str
    revision: str
    source: str


TOKENIZERS = (
    TokenizerSpec(
        family="qwen_bpe",
        model="Qwen/Qwen2.5-0.5B-Instruct",
        revision="7ae557604adf67be50417f59c2c2f167def9a775",
        source="huggingface",
    ),
    TokenizerSpec(
        family="mistral_bpe",
        model="mistralai/Mistral-7B-Instruct-v0.3",
        revision="c170c708c41dac9275d15a8fff4eca08d52bab71",
        source="huggingface",
    ),
    TokenizerSpec(
        family="deepseek",
        model="deepseek-ai/DeepSeek-V2-Lite-Chat",
        revision="85864749cd611b4353ce1decdb286193298f64c7",
        source="huggingface",
    ),
    TokenizerSpec(
        family="gemma_sentencepiece",
        model="google/gemma-4-E2B-it",
        revision="3e22461f65e89153144f8adb70e3b8c2cc9845a7",
        source="approved_local_snapshot",
    ),
)


def compact_json(value: Any) -> str:
    return json.dumps(
        value,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    )


def entropy_text(size: int) -> str:
    alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
    return "".join(alphabet[(index * 29 + index // 7) % len(alphabet)] for index in range(size))


def tool_schema(properties: int) -> list[dict[str, Any]]:
    return [
        {
            "function": {
                "description": "Return all requested values.",
                "name": "lookup",
                "parameters": {
                    "properties": {
                        f"field_{index:05d}": {
                            "description": f"requested property {index:05d}",
                            "type": "string",
                        }
                        for index in range(properties)
                    },
                    "type": "object",
                },
            },
            "type": "function",
        }
    ]


def fixture_definitions() -> list[dict[str, Any]]:
    return [
        {
            "name": "chat_ascii_prose",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {"unit": "word ", "repetitions": 2048},
            "tags": ["ascii", "prose", "hard_bound"],
        },
        {
            "name": "chat_source_code",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {
                "unit": "func add(a, b int) int { return a + b }\n",
                "repetitions": 256,
            },
            "tags": ["source_code", "hard_bound"],
        },
        {
            "name": "chat_json_uuid_long_numbers",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {
                "unit": '{"id":"123e4567-e89b-12d3-a456-426614174000","value":12345678901234567890}\n',
                "repetitions": 256,
            },
            "tags": ["json", "uuid", "numbers", "hard_bound"],
        },
        {
            "name": "chat_repeated_whitespace",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {"unit": " \t\n", "repetitions": 4096},
            "tags": ["whitespace", "hard_bound"],
        },
        {
            "name": "chat_cjk",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {"unit": "中文测试", "repetitions": 2048},
            "tags": ["cjk", "non_ascii", "hard_bound"],
        },
        {
            "name": "chat_emoji_combining",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {"unit": "😀é ", "repetitions": 2048},
            "tags": ["emoji", "combining", "non_ascii", "hard_bound"],
        },
        {
            "name": "chat_escaped_cjk",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_text",
            "parameters": {
                "unit": "中文\nquote\" ",
                "repetitions": 1024,
                "escape_cjk": True,
            },
            "tags": ["escape", "cjk", "comparison", "hard_bound"],
        },
        {
            "name": "chat_entropy_base64_text",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_entropy",
            "parameters": {"bytes": 16384},
            "tags": ["entropy", "base64", "byte_fallback", "hard_bound"],
        },
        {
            "name": "chat_metadata_heavy",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_metadata",
            "parameters": {"metadata_bytes": 65536},
            "tags": ["metadata", "comparison", "hard_bound"],
        },
        {
            "name": "chat_tool_schema",
            "endpoint": "/v1/chat/completions",
            "kind": "chat_tools",
            "parameters": {"properties": 256},
            "tags": ["tools", "schema", "comparison", "hard_bound"],
        },
        {
            "name": "completion_batch_suffix_fanout",
            "endpoint": "/v1/completions",
            "kind": "completion_batch",
            "parameters": {},
            "tags": ["batch", "suffix", "fanout", "hard_bound"],
        },
        {
            "name": "responses_visible_tools",
            "endpoint": "/v1/responses",
            "kind": "responses_visible",
            "parameters": {"properties": 64},
            "tags": ["responses", "tools", "hard_bound"],
        },
        {
            "name": "completion_explicit_token_arrays",
            "endpoint": "/v1/completions",
            "kind": "explicit_token_arrays",
            "parameters": {},
            "tags": ["token_ids", "batch", "exact"],
        },
    ]


def fixture_payload(fixture: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
    kind = fixture["kind"]
    parameters = fixture["parameters"]
    semantic: dict[str, Any]
    if kind == "chat_text":
        content = parameters["unit"] * parameters["repetitions"]
        payload = {
            "max_tokens": 256,
            "messages": [{"content": content, "role": "user"}],
            "model": "model-agnostic",
        }
        semantic = {"messages": payload["messages"], "tools": []}
    elif kind == "chat_entropy":
        content = entropy_text(parameters["bytes"])
        payload = {
            "max_tokens": 256,
            "messages": [{"content": content, "role": "user"}],
            "model": "model-agnostic",
        }
        semantic = {"messages": payload["messages"], "tools": []}
    elif kind == "chat_metadata":
        payload = {
            "max_tokens": 256,
            "messages": [{"content": "hello", "role": "user"}],
            "metadata": {"trace": "m" * parameters["metadata_bytes"]},
            "model": "model-agnostic",
        }
        semantic = {"messages": payload["messages"], "tools": []}
    elif kind == "chat_tools":
        tools = tool_schema(parameters["properties"])
        payload = {
            "max_tokens": 256,
            "messages": [{"content": "look up all requested fields", "role": "user"}],
            "model": "model-agnostic",
            "tools": tools,
        }
        semantic = {"messages": payload["messages"], "tools": tools}
    elif kind == "completion_batch":
        prompts = [
            "plain completion prompt " * 64,
            "中文补全" * 128,
            "func main() { return }\n" * 64,
        ]
        suffix = " suffix" * 32
        payload = {
            "best_of": 3,
            "max_tokens": 128,
            "model": "model-agnostic",
            "n": 2,
            "prompt": prompts,
            "suffix": suffix,
        }
        semantic = {"prompts": prompts, "suffix": suffix}
    elif kind == "responses_visible":
        tools = tool_schema(parameters["properties"])
        instructions = "Answer with concise evidence."
        input_text = "分析这段代码并返回 JSON: func main() { return }"
        payload = {
            "input": input_text,
            "instructions": instructions,
            "max_output_tokens": 256,
            "model": "model-agnostic",
            "tools": tools,
        }
        semantic = {
            "messages": [{"content": f"{instructions}\n\n{input_text}", "role": "user"}],
            "tools": tools,
        }
    elif kind == "explicit_token_arrays":
        prompts = [list(range(1, 257)), list(range(257, 385))]
        payload = {
            "max_tokens": 64,
            "model": "model-agnostic",
            "n": 2,
            "prompt": prompts,
        }
        semantic = {"token_arrays": prompts}
    else:
        raise ValueError(f"unsupported fixture kind: {kind}")
    return payload, semantic


def fixture_body(fixture: dict[str, Any], payload: dict[str, Any]) -> bytes:
    body = compact_json(payload)
    if fixture["parameters"].get("escape_cjk"):
        body = body.replace("中", r"\u4e2d").replace("文", r"\u6587")
    return body.encode("utf-8")


def token_count(tokenizer: Any, fixture: dict[str, Any], semantic: dict[str, Any]) -> tuple[int, int, str]:
    kind = fixture["kind"]
    if kind in {"chat_text", "chat_entropy", "chat_metadata", "chat_tools", "responses_visible"}:
        input_ids = tokenizer.apply_chat_template(
            semantic["messages"],
            tokenize=True,
            add_generation_prompt=True,
        )
        if isinstance(input_ids, dict):
            input_ids = input_ids["input_ids"]
        count = len(input_ids)
        tools = semantic.get("tools") or []
        if tools:
            count += len(tokenizer.encode(compact_json(tools), add_special_tokens=False))
        return count, count, "chat_template_plus_raw_tool_schema"
    if kind == "completion_batch":
        counts = [
            len(tokenizer.encode(prompt + semantic["suffix"], add_special_tokens=True))
            for prompt in semantic["prompts"]
        ]
        return sum(counts), max(counts), "completion_prompt_plus_suffix"
    if kind == "explicit_token_arrays":
        counts = [len(prompt) for prompt in semantic["token_arrays"]]
        return sum(counts), max(counts), "explicit_token_ids"
    raise ValueError(f"unsupported fixture kind: {kind}")


def load_tokenizers(gemma_path: Path, cache_dir: Path) -> list[tuple[TokenizerSpec, Any]]:
    loaded: list[tuple[TokenizerSpec, Any]] = []
    for spec in TOKENIZERS:
        if spec.source == "approved_local_snapshot":
            tokenizer = AutoTokenizer.from_pretrained(
                gemma_path,
                local_files_only=True,
                trust_remote_code=False,
            )
        else:
            tokenizer = AutoTokenizer.from_pretrained(
                spec.model,
                revision=spec.revision,
                cache_dir=cache_dir,
                trust_remote_code=False,
            )
        loaded.append((spec, tokenizer))
    return loaded


def generate(gemma_path: Path, cache_dir: Path) -> dict[str, Any]:
    loaded = load_tokenizers(gemma_path, cache_dir)
    tokenizer_manifest = []
    for spec, tokenizer in loaded:
        tokenizer_manifest.append(
            {
                "family": spec.family,
                "model": spec.model,
                "revision": spec.revision,
                "source": spec.source,
                "class": type(tokenizer).__name__,
                "fast": bool(tokenizer.is_fast),
                "vocabulary_size": len(tokenizer),
            }
        )

    fixture_manifest = []
    for definition in fixture_definitions():
        payload, semantic = fixture_payload(definition)
        body = fixture_body(definition, payload)
        oracle = []
        for spec, tokenizer in loaded:
            aggregate, maximum, method = token_count(tokenizer, definition, semantic)
            oracle.append(
                {
                    "family": spec.family,
                    "aggregate_input_tokens": aggregate,
                    "maximum_sequence_input_tokens": maximum,
                    "method": method,
                }
            )
        fixture_manifest.append(
            {
                **definition,
                "body_bytes": len(body),
                "body_sha256": hashlib.sha256(body).hexdigest(),
                "oracle": oracle,
            }
        )

    return {
        "schema_version": 1,
        "purpose": "offline_cross_tokenizer_acceptance_only",
        "production_runtime_consumes_manifest": False,
        "python": platform.python_version(),
        "transformers": transformers.__version__,
        "tokenizers": tokenizers.__version__,
        "tokenizer_manifest": tokenizer_manifest,
        "fixtures": fixture_manifest,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--gemma-path", required=True, type=Path)
    parser.add_argument("--cache-dir", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = generate(args.gemma_path, args.cache_dir)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(compact_json(result) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
