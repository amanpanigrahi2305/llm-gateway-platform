# Phase 3 evidence: the gateway on a local kind cluster

Raw output of `docs/evidence/phase3-kind.sh` and `docs/evidence/phase3-hpa.sh`,
run on 2026-11-25 from 23:26 UTC with the chart at commit dba5fa9. The gateway
binary reports version `9f840a7-dirty`: built from 9f840a7 (the Go code is the
same at dba5fa9). "dirty" only means docs and chart edits were not committed at
build time; `.dockerignore` keeps everything except `cmd/` and `internal/` out
of the image.

Setup: kind v0.33.0, 3 nodes (1 control plane, 2 workers), Kubernetes v1.36.4,
metrics-server v0.9.1, Docker Desktop 28.5.1 on WSL2, laptop CPU AMD Ryzen AI 9
HX PRO 375. Only the mock providers are used. Traffic from k6 reaches the
NodePort through Docker's port forwarder on 127.0.0.1, so latency includes that
hop. The demo key `sk-gw-local-demo-only` is public on purpose (local clusters
only). Elapsed times come from `/proc/uptime` because the WSL wall clock jumps.

## Results at a glance

| Test | Requests | Failed | p95 | Notes |
|---|---|---|---|---|
| Delete both original gateway pods under load (20 VUs, 60 s) | 11,400 | **0** | 107.80 ms | replacement ready in 3.3 s and 3.1 s; ledger rows added = 11,400 |
| Rolling restart under load (20 VUs, 60 s) | 11,380 | **0** | 107.57 ms | ready endpoints never below 2 |
| HPA, before the keep-alive fix (100 VUs, 180 s) | 334,454 | 0 | 97.25 ms | 2 → 6 pods, but 4 new pods idle (2–3m CPU) |
| HPA, after `max_connection_age` (100 VUs, 180 s) | 688,351 | **0** | 61.22 ms | 2 → 4 → 6 pods, all 6 busy (614–932m CPU) |

## A. Cluster

```
kind v0.33.0 go1.26.7 linux/amd64
NAME                        STATUS   VERSION
llm-gateway-control-plane   Ready    v1.36.4
llm-gateway-worker          Ready    v1.36.4
llm-gateway-worker2         Ready    v1.36.4
NAME READY STATUS NODE
llm-gateway-78568f559c-5l2lb 1/1 Running llm-gateway-worker2
llm-gateway-78568f559c-chgpq 1/1 Running llm-gateway-worker
llm-gateway-mock-a-687f5bdd97-nqzxh 1/1 Running llm-gateway-worker
llm-gateway-mock-b-8646f64b5d-dn9r7 1/1 Running llm-gateway-worker
llm-gateway-postgres-5d86996d58-k79b7 1/1 Running llm-gateway-worker2
llm-gateway-redis-79969dc88-c5sct 1/1 Running llm-gateway-worker
security context (user, non-root, read-only root):
llm-gateway-78568f559c-5l2lb uid=65532 nonRoot=true readOnlyRoot=true
llm-gateway-78568f559c-chgpq uid=65532 nonRoot=true readOnlyRoot=true
llm-gateway-mock-a-687f5bdd97-nqzxh uid=65532 nonRoot=true readOnlyRoot=true
llm-gateway-mock-b-8646f64b5d-dn9r7 uid=65532 nonRoot=true readOnlyRoot=true
llm-gateway-postgres-5d86996d58-k79b7 uid=70 nonRoot=true readOnlyRoot=true
llm-gateway-redis-79969dc88-c5sct uid=999 nonRoot=true readOnlyRoot=true
NAME                                              REFERENCE                TARGETS       MINPODS   MAXPODS   REPLICAS   AGE
horizontalpodautoscaler.autoscaling/llm-gateway   Deployment/llm-gateway   cpu: 0%/70%   2         6         2          21m

NAME                                     MIN AVAILABLE   MAX UNAVAILABLE   ALLOWED DISRUPTIONS   AGE
poddisruptionbudget.policy/llm-gateway   N/A             1                 1                     21m
```

`make kind-up` took 80 s; `make deploy` (build, load into 3 nodes, helm install
with `--wait`) took 52 s the first time.

## B. Smoke through the NodePort

```
healthz 200  readyz 200
200 0.144782s provider=mock-a
200 0.112827s provider=mock-a
200 0.106609s provider=mock-a
stream: 7 chunks, last line: data: [DONE]
failover: mock-a outage on
200 0.135591s provider=mock-b
200 0.162068s provider=mock-b
200 0.181023s provider=mock-b
mock-a reset
200 0.105427s provider=mock-a
ledger rows in the cluster Postgres: 357253
```

