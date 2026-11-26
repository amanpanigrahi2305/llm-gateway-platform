# The whole gateway on AWS: VPC, EKS, ElastiCache, RDS, Secrets Manager + KMS,
# WAF (+ optional ACM and DNS), the cluster add-ons and the gateway itself.
#
# It costs money while it runs (see docs/COST.md). Create it for a test session
# and remove it after: make aws-up CONFIRM=yes / make aws-down CONFIRM=yes.

data "aws_partition" "current" {}

locals {
  https = var.domain_name != ""

  # Bedrock models the gateway may call (foundation models in this region).
  bedrock_model_arns = [
    for id in var.bedrock_model_ids : "arn:${data.aws_partition.current.partition}:bedrock:${var.region}::foundation-model/${id}"
  ]
}

module "secrets" {
  source                = "../../modules/secrets"
  name                  = var.name
  provider_secret_names = var.provider_secret_names
  tags                  = var.tags
}

module "network" {
  source      = "../../modules/network"
  name        = var.name
  azs         = var.azs
  kms_key_arn = module.secrets.kms_key_arn
  tags        = var.tags
}

module "eks" {
  source             = "../../modules/eks"
  name               = var.name
  private_subnet_ids = module.network.private_subnet_ids
  api_public_cidrs   = var.api_public_cidrs
  kms_key_arn        = module.secrets.kms_key_arn
  gateway_secret_arns = concat(
    values(module.secrets.provider_secret_arns),
    [module.redis.url_secret_arn, module.database.master_secret_arn],
  )
  bedrock_model_arns = local.bedrock_model_arns
  tags               = var.tags
}

module "redis" {
  source                     = "../../modules/redis"
  name                       = var.name
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.private_subnet_ids
  allowed_security_group_ids = [module.eks.cluster_security_group_id]
  kms_key_arn                = module.secrets.kms_key_arn
  tags                       = var.tags
}

module "database" {
  source                     = "../../modules/database"
  name                       = var.name
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.private_subnet_ids
  allowed_security_group_ids = [module.eks.cluster_security_group_id]
  kms_key_arn                = module.secrets.kms_key_arn
  tags                       = var.tags
}

module "edge" {
  source          = "../../modules/edge"
  name            = var.name
  cluster_name    = module.eks.cluster_name
  domain_name     = var.domain_name
  route53_zone_id = var.route53_zone_id
  kms_key_arn     = module.secrets.kms_key_arn
  tags            = var.tags
}

# --- Cluster add-ons (pinned chart versions) ------------------------------------

resource "helm_release" "lb_controller" {
  name       = "aws-load-balancer-controller"
  repository = "https://aws.github.io/eks-charts"
  chart      = "aws-load-balancer-controller"
  version    = "3.7.0"
  namespace  = "kube-system"
  values = [yamlencode({
    clusterName    = module.eks.cluster_name
    region         = var.region
    vpcId          = module.network.vpc_id
    serviceAccount = { create = true, name = "aws-load-balancer-controller" } # role via Pod Identity
  })]
}

resource "helm_release" "external_dns" {
  count      = module.edge.external_dns_enabled ? 1 : 0
  name       = "external-dns"
  repository = "https://kubernetes-sigs.github.io/external-dns/"
  chart      = "external-dns"
  version    = "1.24.0"
  namespace  = "kube-system"
  values = [yamlencode({
    provider       = { name = "aws" }
    serviceAccount = { create = true, name = "external-dns" } # role via Pod Identity
    sources        = ["ingress"]
    domainFilters  = [var.domain_name]
    policy         = "upsert-only" # never deletes records it did not create
    txtOwnerId     = var.name
    env            = [{ name = "AWS_DEFAULT_REGION", value = var.region }]
  })]
}

resource "helm_release" "secrets_store_csi" {
  name       = "secrets-store-csi-driver"
  repository = "https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts"
  chart      = "secrets-store-csi-driver"
  version    = "1.6.2"
  namespace  = "kube-system"
  values     = [yamlencode({ syncSecret = { enabled = true } })] # copy into a Kubernetes Secret for env vars
}

