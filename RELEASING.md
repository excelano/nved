# Releasing nved

The release loop lives in `~/notes/releasing.md` — the ordered steps, the apt
step, the winget submission, the spent-tag rule, and the standing facts about
tokens and secrets. Failure recipes are in `~/notes/build_release_gotchas.md`.
This file carries what is true of nved and not of its siblings.

| | |
|---|---|
| Loop | goreleaser |
| `apt-ship` argument | `nved` |
| winget package | `Excelano.nved` |
| Windows assets | `nved_<version>_windows_amd64.zip` **and** `nved_<version>_windows_arm64.zip` |

**The release builds** platform archives for Linux, macOS, and Windows on both
architectures, the two `.deb` packages, `checksums.txt`, the Homebrew formula,
and the GitHub Release, all in one job.

**nved ships two Windows archives, and both belong in the manifest.** Pass both
URLs on one `komac update` and it writes an `Installers` entry per architecture:

```sh
komac update Excelano.nved --version 1.2.3 \
  --urls https://github.com/excelano/nved/releases/download/v1.2.3/nved_1.2.3_windows_amd64.zip \
         https://github.com/excelano/nved/releases/download/v1.2.3/nved_1.2.3_windows_arm64.zip \
  --submit
```

Dropping the arm64 URL silently ships an x64-only manifest. A two-architecture
manifest also takes about twice as long to validate.

**The installers are not release assets here.** `install.sh` and `uninstall.sh`
are served from the rolling `main` branch, which is what the README's curl
one-liner points at, so a fix to either takes effect immediately and
independently of a release. The trade is that a user cannot pin the installer
itself to a version, only the tag it fetches via `NVED_VERSION`.

**The release workflow has no `workflow_dispatch` fallback,** so it only ever
runs from a real tag push. If a tag lands without triggering it, the fix is to
delete and re-push the tag by hand, or add the dispatch input.
