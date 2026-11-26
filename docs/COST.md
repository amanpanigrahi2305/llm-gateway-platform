# What the AWS example costs

`deploy/terraform/examples/aws-complete` creates paid AWS resources. This page
estimates the cost so you can plan a test session and remember to remove it.

**These are estimates, not measurements.** They use us-east-1 on-demand list
prices as published around 2025. Prices change: check the AWS pricing pages or
the [AWS Pricing Calculator](https://calculator.aws/) before you start. Real
costs from test sessions will be added here after they run.

## Per hour while the stack exists

| Item | What the example creates | Approx. USD / hour |
|---|---|---|
| EKS control plane | 1 cluster | 0.10 |
| Nodes | 2 × t4g.medium, spot (spot prices move; on-demand is about 0.034 each) | ~0.03 |
| NAT gateway | 1 (plus 0.045 per GB processed) | 0.045 |
| Interface VPC endpoints | 6 services × 2 AZs = 12 endpoint ENIs (plus 0.01 per GB) | 0.12 |
| Application Load Balancer | 1 (plus capacity units under load) | 0.0225 |
| RDS Postgres | db.t4g.micro, single-AZ, 20 GB gp3 | ~0.02 |
| ElastiCache | cache.t4g.micro, 1 node | 0.016 |
| Public IPv4 addresses | NAT gateway + ALB (about 3) at 0.005 each | 0.015 |
| **Total (before data and logs)** | | **~0.37** |

Monthly items, small for a test session: WAF ACL (5 USD/month) + 3 rules
(1 USD each per month) + 0.60 USD per million requests; 2 KMS keys (1 USD each
per month); about 4 Secrets Manager secrets (0.40 USD each per month);
CloudWatch Logs for VPC flow logs, EKS control plane logs and WAF logs (per GB
ingested and stored). The S3 gateway endpoint is free.

So a 4-hour test session costs roughly **1.50 USD** plus data and logs, and a
stack left running for a month roughly **270 USD**. The most common surprise is
forgetting to delete it: run `make aws-down CONFIRM=yes` at the end, and check
the Billing console the next day.

LLM calls are billed separately by each provider (OpenAI, Anthropic, Bedrock),
per token. The gateway's budgets (`policies` in the config) cap that per key.

## NAT gateway or VPC endpoints?

Pods in private subnets reach the internet through the NAT gateway. That path
is needed anyway, because OpenAI and Anthropic are on the public internet. AWS
services (ECR, STS, Secrets Manager, CloudWatch Logs, Bedrock) can be reached
either through the NAT gateway or through VPC endpoints.

| | Through the NAT gateway | Through interface endpoints |
|---|---|---|
| Fixed cost | none extra (the NAT gateway exists anyway) | 0.01 USD per endpoint per AZ per hour: **0.12 USD/hour** for 6 services in 2 AZs |
| Per GB | 0.045 USD | 0.01 USD |
| Traffic path | leaves the VPC to public AWS endpoints | stays on the AWS network, private IPs only |
| Security | needs internet egress for AWS APIs | AWS API calls work even with no internet route; endpoint policies can limit them |

The endpoints save 0.035 USD per GB, so they pay for themselves only above
about **3.4 GB per hour** of traffic to those services (0.12 / 0.035). A small
gateway sends far less: its prompts and answers are kilobytes, and image pulls
come from S3 through the **free** S3 gateway endpoint. So at low traffic the
endpoints cost **more** than they save: about a third of the hourly total.

Why the example keeps them anyway: Bedrock traffic carries prompts, and with
the `bedrock-runtime` endpoint it never leaves the AWS network; pods can get
secrets and credentials (STS, Secrets Manager) without an internet path; and
it is the setup most security reviews ask for. To save money in a test
session, keep only the endpoints you care about:

```hcl
module "network" {
  # ...
  interface_endpoints = ["bedrock-runtime", "sts"]   # 0.04 USD/hour instead of 0.12
}
```

## One NAT gateway, not one per AZ

The example uses one NAT gateway (0.045 USD/hour) instead of one per AZ (0.09).
If the AZ with the NAT gateway fails, pods in the other AZ lose internet access,
so provider calls to OpenAI and Anthropic fail there (Bedrock still works
through its endpoint). Production should use one NAT gateway per AZ.

## Other choices that keep the cost down

- **Spot nodes** (Graviton t4g): much cheaper than on-demand; AWS can reclaim
  them with a 2-minute notice. The gateway's PodDisruptionBudget, topology
  spread and graceful shutdown handle a node going away.
- **Single-AZ RDS and a single Redis node**: test-session defaults. The gateway
  keeps serving when Redis is down (limits fail open), and the ledger writes in
  the background, so a short outage of either does not stop requests.
  Production: Multi-AZ RDS and two Redis nodes (both are variables).
- **No NAT for image layers**: the S3 gateway endpoint carries ECR's layer
  downloads for free.
