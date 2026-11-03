package release

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Latest returns the tag of the latest release of repository, read from where
// <repository>/releases/latest redirects. c must not follow redirects.
func Latest(ctx context.Context, c *http.Client, repository string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, repository+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	_ = res.Body.Close()
	location, err := res.Location()
	if err != nil {
		return "", fmt.Errorf("%s/releases/latest answered HTTP %d without a redirect", repository, res.StatusCode)
	}
	_, escaped, ok := strings.Cut(location.Path, "/releases/tag/")
	tag, err := url.PathUnescape(escaped)
	if !ok || err != nil || tag == "" {
		return "", fmt.Errorf("%s/releases/latest redirects to %s, not to a release tag", repository, location)
	}
	return tag, nil
}
