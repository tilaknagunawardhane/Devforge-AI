"""Prometheus metrics exposed by the chaos engine."""
from prometheus_client import Counter, Gauge, Histogram

EXPERIMENTS = Counter(
    "chaos_experiments_total",
    "Chaos experiments by type and result (success, failed, skipped, dry-run, not-recovered)",
    ["experiment", "result"],
)
RECOVERY = Histogram(
    "chaos_recovery_seconds",
    "Time for the workload to become healthy again after an experiment (MTTR)",
    ["experiment"],
    buckets=(1, 2, 5, 10, 20, 30, 60, 120, 300),
)
LAST_RECOVERY = Gauge(
    "chaos_last_recovery_seconds",
    "Recovery time of the most recent experiment of each type",
    ["experiment"],
)
# Drives the alert that wakes the AI webhook. Only set when a fault was really injected.
LAST_INJECTION = Gauge(
    "chaos_last_injection_timestamp_seconds",
    "Unix time a fault was last injected into a pod",
    ["experiment", "pod", "namespace"],
)
PAUSED = Gauge("chaos_paused", "1 while the kill switch file exists")
DRY_RUN = Gauge("chaos_dry_run", "1 when the engine only logs what it would do")


def record_injection(experiment, pod, namespace):
    LAST_INJECTION.labels(experiment, pod, namespace).set_to_current_time()


def record_result(experiment, result, recovery_seconds=None):
    EXPERIMENTS.labels(experiment, result).inc()
    if recovery_seconds is not None:
        RECOVERY.labels(experiment).observe(recovery_seconds)
        LAST_RECOVERY.labels(experiment).set(recovery_seconds)
