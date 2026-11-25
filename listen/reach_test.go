package listen

import (
	"net"
	"net/http"
	"testing"
)

// dial connects to a listener of the test's own, until the test ends.
func dial(t *testing.T, network, addr string) (net.Conn, error) {
	t.Helper()
	return (&net.Dialer{}).DialContext(t.Context(), network, addr)
}

// getVia asks the test server for a URL through c, and gives up with the test.
func getVia(t *testing.T, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
