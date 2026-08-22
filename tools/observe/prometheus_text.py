#!/usr/bin/env python3

"""Small, strict Prometheus text parser for offline evidence analysis.

The serving observer must remain standard-library only. This parser covers the
Prometheus 0.0.4 sample syntax emitted by PIG and supported backends. It is not
used on the request path and rejects ambiguous duplicate series.
"""

from __future__ import annotations

from dataclasses import dataclass
import math
import re
from types import MappingProxyType
from typing import Iterable, Mapping


_SAMPLE_RE = re.compile(
    r"^(?P<name>[A-Za-z_:][A-Za-z0-9_:]*)"
    r"(?:\{(?P<labels>.*)\})?"
    r"[ \t]+(?P<value>\S+)"
    r"(?:[ \t]+(?P<timestamp>\S+))?[ \t]*$"
)
_LABEL_NAME_RE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_TYPE_RE = re.compile(
    r"^#[ \t]+TYPE[ \t]+(?P<name>[A-Za-z_:][A-Za-z0-9_:]*)"
    r"[ \t]+(?P<type>counter|gauge|histogram|summary|untyped)[ \t]*$"
)


@dataclass(frozen=True)
class MetricSeries:
    name: str
    labels: Mapping[str, str]
    value: float

    @property
    def identity(self) -> tuple[tuple[str, str], ...]:
        return tuple(sorted(self.labels.items()))


class PrometheusSnapshot:
    def __init__(
        self,
        samples: Iterable[MetricSeries],
        metric_types: Mapping[str, str] | None = None,
    ) -> None:
        by_name: dict[str, list[MetricSeries]] = {}
        identities: set[tuple[str, tuple[tuple[str, str], ...]]] = set()
        for sample in samples:
            key = (sample.name, sample.identity)
            if key in identities:
                rendered = ",".join(
                    f'{name}="{value}"' for name, value in sample.identity
                )
                raise ValueError(f"duplicate series: {sample.name}{{{rendered}}}")
            identities.add(key)
            by_name.setdefault(sample.name, []).append(sample)
        self._by_name = {
            name: tuple(sorted(values, key=lambda item: item.identity))
            for name, values in by_name.items()
        }
        self.metric_types = MappingProxyType(dict(metric_types or {}))

    def series(self, name: str) -> tuple[MetricSeries, ...]:
        return self._by_name.get(name, ())

    def one(self, name: str) -> MetricSeries:
        values = self.series(name)
        if len(values) != 1:
            raise ValueError(f"expected one series for {name}, found {len(values)}")
        return values[0]

    def names(self) -> tuple[str, ...]:
        return tuple(sorted(self._by_name))


def _parse_labels(raw: str | None, line_number: int) -> Mapping[str, str]:
    if raw is None or raw == "":
        return MappingProxyType({})
    labels: dict[str, str] = {}
    position = 0
    length = len(raw)
    while position < length:
        while position < length and raw[position] in " \t":
            position += 1
        match = _LABEL_NAME_RE.match(raw, position)
        if match is None:
            raise ValueError(f"invalid label name on line {line_number}")
        name = match.group(0)
        position = match.end()
        while position < length and raw[position] in " \t":
            position += 1
        if position >= length or raw[position] != "=":
            raise ValueError(f"missing '=' after label {name} on line {line_number}")
        position += 1
        while position < length and raw[position] in " \t":
            position += 1
        if position >= length or raw[position] != '"':
            raise ValueError(
                f"missing quoted value for label {name} on line {line_number}"
            )
        position += 1
        value: list[str] = []
        closed = False
        while position < length:
            character = raw[position]
            position += 1
            if character == '"':
                closed = True
                break
            if character != "\\":
                value.append(character)
                continue
            if position >= length:
                raise ValueError(f"trailing label escape on line {line_number}")
            escaped = raw[position]
            position += 1
            if escaped == "n":
                value.append("\n")
            elif escaped in ('"', "\\"):
                value.append(escaped)
            else:
                raise ValueError(
                    f"unsupported label escape \\{escaped} on line {line_number}"
                )
        if not closed:
            raise ValueError(f"unterminated label value on line {line_number}")
        if name in labels:
            raise ValueError(f"duplicate label {name} on line {line_number}")
        labels[name] = "".join(value)
        while position < length and raw[position] in " \t":
            position += 1
        if position == length:
            break
        if raw[position] != ",":
            raise ValueError(
                f"expected ',' after label {name} on line {line_number}"
            )
        position += 1
        if position == length:
            raise ValueError(f"trailing label comma on line {line_number}")
    return MappingProxyType(labels)


def _parse_value(raw: str, line_number: int) -> float:
    if raw in ("+Inf", "Inf"):
        return math.inf
    if raw == "-Inf":
        return -math.inf
    if raw == "NaN":
        return math.nan
    try:
        return float(raw)
    except ValueError as error:
        raise ValueError(
            f"invalid sample value on line {line_number}: {raw}"
        ) from error


def parse_prometheus(text: str) -> PrometheusSnapshot:
    samples: list[MetricSeries] = []
    metric_types: dict[str, str] = {}
    for line_number, raw_line in enumerate(text.splitlines(), start=1):
        line = raw_line.strip()
        if not line:
            continue
        if line.startswith("#"):
            type_match = _TYPE_RE.match(line)
            if type_match is not None:
                name = type_match.group("name")
                metric_type = type_match.group("type")
                previous = metric_types.get(name)
                if previous is not None and previous != metric_type:
                    raise ValueError(
                        f"conflicting TYPE for {name} on line {line_number}"
                    )
                metric_types[name] = metric_type
            continue
        match = _SAMPLE_RE.match(line)
        if match is None:
            raise ValueError(f"invalid Prometheus sample on line {line_number}")
        samples.append(
            MetricSeries(
                name=match.group("name"),
                labels=_parse_labels(match.group("labels"), line_number),
                value=_parse_value(match.group("value"), line_number),
            )
        )
    return PrometheusSnapshot(samples, metric_types)
