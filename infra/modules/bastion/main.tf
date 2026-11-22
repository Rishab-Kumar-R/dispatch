data "aws_ssm_parameter" "al2023" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "this" {
  count              = var.enabled ? 1 : 0
  name               = "${var.name}-bastion"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy_attachment" "ssm" {
  count      = var.enabled ? 1 : 0
  role       = aws_iam_role.this[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "this" {
  count = var.enabled ? 1 : 0
  name  = "${var.name}-bastion"
  role  = aws_iam_role.this[0].name
}

resource "aws_security_group" "this" {
  count       = var.enabled ? 1 : 0
  name_prefix = "${var.name}-bastion-"
  vpc_id      = var.vpc_id

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_vpc_security_group_egress_rule" "all" {
  count             = var.enabled ? 1 : 0
  security_group_id = aws_security_group.this[0].id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_instance" "this" {
  count                  = var.enabled ? 1 : 0
  ami                    = nonsensitive(data.aws_ssm_parameter.al2023.value)
  instance_type          = var.instance_type
  subnet_id              = var.subnet_id
  vpc_security_group_ids = [aws_security_group.this[0].id]
  iam_instance_profile   = aws_iam_instance_profile.this[0].name

  metadata_options {
    http_tokens = "required"
  }

  root_block_device {
    encrypted = true
  }

  tags = {
    Name = "${var.name}-bastion"
  }

  lifecycle {
    ignore_changes = [ami]
  }
}
