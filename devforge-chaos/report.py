"""Keep a history of experiments and render it as a Markdown report."""
import json
import math
import os
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone


@dataclass
class Record:
    experiment: str
    namespace: str
    targets: list = field(default_factory=list)
    result: str = "success"  # success, failed, skipped, dry-run, not-recovered
    recovery_seconds: float = None
    detail: str = ""
    started_at: str = field(default_factory=lambda: datetime.now(timezone.utc).isoformat(timespec="seconds"))


def _history_path(report_dir):
    return os.path.join(report_dir, "results.jsonl")


def append_record(report_dir, record):
    os.makedirs(report_dir, exist_ok=True)
    with open(_history_path(report_dir), "a", encoding="utf-8") as f:
        f.write(json.dumps(asdict(record)) + "\n")


def load_records(report_dir):
    path = _history_path(report_dir)
    if not os.path.exists(path):
        return []
    with open(path, encoding="utf-8") as f:
        return [Record(**json.loads(line)) for line in f if line.strip()]


def percentile(values, pct):
    """Nearest-rank percentile of a non-empty list."""
    ordered = sorted(values)
    rank = max(1, math.ceil(pct / 100 * len(ordered)))
    return ordered[rank - 1]


def render_markdown(records):
    lines = ["# Chaos report", ""]
    if not records:
        return "\n".join(lines + ["No experiments have run yet.", ""])

    lines += [
        f"Generated {datetime.now(timezone.utc).isoformat(timespec='seconds')} - {len(records)} experiment(s)",
        "",
        "## Recovery time (MTTR) by experiment",
        "",
        "| Experiment | Runs | Recovered | Avg (s) | p95 (s) | Max (s) | Not recovered / failed |",
        "|---|---|---|---|---|---|---|",
    ]
    for name in sorted({r.experiment for r in records}):
        runs = [r for r in records if r.experiment == name]
        times = [r.recovery_seconds for r in runs if r.recovery_seconds is not None]
        bad = sum(1 for r in runs if r.result in ("failed", "not-recovered"))
        if times:
            stats = f"{sum(times) / len(times):.1f} | {percentile(times, 95):.1f} | {max(times):.1f}"
        else:
            stats = "- | - | -"
        lines.append(f"| {name} | {len(runs)} | {len(times)} | {stats} | {bad} |")

    lines += ["", "## Timeline", "", "| Time (UTC) | Experiment | Targets | Result | Recovery (s) | Detail |", "|---|---|---|---|---|---|"]
    for r in reversed(records):
        rec = "-" if r.recovery_seconds is None else f"{r.recovery_seconds:.1f}"
        lines.append(f"| {r.started_at} | {r.experiment} | {', '.join(r.targets) or '-'} | {r.result} | {rec} | {r.detail} |")
    return "\n".join(lines + [""])


def write_report(report_dir):
    """Rewrite chaos-report.md from the full history (atomic replace, so readers never see half a file)."""
    os.makedirs(report_dir, exist_ok=True)
    path = os.path.join(report_dir, "chaos-report.md")
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        f.write(render_markdown(load_records(report_dir)))
    os.replace(tmp, path)
    return path