(The row count includes the earlier test runs on this cluster.)

## C. Delete both original gateway pods, one at a time, under load

```
original pods: llm-gateway-78568f559c-5l2lb llm-gateway-78568f559c-chgpq
t=15.0 delete llm-gateway-78568f559c-5l2lb
replacement ready after 3.3s
t=28.4 delete llm-gateway-78568f559c-chgpq
replacement ready after 3.1s
requests=11400 failed=0 rate=189/s p50=104.74ms p95=107.80ms max=180.25ms
outcomes: ok=11400
pod 0 shutdown log:
"msg":"shutdown started","drain_delay":"5s","shutdown_timeout":"30s"
"msg":"shutdown complete"
"msg":"usage ledger closed","written":1901,"dropped":0,"failed":0
pod 1 shutdown log:
"msg":"shutdown started","drain_delay":"5s","shutdown_timeout":"30s"
"msg":"shutdown complete"
"msg":"usage ledger closed","written":3527,"dropped":0,"failed":0
ledger rows added during the test: 11400
lowest ready endpoints seen: 1
```

The same test on the build before `max_connection_age` (commit 81f382d chart):
11,397 requests, 0 failed, p95 106.80 ms, replacements ready in 3.3 s and 3.1 s,
ledger rows added 11,397.

## D. Rolling restart under load

```
rollout finished after 5.8s
requests=11380 failed=0 rate=189/s p50=104.96ms p95=107.57ms max=178.90ms
outcomes: ok=11380
ledger rows added during the test: 11380
lowest ready endpoints seen: 2
NAME READY STATUS NODE
llm-gateway-6cd9f8d5cb-vfwmg 1/1 Running llm-gateway-worker
llm-gateway-6cd9f8d5cb-w99kc 1/1 Running llm-gateway-worker2
```

"rollout finished" is when `kubectl rollout status` returned: both new pods ready
and the old ReplicaSet scaled to 0. The old pods then drained for 5 s each.

## E. HPA under load, after `max_connection_age` (the current build)

mock-a's delay was set to 5 ms for this test (reset afterwards), so each virtual
user sends more requests. HPA: target 70% of the 250m CPU request, 2 to 6 pods;
scale up 100% per 15 s; scale down after a 120 s window, 1 pod per 30 s.

