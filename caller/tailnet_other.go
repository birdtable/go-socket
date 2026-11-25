//go:build !unix

package caller

// socketOwner is ErrNoOwner: the owner of a socket is not read here.
func socketOwner(string) (uint32, bool, error) { return 0, false, ErrNoOwner }
