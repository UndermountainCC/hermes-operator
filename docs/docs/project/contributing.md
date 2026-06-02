# Contributing

The contribution flow, DCO requirements, and local test commands are documented in [`CONTRIBUTING.md`](https://github.com/UndermountainCC/hermes-operator/blob/main/CONTRIBUTING.md) in the repo root. That file is the source of truth.

## TL;DR

```bash
git clone https://github.com/UndermountainCC/hermes-operator
cd hermes-operator
make test-unit  # fast
make test       # envtest integration (downloads kube-apiserver + etcd)
make lint
```

For non-trivial changes, **open an issue first** to discuss approach. PR titles use [Conventional Commits](https://www.conventionalcommits.org/); commits need DCO sign-off (`git commit -s`).

## Where to file

- **Bugs / feature requests**: [GitHub Issues](https://github.com/UndermountainCC/hermes-operator/issues).
- **Security**: do not file publicly. See [`SECURITY.md`](https://github.com/UndermountainCC/hermes-operator/blob/main/SECURITY.md) for the responsible-disclosure process.
- **Discussion**: open an issue with the `discussion` label.
