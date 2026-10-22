import time
import random
from kubernetes import client, config

def main():
    print("🌪️ DevForge Chaos Engine started...")
    # Authenticate securely from inside the cluster
    config.load_incluster_config()
    v1 = client.CoreV1Api()

    while True:
        # We target the 'default' namespace where ArgoCD deployed our apps
        pods = v1.list_namespaced_pod("default")
        
        # Filter out the chaos engine itself and any pending pods
        target_pods = [p for p in pods.items if "chaos" not in p.metadata.name and p.status.phase == "Running"]
        
        if not target_pods:
            print("No active target pods found. Waiting 30 seconds...")
            time.sleep(30)
            continue

        # Pick a random victim pod
        victim = random.choice(target_pods)
        pod_name = victim.metadata.name
        
        print(f"💀 Chaos Event: Terminating pod '{pod_name}'...")
        v1.delete_namespaced_pod(name=pod_name, namespace="default")
        print(f"✅ Terminated '{pod_name}'. Watch ArgoCD heal it instantly.")
        
        # Wait 60 seconds before launching the next attack
        time.sleep(60)

if __name__ == '__main__':
    main()