resource "aws_secretsmanager_secret" "app_db" {
  name                    = "${local.name}/app-db"
  description             = "Credentials for the dispatch_app database role"
  recovery_window_in_days = var.secret_recovery_window_days
}
