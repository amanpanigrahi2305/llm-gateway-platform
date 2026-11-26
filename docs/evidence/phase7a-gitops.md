# Phase 7a evidence: GitOps with Argo CD and Argo Rollouts on kind

Raw output of `docs/evidence/phase7a-gitops.sh`. Setup: `make kind-up` and
`make gitops-up` (Argo CD v3.6.3, Argo Rollouts v1.10.2, Gitea 28.4.0 in the
cluster as the git server). Every release below is a commit pushed to the
in-cluster git server by a test identity (`release-bot`); nothing was pushed to
GitHub. Only the mock providers are used. Times come from `/proc/uptime`
(the WSL wall clock jumps).

## Results (final run, 2026-11-26 04:02 UTC)

k6 sent a fixed 100 requests/s through the gateway NodePort during each release.

| Step | Result |
|---|---|
| push -> Argo CD sync | about 4 s (webhook from the git server) |
| good release (gitops-1 -> gitops-2) | 10% -> check -> 50% -> check -> 100% in **55.9 s**; **0 failed** of 5,819 requests; both checks 100/100 OK |
| bad release (gitops-3, alias -> flaky upstream) | check: 84 of 100 failed -> **aborted 21.4 s after the push**, canary pod gone at 25.3 s; users saw **29 errors in 3,920 requests (0.74%)** |
| after the abort | **10 of 10** stable pods answered 20/20 |
| fix forward (`git revert`) | Healthy **7.5 s** after the push; 0 failed of 2,032 |
| drift | deleted NetworkPolicy back in 1.2 s; PDB loosened to 5 back to 1 in 2.2 s; Rollout scaled to 3 back to 10 in 7.2 s; hand edit of the ConfigMap rejected (immutable) |

## Before the fixes: what the first run showed (2026-11-26 03:40 UTC)

The first run (commit 72c211d) found two problems.

**1. The push webhook did not work.** Argo CD synced 100 to 238 s after each
push, which is its 3-minute polling, not the webhook. Gitea's log:

```
Post "https://argocd-server.argocd.svc.cluster.local/api/webhook": dial tcp 10.96.241.255:443: denied by egress policy: argocd-server.argocd.svc.cluster.local(10.96.241.255) needs an explicit IP allow entry (non-public address)
```

