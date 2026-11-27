output "api_url" {
  value = aws_apigatewayv2_api.this.api_endpoint
}

output "queue_url" {
  value = module.queues.queue_url
}

output "db_address" {
  value = module.rds.address
}

output "db_secret_arn" {
  value = module.rds.secret_arn
}

output "nat_egress_ip" {
  value = module.network.nat_public_ip
}

output "migrate_function_name" {
  value = module.migrate.function_name
}

output "bastion_instance_id" {
  value = module.bastion.instance_id
}

output "app_db_secret_arn" {
  value = aws_secretsmanager_secret.app_db.arn
}
