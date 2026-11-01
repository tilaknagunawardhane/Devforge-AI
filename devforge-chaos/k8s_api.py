"""Thin wrapper over the Kubernetes client, so experiments can be tested with a fake."""
import os

from kubernetes import client, config
from kubernetes.stream import stream

CORDON_ANNOTATION = "chaos.devforge/cordoned-by"


def load_k8s_config():
    """In-cluster when running as a pod, kubeconfig when running in Docker Compose."""
    try:
        config.load_incluster_config()
    except config.ConfigException:
        config.load_kube_config()
        # kubeconfig points at 127.0.0.1; allow overriding to the compose service name
        host = os.getenv("KUBE_API_HOST")
        if host:
            cfg = client.Configuration.get_default_copy()
            cfg.host = host
            client.Configuration.set_default(cfg)


class K8s:
    def __init__(self):
        load_k8s_config()
        self.core = client.CoreV1Api()

    def list_pods(self, namespace, selector=""):
        return self.core.list_namespaced_pod(namespace, label_selector=selector or None).items

    def delete_pod(self, namespace, name):
        self.core.delete_namespaced_pod(name=name, namespace=namespace)

    def evict_pod(self, namespace, name):
        body = client.V1Eviction(metadata=client.V1ObjectMeta(name=name, namespace=namespace))
        self.core.create_namespaced_pod_eviction(name=name, namespace=namespace, body=body)

    def exec(self, namespace, pod, command):
        """Run a command in the pod's first container and return its output."""
        return stream(
            self.core.connect_get_namespaced_pod_exec,
            pod, namespace, command=command,
            stderr=True, stdin=False, stdout=True, tty=False,
        )

    def list_nodes(self):
        return self.core.list_node().items

    def set_cordon(self, node, cordoned):
        annotations = {CORDON_ANNOTATION: "true" if cordoned else None}
        self.core.patch_node(node, {"spec": {"unschedulable": cordoned}, "metadata": {"annotations": annotations}})

    def nodes_cordoned_by_chaos(self):
        return [
            n.metadata.name for n in self.list_nodes()
            if (n.metadata.annotations or {}).get(CORDON_ANNOTATION) == "true"
        ]