```
## E. HPA under load (100 VUs for 180s, mock-a delay 5 ms)
before: llm-gateway-6cd9f8d5cb-vfwmg 71m 10Mi; llm-gateway-6cd9f8d5cb-w99kc 176m 15Mi; 
load ended at t=181s
k6: requests=688351 failed=0 rate=3814/s p50=20.11ms p95=61.22ms max=260.90ms
outcomes: ok=688351
timeline (every 5 s; t in seconds since load start):
t=1 cpu=49% replicas=2 desired=2 ready=2
t=7 cpu=49% replicas=2 desired=2 ready=2
t=13 cpu=46% replicas=2 desired=2 ready=2
t=19 cpu=46% replicas=2 desired=2 ready=2
t=26 cpu=46% replicas=2 desired=2 ready=2
t=32 cpu=220% replicas=2 desired=4 ready=4
t=38 cpu=220% replicas=2 desired=4 ready=4
t=45 cpu=399% replicas=4 desired=6 ready=6
t=52 cpu=399% replicas=4 desired=6 ready=6
t=58 cpu=394% replicas=6 desired=6 ready=6
t=65 cpu=394% replicas=6 desired=6 ready=6
t=72 cpu=394% replicas=6 desired=6 ready=6
t=79 cpu=319% replicas=6 desired=6 ready=6
t=85 cpu=319% replicas=6 desired=6 ready=6
t=92 cpu=329% replicas=6 desired=6 ready=6
t=98 cpu=329% replicas=6 desired=6 ready=6
t=105 cpu=330% replicas=6 desired=6 ready=6
t=112 cpu=330% replicas=6 desired=6 ready=6
t=119 cpu=333% replicas=6 desired=6 ready=6
t=126 cpu=333% replicas=6 desired=6 ready=6
t=133 cpu=318% replicas=6 desired=6 ready=6
t=140 cpu=318% replicas=6 desired=6 ready=6
t=146 cpu=318% replicas=6 desired=6 ready=6
t=153 cpu=328% replicas=6 desired=6 ready=6
t=159 cpu=328% replicas=6 desired=6 ready=6
t=166 cpu=325% replicas=6 desired=6 ready=6
t=173 cpu=325% replicas=6 desired=6 ready=6
t=180 cpu=325% replicas=6 desired=6 ready=6
t=186 cpu=325% replicas=6 desired=6 ready=6
t=192 cpu=325% replicas=6 desired=6 ready=6
t=198 cpu=324% replicas=6 desired=6 ready=6
t=204 cpu=324% replicas=6 desired=6 ready=6
t=210 cpu=141% replicas=6 desired=6 ready=6
t=216 cpu=141% replicas=6 desired=6 ready=6
t=222 cpu=141% replicas=6 desired=6 ready=6
t=228 cpu=1% replicas=6 desired=6 ready=6
t=233 cpu=1% replicas=6 desired=6 ready=6
t=239 cpu=1% replicas=6 desired=6 ready=6
t=245 cpu=1% replicas=6 desired=6 ready=6
t=251 cpu=1% replicas=6 desired=6 ready=6
t=257 cpu=0% replicas=6 desired=6 ready=6
t=263 cpu=0% replicas=6 desired=6 ready=6
t=269 cpu=0% replicas=6 desired=6 ready=6
t=275 cpu=0% replicas=6 desired=6 ready=6
t=281 cpu=0% replicas=6 desired=6 ready=6
t=287 cpu=0% replicas=6 desired=6 ready=6
t=293 cpu=0% replicas=6 desired=6 ready=6
t=299 cpu=0% replicas=6 desired=6 ready=6
t=304 cpu=0% replicas=6 desired=6 ready=6
t=310 cpu=0% replicas=6 desired=6 ready=6
t=316 cpu=0% replicas=6 desired=6 ready=6
t=322 cpu=0% replicas=6 desired=6 ready=6
t=328 cpu=0% replicas=6 desired=5 ready=5
t=334 cpu=0% replicas=6 desired=5 ready=5
t=340 cpu=0% replicas=6 desired=5 ready=5
t=346 cpu=0% replicas=5 desired=5 ready=5
t=352 cpu=0% replicas=5 desired=5 ready=5
t=358 cpu=0% replicas=5 desired=5 ready=4
t=364 cpu=0% replicas=5 desired=4 ready=4
t=370 cpu=0% replicas=5 desired=4 ready=4
t=376 cpu=0% replicas=4 desired=4 ready=4
t=382 cpu=0% replicas=4 desired=4 ready=4
t=388 cpu=0% replicas=4 desired=4 ready=3
t=394 cpu=0% replicas=4 desired=3 ready=3
t=400 cpu=0% replicas=4 desired=3 ready=3
t=406 cpu=0% replicas=3 desired=3 ready=3
t=412 cpu=0% replicas=3 desired=3 ready=3
t=418 cpu=0% replicas=3 desired=3 ready=3
t=424 cpu=0% replicas=3 desired=2 ready=2
t=430 cpu=0% replicas=3 desired=2 ready=2
t=436 cpu=0% replicas=2 desired=2 ready=2
at load end: llm-gateway-6cd9f8d5cb-jxsrx 932m 19Mi; llm-gateway-6cd9f8d5cb-njcpx 843m 19Mi; llm-gateway-6cd9f8d5cb-v6rd9 614m 18Mi; llm-gateway-6cd9f8d5cb-vfwmg 810m 21Mi; llm-gateway-6cd9f8d5cb-w99kc 823m 21Mi; llm-gateway-6cd9f8d5cb-wm8kj 857m 19Mi; 
HPA events:
SuccessfulRescale              New size: 2; reason: Current number of replicas below Spec.MinReplicas
FailedGetResourceMetric        failed to get cpu utilization: unable to get metrics for resource cpu: no metrics returned from resource metrics API
FailedComputeMetricsReplicas   invalid metrics (1 invalid out of 1), first error is: failed to get cpu resource metric value: failed to get cpu utilization: unable to get metrics for resource cpu: no metrics returned from resource metrics API
FailedGetResourceMetric        failed to get cpu utilization: did not receive metrics for targeted pods (pods might be unready)
FailedComputeMetricsReplicas   invalid metrics (1 invalid out of 1), first error is: failed to get cpu resource metric value: failed to get cpu utilization: did not receive metrics for targeted pods (pods might be unready)
SuccessfulRescale              New size: 4; reason: cpu resource utilization (percentage of request) above target
SuccessfulRescale              New size: 6; reason: cpu resource utilization (percentage of request) above target
SuccessfulRescale              New size: 5; reason: All metrics below target
SuccessfulRescale              New size: 4; reason: All metrics below target
SuccessfulRescale              New size: 3; reason: All metrics below target
SuccessfulRescale              New size: 2; reason: All metrics below target
```

