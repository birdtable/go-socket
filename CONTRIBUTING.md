# Contributing

Thanks for helping. Issues and pull requests are both welcome.

## Running the checks

The checks need Go and nothing else: every tool is pinned in its own modfile under `tools/`.

```sh
go tool -modfile=tools/task.mod task hooks      # once per clone: installs the pre-push hook
go tool -modfile=tools/task.mod task pre-commit # quick: format, build, vet, the second consumer's tests
go tool -modfile=tools/task.mod task pre-push   # what the hook runs: race tests, lint, API check and more
```

`go test ./...` runs the tests alone.

## Pull requests

- Keep a pull request to one change, and say in its description what it changes and why.
- Add tests with the change. A bug fix comes with a test that fails without it.
- Add a change file for anything a user of the library would notice:
  `go tool -modfile=tools/changie.mod changie new`. The release turns these into
  [CHANGELOG.md](CHANGELOG.md).
- Start a commit's first line with the area it touches (`listen:`, `peer:`, `docs:`), and keep it
  short.
- Don't skip the hooks. If a check is wrong, fix the check in the same pull request.

## Defaults

Every exported constant or default a pull request adds is asked one question: would another
program, for another business, ever want a different value? If not, it is a fact of the system
or the protocol and it stays. If so, the caller passes it, with no default. The one default
allowed is a safe one: it fails closed, it is documented, and the caller can override it.

`internal/second` is a second consumer, a print shop's kiosk built on values unlike any other
program's. It uses every exported package, so a value it cannot set shows up as a failing test.

## Security problems

Please don't open an issue for a vulnerability. [SECURITY.md](SECURITY.md) says how to report
one privately.