resource "helm_release" "secrets_store_aws" {
  name       = "secrets-store-csi-driver-provider-aws"
  repository = "https://aws.github.io/secrets-store-csi-driver-provider-aws"
  chart      = "secrets-store-csi-driver-provider-aws"
  version    = "3.2.0"
  namespace  = "kube-system"
  values     = [yamlencode({ "secrets-store-csi-driver" = { install = false } })] # installed above
  depends_on = [helm_release.secrets_store_csi]
}

# --- The gateway ------------------------------------------------------------------

locals {
  file_config = yamldecode(file("${path.module}/gateway-config.yaml"))

  gateway_config = merge(local.file_config, {
    server = {
      addr             = ":8080"
      shutdown_timeout = "30s"
      drain_delay      = "15s" # the ALB needs a few seconds to stop sending to a stopping pod
    }
    providers = merge(local.file_config.providers, {
      bedrock = { type = "bedrock", region = var.region }
    })
    limits = { redis_url_env = "GATEWAY_REDIS_URL", on_store_error = "fail_open" }
    ledger = { database_url_env = "GATEWAY_DATABASE_URL", password_env = "GATEWAY_DB_PASSWORD" }
    keys   = var.gateway_keys
  })

  ingress_annotations = merge(
    {
      "alb.ingress.kubernetes.io/scheme"                   = "internet-facing"
      "alb.ingress.kubernetes.io/target-type"              = "ip"
      "alb.ingress.kubernetes.io/healthcheck-path"         = "/readyz"
      "alb.ingress.kubernetes.io/wafv2-acl-arn"            = module.edge.web_acl_arn
      "alb.ingress.kubernetes.io/target-group-attributes"  = "deregistration_delay.timeout_seconds=30"
      "alb.ingress.kubernetes.io/load-balancer-attributes" = "routing.http.drop_invalid_header_fields.enabled=true,idle_timeout.timeout_seconds=300"
    },
    local.https ? {
      "alb.ingress.kubernetes.io/listen-ports"    = jsonencode([{ HTTPS = 443 }])
      "alb.ingress.kubernetes.io/certificate-arn" = module.edge.certificate_arn
      "alb.ingress.kubernetes.io/ssl-policy"      = "ELBSecurityPolicy-TLS13-1-2-2021-06"
      } : {
      "alb.ingress.kubernetes.io/listen-ports" = jsonencode([{ HTTP = 80 }])
    },
    module.edge.external_dns_enabled ? { "external-dns.alpha.kubernetes.io/hostname" = var.domain_name } : {},
  )

  # Every secret becomes an env var through the Secrets Store CSI Driver.
  secret_objects = concat(
    [for k, arn in module.secrets.provider_secret_arns : { name = arn, env = upper(replace(k, "-", "_")) }],
    [
      { name = module.redis.url_secret_arn, env = "GATEWAY_REDIS_URL" },
      { name = module.database.master_secret_arn, env = "GATEWAY_DB_PASSWORD", jmesPath = "password" },
    ],
  )
}

resource "helm_release" "gateway" {
  count            = var.install_gateway ? 1 : 0
  name             = "llm-gateway" # the service account is llm-gateway: the Pod Identity association expects it
  chart            = "${path.module}/../../../helm/llm-gateway"
  namespace        = "llm-gateway"
  create_namespace = true
  values = [yamlencode({
    image                         = { repository = var.gateway_image_repository, tag = var.gateway_image_tag }
    terminationGracePeriodSeconds = 60
    ingress                       = { enabled = true, className = "alb", host = var.domain_name, annotations = local.ingress_annotations }
    secretsStore                  = { enabled = true, objects = local.secret_objects }
    extraEnv = [{
      name  = "GATEWAY_DATABASE_URL" # the password comes from GATEWAY_DB_PASSWORD (ledger.password_env)
      value = "postgres://gateway@${module.database.address}:${module.database.port}/gateway?sslmode=require"
    }]
    config = local.gateway_config
  })]
  depends_on = [helm_release.lb_controller, helm_release.secrets_store_aws]
}
