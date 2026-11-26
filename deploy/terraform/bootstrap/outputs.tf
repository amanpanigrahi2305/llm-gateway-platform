output "state_bucket" {
  description = "S3 bucket for the remote state."
  value       = aws_s3_bucket.state.bucket
}

output "backend_config" {
  description = "Put this in examples/aws-complete/backend.hcl (git-ignored), then terraform init -backend-config=backend.hcl."
  value       = <<-EOT
    bucket       = "${aws_s3_bucket.state.bucket}"
    key          = "aws-complete/terraform.tfstate"
    region       = "${var.region}"
    encrypt      = true
    use_lockfile = true
  EOT
}
