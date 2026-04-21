package dockerx

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	http *http.Client
	base *url.URL
}

func New(socketPath string, timeout time.Duration) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	return &Client{
		http: &http.Client{Transport: tr, Timeout: timeout},
		base: &url.URL{Scheme: "http", Host: "docker"},
	}
}

type ContainerSummary struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
}

type PortBinding struct {
	HostIp   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type ContainerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Hostname     string              `json:"Hostname"`
		Image        string              `json:"Image"`
		Labels       map[string]string   `json:"Labels"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
	State struct {
		Running bool   `json:"Running"`
		Status  string `json:"Status"`
	} `json:"State"`
	HostConfig struct {
		NetworkMode  string                   `json:"NetworkMode"`
		PortBindings map[string][]PortBinding `json:"PortBindings"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		IPAddress string                   `json:"IPAddress"`
		Ports     map[string][]PortBinding `json:"Ports"`
		Networks  map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

type Event struct {
	Status string `json:"status"`
	Type   string `json:"Type"`
	Action string `json:"Action"`
	ID     string `json:"id"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	Time     int64 `json:"time"`
	TimeNano int64 `json:"timeNano"`
}

func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/containers/json?all=1"), nil)
	if err != nil {
		return nil, err
	}
	var out []ContainerSummary
	if err := c.doJSON(req, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerInspect, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/containers/"+id+"/json"), nil)
	if err != nil {
		return ContainerInspect{}, err
	}
	var out ContainerInspect
	if err := c.doJSON(req, &out); err != nil {
		return ContainerInspect{}, err
	}
	return out, nil
}

func (c *Client) WatchEvents(ctx context.Context, since time.Time, onEvent func(Event)) error {
	values := url.Values{}
	values.Set("type", "container")
	values.Set("since", fmt.Sprint(since.Unix()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/events?"+values.Encode()), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("docker events: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var ev Event
		if err := dec.Decode(&ev); err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		onEvent(ev)
	}
}

func (c *Client) mustURL(path string) string {
	u := *c.base
	u.Path = path
	return u.String()
}

func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
