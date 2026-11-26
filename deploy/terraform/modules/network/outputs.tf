output "vpc_id" {
  description = "VPC ID."
  value       = aws_vpc.this.id
}

output "vpc_cidr" {
  description = "VPC CIDR block."
  value       = aws_vpc.this.cidr_block
}

output "public_subnet_ids" {
  description = "Public subnets (load balancer)."
  value       = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  description = "Private subnets (nodes, Redis, Postgres, endpoints)."
  value       = aws_subnet.private[*].id
}
