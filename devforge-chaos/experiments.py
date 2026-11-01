"""The fault injections. Each takes (api, cfg, ctl, targets) and returns an Outcome.

`ctl` supplies hold(seconds), which waits while watching for the kill switch / shutdown, and
clock(). Every fault is removed in a `finally` block so a crash or kill switch never leaves
a cordoned node or a running stress process behind.
"""
import logging
from dataclasses import dataclass, field

from targets import eligible_pods

log = logging.getLogger("chaos")


class ExperimentError(Exception):
    pass


@dataclass
class Outcome:
    targets: list
    recovery_from: float  # monotonic time at which the recovery stopwatch starts
    detail: str = ""
    injected: list = field(default_factory=list)  # pods a fault was really applied to


def _names(targets):
    return [p.metadata.name for p in targets]


def pod_kill(api, cfg, ctl, targets):
    for p in targets:
        api.delete_pod(p.metadata.namespace, p.metadata.name)
    return Outcome(_names(targets), ctl.clock(), "deleted pod(s)", injected=targets)


def _exec_ok(api, pod, script):
    out = api.exec(pod.metadata.namespace, pod.metadata.name, ["sh", "-c", script + "; echo CHAOS_OK"])
    if "CHAOS_OK" not in out:
        raise ExperimentError(f"command failed in {pod.metadata.name}: {out.strip()[:200]}")


def cpu_stress(api, cfg, ctl, targets):
    """Burn CPU with busy loops (`timeout` makes them stop on their own). Needs `sh` in the container."""
    burn = "timeout {d} sh -c 'while :; do :; done' >/dev/null 2>&1 &".format(d=cfg.duration)
    started = []
    try:
        for p in targets:
            _exec_ok(api, p, "; ".join([f"({burn})"] * cfg.cpu_workers))
            started.append(p)
        ctl.hold(cfg.duration)
    finally:
        for p in started:
            try:
                _exec_ok(api, p, "killall timeout 2>/dev/null")
            except Exception as e:  # the loops stop by themselves after `duration`, but say so if cleanup failed
                log.warning("could not stop the CPU stress in %s: %s", p.metadata.name, e)
    detail = f"{cfg.cpu_workers} busy loop(s) for {cfg.duration}s"
    return Outcome(_names(targets), ctl.clock(), detail, injected=targets)


def memory_stress(api, cfg, ctl, targets):
    """Fill /dev/shm (counted against the container's memory) for the configured duration."""
    path = "/dev/shm/chaos-mem"
    started = []
    try:
        for p in targets:
            _exec_ok(api, p, f"dd if=/dev/zero of={path} bs=1M count={cfg.memory_mb} 2>/dev/null")
            started.append(p)
        ctl.hold(cfg.duration)
    finally:
        for p in started:
            try:
                _exec_ok(api, p, f"rm -f {path}")
            except Exception as e:
                log.warning("could not remove %s from %s (memory stays allocated): %s", path, p.metadata.name, e)
    return Outcome(_names(targets), ctl.clock(), f"{cfg.memory_mb}MiB held for {cfg.duration}s", injected=targets)


def node_drain(api, cfg, ctl, targets):
    """Cordon the node(s) the targets run on, evict the workload's pods there, then uncordon.

    Only pods of the target namespace/selector are evicted (never kube-system), but cordoning blocks
    new pods on that node, so on a single-node cluster this is a full outage for `duration` seconds.
    """
    nodes = sorted({p.spec.node_name for p in targets if p.spec.node_name})
    if not nodes:
        raise ExperimentError("targets are not scheduled on a node")
    if len(api.list_nodes()) < 2 and not cfg.allow_single_node_drain:
        raise ExperimentError("refusing to drain the only node; set CHAOS_ALLOW_SINGLE_NODE_DRAIN=true to allow it")

    start = ctl.clock()
    cordoned, evicted = [], []
    try:
        for n in nodes:
            api.set_cordon(n, True)
            cordoned.append(n)
        for p in eligible_pods(api.list_pods(cfg.namespace, cfg.selector), cfg):
            if p.spec.node_name in nodes:
                api.evict_pod(p.metadata.namespace, p.metadata.name)
                evicted.append(p)
        ctl.hold(cfg.duration)
    finally:
        for n in cordoned:
            try:
                api.set_cordon(n, False)
            except Exception as e:
                # The node keeps the cordon annotation, so the next start of the engine retries (cleanup_stale).
                log.error("COULD NOT UNCORDON node %s: %s", n, e)
    # The outage counts from the moment of eviction, not from the uncordon.
    return Outcome(_names(evicted), start, f"drained {', '.join(nodes)} for {cfg.duration}s", injected=evicted)


EXPERIMENTS = {
    "pod-kill": pod_kill,
    "cpu-stress": cpu_stress,
    "memory-stress": memory_stress,
    "node-drain": node_drain,
}
