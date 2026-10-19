package llamaswap

import (
	"context"

	"gospark-hub/internal/bench"
)

// Measure runs the shared benchmark against the sidecar and adds the memory of
// the model's own process or container, which only the sidecar knows how to
// find (its /running lists the cmd and the proxy URL).
func (c *Client) Measure(ctx context.Context, model string) (bench.Measurement, error) {
	m, err := bench.Measure(ctx, c.Base, c.Key, model)
	if err != nil {
		return m, err
	}
	m.Target = model
	m.MemoryBytes, m.MemorySource = c.modelMemory(ctx, model)
	return m, nil
}

// modelMemory picks the accounting that matches how the model runs: a
// container started by its cmd, or a host process owning the proxy port.
func (c *Client) modelMemory(ctx context.Context, model string) (int64, string) {
	running, err := c.Running(ctx)
	if err != nil {
		return 0, ""
	}
	for _, r := range running {
		if r.Model != model {
			continue
		}
		if name := bench.ContainerFromCmd(r.Cmd); name != "" {
			if size, source := bench.ContainerMemory(ctx, name); size > 0 {
				return size, source
			}
		}
		if port := bench.ProxyPort(r.Proxy); port != 0 {
			return bench.ProcessMemory(bench.PIDForPort(port))
		}
		return 0, ""
	}
	return 0, ""
}
