/******************************************
ALB - Target Group and Listener Rule
******************************************/

resource "aws_lb_target_group" "group" {
  name        = "${var.environment}-${var.name}"
  port        = 80
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = data.terraform_remote_state.environment.outputs.vpc.id

  tags = {
    Description = var.description
    Environment = var.environment
    Name        = var.name
  }

  health_check {
    interval = 60
    matcher  = "200"
    path     = "/health"
    timeout  = 10
  }

  # Stickiness is a no-op while the autoscaling target is pinned to
  # min/max = 1 (only one target to stick to). Kept enabled +
  # configured anyway so that if/when the single-replica constraint
  # is lifted (see the long note on aws_appautoscaling_target.ecs),
  # the sticky behavior is already in place.
  #
  # Verified end-to-end on 2026-05-17 by temporarily bumping
  # min/max=2: with both tasks healthy, 15/15 cookie-bound requests
  # routed to the same task; 10 cookieless requests split 5/5. So
  # the ALB side is correct — what blocks multi-replica today is
  # purely the Go MCP SDK keeping session state in-process with no
  # client-side cookie jar. Browser-based clients hitting the same
  # ALB (e.g. the Connector flow from Claude.ai) WILL benefit.
  stickiness {
    type            = "lb_cookie"
    cookie_duration = 3600
    enabled         = true
  }
}

resource "aws_lb_listener_rule" "listener_rule" {
  listener_arn = data.terraform_remote_state.environment.outputs.public_alb.https_listener
  priority     = var.priority

  action {
    target_group_arn = aws_lb_target_group.group.arn
    type             = "forward"
  }

  condition {
    host_header {
      values = [var.domain[var.environment]]
    }
  }
}

/******************************************
ECS - Task Definition

Two notable differences vs. platform-api:

  - PORT is unset; the binary defaults to 8080.
  - PLATFORM_API_URL is the only required env var; the MCP doesn't
    touch the database. Authentication is per-request: the
    Authorization header is forwarded to the platform API for
    validation on every tool call.

The image reference `ecr.mcp_remote` must exist in the environment/
remote state — see ./README.md prerequisites.
******************************************/

resource "aws_ecs_task_definition" "definition" {
  cpu                      = 256
  execution_role_arn       = data.terraform_remote_state.environment.outputs.iam.ecs_task_execution_role_arn
  family                   = "${var.environment}-${var.name}"
  memory                   = 512
  network_mode             = "awsvpc"
  requires_compatibilities = ["FARGATE"]
  task_role_arn            = data.terraform_remote_state.environment.outputs.iam.ecs_task_execution_role_arn
  container_definitions = jsonencode([
    {
      essential         = true
      image             = "${data.terraform_remote_state.environment.outputs.ecr.mcp_remote}:${var.version_tag}"
      memory            = 512
      memoryReservation = 512
      cpu               = 256
      name              = var.name
      environment = [
        { name = "ENVIRONMENT", value = var.environment },
        { name = "PLATFORM_API_URL", value = local.platform_api_url },
        # OAuth — see cmd/mcp-remote/oauth.go. When set, the server's
        # /oauth/* endpoints become functional and the metadata doc
        # advertises real endpoints; when absent the metadata surfaces
        # x-buildpulse-oauth-status=unconfigured and /authorize returns
        # 501. Bearer-token auth on /mcp works either way.
        { name = "COGNITO_DOMAIN", value = "https://${data.terraform_remote_state.environment.outputs.cognito.user_pool_domain}" },
        { name = "COGNITO_CLIENT_ID", value = data.terraform_remote_state.environment.outputs.cognito.mcp_client_id },
        { name = "MCP_ISSUER", value = "https://${var.domain[var.environment]}" },
        # DocumentDB — needed so cmd/mcp-remote/mongo.go can resolve
        # the Cognito user → memberships → org UUIDs on OAuth callback
        # and persist the resulting mcpSession. Without this, OAuth
        # tokens are minted but won't authenticate on tool calls.
        { name = "MONGODB_URI", value = var.mongodb_uri },

        # OAuth state DynamoDB tables — make the OAuth flow survive
        # rolling deploys + horizontal scaling. When unset the server
        # falls back to in-process sync.Maps. See store_init.go.
        { name = "OAUTH_CLIENTS_TABLE", value = data.terraform_remote_state.environment.outputs.mcp.oauth_clients_table },
        { name = "OAUTH_CODES_TABLE", value = data.terraform_remote_state.environment.outputs.mcp.oauth_codes_table },
        { name = "OAUTH_PENDING_TABLE", value = data.terraform_remote_state.environment.outputs.mcp.oauth_pending_table },
        # Refresh-token grant (silent re-auth so users don't log in daily).
        # OAUTH_REFRESH_TABLE stores rotating refresh tokens; OAUTH_KMS_KEY_ARN
        # app-encrypts the upstream Cognito refresh token before it lands
        # there. Both are Cognito-validated on refresh — see oauth.go
        # tokenRefresh + crypto.go. When either is unset the server still
        # mints working 1h access tokens, just without silent refresh.
        { name = "OAUTH_REFRESH_TABLE", value = data.terraform_remote_state.environment.outputs.mcp.oauth_refresh_table },
        { name = "OAUTH_KMS_KEY_ARN", value = data.terraform_remote_state.environment.outputs.mcp.oauth_refresh_kms_key_arn },
      ]
      # The Cognito client secret comes from Secrets Manager so it
      # never lands in the task definition JSON in plaintext. ECS pulls
      # it at task start and injects it as the named env var.
      secrets = [
        { name = "COGNITO_CLIENT_SECRET", valueFrom = data.terraform_remote_state.environment.outputs.mcp.cognito_client_secret_arn },
      ]
      portMappings = [{
        containerPort = 8080
        hostPort      = 8080
        protocol      = "tcp"
      }]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = "/aws/ecs/${var.environment}"
          "awslogs-region"        = data.aws_region.current.name
          "awslogs-stream-prefix" = var.name
        }
      }
    }
  ])

  tags = {
    Description = var.description
    Environment = var.environment
    Version     = var.version_tag
    Name        = "${var.environment}-${var.name}"
  }
}

