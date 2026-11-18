// Package rw sends metrics to a Prometheus server using the standard "remote write" protocol,
// so a laptop can push its readings straight to Prometheus without any other software installed.
package rw

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// StaleNaN is the special value that tells Prometheus a series has ended, so it disappears from queries
// straight away instead of lingering for five minutes.
var StaleNaN = math.Float64frombits(0x7ff0000000000002)

type Label struct{ Name, Value string }

type Point struct {
	TS    int64 // unix milliseconds
	Value float64
}

// Series is one metric (Name) with its labels and one or more readings, oldest first.
type Series struct {
	Name    string
	Labels  []Label
	Samples []Point
}

// Encode builds the request body Prometheus expects: protobuf, then snappy-compressed.
func Encode(series []Series) []byte {
	var req []byte
	for _, s := range series {
		labels := append([]Label{{"__name__", s.Name}}, s.Labels...)
		sort.Slice(labels, func(i, j int) bool { return labels[i].Name < labels[j].Name }) // Prometheus requires sorted labels

		var ts []byte
		for _, l := range labels {
			var lb []byte
			lb = protowire.AppendTag(lb, 1, protowire.BytesType)
			lb = protowire.AppendString(lb, l.Name)
			lb = protowire.AppendTag(lb, 2, protowire.BytesType)
			lb = protowire.AppendString(lb, l.Value)
			ts = protowire.AppendTag(ts, 1, protowire.BytesType)
			ts = protowire.AppendBytes(ts, lb)
		}
		for _, p := range s.Samples {
			var sb []byte
			sb = protowire.AppendTag(sb, 1, protowire.Fixed64Type)
			sb = protowire.AppendFixed64(sb, math.Float64bits(p.Value))
			sb = protowire.AppendTag(sb, 2, protowire.VarintType)
			sb = protowire.AppendVarint(sb, uint64(p.TS))
			ts = protowire.AppendTag(ts, 2, protowire.BytesType)
			ts = protowire.AppendBytes(ts, sb)
		}
		req = protowire.AppendTag(req, 1, protowire.BytesType)
		req = protowire.AppendBytes(req, ts)
	}
	return snappy.Encode(nil, req)
}

type Auth struct{ Username, Password, Token string }

// Push sends the series. It returns the HTTP status (0 if the server could not be reached)
// and, for a failure, a short message from the server.
func Push(ctx context.Context, client *http.Client, url string, auth Auth, series []Series) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(Encode(series)))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")
	req.Header.Set("User-Agent", "stream-agent")
	switch {
	case auth.Username != "":
		req.SetBasicAuth(auth.Username, auth.Password)
	case auth.Token != "":
		req.Header.Set("Authorization", "Bearer "+auth.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<12))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, strings.TrimSpace(string(body)), fmt.Errorf("server said %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.StatusCode, "", nil
}
