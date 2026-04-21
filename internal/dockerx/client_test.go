package dockerx

import (
	"net/url"
	"testing"
)

func TestMustURL(t *testing.T) {
	c := &Client{base: &url.URL{Scheme: "http", Host: "docker"}}

	tests := []struct {
		name  string
		path  string
		query url.Values
		want  string
	}{
		{
			name: "plain path",
			path: "/containers/abc/json",
			want: "http://docker/containers/abc/json",
		},
		{
			name:  "query params",
			path:  "/containers/json",
			query: url.Values{"all": {"1"}},
			want:  "http://docker/containers/json?all=1",
		},
		{
			name:  "events query",
			path:  "/events",
			query: url.Values{"since": {"123"}, "type": {"container"}},
			want:  "http://docker/events?since=123&type=container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.mustURL(tt.path, tt.query)
			if got != tt.want {
				t.Fatalf("mustURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
