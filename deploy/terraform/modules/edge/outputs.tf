output "web_acl_arn" {
  description = "WAF ACL for the ALB (Ingress annotation alb.ingress.kubernetes.io/wafv2-acl-arn)."
  value       = aws_wafv2_web_acl.this.arn
}

output "certificate_arn" {
  description = "ACM certificate ARN, or empty without a domain."
  value       = local.with_domain ? aws_acm_certificate.this[0].arn : ""
}

output "external_dns_enabled" {
  description = "True when ExternalDNS has a role (domain and zone given)."
  value       = local.with_zone
}