locals {
  default_platform_api_url = {
    development = "https://platform.dev.buildpulse.io"
    production  = "https://platform.buildpulse.io"
  }
  platform_api_url = var.platform_api_url != "" ? var.platform_api_url : local.default_platform_api_url[var.environment]
}

/******************************************
ECS - Service
******************************************/

resource "aws_ecs_service" "service" {
  cluster = data.terraform_remote_state.environment.outputs.ecs.primary_cluster_id
  # Initial desired count; ECS auto-scaling (see
  # aws_appautoscaling_target.ecs below) takes over after the first
  # apply. lifecycle.ignore_changes keeps Terraform from clobbering
  # the autoscaler.
  desired_count   = 2
  launch_type     = "FARGATE"
  name            = var.name
  task_definition = aws_ecs_task_definition.definition.arn

  network_configuration {
    assign_public_ip = false
    security_groups  = [data.terraform_remote_state.environment.outputs.vpc.private_security_group]
    subnets          = data.terraform_remote_state.environment.outputs.vpc.private_subnets
  }

  load_balancer {
    container_name   = var.name
    container_port   = 8080
    target_group_arn = aws_lb_target_group.group.arn
  }

  lifecycle {
    ignore_changes = [desired_count]
  }

  tags = {
    Description = var.description
    Environment = var.environment
    Name        = var.name
  }
}

/******************************************
ECS - Auto Scaling

Streamable HTTP keeps connections open for SSE; CPU stays low but
concurrent connection count drives scaling needs. We scale on CPU as
a proxy until we have a custom metric for in-flight MCP sessions.
******************************************/

resource "aws_appautoscaling_target" "ecs" {
  # Multi-replica enabled via the SDK's Stateless mode (see
  # cmd/mcp-remote/main.go). Every POST is self-contained — no
  # Mcp-Session-Id validation, no per-session in-process state — so
  # the ALB can freely round-robin across tasks without the previous
  # 404-on-follow-up failure mode. The OAuth-flow state (clients,
  # codes, pending) was already external via store_dynamo.go.
  #
  # Verified 2026-05-17 with min/max=2: 20/20 MCP RPCs (10 tools/list,
  # 10 tools/call list_my_organizations) succeeded across both
  # replicas with no sticky cookies. Stickiness on the target group
  # is still configured for browser-based clients that route through
  # the same ALB.
  max_capacity       = 5
  min_capacity       = 2
  resource_id        = "service/${var.environment}/${aws_ecs_service.service.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "cpu" {
  name               = "${var.environment}-${var.name}-cpu-scaling"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.ecs.resource_id
  scalable_dimension = aws_appautoscaling_target.ecs.scalable_dimension
  service_namespace  = aws_appautoscaling_target.ecs.service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value       = 60
    scale_in_cooldown  = 300
    scale_out_cooldown = 60
  }
}

/********************************
 * CloudWatch Alarms
 ********************************/

resource "aws_cloudwatch_metric_alarm" "ecs_cpu" {
  alarm_name          = "${var.environment}-${var.name}-cpu"
  alarm_description   = "MCP service ${var.name} CPU utilization is high"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 2
  metric_name         = "CPUUtilization"
  namespace           = "AWS/ECS"
  period              = 60
  statistic           = "Average"
  threshold           = 80
  treat_missing_data  = "notBreaching"

  dimensions = {
    ClusterName = var.environment
    ServiceName = aws_ecs_service.service.name
  }

  alarm_actions = [data.terraform_remote_state.environment.outputs.sns.ops_alerts_topic_arn]

  tags = {
    Description = var.description
    Environment = var.environment
    Name        = var.name
  }
}

resource "aws_cloudwatch_metric_alarm" "alb_unhealthy_hosts" {
  alarm_name          = "${var.environment}-${var.name}-unhealthy-hosts"
  alarm_description   = "MCP service ${var.name} has unhealthy ALB targets"
  comparison_operator = "GreaterThanThreshold"
  evaluation_periods  = 1
  metric_name         = "UnHealthyHostCount"
  namespace           = "AWS/ApplicationELB"
  period              = 60
  statistic           = "Maximum"
  threshold           = 0
  treat_missing_data  = "notBreaching"

  dimensions = {
    LoadBalancer = data.terraform_remote_state.environment.outputs.public_alb.arn_suffix
    TargetGroup  = aws_lb_target_group.group.arn_suffix
  }

  alarm_actions = [data.terraform_remote_state.environment.outputs.sns.ops_alerts_topic_arn]

  tags = {
    Description = var.description
    Environment = var.environment
    Name        = var.name
  }
}
