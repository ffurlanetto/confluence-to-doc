# Operations

Everything needed to run the application in production, after [configuration](../configuration.md) and the
[Helm chart](../../charts/confluence-to-doc/).

| Page | For |
| --- | --- |
| [Monitoring](monitoring.md) | Getting the metrics into Prometheus, the alerts, the dashboard |
| [Service level objectives](slo.md) | What "working" means, measured, and the error budget policy |
| [Runbooks](runbooks.md) | What to do when an alert fires, and the routine procedures |
| [Backup and recovery](backup-and-recovery.md) | What to back up, RPO/RTO, restoring, disaster scenarios |
| [Threat model](threat-model.md) | Assets, actors, threats and their mitigations, accepted risks |
| [Confluence compatibility](confluence-compatibility.md) | What is used from Confluence Data Center 9.x, and checking an instance |
| [Reference documents](reference-documents.md) | The page tree and checklist to accept a change that alters documents |
| [Load testing](load-testing.md) | Measuring capacity before going live |

The administration console (`/admin`, members of `OIDC_ADMIN_GROUPS`) covers the day-to-day: the export
queue of every user with cancel and retry, the users with blocking, usage figures, the company Word
template, and the audit trail.
