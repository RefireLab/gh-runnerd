# Security

- **JIT, not long-lived registration tokens.** Each VM gets `generate-jitconfig` and is allowed one job. Idle VMs are recycled before the ~60 minute JIT window ends (default 45 minutes).
- **No host jobs.** Workflow code cannot see the Ubuntu host filesystem or host Docker.
- **No public registry port.** OCI pull is served on the bridge address only (default `10.87.0.1:42443`); VMs dial 443 which is redirected to it by an iptables rule scoped to `br-ghrunnerd`. The host's real port 443 is never claimed.
- **Pull-only.** VMs cannot push or overwrite pinned tags.
- **Private pull isolation.** Requests with `Authorization` are proxied upstream and not stored in the shared cache.
- **VM isolation.** iptables drops VM-to-VM traffic. Each VM has its own overlay disk, destroyed after the job.
- **Internal CA.** TLS for the registry uses a CA written by `gh-runnerd init`. Do not copy `state/ca/ca.key` into jobs or git.
- **Root on the host is expected** for KVM/bridge. Compromise of the host is compromise of the runner farm — treat this machine like any self-hosted runner host (org-level runner group restrictions, required reviewers, etc.).
