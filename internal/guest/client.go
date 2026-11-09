package guest

import (
	"net/http"
	"net/url"
	"time"
)

func NewClient(wslcPath string) *http.Client {
	dialer := &StdioDialer{WSLCPath: wslcPath}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		DisableCompression:    true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 0,
		ForceAttemptHTTP2:     false,
		Proxy:                 func(*http.Request) (*url.URL, error) { return nil, nil },
	}
	return &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
