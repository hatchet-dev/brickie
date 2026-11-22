# Security policy

## Supported versions

Fixes go into the next release. There are no backports before 1.0, so keep the provider and the Helm chart current. A vulnerability triaged as high or critical gets a release on its own.

## Reporting a vulnerability

Use the ["Report a vulnerability"](https://github.com/hatchet-dev/brickie/security/advisories/new) button under the Security tab of this repository. It opens a private channel with the maintainers. If you cannot use it, write to [security@hatchet.run](mailto:security@hatchet.run).

If you used AI to find the bug or write the report, say so. The advisory form has a checkbox for it below the CWE field.

There is no bug bounty program. Reports are read and answered all the same.

## Scope

The provider, its images, the Helm chart and the `brickie-sandbox` wire contract. The `@hatchet-dev/sandbox` SDK lives in [hatchet-dev/hatchet](https://github.com/hatchet-dev/hatchet/security) and follows its policy.

The trust boundaries and the controls that hold them are described in the [security model](https://hatchet-dev.github.io/brickie/security), together with how to verify image signatures and SBOMs.
