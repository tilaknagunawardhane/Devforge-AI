import random

from conftest import FakeClock, FakeK8s, make_pod

import metrics
import report
from chaos import Controller
from config import Config
from recovery import wait_for_recovery


def make_controller(tmp_path, pods, **overrides):
    clock = FakeClock()
    cfg = Config(report_dir=str(tmp_path / "reports"), kill_switch_file=str(tmp_path / "pause"),
                 duration=3, recovery_timeout=60, **overrides)
    api = FakeK8s(pods)
    return Controller(cfg, api, clock=clock, sleep=clock.sleep, rng=random.Random(0)), api, clock


def counter(experiment, result):
    return metrics.EXPERIMENTS.labels(experiment, result)._value.get()


def test_wait_for_recovery_measures_seconds():
    clock = FakeClock()
    healthy = [make_pod("a"), make_pod("b")]
    sick = [make_pod("a"), make_pod("b", ready=False)]
    script = [sick, sick, sick, healthy]
    got = wait_for_recovery(lambda: script.pop(0) if len(script) > 1 else script[0], 2, 0.0, 60,
                            poll=2.0, clock=clock, sleep=clock.sleep)
    assert got == 6.0


def test_wait_for_recovery_times_out():
    clock = FakeClock()
    sick = [make_pod("a", ready=False)]
    assert wait_for_recovery(lambda: sick, 1, 0.0, 10, poll=1.0, clock=clock, sleep=clock.sleep) is None


def test_pod_kill_records_mttr(tmp_path):
    pods = [make_pod("web-1"), make_pod("web-2")]
    ctl, api, _ = make_controller(tmp_path, pods)
    # After the kill: one pod terminating, then a replacement not ready, then healthy
    api.list_script = [pods, pods,                                   # eligible + baseline
                       [make_pod("web-1", terminating=True), make_pod("web-2")],
                       [make_pod("web-2"), make_pod("web-3", ready=False)],
                       [make_pod("web-2"), make_pod("web-3")]]
    before = counter("pod-kill", "success")

    rec = ctl.run_experiment("pod-kill")

    assert rec.result == "success"
    assert rec.recovery_seconds == 2.0
    assert [c for c in api.calls if c[0] == "delete"] == [("delete", rec.targets[0])]
    assert counter("pod-kill", "success") == before + 1
    # history + markdown report were written
    assert len(report.load_records(ctl.cfg.report_dir)) == 1
    assert "pod-kill" in open(ctl.cfg.report_dir + "/chaos-report.md", encoding="utf-8").read()


def test_dry_run_changes_nothing(tmp_path):
    ctl, api, _ = make_controller(tmp_path, [make_pod("web-1")], dry_run=True)
    rec = ctl.run_experiment("pod-kill")
    assert rec.result == "dry-run"
    assert api.calls == []


def test_no_eligible_pods_is_skipped(tmp_path):
    ctl, api, _ = make_controller(tmp_path, [make_pod("chaos-engine")])
    assert ctl.run_experiment("pod-kill").result == "skipped"
    assert api.calls == []


def test_not_recovered(tmp_path):
    pods = [make_pod("web-1")]
    ctl, api, _ = make_controller(tmp_path, pods)
    api.list_script = [pods, pods, [make_pod("web-1", ready=False)]]   # never gets healthy again
    rec = ctl.run_experiment("pod-kill")
    assert rec.result == "not-recovered" and rec.recovery_seconds is None


def test_memory_stress_cleans_up_even_if_aborted(tmp_path):
    pods = [make_pod("web-1")]
    ctl, api, _ = make_controller(tmp_path, pods, memory_mb=16)
    open(ctl.cfg.kill_switch_file, "w").close()      # kill switch already flipped: hold() returns at once
    rec = ctl.run_experiment("memory-stress")
    scripts = [c[2] for c in api.calls if c[0] == "exec"]
    assert any("count=16" in s for s in scripts)
    assert any("rm -f /dev/shm/chaos-mem" in s for s in scripts)       # fault removed
    assert rec.result == "success"


def test_exec_failure_is_reported_not_raised(tmp_path):
    ctl, api, _ = make_controller(tmp_path, [make_pod("web-1")])
    api.exec_output = "sh: not found"                 # e.g. a distroless image
    rec = ctl.run_experiment("cpu-stress")
    assert rec.result == "failed" and "command failed" in rec.detail


def test_node_drain_refuses_single_node(tmp_path):
    ctl, api, _ = make_controller(tmp_path, [make_pod("web-1")])
    rec = ctl.run_experiment("node-drain")
    assert rec.result == "failed" and "only node" in rec.detail
    assert not any(c[0] in ("cordon", "evict") for c in api.calls)


def test_node_drain_always_uncordons(tmp_path):
    pods = [make_pod("web-1"), make_pod("web-2")]
    ctl, api, _ = make_controller(tmp_path, pods, allow_single_node_drain=True)
    rec = ctl.run_experiment("node-drain")
    kinds = [c[0] for c in api.calls]
    assert kinds[0] == "cordon" and kinds[-1] == "uncordon"
    assert kinds.count("evict") == 2
    assert api.cordoned == set()
    assert rec.result in ("success", "not-recovered")


def test_stale_cordon_cleaned_on_start(tmp_path):
    ctl, api, _ = make_controller(tmp_path, [])
    api.cordoned.add("node-1")
    ctl.cleanup_stale()
    assert api.cordoned == set()


def test_kill_switch(tmp_path):
    ctl, _, _ = make_controller(tmp_path, [])
    assert not ctl.paused()
    open(ctl.cfg.kill_switch_file, "w").close()
    assert ctl.paused() and ctl.aborted()
