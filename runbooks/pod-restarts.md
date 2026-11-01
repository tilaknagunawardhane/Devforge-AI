# Pod restarts and CrashLoopBackOff

## Symptoms
A pod restarts repeatedly, shows CrashLoopBackOff, or is killed and replaced. Kubernetes events show Killing, BackOff or Unhealthy. The Deployment's replica count briefly drops below the desired number.

## Diagnose
Read the previous container's logs with `kubectl logs <pod> --previous`. Check `kubectl describe pod <pod>` for the last state, exit code and reason. Exit code 137 means the container was killed (OOMKilled or SIGKILL); exit code 1 or 2 usually means an application error on startup.

## Fix
If the reason is OOMKilled, raise the memory limit in the Deployment and check for leaks. If the application crashes on startup, roll back to the previous image with `kubectl rollout undo deployment/<name>`. If the pod was deleted on purpose (a chaos experiment or a node drain), confirm the replacement becomes Ready and no action is needed.

## Verify
`kubectl rollout status deployment/<name>` completes, restart count stops increasing, and the readiness probe passes.