Gitea 28 needs private addresses allowed by IP range under
`[security].ALLOWED_HOST_LIST`; the old `[webhook]` key with a host name was
ignored. After setting `10.96.0.0/12` (kind's Service range) and removing the
stale key from `app.ini` on the volume: push -> sync in 2.0 s (was 84.6 s
on the try before the stale key was removed).

**2. A bad release's config leaked into a stable pod.** During the revert step
of the first run, k6 (closed model, 20 users) counted 171,279 failures,
almost all 503, while every ready pod ran the stable image:

```
## D. Fix forward: git revert of the bad commit
pushed 017c1cc (Revert "release: gateway gitops-3 (bad config)")
t=3.0s argocd=15e970e Synced Degraded | rollout=Degraded step=0 abort=true | ready pods: gitops-2=10 
t=203.0s argocd=017c1cc OutOfSync Degraded | rollout=Degraded step=0 abort=true | ready pods: gitops-2=10 
healthy again after 204.4 s
k6 during this release: requests=205010 failed=171279 p95=105.32ms; outcomes: http_502=24 http_503=171255 ok=33731
```

(The count is inflated: a failure returns in about 1 ms, so a user stuck on a
bad pod sends many requests. The final run uses a fixed request rate.)

Reproduction on the same cluster, before the fix: a bad release, then 20
requests straight to each pod after the abort, and again after the revert:

```
current release file:   tag: gitops-2
== bad release (gitops-3 + alias fast -> mock-flaky)
aborted 17.1 s after the push (03:57:52 UTC)
== every pod after the abort (all run the stable image gitops-2):
  llm-gateway-5d8db46b68-6vgq2 gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-cclg2 gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-fc82q gitops-2 created=2026-11-26T03:57:52Z ok=13 bad=7
  llm-gateway-5d8db46b68-klzzn gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-kpxzn gitops-2 created=2026-11-26T03:45:03Z ok=20 bad=0
  llm-gateway-5d8db46b68-pphvx gitops-2 created=2026-11-26T03:45:34Z ok=20 bad=0
  llm-gateway-5d8db46b68-tx9wg gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-vw2rj gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-wxwnj gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-xbgc4 gitops-2 created=2026-11-26T03:45:03Z ok=20 bad=0
== revert
Healthy 4.7 s after the push
== every pod after the revert:
  llm-gateway-5d8db46b68-6vgq2 gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-cclg2 gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-fc82q gitops-2 created=2026-11-26T03:57:52Z ok=15 bad=5
  llm-gateway-5d8db46b68-klzzn gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-kpxzn gitops-2 created=2026-11-26T03:45:03Z ok=20 bad=0
  llm-gateway-5d8db46b68-pphvx gitops-2 created=2026-11-26T03:45:34Z ok=20 bad=0
  llm-gateway-5d8db46b68-tx9wg gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-vw2rj gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-wxwnj gitops-2 created=2026-11-26T03:51:53Z ok=20 bad=0
  llm-gateway-5d8db46b68-xbgc4 gitops-2 created=2026-11-26T03:45:03Z ok=20 bad=0
== the ConfigMap now (alias fast):
aliases:
  fast:
  - mock-a/mock-1
  - mock-b/mock-1
```

Pod `fc82q` was created at the moment of the abort (03:57:52), when Argo
Rollouts scaled the stable ReplicaSet back from 9 to 10. It runs the stable
image, but it read the **bad** config: every revision shared one ConfigMap,
and the bad release had changed it. The revert fixed the ConfigMap, but not
the pod that had already read it.

Fix (chart): one immutable ConfigMap per config version, named by a hash of the
config, so each ReplicaSet keeps the config it was released with. Old versions
are kept (`Prune=false`, `IgnoreExtraneous`, `helm.sh/resource-policy: keep`).
The final run above shows 10 of 10 pods healthy after the abort.

## Final run: raw output

```
started: 2026-11-26 04:02 UTC snapshot 101f1f5
## A. Starting point
Argo CD: quay.io/argoproj/argocd:v3.6.3
Argo Rollouts: quay.io/argoproj/argo-rollouts:v1.10.2
argocd: 101f1f56a5dc4a473309fd88935a9f7951671f0d Synced Healthy | rollout: Healthy step=4 abort= | ready pods: gitops-1=10 
steps: [{"setWeight":10},{"analysis":{"templates":[{"templateName":"llm-gateway-canary-check"}]}},{"setWeight":50},{"analysis":{"templates":[{"templateName":"llm-gateway-canary-check"}]}}]

## B. Good release: image gitops-1 -> gitops-2
images gitops-2 built and loaded into kind
pushed 043dd0b
t=3.1s argocd=101f1f5 Synced Healthy | rollout=Healthy step=4 abort= | ready pods: gitops-1=10 
t=7.8s argocd=043dd0b Synced Progressing | rollout=Progressing step=0 abort= | ready pods: gitops-1=10 
t=11.2s argocd=043dd0b Synced Progressing | rollout=Progressing step=1 abort= | ready pods: gitops-1=10 gitops-2=1 
t=14.6s argocd=043dd0b Synced Progressing | rollout=Progressing step=1 abort= | ready pods: gitops-1=9 gitops-2=1 
t=28.3s argocd=043dd0b Synced Progressing | rollout=Progressing step=2 abort= | ready pods: gitops-1=9 gitops-2=3 
t=31.9s argocd=043dd0b Synced Progressing | rollout=Progressing step=3 abort= | ready pods: gitops-1=7 gitops-2=5 
t=35.5s argocd=043dd0b Synced Progressing | rollout=Progressing step=3 abort= | ready pods: gitops-1=5 gitops-2=5 
t=50.9s argocd=043dd0b Synced Progressing | rollout=Progressing step=4 abort= | ready pods: gitops-1=5 gitops-2=5 
t=54.5s argocd=043dd0b Synced Progressing | rollout=Healthy step=4 abort= | ready pods: gitops-1=2 gitops-2=10 
promoted to 100% after 55.9 s
k6 (100 requests/s): requests=5819 failed=0 (0.00%) p95=108.03ms; outcomes: ok=5819
canary checks:
  canary check: 100 requests, 0 not 200 (allowed: 1)
  canary check: 100 requests, 0 not 200 (allowed: 1)

## C. Bad release: image gitops-3 and alias fast -> mock-flaky (fails 50%)
images gitops-3 built and loaded into kind
pushed d9b38ce
t=2.8s argocd=043dd0b Synced Healthy | rollout=Healthy step=4 abort= | ready pods: gitops-2=10 
t=7.3s argocd=d9b38ce Synced Progressing | rollout=Progressing step=0 abort= | ready pods: gitops-2=10 
t=10.8s argocd=d9b38ce Synced Progressing | rollout=Progressing step=1 abort= | ready pods: gitops-2=9 gitops-3=1 
t=21.4s argocd=d9b38ce Synced Progressing | rollout=Degraded step=0 abort=true | ready pods: gitops-2=10 gitops-3=1 
t=25.3s argocd=d9b38ce Synced Degraded | rollout=Degraded step=0 abort=true | ready pods: gitops-2=10 
aborted and canary removed after 26.4 s
k6 (100 requests/s): requests=3920 failed=29 (0.74%) p95=106.67ms; outcomes: http_502=2 http_503=27 ok=3891
rollout message: RolloutAborted: Rollout aborted update to revision 9: Step-based analysis phase error/failed: Metric "canary-requests" assessed Failed due to failed (1) > failureLimit (0)
canary check:
  canary check: 100 requests, 84 not 200 (allowed: 1)
after the abort, every pod (all stable gitops-2):
  per-pod check: 10 of 10 pods answered 20/20

## D. Fix forward: git revert of the bad commit
pushed cfad0bc (Revert "release: gateway gitops-3 (bad config)")
t=2.9s argocd=d9b38ce Synced Degraded | rollout=Degraded step=0 abort=true | ready pods: gitops-2=10 
t=6.4s argocd=cfad0bc Synced Healthy | rollout=Healthy step=4 abort= | ready pods: gitops-2=10 
healthy again after 7.5 s
k6 (100 requests/s): requests=2032 failed=0 (0.00%) p95=106.19ms; outcomes: ok=2032
after the revert:
  per-pod check: 10 of 10 pods answered 20/20

## E. Drift: changes made with kubectl are undone
deleted the NetworkPolicy: back after 1.2 s
loosened the PodDisruptionBudget to 5: back to 1 after 2.2 s
scaled the Rollout to 3: back to 10 after 7.2 s
hand edit of configmap/llm-gateway-config-8ff0a369fa: field is immutable
final: argocd=cfad0bc410dd05f44829f119832c99cab8f520ae Synced Healthy | rollout=Healthy step=4 abort= | ready pods: gitops-2=10 
config versions kept: configmap/llm-gateway-config-8ff0a369fa configmap/llm-gateway-config-c3e6888bb2 

git history on the in-cluster server:
  cfad0bc release-bot: Revert "release: gateway gitops-3 (bad config)"
  d9b38ce release-bot: release: gateway gitops-3 (bad config)
  043dd0b release-bot: release: gateway gitops-2
  101f1f5 snapshot: snapshot of the working tree
```
