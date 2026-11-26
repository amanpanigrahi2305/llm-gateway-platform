# ElastiCache for the shared rate-limit and budget counters: private subnets
# only, TLS in transit, encrypted at rest with the stack's KMS key, AUTH token.
#
# One node, no failover: the gateway fails open when Redis is down (limits are
# skipped, requests still work), so a short outage is acceptable for tests.
# Production: num_cache_clusters = 2 and automatic failover across AZs.
#
# The AUTH token is generated here, so it is in the Terraform state (keep the
# state bucket private and encrypted). The gateway reads the full rediss:// URL
# from the Secrets Manager entry this module creates.

resource "random_password" "auth" {
  length  = 40
  special = false # ElastiCache forbids some symbols; letters and digits are enough at this length
}

resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = var.tags
}

resource "aws_security_group" "this" {
  name        = "${var.name}-redis"
  description = "Redis from the EKS cluster only"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-redis" })
}

resource "aws_vpc_security_group_ingress_rule" "redis" {
  for_each                     = toset(var.allowed_security_group_ids)
  security_group_id            = aws_security_group.this.id
  description                  = "Redis TLS from the cluster"
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

resource "aws_elasticache_replication_group" "this" {
  #checkov:skip=CKV2_AWS_50:test-session default (1 node); num_cache_clusters >= 2 turns on Multi-AZ failover
  replication_group_id       = var.name
  description                = "${var.name} rate limits and budgets"
  engine                     = "redis"
  engine_version             = var.engine_version
  node_type                  = var.node_type
  num_cache_clusters         = var.num_cache_clusters
  automatic_failover_enabled = var.num_cache_clusters > 1
  multi_az_enabled           = var.num_cache_clusters > 1
  port                       = 6379
  subnet_group_name          = aws_elasticache_subnet_group.this.name
  security_group_ids         = [aws_security_group.this.id]
  at_rest_encryption_enabled = true
  kms_key_id                 = var.kms_key_arn
  transit_encryption_enabled = true
  auth_token                 = random_password.auth.result
  auto_minor_version_upgrade = true
  snapshot_retention_limit   = 1
  apply_immediately          = true
  tags                       = var.tags
}

resource "aws_secretsmanager_secret" "url" {
  #checkov:skip=CKV2_AWS_57:the URL holds the ElastiCache AUTH token; rotate by changing the token (no Lambda rotator)
  name                    = "${var.name}/redis-url"
  description             = "rediss:// URL with the AUTH token, for the gateway"
  kms_key_id              = var.kms_key_arn
  recovery_window_in_days = var.secret_recovery_window_days
  tags                    = var.tags
}

resource "aws_secretsmanager_secret_version" "url" {
  secret_id     = aws_secretsmanager_secret.url.id
  secret_string = "rediss://:${random_password.auth.result}@${aws_elasticache_replication_group.this.primary_endpoint_address}:6379/0"
}
