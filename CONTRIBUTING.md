# Contributing to Watchdog

Please note that this project is released with a [Code of Conduct](./CODE_OF_CONDUCT.md); by participating, you agree to uphold its terms.

## How to Contribute

1. Fork the repository by clicking the fork button in the top-right corner.
2. Clone your fork locally and set the upstream to the official Watchdog repository.
3. Follow the setup instructions [here](./README.md#setup).
4. Keep changes scoped:
   - Use a dedicated branch per bug fix or feature.
   - This allows you to open and manage multiple pull requests in parallel.
5. Stay up to date:
   - Sync your fork regularly with the upstream `main` branch to avoid conflicts.
6. **Open an issue first** for new features or significant changes so we can discuss design and direction.
7. Follow [code guidelines](#code-guidelines) and write conventional commit messages.
8. Submit a Pull Request
   - Clearly describe the problem being solved.
   - Reference related issues in your commits or PR description (e.g., `Fixes #42: Improve GTFS parsing`).

## Code Guidelines

- Run Watchdog using one of the methods described [here](./README.md#running) and ensure there are no build or runtime errors.

Use the `make` targets rather than the bare `go` commands — CI runs the same
targets, so a green local run and a green CI run mean the same thing.

- **Formatting**: Run the Go formatter before committing.

```bash
  make fmt        # rewrites files
  make fmt-check  # reports what needs rewriting; this is what CI runs
```

- **Linting**: Run `go vet` to catch common mistakes.

```bash
  make vet
```

  This vets with `-tags=integration` so the integration tests are type-checked
  too. A bare `go vet ./...` skips them entirely, and they will rot unnoticed.

- **Testing**:
  - Write unit tests for new functionality.
  - Ensure the full test suite passes with:

```bash
  make test
```

  CI runs the suite under the race detector, which is worth doing locally
  before pushing anything that touches shared state:

```bash
  go test -race ./...
```
