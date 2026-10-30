import random

import pytest

from config import Config, ConfigError, load_config
from targets import baseline_ready, eligible_pods, is_healthy, pick_targets
from conftest import make_pod


def test_defaults_are_safe():
    cfg = load_config({})
    assert cfg.experiments == ("pod-kill",)
    assert cfg.namespace == "default"
    assert cfg.max_targets == 1
    assert cfg.dry_run is False


def test_env_parsing():
    cfg = load_config({
        "TARGET_NAMESPACE": "shop", "TARGET_SELECTOR": "app=cart",
        "CHAOS_EXPERIMENTS": "pod-kill, cpu-stress", "CHAOS_DRY_RUN": "TRUE",
        "CHAOS_INTERVAL_SECONDS": "10", "CHAOS_MAX_TARGETS": "2",
    })
    assert cfg.experiments == ("pod-kill", "cpu-stress")
    assert cfg.selector == "app=cart" and cfg.dry_run and cfg.interval == 10 and cfg.max_targets == 2


@pytest.mark.parametrize("env", [
    {"CHAOS_EXPERIMENTS": "pod-kill,explode"},
    {"CHAOS_INTERVAL_SECONDS": "abc"},
    {"CHAOS_INTERVAL_SECONDS": "0"},
    {"CHAOS_MEMORY_MB": "500"},
    {"TARGET_NAMESPACE": "kube-system"},                       # deny list
    {"TARGET_NAMESPACE": "shop", "CHAOS_ALLOW_NAMESPACES": "other"},  # not in allow list
])
def test_invalid_config_rejected(env):
    with pytest.raises(ConfigError):
        load_config(env)


def test_eligible_pods_filters():
    cfg = Config(namespace="default")
    pods = [
        make_pod("good-1"),
        make_pod("chaos-engine"),                       # never attack ourselves
        make_pod("pending-1", phase="Pending"),
        make_pod("dying-1", terminating=True),
        make_pod("opted-out", labels={"chaos.devforge/exclude": "true"}),
        make_pod("other-ns", ns="kube-system"),
    ]
    assert [p.metadata.name for p in eligible_pods(pods, cfg)] == ["good-1"]


def test_blast_radius_caps_targets():
    cfg = Config(max_targets=2)
    pods = [make_pod(f"p{i}") for i in range(5)]
    assert len(pick_targets(pods, cfg, random.Random(1))) == 2
    assert len(pick_targets(pods[:1], cfg, random.Random(1))) == 1


def test_health():
    ready = [make_pod("a"), make_pod("b")]
    assert baseline_ready(ready) == 2
    assert is_healthy(ready, 2)
    assert not is_healthy(ready + [make_pod("c", terminating=True)], 2)   # something still terminating
    assert not is_healthy(ready + [make_pod("c", ready=False)], 2)        # a pod not ready
    assert not is_healthy([make_pod("a")], 2)                             # fewer pods than before
    assert is_healthy([make_pod("a"), make_pod("chaos-engine", ready=False)], 1)  # chaos pod ignored
