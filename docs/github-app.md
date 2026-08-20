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

`github.runner_group_id` defaults to `1` (the Default runner group). `init` lists groups from the API and accepts a name or id. You can also set the id in config.

## Webhook

Point the App webhook at a URL GitHub can reach, forwarded to `webhook.listen` (default `127.0.0.1:8080`) + `webhook.path` (`/webhook`).

Put the webhook secret in `webhook.secret` / `GH_RUNNERD_WEBHOOK_SECRET`.

If GitHub cannot reach the host (lab NAT), leave the webhook unused. gh-runnerd **polls** `actions/runs?status=queued` for `github.scope = repo`. For org scope, set `github.poll_repos = ["org/repo1", "org/repo2"]` or expose the webhook.

## Labels

Default runner label: `gh-runnerd`. `init` asks for a comma-separated list; keep `gh-runnerd` unique so these VMs do not take ordinary `self-hosted` jobs.

```yaml
runs-on: gh-runnerd
```

JIT configs are created with those labels (plus any extra labels from the queued job) so GitHub can assign the job. Idle VMs are recycled after 45 minutes because JIT registrations expire in about an hour.

## Fine-grained PAT

Runners in a single repository — token with:

- Repository permissions: **Actions: Read-only**, **Administration: Read and write**, Metadata: Read-only (added automatically)

Runners shared by an organization — token with:

- Resource owner: **the organization** (not your personal account), and you must be an org admin
- Organization permissions: **Self-hosted runners: Read and write**
- Repository permissions: **Actions: Read-only** on the repos gh-runnerd polls for queued jobs (`github.poll_repos`)

Classic tokens: `repo` scope for repo runners, plus `admin:org` for org runners.

Export as `GH_RUNNERD_GITHUB_TOKEN` or paste into the `init` wizard, which verifies runner access live.
