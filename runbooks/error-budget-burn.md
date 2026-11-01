# Error budget burn

## Symptoms
An ErrorBudgetFastBurn alert means the service is failing enough requests to use up its monthly error budget in about two days (page). An ErrorBudgetSlowBurn alert means the budget would be gone in about five days (ticket).

## Diagnose
Check what changed first: a recent deploy, config change or dependency outage. Look at the 5xx rate by route in the service's Grafana dashboard and compare the p95 latency. If errors started right after a rollout, suspect the new version.

## Fix
For a bad release, roll back with `kubectl rollout undo deployment/<name>`. For a failing dependency, enable a fallback or shed load, and contact the dependency owner. For a traffic spike, scale out the Deployment or raise the HPA maximum.

## Follow up
Record how much budget was spent. If the budget is exhausted, freeze risky releases until reliability recovers, and add a test or alert that would have caught the cause earlier.