How to read it:
- The CPU column lags: metrics-server reports a 15 s window and the HPA reads it
  every 15 s. The 46–49% at the start is left over from test D.
- First reading under load at t=32: 2 → 4 pods; at t=45: 4 → 6 (the maximum).
  New pods were ready within one 5 s sample.
- At load end all 6 pods were busy (614m–932m).
- Scale-down: the CPU reading reached ~0 at t=228; the first pod was removed at
  t=328 (the 120 s window), then 1 pod per 30 s; back to 2 pods at t=436,
  255 s after the load ended.
- The `FailedGetResourceMetric` events are from the first minute after the very
  first install, before metrics-server had data for the new pods.

## E0. The same HPA test before the keep-alive fix (commit 81f382d chart)

```
## E. HPA under load (100 VUs for 180s, mock-a delay 5 ms)
before: llm-gateway-85bd7789b6-48rx6 2m 13Mi; llm-gateway-85bd7789b6-m5k4h 2m 13Mi; 
load ended at t=181s
k6: requests=334454 failed=0 rate=1853/s p50=51.71ms p95=97.25ms max=486.28ms
outcomes: ok=334454
timeline (every 5 s; t in seconds since load start):
t=1 cpu=0% replicas=2 desired=2 ready=2
t=7 cpu=0% replicas=2 desired=2 ready=2
t=14 cpu=0% replicas=2 desired=2 ready=2
t=21 cpu=114% replicas=2 desired=4 ready=2
t=27 cpu=114% replicas=2 desired=4 ready=4
t=34 cpu=114% replicas=2 desired=4 ready=4
t=40 cpu=398% replicas=4 desired=6 ready=6
t=47 cpu=398% replicas=4 desired=6 ready=6
t=54 cpu=266% replicas=6 desired=6 ready=6
t=60 cpu=266% replicas=6 desired=6 ready=6
t=67 cpu=133% replicas=6 desired=6 ready=6
t=73 cpu=133% replicas=6 desired=6 ready=6
t=80 cpu=133% replicas=6 desired=6 ready=6
t=86 cpu=133% replicas=6 desired=6 ready=6
t=92 cpu=133% replicas=6 desired=6 ready=6
t=99 cpu=133% replicas=6 desired=6 ready=6
t=105 cpu=133% replicas=6 desired=6 ready=6
t=111 cpu=133% replicas=6 desired=6 ready=6
t=118 cpu=133% replicas=6 desired=6 ready=6
t=124 cpu=133% replicas=6 desired=6 ready=6
t=130 cpu=133% replicas=6 desired=6 ready=6
t=137 cpu=133% replicas=6 desired=6 ready=6
t=144 cpu=133% replicas=6 desired=6 ready=6
t=150 cpu=133% replicas=6 desired=6 ready=6
t=157 cpu=133% replicas=6 desired=6 ready=6
t=164 cpu=133% replicas=6 desired=6 ready=6
t=170 cpu=133% replicas=6 desired=6 ready=6
t=176 cpu=133% replicas=6 desired=6 ready=6
t=182 cpu=133% replicas=6 desired=6 ready=6
t=188 cpu=133% replicas=6 desired=6 ready=6
t=194 cpu=133% replicas=6 desired=6 ready=6
t=200 cpu=133% replicas=6 desired=6 ready=6
t=206 cpu=80% replicas=6 desired=6 ready=6
t=212 cpu=80% replicas=6 desired=6 ready=6
t=218 cpu=0% replicas=6 desired=6 ready=6
t=224 cpu=0% replicas=6 desired=6 ready=6
t=230 cpu=0% replicas=6 desired=6 ready=6
t=236 cpu=0% replicas=6 desired=6 ready=6
t=242 cpu=0% replicas=6 desired=6 ready=6
t=248 cpu=0% replicas=6 desired=6 ready=6
t=254 cpu=0% replicas=6 desired=6 ready=6
t=260 cpu=0% replicas=6 desired=6 ready=6
t=266 cpu=0% replicas=6 desired=6 ready=6
t=272 cpu=0% replicas=6 desired=6 ready=6
t=278 cpu=0% replicas=6 desired=6 ready=6
t=284 cpu=0% replicas=6 desired=6 ready=6
t=290 cpu=0% replicas=6 desired=6 ready=6
t=296 cpu=0% replicas=6 desired=6 ready=6
t=302 cpu=0% replicas=6 desired=6 ready=6
t=308 cpu=0% replicas=6 desired=6 ready=6
t=314 cpu=0% replicas=6 desired=6 ready=6
t=320 cpu=0% replicas=6 desired=6 ready=6
t=326 cpu=0% replicas=6 desired=5 ready=5
t=332 cpu=0% replicas=6 desired=5 ready=5
t=338 cpu=0% replicas=5 desired=5 ready=5
t=344 cpu=0% replicas=5 desired=5 ready=5
t=350 cpu=0% replicas=5 desired=5 ready=4
t=356 cpu=0% replicas=5 desired=4 ready=4
t=362 cpu=0% replicas=5 desired=4 ready=4
t=368 cpu=0% replicas=4 desired=4 ready=4
t=374 cpu=0% replicas=4 desired=4 ready=4
t=380 cpu=0% replicas=4 desired=4 ready=4
t=386 cpu=0% replicas=4 desired=3 ready=3
t=392 cpu=0% replicas=4 desired=3 ready=3
t=398 cpu=0% replicas=3 desired=3 ready=3
t=404 cpu=0% replicas=3 desired=3 ready=3
t=410 cpu=0% replicas=3 desired=3 ready=3
t=416 cpu=0% replicas=3 desired=2 ready=2
t=422 cpu=0% replicas=3 desired=2 ready=2
t=428 cpu=0% replicas=2 desired=2 ready=2
at load end: llm-gateway-85bd7789b6-48rx6 998m 23Mi; llm-gateway-85bd7789b6-6jmzh 3m 3Mi; llm-gateway-85bd7789b6-7mnq6 2m 3Mi; llm-gateway-85bd7789b6-fhmpg 2m 3Mi; llm-gateway-85bd7789b6-h4fzq 2m 3Mi; llm-gateway-85bd7789b6-m5k4h 995m 24Mi; 
```

