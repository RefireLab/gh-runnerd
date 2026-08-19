# GitHub App

A GitHub App is the supported production credential. A PAT works for a single repository and is fine for the first green check.

## App permissions

Repository (or organization) permissions:

- **Actions**: Read
- **Administration**: Read and write (repo scope; needed to create JIT runners)
- **Metadata**: Read
- **Packages**: Read (if jobs pull private GHCR images via `GITHUB_TOKEN` inside the job — this is the workflow token, not the App)

Organization:

- **Self-hosted runners**: Read and write
- **Administration** as required by your org's runner groups

Subscribe to the **Workflow job** webhook event.

## JIT runner groups

`github.runner_group_id` defaults to `1` (the Default runner group). If your org uses custom groups, put that group's id in config.

## Webhook

Point the App webhook at a URL GitHub can reach, forwarded to `webhook.listen` (default `127.0.0.1:8080`) + `webhook.path` (`/webhook`).

Put the webhook secret in `webhook.secret` / `GH_RUNNERD_WEBHOOK_SECRET`.

If GitHub cannot reach the host (lab NAT), leave the webhook unused. gh-runnerd **polls** `actions/runs?status=queued` for `github.scope = repo`. For org scope, set `github.poll_repos = ["org/repo1", "org/repo2"]` or expose the webhook.

## Labels

Default runner label: `gh-runnerd`.

```yaml
runs-on: gh-runnerd
```

JIT configs are created with those labels (plus any extra labels from the queued job) so GitHub can assign the job. Idle VMs are recycled after 45 minutes because JIT registrations expire in about an hour.

## Fine-grained PAT (dev)

For a single repo, a PAT with:

- Actions: read
- Administration: read/write (or the "Self-hosted runners" permission where available)

Export as `GH_RUNNERD_GITHUB_TOKEN` and pass `--token` to `init`.
