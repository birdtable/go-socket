package listen

// sockCloexec is 0 on darwin, which has no SOCK_CLOEXEC: noneBound marks its
// socket after making it, under syscall.ForkLock.
const sockCloexec = 0
