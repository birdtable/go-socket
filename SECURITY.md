# Security

go-socket decides who may reach a socket and who is at the other end of it, so we take reports
seriously.

## Reporting a vulnerability

Please report it privately through GitHub's
[private vulnerability reporting](https://github.com/birdtable/go-socket/security/advisories/new).
Don't open a public issue, pull request or discussion for it.

Tell us the go-socket version, the system and its version, and which guarantee breaks: what a
caller or a peer can do that the documentation says it cannot. A short program that shows it
helps most.

## Supported versions

The latest minor release gets security fixes. go-socket is v0, so a fix ships in a new release
rather than as a patch to an older minor.
