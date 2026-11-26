output "cluster_name" {
  description = "EKS cluster name."
  value       = module.eks.cluster_name
}

output "kubeconfig_command" {
  description = "Point kubectl at the cluster."
  value       = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name}"
}

output "set_provider_keys" {
  description = "Set each provider key once (the value never goes through Terraform)."
  value = [
    for k, arn in module.secrets.provider_secret_arns :
    "aws secretsmanager put-secret-value --region ${var.region} --secret-id ${arn} --secret-string \"$(cat ${k}.txt)\""
  ]
}

output "gateway_url_hint" {
  description = "Where the gateway answers."
  value       = var.domain_name != "" ? "https://${var.domain_name}" : "http://<ALB DNS name>: kubectl -n llm-gateway get ingress llm-gateway"
}

output "gateway_role_arn" {
  description = "IAM role of the gateway pods (Pod Identity)."
  value       = module.eks.gateway_role_arn
}
