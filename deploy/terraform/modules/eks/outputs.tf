output "cluster_name" {
  description = "EKS cluster name."
  value       = aws_eks_cluster.this.name
}

output "cluster_endpoint" {
  description = "Kubernetes API endpoint."
  value       = aws_eks_cluster.this.endpoint
}

output "cluster_ca_data" {
  description = "Base64 CA certificate of the API server."
  value       = aws_eks_cluster.this.certificate_authority[0].data
}

output "cluster_security_group_id" {
  description = "Security group of the cluster and its managed nodes (allow it into Redis and Postgres)."
  value       = aws_eks_cluster.this.vpc_config[0].cluster_security_group_id
}

output "gateway_role_arn" {
  description = "IAM role the gateway pods get through Pod Identity."
  value       = aws_iam_role.gateway.arn
}
