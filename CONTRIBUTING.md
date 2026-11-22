# Contributing

This guide explains how to contribute to Brickie. It follows the same rules as [hatchet-dev/hatchet](https://github.com/hatchet-dev/hatchet/blob/main/CONTRIBUTING.md).

## Guidelines

The following requirements apply to all contributions.

- You must be [vouched](#building-trust-vouching) before opening a pull request. Pull requests from unvouched authors are closed automatically.
- New contributors start with small pull requests and may have at most one open at a time. Changes to Brickie's core come after we have worked together on a few of those.
- Issues labeled [![good first issue](https://img.shields.io/github/labels/hatchet-dev/brickie/good%20first%20issue)](https://github.com/hatchet-dev/brickie/issues?q=is%3Aissue%20state%3Aopen%20label%3A%22good%20first%20issue%22) are reserved for first-time contributors.
- Pull requests should reference an issue labeled [![accepted](https://img.shields.io/github/labels/hatchet-dev/brickie/accepted)](https://github.com/hatchet-dev/brickie/issues?q=is%3Aissue%20state%3Aopen%20label%3Aaccepted). Open one first for anything non-trivial.
- Your GitHub account's [Activity Overview](https://docs.github.com/en/account-and-profile/how-tos/contribution-settings/showing-an-overview-of-your-activity-on-your-profile) must be public.
- AI usage must be disclosed and comply with [AI_POLICY.md](./AI_POLICY.md) (see [AI Usage](#ai-usage)).

### AI Usage

Pull requests, issues, and discussions that use AI require explicit disclosure. For example:

> <details open id="ai-disclosure">
> <summary><b>🤖 AI Disclosure</b></summary>
>
> - [x] _I acknowledge that an LLM was used in the creation of this Pull Request, in accordance with Brickie's [AI_POLICY.md](./AI_POLICY.md)._
>
> - **Details**: Claude Code was used to draft the docs page.
>
> </details>

## Building Trust (Vouching)

Brickie is maintained by a small team, and reviewing a change to a sandbox provider takes far longer than writing one. Open source works on trust, and AI has made it too easy to produce plausible-looking but low-quality contributions for us to trust by default. We use [vouch](https://github.com/mitchellh/vouch) to keep an explicit list of trusted contributors in [`.github/VOUCHED.td`](.github/VOUCHED.td). Pull requests from anyone not on that list are closed automatically.

**To get vouched:**

1. Open a [Vouch Request discussion](https://github.com/hatchet-dev/brickie/discussions/new?category=vouch-request) describing the first change you want to make and why. Follow the template.
2. Keep it short, and write it in your own voice. Do not have an AI write it.
3. A maintainer will comment `!vouch` if approved. This opens a pull request adding you to the list. Once it is merged, you can open pull requests.

**Once vouched, start small.** We would rather build a relationship over a few small, focused pull requests (documentation, examples, chart values, well-scoped bug fixes) than review a large change from someone we have not worked with. Keep those early pull requests light on AI. We want to see that you understand the code you are changing and can discuss and revise it yourself in review. A pull request that is mostly generated does not tell us that, even when it is disclosed in line with [AI_POLICY.md](./AI_POLICY.md). Changes to Brickie's core (the engine, the drivers, the store layout, the wire contract with the SDK, the TLS path, and CI or release tooling) come after that.

Contributors who repeatedly ignore these guidelines, submit low-quality work, or do not disclose AI usage will be **denounced**. The list is public, and all future pull requests from denounced accounts are closed automatically.

<details>
<summary>For maintainers</summary>

Anyone with write access can manage the list by commenting on a discussion, issue, or pull request:

- `!vouch` vouches for the author, `!vouch @user` for any user.
- `!denounce <reason>` or `!denounce @user <reason>` blocks a user.
- `!unvouch` or `!unvouch @user` removes a user from the list.

Each command opens a pull request against `main` with the change to `.github/VOUCHED.td`. It takes effect once another maintainer merges it.

</details>

## Getting Started

New to Brickie? Start with the [architecture](https://hatchet-dev.github.io/brickie/architecture) and [security](https://hatchet-dev.github.io/brickie/security) pages, then [AGENTS.md](./AGENTS.md), which holds the working rules for the repository.

## Development Environment Setup

- [Go 1.27+](https://go.dev/doc/install)
- [Node.js 24](https://nodejs.org/en/download) and [pnpm](https://pnpm.io/installation)
- [Docker](https://docs.docker.com/engine/install/)
- [kind](https://kind.sigs.k8s.io/) and [Helm](https://helm.sh/docs/intro/install/) for the Kubernetes driver
- [pre-commit](https://pre-commit.com/), installed into a virtualenv with `task pre-commit-install`

## Pull Requests

You need to be [vouched](#building-trust-vouching) before opening a pull request. Then, to keep the review queue focused:

1. **Find or open an issue.** Check the [backlog](https://github.com/hatchet-dev/brickie/issues) for a related issue, or open one describing the change, and say you would like to take it on.
2. **Wait for the `accepted` label.** It means the team agrees with the change and nobody else is working on it.
3. **Link the issue** from the PR description with a closing keyword, for example `Closes #123`.

Before opening the PR:

```bash
go build ./... && go vet ./...
pnpm install && pnpm -r typecheck
hack/run-demo.sh          # the Docker driver, end to end
hack/e2e-kind.sh          # the Kubernetes driver on a kind cluster, when a driver changed
```

A user-facing change gets a line under `Unreleased` in [`CHANGELOG.md`](./CHANGELOG.md) and, where it changes behaviour, a docs update under `docs/content`. Never edit a released changelog section.

### Titles

Pull request titles follow [conventional commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short description>
```

Scopes are `engine`, `docker`, `kubernetes`, `sandboxd`, `tls`, `store`, `cli`, `docs`, `ci`, `release`. Leave the scope empty for a cross-cutting change, for example `chore: fix typo in README`.

## Questions

Open an issue, or come find us in the `#contributing` channel on [Discord](https://hatchet.run/discord?utm_source=brickie-github&utm_medium=referral&utm_campaign=brickie-launch&utm_content=contributing).
