# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security Advisories at https://github.com/excelano/nved/security/advisories/new. If you would rather not use GitHub, email david.anderson@excelano.com instead. I aim to respond within seven days.

Please do not open public issues for security problems.

## Supported versions

The latest release receives security fixes. Older versions are not supported.

## What nved can access

nved is a text editor that runs locally on your machine. It reads the file you open and writes only the file you save (the one named on the command line, or the name you give a `save` command). It makes no network connections of any kind — there is no telemetry, no analytics, no update check, and no remote logging. There is no daemon, no server component, and no mounted filesystem. nved cannot touch any file your account cannot already read or write directly.

## What nved stores

Undo history and scrollback live in memory only and are gone when you exit. nved writes no configuration file, no command history, and no cache anywhere on disk; the sole thing it ever writes is the file you explicitly save, with the normal `0644` file mode. The only third-party dependencies are `golang.org/x/term` and `golang.org/x/sys` (both maintained under the Go project) for raw-mode terminal handling.

## Verifying releases

Every GitHub release includes a `checksums.txt` file listing SHA-256 hashes of all binary archives. Verify any download before running it:

    sha256sum nved_1.0.0_linux_amd64.tar.gz
    # compare against the value in checksums.txt

Release artifacts are built by GitHub Actions from a tagged commit using the goreleaser configuration in this repo. The workflow and build configuration are public and auditable.
