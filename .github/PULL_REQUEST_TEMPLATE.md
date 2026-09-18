<!--
Thanks for the pull request. Keep it focused: one change per pull request is
much easier to review than several bundled together.
-->

## What this changes

<!-- A short description of the behaviour before and after. -->

## Why

<!-- The problem it solves. Link the issue if there is one: Closes #123 -->

## How it was tested

<!--
Tests you added or ran, and any manual check against real traffic. If the
change is in the UI, say which terminal you tried it in.
-->

## Checklist

- [ ] `gofmt -l .` reports nothing
- [ ] `go vet ./...` is clean
- [ ] `go test ./...` passes
- [ ] New behaviour is covered by a test
- [ ] No new dependency was added (or the description explains why one is needed)
- [ ] `README.md` / `docs/guide.md` updated if flags, keys or the rule DSL changed
