# Releasing RCSS

Releases are built by [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`)
in CI (`.github/workflows/release.yml`). Don't build release artifacts by hand.

## Cutting a release

```bash
git switch main && git pull
go test -race ./...
git tag -a v0.2.0 -m "v0.2.0"
git push origin v0.2.0
```

The tag push publishes a GitHub Release with:

- `rcss_<ver>_<os>_<arch>.tar.gz` / `.zip` for Linux, macOS, Windows (amd64, arm64)
- `.deb`, `.rpm`, `.apk`, and Arch `.pkg.tar.zst` packages (Linux)
- `rcss-<ver>.tar.gz` source tarball and `checksums.txt`

`install.sh` / `install.ps1` always fetch the latest published release.

Preview everything locally (nothing is published) with:

```bash
goreleaser release --snapshot --clean   # output in dist/, incl. dist/aur/*.pkgbuild
```

## Package repositories (optional, one-time setup)

Each publisher runs only when its GitHub Actions secret is set
(**Settings → Secrets and variables → Actions**); without it, that step is
skipped and the release still succeeds.

### AUR — `rcss-bin` (prebuilt) and `rcss` (from source)

1. Create an account on <https://aur.archlinux.org> and add an SSH **public**
   key to it (a dedicated key without a passphrase):
   `ssh-keygen -t ed25519 -f aur_rcss -N "" -C "rcss goreleaser"`
2. Add the **private** key (`aur_rcss`) as the `AUR_KEY` secret.
3. That's it: the AUR creates `rcss-bin` and `rcss` on the first push, and each
   release pushes an updated `PKGBUILD` and `.SRCINFO` to both.

To test a generated PKGBUILD before publishing, run a snapshot and build it
with `makepkg` from `dist/aur/` (point `source` at the local `dist/` files).

### Homebrew — `brew install --cask dougmb/tap/rcss`

1. Create a public repo `dougmb/homebrew-tap`.
2. Create a fine-grained token with *Contents: read & write* on that repo and
   add it as `HOMEBREW_TAP_TOKEN`.

### Scoop — `scoop install rcss`

1. Create a public repo `dougmb/scoop-bucket`.
2. Create a fine-grained token with *Contents: read & write* on that repo and
   add it as `SCOOP_BUCKET_TOKEN`.

### Distro packaging by others

Packagers can build from the source tarball with just Go:

```bash
go build -trimpath -ldflags "-X github.com/dougmb/rcss/tui.version=<ver>" -o rcss ./cmd/rcss
```

The only runtime dependency is `rclone`. `rcss version` prints the embedded version.
