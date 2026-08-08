# Releasing

Bedrock is two Go modules in one repository:

| module | path | tag format |
|---|---|---|
| `github.com/Jack4Code/bedrock` | `/` | `v1.2.3` |
| `github.com/Jack4Code/bedrock/grpc` | `/grpc` | `grpc/v1.2.3` |

The second imports the first, which makes the release order load-bearing. Getting it wrong ships a module nobody can install, and it is invisible from inside the repository.

## Why the order matters

`grpc/go.mod` contains both of these:

```go
replace github.com/Jack4Code/bedrock => ../

require github.com/Jack4Code/bedrock v0.5.0
```

**A `replace` directive is only honoured in the main module.** When you build inside `grpc/`, the replace applies and the parent resolves to the working tree — which is what makes a change spanning both modules testable before either is tagged. When somebody else imports `bedrock/grpc`, their module is the main one, the replace is ignored, and the `require` line is the whole story.

So the `require` must name a bedrock version that actually exists on the proxy. If it names a version that was never tagged, every consumer gets:

```
go: github.com/Jack4Code/bedrock/grpc imports
    github.com/Jack4Code/bedrock: github.com/Jack4Code/bedrock@v0.5.0:
    invalid version: unknown revision v0.5.0
```

and there is nothing they can do about it except add a `replace` of their own.

## The order

1. **Land the change** on `main`, both modules, with `grpc/go.mod` requiring the bedrock version you are about to tag, and a [CHANGELOG.md](CHANGELOG.md) entry for it. The changelog is where anyone deciding whether to upgrade will look first — behavioural changes that do not break compilation belong there, because a green `go build` will not surface them.

2. **Tag the parent first.**

   ```bash
   git tag v0.5.0
   git push origin v0.5.0
   ```

3. **Wait for the proxy to have it.** Until this succeeds, step 4 cannot be verified:

   ```bash
   GOPROXY=https://proxy.golang.org go list -m github.com/Jack4Code/bedrock@v0.5.0
   ```

4. **Check the submodule is consumable**, which builds a throwaway module that imports `bedrock/grpc` the way a real consumer would — with the `replace` ignored:

   ```bash
   scripts/check_submodule.sh
   ```

5. **Tag the submodule.**

   ```bash
   git tag grpc/v0.1.0
   git push origin grpc/v0.1.0
   ```

## Bumping the require

When the `grpc` module needs an API that only exists in an unreleased parent — as it did for `bedrock.Server` — update the `require` to the version you intend to tag *before* you tag it, in the same commit as the change. `scripts/check_submodule.sh` will report the version as unpublished; that is expected and it exits non-zero only for a placeholder or malformed version, so CI stays green while you finish the release.

## What not to do

- **Don't delete the `replace`.** It is what makes local development and CI work across both modules. It is harmless to consumers, who ignore it.
- **Don't leave the `require` at a placeholder.** `v0.0.0-00010101000000-000000000000` is what `go mod tidy` writes when it resolves a dependency purely through a `replace`. It builds fine in-tree and cannot be installed by anyone.
- **Don't tag `grpc/` first.** Its `require` would point at a version that does not exist yet.
