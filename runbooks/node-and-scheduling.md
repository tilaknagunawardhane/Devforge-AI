# Node problems and pods stuck Pending

## Symptoms
Pods stay Pending, events show FailedScheduling, or a node is NotReady or SchedulingDisabled (cordoned).

## Diagnose
Run `kubectl get nodes` and `kubectl describe node <node>`. A cordoned node shows SchedulingDisabled; new pods cannot land on it. FailedScheduling events list the reason, such as insufficient CPU or memory, or taints with no matching toleration.

## Fix
If the node was cordoned by a drain or a chaos node-drain experiment, uncordon it with `kubectl uncordon <node>`. If resources are short, add nodes or lower resource requests. For a NotReady node, check the kubelet and the node's disk and memory pressure conditions.

## Notes
On a single-node cluster, draining the node is a full outage until it is uncordoned. The chaos engine always uncordons after its duration and cleans up stale cordons when it restarts.
