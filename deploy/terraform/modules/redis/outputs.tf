output "url_secret_arn" {
  description = "Secrets Manager entry with the rediss:// URL."
  value       = aws_secretsmanager_secret.url.arn
}

output "primary_endpoint" {
  description = "Primary endpoint address."
  value       = aws_elasticache_replication_group.this.primary_endpoint_address
}
