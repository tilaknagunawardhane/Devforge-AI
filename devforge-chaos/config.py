"""Chaos engine configuration, read from environment variables and validated up front."""
import os
from dataclasses import dataclass

KNOWN_EXPERIMENTS = ("pod-kill", "cpu-stress", "memory-stress", "node-drain")
DEFAULT_DENY_NAMESPACES = ("kube-system", "kube-public", "kube-node-lease")

# /dev/shm is 64Mi by default in a pod, which caps how much memory the stress test can allocate.
MAX_MEMORY_MB = 60


class ConfigError(ValueError):
    pass


def _csv(value):
    return tuple(x.strip() for x in value.split(",") if x.strip())


def _bool(value):
    return value.strip().lower() in ("1", "true", "yes", "on")


@dataclass(frozen=True)
class Config:
    namespace: str = "default"
    selector: str = ""  # Kubernetes label selector, e.g. "app=demo-app"
    experiments: tuple = ("pod-kill",)
    interval: int = 60  # seconds between experiments
    duration: int = 30  # seconds a stress / drain fault is held
    max_targets: int = 1  # blast radius: pods affected per experiment
    memory_mb: int = 32
    cpu_workers: int = 1
    recovery_timeout: int = 300
    allow_namespaces: tuple = ()  # empty means only `namespace`
    deny_namespaces: tuple = DEFAULT_DENY_NAMESPACES
    exclude_label: str = "chaos.devforge/exclude"  # pods with this label set to "true" are never touched
    dry_run: bool = False
    kill_switch_file: str = "/var/run/chaos/pause"
    report_dir: str = "/reports"
    metrics_port: int = 8000
    allow_single_node_drain: bool = False

    def allowed_namespaces(self):
        return self.allow_namespaces or (self.namespace,)


def load_config(env=None):
    env = os.environ if env is None else env

    def num(key, default):
        raw = env.get(key)
        if raw in (None, ""):
            return default
        try:
            return int(raw)
        except ValueError:
            raise ConfigError(f"{key} must be a whole number, got {raw!r}")

    cfg = Config(
        namespace=env.get("TARGET_NAMESPACE", "default"),
        selector=env.get("TARGET_SELECTOR", ""),
        experiments=_csv(env.get("CHAOS_EXPERIMENTS", "pod-kill")) or ("pod-kill",),
        interval=num("CHAOS_INTERVAL_SECONDS", 60),
        duration=num("CHAOS_DURATION_SECONDS", 30),
        max_targets=num("CHAOS_MAX_TARGETS", 1),
        memory_mb=num("CHAOS_MEMORY_MB", 32),
        cpu_workers=num("CHAOS_CPU_WORKERS", 1),
        recovery_timeout=num("CHAOS_RECOVERY_TIMEOUT_SECONDS", 300),
        allow_namespaces=_csv(env.get("CHAOS_ALLOW_NAMESPACES", "")),
        deny_namespaces=_csv(env.get("CHAOS_DENY_NAMESPACES", "")) or DEFAULT_DENY_NAMESPACES,
        exclude_label=env.get("CHAOS_EXCLUDE_LABEL", "chaos.devforge/exclude"),
        dry_run=_bool(env.get("CHAOS_DRY_RUN", "false")),
        kill_switch_file=env.get("CHAOS_KILL_SWITCH_FILE", "/var/run/chaos/pause"),
        report_dir=env.get("CHAOS_REPORT_DIR", "/reports"),
        metrics_port=num("METRICS_PORT", 8000),
        allow_single_node_drain=_bool(env.get("CHAOS_ALLOW_SINGLE_NODE_DRAIN", "false")),
    )
    validate(cfg)
    return cfg


def validate(cfg):
    unknown = [e for e in cfg.experiments if e not in KNOWN_EXPERIMENTS]
    if unknown:
        raise ConfigError(f"unknown experiment(s) {unknown}; choose from {list(KNOWN_EXPERIMENTS)}")
    if cfg.interval < 1 or cfg.duration < 1 or cfg.max_targets < 1 or cfg.cpu_workers < 1:
        raise ConfigError("interval, duration, max targets and cpu workers must be at least 1")
    if not 1 <= cfg.memory_mb <= MAX_MEMORY_MB:
        raise ConfigError(f"CHAOS_MEMORY_MB must be between 1 and {MAX_MEMORY_MB}")
    for ns in cfg.allowed_namespaces():
        if ns in cfg.deny_namespaces:
            raise ConfigError(f"namespace {ns!r} is on the deny list {list(cfg.deny_namespaces)}")
    if cfg.namespace not in cfg.allowed_namespaces():
        raise ConfigError(f"target namespace {cfg.namespace!r} is not in the allow list {list(cfg.allow_namespaces)}")
