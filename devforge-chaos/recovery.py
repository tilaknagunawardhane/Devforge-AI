"""Measure how long the workload takes to become healthy again (MTTR)."""
import time

from targets import is_healthy


def wait_for_recovery(list_pods, baseline, started, timeout, poll=1.0, clock=time.monotonic, sleep=time.sleep):
    """Seconds from `started` until the workload is healthy again, or None if it did not recover in time."""
    deadline = started + timeout
    while True:
        if is_healthy(list_pods(), baseline):
            return clock() - started
        if clock() >= deadline:
            return None
        sleep(poll)
