import os
import time
import random
from kubernetes import client, config
from prometheus_client import Counter, Gauge, start_http_server

NAMESPACE = os.getenv("TARGET_NAMESPACE", "default")
INTERVAL = int(os.getenv("CHAOS_INTERVAL_SECONDS", "60"))
METRICS_PORT = int(os.getenv("METRICS_PORT", "8000"))

POD_KILLS = Counter(
    "chaos_pod_kills_total",
    "Pods terminated by the chaos engine",
    ["pod", "namespace"],
)


LAST_KILL = Gauge(
    "chaos_pod_last_kill_timestamp_seconds",
    "Unix time of the last kill of a pod",
    ["pod", "namespace"],
)


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


def main():
    print("🌪️ DevForge Chaos Engine started...")
    load_k8s_config()
    v1 = client.CoreV1Api()
    start_http_server(METRICS_PORT)
    print(f"📈 Metrics on :{METRICS_PORT}, targeting namespace '{NAMESPACE}' every {INTERVAL}s")

    while True:
        pods = v1.list_namespaced_pod(NAMESPACE)

        # Filter out the chaos engine itself and any pods that are not running
        target_pods = [p for p in pods.items if "chaos" not in p.metadata.name and p.status.phase == "Running"]

        if not target_pods:
            print("No active target pods found. Waiting 30 seconds...")
            time.sleep(30)
            continue

        victim = random.choice(target_pods)
        pod_name = victim.metadata.name

        print(f"💀 Chaos Event: Terminating pod '{pod_name}'...")
        v1.delete_namespaced_pod(name=pod_name, namespace=NAMESPACE)
        POD_KILLS.labels(pod=pod_name, namespace=NAMESPACE).inc()
        LAST_KILL.labels(pod=pod_name, namespace=NAMESPACE).set_to_current_time()
        print(f"✅ Terminated '{pod_name}'. Kubernetes should heal it.")

        time.sleep(INTERVAL)


if __name__ == '__main__':
    main()
