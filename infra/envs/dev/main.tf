locals {
  name = "${var.project}-${var.env}"
  dist = "${path.root}/../../../dist"

  db_env = {
    DB_HOST       = module.rds.address
    DB_PORT       = tostring(module.rds.port)
    DB_NAME       = module.rds.database_name
    DB_SECRET_ARN = module.rds.secret_arn
    DB_SSLMODE    = "require"
    LOG_LEVEL     = var.log_level
  }

  db_policy = [
    {
      actions   = ["secretsmanager:GetSecretValue"]
      resources = [module.rds.secret_arn]
    },
  ]

  sqs_send_policy = {
    actions   = ["sqs:SendMessage"]
    resources = [module.queues.queue_arn]
  }
}

module "network" {
  source            = "../../modules/network"
  name              = local.name
  vpc_cidr          = var.vpc_cidr
  nat_instance_type = var.nat_instance_type
}

module "rds" {
  source = "../../modules/rds"

  name                  = local.name
  vpc_id                = module.network.vpc_id
  subnet_ids            = module.network.private_subnet_ids
  allowed_sg_ids        = concat([module.network.lambda_sg_id], module.bastion.security_group_ids)
  engine_version        = var.db_engine_version
  database_name         = "dispatchdb"
  instance_class        = var.db_instance_class
  allocated_storage     = var.db_allocated_storage
  backup_retention_days = var.db_backup_retention_days
  multi_az              = var.db_multi_az
  deletion_protection   = var.deletion_protection
}

module "queues" {
  source = "../../modules/queues"

  name                       = local.name
  max_receive_count          = var.max_receive_count
  visibility_timeout_seconds = 180
}

module "ingest" {
  source = "../../modules/lambda"

  name                   = "${local.name}-ingest"
  zip_path               = "${local.dist}/ingest.zip"
  memory_size            = 256
  timeout                = 15
  log_retention_days     = var.log_retention_days
  environment            = merge(local.db_env, { QUEUE_URL = module.queues.queue_url })
  policy_statements      = concat(local.db_policy, [local.sqs_send_policy])
  vpc_subnet_ids         = module.network.private_subnet_ids
  vpc_security_group_ids = [module.network.lambda_sg_id]
}

module "relay" {
  source = "../../modules/lambda"

  name                   = "${local.name}-relay"
  zip_path               = "${local.dist}/relay.zip"
  memory_size            = 256
  timeout                = 60
  log_retention_days     = var.log_retention_days
  environment            = merge(local.db_env, { QUEUE_URL = module.queues.queue_url })
  policy_statements      = concat(local.db_policy, [local.sqs_send_policy])
  vpc_subnet_ids         = module.network.private_subnet_ids
  vpc_security_group_ids = [module.network.lambda_sg_id]
}

module "worker" {
  source = "../../modules/lambda"

  name               = "${local.name}-worker"
  zip_path           = "${local.dist}/worker.zip"
  memory_size        = 256
  timeout            = 30
  log_retention_days = var.log_retention_days
  environment        = local.db_env
  policy_statements = concat(local.db_policy, [{
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
      "sqs:ChangeMessageVisibility",
    ]
    resources = [module.queues.queue_arn]
  }])
  vpc_subnet_ids         = module.network.private_subnet_ids
  vpc_security_group_ids = [module.network.lambda_sg_id]
}

module "migrate" {
  source = "../../modules/lambda"

  name                   = "${local.name}-migrate"
  zip_path               = "${local.dist}/migrate.zip"
  memory_size            = 256
  timeout                = 300
  log_retention_days     = var.log_retention_days
  environment            = local.db_env
  policy_statements      = local.db_policy
  vpc_subnet_ids         = module.network.private_subnet_ids
  vpc_security_group_ids = [module.network.lambda_sg_id]
}

module "bastion" {
  source = "../../modules/bastion"

  name      = local.name
  enabled   = var.enable_bastion
  vpc_id    = module.network.vpc_id
  subnet_id = module.network.private_subnet_ids[1]
}

resource "aws_lambda_event_source_mapping" "worker" {
  event_source_arn                   = module.queues.queue_arn
  function_name                      = module.worker.function_arn
  batch_size                         = 10
  maximum_batching_window_in_seconds = 2
  function_response_types            = ["ReportBatchItemFailures"]

  scaling_config {
    maximum_concurrency = var.worker_max_concurrency
  }
}