The HPA scaled correctly, but the 4 new pods got almost no traffic: the 2
original pods sat at their 1-core limit (998m, 995m) and the average stayed at
133%. kube-proxy picks a pod once per TCP connection, and k6's 100 keep-alive
connections were all opened before the scale-up. Fix: `server.max_connection_age`
(commit 9f840a7). With the same test, throughput went from 1,853 to 3,814
requests/s and p95 from 97.25 ms to 61.22 ms.

## F. Pod spread across nodes

With `whenUnsatisfiable: ScheduleAnyway`, a rollout placed both gateway pods on
`llm-gateway-worker2`:

```
llm-gateway-5776fb7f44-87w4g 1/1 Running llm-gateway-worker2
llm-gateway-5776fb7f44-xwx99 1/1 Running llm-gateway-worker2
```

After the fix (`DoNotSchedule`, `matchLabelKeys: [pod-template-hash]`,
`nodeTaintsPolicy: Honor`, commit dba5fa9), ready gateway pods per node:

```
after deploy:       1 llm-gateway-worker;      1 llm-gateway-worker2;
after rollout 1:       1 llm-gateway-worker;      1 llm-gateway-worker2;
after rollout 2:       1 llm-gateway-worker;      1 llm-gateway-worker2;
after rollout 3:       1 llm-gateway-worker;      1 llm-gateway-worker2;
```

## G. Note: the first kind node image was damaged

The first `make kind-up` failed at "Starting control-plane". The node image
`kindest/node:v1.37.0` had been downloaded while the laptop disk was full, and
some files in it were written as zeros:

```
kubelet.service: Failed to execute /usr/bin/kubelet: Exec format error
/usr/bin/kubelet 61243657 bytes  00 00 00 00
zero-filled? count of non-zero bytes in the first 1 MB of kubelet: 0
zero-start files: 30   (28 under /var/lib/containerd, plus kubelet and kubectl)
```

Removing and pulling the image again reused the damaged unpacked layer (the pull
took 2–3 s), so the cluster now pins `kindest/node:v1.36.4` by digest, one of
the images listed in the kind v0.33.0 release. Its check:

```
pull seconds: 23
scanned=1035
zero-start files: 0
kubelet header:  7f 45 4c 46
Kubernetes v1.36.4
```

End of run.
