import os
import sys
from types import SimpleNamespace

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))


def make_pod(name, ns="default", ready=True, phase="Running", terminating=False, labels=None, node="node-1"):
    return SimpleNamespace(
        metadata=SimpleNamespace(
            name=name, namespace=ns, labels=labels or {},
            deletion_timestamp="2026-01-01T00:00:00Z" if terminating else None,
        ),
        spec=SimpleNamespace(node_name=node),
        status=SimpleNamespace(
            phase=phase,
            conditions=[SimpleNamespace(type="Ready", status="True" if ready else "False")],
        ),
    )


class FakeClock:
    """Deterministic time: sleep() advances the clock, so tests never wait."""

    def __init__(self):
        self.now = 0.0

    def __call__(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


class FakeK8s:
    """Records every mutating call; pods can be scripted per list_pods() call."""

    def __init__(self, pods=None, nodes=("node-1",)):
        self.pods = pods or []
        self.nodes = list(nodes)
        self.calls = []
        self.exec_output = "CHAOS_OK"
        self.list_script = []  # optional: successive pod lists returned by list_pods
        self.cordoned = set()

    def list_pods(self, namespace, selector=""):
        if self.list_script:
            return self.list_script.pop(0) if len(self.list_script) > 1 else self.list_script[0]
        return self.pods

    def delete_pod(self, ns, name):
        self.calls.append(("delete", name))

    def evict_pod(self, ns, name):
        self.calls.append(("evict", name))

    def exec(self, ns, pod, command):
        self.calls.append(("exec", pod, command[-1]))
        return self.exec_output

    def list_nodes(self):
        return [SimpleNamespace(metadata=SimpleNamespace(name=n, annotations={})) for n in self.nodes]

    def set_cordon(self, node, cordoned):
        self.calls.append(("cordon" if cordoned else "uncordon", node))
        (self.cordoned.add if cordoned else self.cordoned.discard)(node)

    def nodes_cordoned_by_chaos(self):
        return sorted(self.cordoned)
