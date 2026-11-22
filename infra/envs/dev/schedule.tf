resource "aws_cloudwatch_event_rule" "relay" {
  name                = "${local.name}-relay"
  schedule_expression = var.relay_schedule
  state               = var.relay_schedule_enabled ? "ENABLED" : "DISABLED"
}

resource "aws_cloudwatch_event_target" "relay" {
  rule = aws_cloudwatch_event_rule.relay.name
  arn  = module.relay.function_arn
}

resource "aws_lambda_permission" "relay_events" {
  statement_id  = "AllowEventBridgeInvoke"
  action        = "lambda:InvokeFunction"
  function_name = module.relay.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.relay.arn
}
