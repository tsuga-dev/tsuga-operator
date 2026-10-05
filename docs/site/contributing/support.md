# Help and releases

## Ask for help or report a bug

Start with [troubleshooting](../operations/troubleshooting.md), then search the repository's [issues](https://github.com/tsuga-dev/tsuga-operator/issues). If none matches, [open an issue on GitHub](https://github.com/tsuga-dev/tsuga-operator/issues) with:

- Operator image tag or commit, Kubernetes version, and OpenTelemetry Operator version if relevant.
- The command or change that triggered the problem and what you expected.
- A minimal, redacted resource manifest and its status.
- Relevant error messages, with credentials and private data removed.

For a documentation correction, use the edit button on the affected page or open a pull request. Include which step was confusing or failed.

## Release notes

[GitHub Releases](https://github.com/tsuga-dev/tsuga-operator/releases) is the source for release notes and versioned `install.yaml` assets. [Commit history](https://github.com/tsuga-dev/tsuga-operator/commits/main/) shows changes that may not yet be released.

The website documents the main branch. Use a release's source tag to check behavior when it differs from your installed version.

## Glossary

| Term | Meaning |
| --- | --- |
| CRD | CustomResourceDefinition: extends Kubernetes with a resource type |
| CR | Custom resource: an instance of that type |
| Reconciliation | Bringing managed state in line with a resource's spec |
| Operation key | Credential for Tsuga management API operations |
| Ingestion key | Credential for exporting telemetry to Tsuga |
| OTLP | OpenTelemetry Protocol for transporting telemetry |
| Agent | Per-node collector for node data and application telemetry |
| Gateway | Collector for cluster-wide Kubernetes data in this operator |
| Target Allocator | Component that assigns scrape targets to collector replicas |
| Finalizer | Kubernetes deletion hook used to complete remote cleanup |
