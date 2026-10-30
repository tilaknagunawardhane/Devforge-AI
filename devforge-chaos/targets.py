"""Which pods may be attacked, and how to tell whether the workload is healthy."""
import random


def is_terminating(pod):
    return pod.metadata.deletion_timestamp is not None


def is_ready(pod):
    if pod.status.phase != "Running":
        return False
    return any(c.type == "Ready" and c.status == "True" for c in (pod.status.conditions or []))


def is_workload_pod(pod):
    """The chaos engine never counts itself, in health checks or as a target."""
    return "chaos" not in pod.metadata.name


def eligible_pods(pods, cfg):
    """Pods that may be targeted: right namespace, running, not terminating, not opted out."""
    allowed = cfg.allowed_namespaces()
    out = []
    for p in pods:
        ns = p.metadata.namespace
        if ns not in allowed or ns in cfg.deny_namespaces:
            continue
        if not is_workload_pod(p) or p.status.phase != "Running" or is_terminating(p):
            continue
        if (p.metadata.labels or {}).get(cfg.exclude_label) == "true":
            continue
        out.append(p)
    return out


def pick_targets(pods, cfg, rng=random):
    """Random targets, capped by the blast radius (max_targets)."""
    return rng.sample(pods, min(cfg.max_targets, len(pods)))


def baseline_ready(pods):
    return sum(1 for p in pods if is_workload_pod(p) and is_ready(p) and not is_terminating(p))


def is_healthy(pods, baseline):
    """Healthy = nothing terminating, every pod ready, and at least as many ready as before the fault."""
    pods = [p for p in pods if is_workload_pod(p)]
    if any(is_terminating(p) for p in pods):
        return False
    return all(is_ready(p) for p in pods) and sum(1 for p in pods if is_ready(p)) >= baseline
