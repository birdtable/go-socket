package listen

import "syscall"

// sockCloexec makes noneBound's socket close-on-exec in the call that makes
// it, so no fork, Go's or one made by C code, can take it.
const sockCloexec = syscall.SOCK_CLOEXEC
