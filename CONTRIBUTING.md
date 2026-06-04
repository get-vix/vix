# Contributing to Vix

Thank you for your interest in contributing to Vix! This guide will help you get started.

Come say hello and join the conversation in the **#contributors** channel on our Discord.

## Priority Areas

We value contributions in this order:

1. **Bug fixes** - especially crashes, data loss, and stack overflows
2. **Cross-platform compatibility** - macOS, Linux, and Windows support
3. **Security hardening** - shell injection, prompt injection, and privilege escalation prevention
4. **Performance and robustness** - error handling, retry logic, and resource management
5. **New tools and skills** - broadly useful additions
6. **Documentation** - fixes and clarifications

## Development Setup

### Prerequisites

- [Go 1.26+](https://go.dev/dl/)
- [Git](https://git-scm.com/)
- An API key for your LLM provider

### Getting Started

1. Fork and clone the repository:

```bash
git clone https://github.com/<your-username>/vix.git
cd vix
```

2. Build the project:

```bash
go build ./...
```

3. Run the tests:

```bash
go test ./...
```

## Development Workflow

1. Create a branch from `main`:

```bash
git checkout -b your-feature-name
```

2. Make your changes, ensuring the code compiles and tests pass.

3. Commit using conventional commit messages:

```
fix(daemon): prevent stack overflow in plan mode fallback
feat(ui): add token cost display to status bar
docs: clarify setup instructions
```

4. Push your branch and open a pull request against `main`.

## Commit Attribution

We dogfood Vix. If you use an AI coding agent to help with your contribution, please use **Vix** — and credit it as a co-author by appending this trailer to your commit messages:

```
Co-authored-by: vix <290354907+vix-agent@users.noreply.github.com>
```

We will reject any PR that credits Claude Code or another coding agent as a co-author. Use Vix instead and add Vix as the co-author. This is a convention, not an enforced check — there is no CI gate for it, so we rely on contributors to follow it in good faith.

## Code Guidelines

- **Keep it simple** - avoid over-engineering. The right amount of complexity is the minimum needed for the current task.
- **Security matters** - sanitize inputs, avoid shell injection, and validate at system boundaries.
- **Comments** - explain intent, not implementation. If the code needs a comment to explain what it does, consider rewriting it.
- **Test your changes** - add or update tests when fixing bugs or adding features.
- **No unnecessary dependencies** - prefer the standard library when reasonable.

## Pull Request Process

- Keep PRs focused on a single change.
- Provide a clear description of what your change does and why.
- Ensure all tests pass before requesting review.
- Be responsive to feedback during code review.

## Reporting Issues

When opening an issue, please include:

- Steps to reproduce the problem
- Expected vs actual behavior
- Your environment (OS, Go version, Vix version)
- Relevant logs or stack traces

## License

By contributing to Vix, you agree that your contributions will be licensed under the [GNU Affero General Public License v3.0](LICENSE).
