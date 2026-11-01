import itertools
import logging
import os
import random
import signal
import threading
import time

from prometheus_client import start_http_server

import metrics
import report
from config import ConfigError, load_config
from experiments import EXPERIMENTS, ExperimentError
from recovery import wait_for_recovery
from targets import baseline_ready, eligible_pods, pick_targets

log = logging.getLogger("chaos")


class Controller:
    """Runs experiments one at a time, honouring the kill switch and shutdown signals."""

    def __init__(self, cfg, api, clock=time.monotonic, sleep=time.sleep, rng=None):
        self.cfg = cfg
        self.api = api
        self.clock = clock
        self.sleep = sleep
        self.rng = rng
        self.stop = threading.Event()

    # ---- kill switch -------------------------------------------------------------
    def paused(self):
        return os.path.exists(self.cfg.kill_switch_file)

    def aborted(self):
        return self.stop.is_set() or self.paused()

    def hold(self, seconds):
        """Wait up to `seconds`, returning early if the kill switch is flipped or we are shutting down."""
        end = self.clock() + seconds
        while self.clock() < end and not self.aborted():
            self.sleep(min(1.0, max(0.0, end - self.clock())))

    # ---- one experiment ----------------------------------------------------------
    def run_experiment(self, name):
        cfg = self.cfg
        record = report.Record(experiment=name, namespace=cfg.namespace)

        pods = eligible_pods(self.api.list_pods(cfg.namespace, cfg.selector), cfg)
        if not pods:
            record.result, record.detail = "skipped", "no eligible pods"
            return self._finish(record)
        targets = pick_targets(pods, cfg, self.rng or random)
        record.targets = [p.metadata.name for p in targets]

        if cfg.dry_run:
            record.result, record.detail = "dry-run", f"would run {name} on {', '.join(record.targets)}"
            return self._finish(record)

        baseline = baseline_ready(self.api.list_pods(cfg.namespace, cfg.selector))
        try:
            outcome = EXPERIMENTS[name](self.api, cfg, self, targets)
        except ExperimentError as e:
            record.result, record.detail = "failed", str(e)
            return self._finish(record)
        except Exception as e:  # keep the engine alive on API errors
            record.result, record.detail = "failed", f"{type(e).__name__}: {e}"
            return self._finish(record)

        for pod in outcome.injected:
            metrics.record_injection(name, pod.metadata.name, pod.metadata.namespace)
        record.targets, record.detail = outcome.targets or record.targets, outcome.detail

        seconds = wait_for_recovery(
            lambda: self.api.list_pods(cfg.namespace, cfg.selector),
            baseline, outcome.recovery_from, cfg.recovery_timeout,
            clock=self.clock, sleep=self.sleep,
        )
        if seconds is None:
            record.result = "not-recovered"
            record.detail += f"; not healthy after {cfg.recovery_timeout}s"
        else:
            record.recovery_seconds = round(seconds, 2)
        return self._finish(record)

    def _finish(self, record):
        metrics.record_result(record.experiment, record.result, record.recovery_seconds)
        report.append_record(self.cfg.report_dir, record)
        report.write_report(self.cfg.report_dir)
        rec = "" if record.recovery_seconds is None else f" recovery={record.recovery_seconds}s"
        log.info("experiment=%s result=%s targets=%s%s %s", record.experiment, record.result,
                 ",".join(record.targets) or "-", rec, record.detail)
        return record

    # ---- main loop -----------------------------------------------------------------
    def cleanup_stale(self):
        """If a previous run died mid-experiment, undo what it left behind."""
        try:
            for node in self.api.nodes_cordoned_by_chaos():
                log.warning("uncordoning node %s left cordoned by a previous run", node)
                self.api.set_cordon(node, False)
        except Exception as e:
            log.warning("stale cleanup skipped: %s", e)

    def wait_interval(self):
        end = self.clock() + self.cfg.interval
        while self.clock() < end and not self.stop.is_set():
            metrics.PAUSED.set(1 if self.paused() else 0)
            self.sleep(1.0)

    def run_forever(self):
        self.cleanup_stale()
        for name in itertools.cycle(self.cfg.experiments):
            if self.stop.is_set():
                break
            metrics.PAUSED.set(1 if self.paused() else 0)
            if self.paused():
                log.info("kill switch active (%s exists); not running experiments", self.cfg.kill_switch_file)
                self.sleep(5)
                continue
            self.run_experiment(name)
            self.wait_interval()
        log.info("chaos engine stopped")


def main():
    logging.basicConfig(level=logging.INFO, format='{"time":"%(asctime)s","level":"%(levelname)s","msg":"%(message)s"}')
    try:
        cfg = load_config()
    except ConfigError as e:
        log.error("invalid configuration: %s", e)
        raise SystemExit(2) from e

    from k8s_api import K8s

    ctl = Controller(cfg, K8s())
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: ctl.stop.set())

    metrics.DRY_RUN.set(1 if cfg.dry_run else 0)
    start_http_server(cfg.metrics_port)
    log.info(
        "DevForge Chaos Engine started: namespace=%s selector=%r experiments=%s interval=%ss dry_run=%s kill_switch=%s",
        cfg.namespace, cfg.selector, ",".join(cfg.experiments), cfg.interval, cfg.dry_run, cfg.kill_switch_file,
    )
    ctl.run_forever()


if __name__ == '__main__':
    main()
