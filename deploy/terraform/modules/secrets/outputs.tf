output "kms_key_arn" {
  description = "ARN of the stack's KMS key."
  value       = aws_kms_key.this.arn
}

output "provider_secret_arns" {
  description = "Secret ARN per provider key name."
  value       = { for k, s in aws_secretsmanager_secret.provider : k => s.arn }
}
