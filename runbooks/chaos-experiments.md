# Chaos experiments

## Recognising a chaos fault
Alerts named ChaosFaultInjected carry the experiment name (pod-kill, cpu-stress, memory-stress, node-drain) and the affected pod. These are deliberate faults from the DevForge chaos engine, not real outages. Check the chaos report in `reports/chaos-report.md` to confirm the experiment and its recovery time.

## Expected recovery
A pod-kill should recover in a few seconds as the Deployment creates a replacement. A node-drain keeps the workload down for the configured duration plus rescheduling time. CPU and memory stress should not restart the pod unless the container has a low memory limit.

## When recovery is too slow
If recovery takes much longer than the baseline, check that the image is already pulled on the node, the readiness probe is not too strict, and the cluster has spare capacity. A ChaosNotRecovering alert means the workload never became healthy again: stop the engine with the kill switch (`make chaos-pause`) and investigate before resuming.

## Stopping the experiments
Run `make chaos-pause` to stop new experiments immediately and remove active faults. Run `make chaos-resume` to continue.
