package dockerx

import (
	"bufio"
	"bytes"
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
	Image  string `json:"Image"`
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

type ContainerCreateRequest struct {
	Image        string               `json:"Image"`
	Env          []string             `json:"Env,omitempty"`
	Cmd          []string             `json:"Cmd,omitempty"`
	Labels       map[string]string    `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{}  `json:"ExposedPorts,omitempty"`
	HostConfig   *ContainerHostConfig `json:"HostConfig,omitempty"`
}

type ContainerHostConfig struct {
	AutoRemove bool `json:"AutoRemove,omitempty"`
}

type ContainerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings,omitempty"`
}

type ImageInspect struct {
	ID string `json:"Id"`
}

func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/containers/json", url.Values{"all": {"1"}}), nil)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/containers/"+id+"/json", nil), nil)
	if err != nil {
		return ContainerInspect{}, err
	}
	var out ContainerInspect
	if err := c.doJSON(req, &out); err != nil {
		return ContainerInspect{}, err
	}
	return out, nil
}

func (c *Client) InspectImage(ctx context.Context, name string) (ImageInspect, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/images/"+name+"/json", nil), nil)
	if err != nil {
		return ImageInspect{}, err
	}
	var out ImageInspect
	if err := c.doJSON(req, &out); err != nil {
		return ImageInspect{}, err
	}
	return out, nil
}

func (c *Client) CreateContainer(ctx context.Context, name string, req ContainerCreateRequest) (ContainerCreateResponse, error) {
	u := c.mustURL("/containers/create", url.Values{"name": {name}})
	body, err := json.Marshal(req)
	if err != nil {
		return ContainerCreateResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return ContainerCreateResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	var out ContainerCreateResponse
	if err := c.doJSON(httpReq, &out); err != nil {
		return ContainerCreateResponse{}, err
	}
	return out, nil
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mustURL("/containers/"+id+"/start", nil), nil)
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
		return fmt.Errorf("POST /containers/%s/start: %s", id, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) RemoveContainer(ctx context.Context, id string, force bool) error {
	values := url.Values{}
	if force {
		values.Set("force", "1")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.mustURL("/containers/"+id, values), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("DELETE /containers/%s: %s", id, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) ConnectNetwork(ctx context.Context, network, container string) error {
	body, err := json.Marshal(map[string]string{"Container": container})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.mustURL("/networks/"+network+"/connect", nil), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("POST /networks/%s/connect: %s", network, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) WatchEvents(ctx context.Context, since time.Time, onEvent func(Event)) error {
	values := url.Values{}
	values.Set("type", "container")
	values.Set("since", fmt.Sprint(since.Unix()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.mustURL("/events", values), nil)
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

func (c *Client) mustURL(path string, query url.Values) string {
	u := *c.base
	u.Path = path
	if query != nil {
		u.RawQuery = query.Encode()
	}
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
