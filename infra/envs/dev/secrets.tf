resource "aws_secretsmanager_secret" "app_db" {
  name                    = "${local.name}/app-db"
  description             = "Credentials for the dispatch_app database role"
  recovery_window_in_days = var.secret_recovery_window_days
}

resource "aws_kms_key" "secrets" {
  description             = "${local.name} endpoint signing secrets"
  enable_key_rotation     = true
  deletion_window_in_days = 7
}

resource "aws_kms_alias" "secrets" {
  name          = "alias/${local.name}-secrets"
  target_key_id = aws_kms_key.secrets.key_id
}
