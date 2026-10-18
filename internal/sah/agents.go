// Package sah reads the integration registry out of the shipped `sah` CLI.
//
// The Hub's "Connect an agent" page must never list a client the CLI does not
// have, so upstream derives that list from the CLI's own INTEGRATIONS registry
// instead of keeping a second copy (connect_service.py:43-63). Go cannot import
// a stdlib-only Python script, so this package reads the same registry by
// parsing the class declarations: the file ships in our repo and its shapes are
// pinned by tests against it.
package sah

import (
	"os"
	"regexp"
	"strings"
	"sync"
)

// Agent is one client `sah` can wire to the Hub, in wire order.
type Agent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Command      string `json:"command"`
	WritesConfig bool   `json:"writes_config"`
}

var (
	classRe = regexp.MustCompile(`^class\s+(\w+)\s*(?:\(([\w.]+)\))?\s*:`)
	// name, display[, binary] = "id", "Display Name"[, "binary"]
	nameTupleRe = regexp.MustCompile(`^    name\s*,\s*display(?:\s*,\s*\w+)*\s*=\s*"([^"]*)"\s*,\s*"([^"]*)"`)
	nameRe      = regexp.MustCompile(`^    name\s*=\s*"([^"]*)"`)
	displayRe   = regexp.MustCompile(`^    display\s*=\s*"([^"]*)"`)
	kindRe      = regexp.MustCompile(`^    kind\s*=\s*"([^"]*)"`)
	boolRe      = regexp.MustCompile(`^    (\w+)\s*=\s*(True|False)\b`)
	ctorRe      = regexp.MustCompile(`(\w+)\s*\(\s*\)`)
)

type attrs struct {
	name, display, kind                     string
	launchable, writesConfig, opensFolder   bool
	hasName, hasDisplay, hasKind, hasLaunch bool
	hasWrites, hasOpens                     bool
	base                                    string
}

func (a attrs) merge(o attrs) attrs {
	if o.hasName {
		a.name, a.hasName = o.name, true
	}
	if o.hasDisplay {
		a.display, a.hasDisplay = o.display, true
	}
	if o.hasKind {
		a.kind, a.hasKind = o.kind, true
	}
	if o.hasLaunch {
		a.launchable, a.hasLaunch = o.launchable, true
	}
	if o.hasWrites {
		a.writesConfig, a.hasWrites = o.writesConfig, true
	}
	if o.hasOpens {
		a.opensFolder, a.hasOpens = o.opensFolder, true
	}
	return a
}

// defaults is the Integration base class (sah/sah:785-796).
func defaults() attrs {
	return attrs{
		kind:       "CLI",
		launchable: true,
	}
}

// Parse extracts the INTEGRATIONS list from the source of `sah`.
func Parse(src []byte) []Agent {
	classes := make(map[string]attrs)
	order := []string{} // constructor names, in registry order

	var current string
	inRegistry := false
	for _, line := range strings.Split(string(src), "\n") {
		if !inRegistry && isRegistryStart(line) {
			inRegistry = true
			continue
		}
		if inRegistry {
			if strings.TrimSpace(line) == "]" {
				inRegistry = false
				continue
			}
			for _, m := range ctorRe.FindAllStringSubmatch(line, -1) {
				order = append(order, m[1])
			}
			continue
		}

		if m := classRe.FindStringSubmatch(line); m != nil {
			current = m[1]
			a := attrs{base: m[2]}
			classes[current] = a
			continue
		}
		// A module-level statement ends the class body.
		if line != "" && !strings.HasPrefix(line, " ") {
			current = ""
			continue
		}
		if current == "" {
			continue
		}
		if m := nameTupleRe.FindStringSubmatch(line); m != nil {
			a := classes[current]
			a.name, a.hasName = m[1], true
			a.display, a.hasDisplay = m[2], true
			classes[current] = a
			continue
		}
		if m := nameRe.FindStringSubmatch(line); m != nil {
			a := classes[current]
			a.name, a.hasName = m[1], true
			classes[current] = a
			continue
		}
		if m := displayRe.FindStringSubmatch(line); m != nil {
			a := classes[current]
			a.display, a.hasDisplay = m[1], true
			classes[current] = a
			continue
		}
		if m := kindRe.FindStringSubmatch(line); m != nil {
			a := classes[current]
			a.kind, a.hasKind = m[1], true
			classes[current] = a
			continue
		}
		if m := boolRe.FindStringSubmatch(line); m != nil {
			value := m[2] == "True"
			a := classes[current]
			switch m[1] {
			case "launchable":
				a.launchable, a.hasLaunch = value, true
			case "writes_config":
				a.writesConfig, a.hasWrites = value, true
			case "opens_folder":
				a.opensFolder, a.hasOpens = value, true
			default:
				continue
			}
			classes[current] = a
		}
	}

	resolve := func(name string) attrs {
		seen := make(map[string]bool)
		chain := []attrs{}
		for name != "" && !seen[name] {
			seen[name] = true
			a, ok := classes[name]
			if !ok {
				break
			}
			chain = append(chain, a)
			name = a.base
		}
		out := defaults()
		for i := len(chain) - 1; i >= 0; i-- {
			out = out.merge(chain[i])
		}
		return out
	}

	agents := make([]Agent, 0, len(order))
	seen := make(map[string]bool)
	for _, ctor := range order {
		a := resolve(ctor)
		if a.name == "" || seen[a.name] {
			continue
		}
		seen[a.name] = true
		command := ""
		switch {
		case !a.launchable:
			command = "sah " + a.name + " --install"
		case a.opensFolder:
			command = "sah " + a.name + " ."
		default:
			command = "sah " + a.name
		}
		agents = append(agents, Agent{
			ID:           a.name,
			Name:         a.display,
			Kind:         a.kind,
			Command:      command,
			WritesConfig: a.writesConfig,
		})
	}
	return agents
}

func isRegistryStart(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "INTEGRATIONS") {
		return false
	}
	return strings.HasSuffix(trimmed, "[")
}

var (
	once   sync.Once
	cached []Agent
)

// Load parses the sah CLI at path once and caches the result, like upstream's
// functools.cache. Any failure yields an empty list: the Connect page falls
// back to its built-in entries rather than failing the whole request.
func Load(path string) []Agent {
	once.Do(func() {
		src, err := os.ReadFile(path)
		if err != nil {
			return
		}
		cached = Parse(src)
	})
	return cached
}
