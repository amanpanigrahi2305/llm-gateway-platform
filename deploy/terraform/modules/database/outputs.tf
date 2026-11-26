output "address" {
  description = "Hostname of the instance."
  value       = aws_db_instance.this.address
}

output "port" {
  description = "Port."
  value       = aws_db_instance.this.port
}

output "master_secret_arn" {
  description = "Secrets Manager entry (managed by RDS) with username and password."
  value       = aws_db_instance.this.master_user_secret[0].secret_arn
}
