# RDS Postgres for the usage ledger only: the smallest class, single-AZ,
# private, TLS required, encrypted with the stack's KMS key.
#
# RDS creates and keeps the master password in Secrets Manager
# (manage_master_user_password), so it never appears in Terraform state.
#
# Test-session settings, and what production changes:
#   deletion_protection = false   -> true
#   skip_final_snapshot = true    -> false (keep a final snapshot)
#   multi_az            = false   -> true (standby in a second AZ)
#   backup_retention    = 1 day   -> 7 to 35 days

resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
  tags       = var.tags
}

resource "aws_security_group" "this" {
  name        = "${var.name}-postgres"
  description = "Postgres from the EKS cluster only"
  vpc_id      = var.vpc_id
  tags        = merge(var.tags, { Name = "${var.name}-postgres" })
}

resource "aws_vpc_security_group_ingress_rule" "postgres" {
  for_each                     = toset(var.allowed_security_group_ids)
  security_group_id            = aws_security_group.this.id
  description                  = "Postgres from the cluster"
  referenced_security_group_id = each.value
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

resource "aws_db_parameter_group" "this" {
  name   = var.name
  family = "postgres${split(".", var.engine_version)[0]}"
  parameter {
    name  = "rds.force_ssl"
    value = "1"
  }
  parameter {
    name  = "log_min_duration_statement"
    value = "1000" # log queries slower than 1 s
  }
  tags = var.tags
}

# Enhanced Monitoring: OS metrics (CPU, memory, disk) every 60 s.
data "aws_iam_policy_document" "monitoring_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["monitoring.rds.amazonaws.com"]
    }
  }
}

data "aws_partition" "current" {}

resource "aws_iam_role" "monitoring" {
  name               = "${var.name}-rds-monitoring"
  assume_role_policy = data.aws_iam_policy_document.monitoring_assume.json
  tags               = var.tags
}

resource "aws_iam_role_policy_attachment" "monitoring" {
  role       = aws_iam_role.monitoring.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/service-role/AmazonRDSEnhancedMonitoringRole"
}

resource "aws_db_instance" "this" {
  #checkov:skip=CKV_AWS_293:test-session default (variable deletion_protection); production sets true
  #checkov:skip=CKV_AWS_157:test-session default (variable multi_az); production sets true
  identifier     = var.name
  engine         = "postgres"
  engine_version = var.engine_version
  instance_class = var.instance_class

  allocated_storage = var.allocated_storage_gb
  storage_type      = "gp3"
  storage_encrypted = true
  kms_key_id        = var.kms_key_arn

  db_name                       = "gateway"
  username                      = "gateway"
  manage_master_user_password   = true
  master_user_secret_kms_key_id = var.kms_key_arn

  db_subnet_group_name   = aws_db_subnet_group.this.name
  vpc_security_group_ids = [aws_security_group.this.id]
  parameter_group_name   = aws_db_parameter_group.this.name
  publicly_accessible    = false
  multi_az               = var.multi_az

  iam_database_authentication_enabled = true
  enabled_cloudwatch_logs_exports     = ["postgresql", "upgrade"]
  performance_insights_enabled        = true
  performance_insights_kms_key_id     = var.kms_key_arn
  monitoring_interval                 = 60
  monitoring_role_arn                 = aws_iam_role.monitoring.arn
  auto_minor_version_upgrade          = true
  copy_tags_to_snapshot               = true
  backup_retention_period             = var.backup_retention_days

  deletion_protection       = var.deletion_protection
  skip_final_snapshot       = var.skip_final_snapshot
  final_snapshot_identifier = var.skip_final_snapshot ? null : "${var.name}-final"

  apply_immediately = true
  tags              = var.tags
}
