terraform {
  # The s3 backend block must be declared to engage the S3 backend;
  # the bucket / key / region are filled in at `terraform init` time
  # via the workflow's -backend-config flags. Without this block,
  # Terraform silently falls back to LOCAL state — meaning every CI
  # run loses prior state and tries to recreate everything (which
  # then collides with the resources AWS already has from the
  # previous run).
  backend "s3" {
    use_lockfile = true
  }
}
