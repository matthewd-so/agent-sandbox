package host

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Flavor is a parsed flavors/<name>/flavor file.
type Flavor struct {
	Name string
	Desc string
	Deps []string
}

// ReadFlavor parses the simple KEY="value" assignments of a flavor file.
func ReadFlavor(dirs []string, name string) (Flavor, error) {
	for _, d := range dirs {
		f, err := os.Open(filepath.Join(d, name, "flavor"))
		if err != nil {
			continue
		}
		defer f.Close()
		fl := Flavor{Name: name}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			switch strings.TrimSpace(k) {
			case "DESC":
				fl.Desc = v
			case "DEPS":
				fl.Deps = strings.Fields(v)
			}
		}
		return fl, sc.Err()
	}
	return Flavor{}, fmt.Errorf("unknown flavor %q", name)
}

// Closure returns name and all of its transitive dependencies, dependencies
// first, matching the order the bash CLI and sbx-init use.
func Closure(dirs []string, name string) ([]string, error) {
	var (
		order []string
		done  = map[string]bool{}
		path  []string
	)
	var visit func(string) error
	visit = func(n string) error {
		if done[n] {
			return nil
		}
		for _, p := range path {
			if p == n {
				return fmt.Errorf("flavor dependency cycle: %s -> %s", strings.Join(path, " -> "), n)
			}
		}
		fl, err := ReadFlavor(dirs, n)
		if err != nil {
			if len(path) > 0 {
				return fmt.Errorf("%w (needed by %s)", err, path[len(path)-1])
			}
			return err
		}
		path = append(path, n)
		for _, d := range fl.Deps {
			if err := visit(d); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		done[n] = true
		order = append(order, n)
		return nil
	}
	if err := visit(name); err != nil {
		return nil, err
	}
	return order, nil
}

// Published returns the current build of a flavor in the pool, or "".
func Published(pool, name string) string {
	b, err := os.ReadFile(filepath.Join(pool, "ptr", name))
	if err != nil {
		return ""
	}
	build, _, _ := strings.Cut(string(b), "\n")
	build = strings.TrimSpace(build)
	if st, err := os.Stat(filepath.Join(pool, "store", build)); err != nil || !st.IsDir() {
		return ""
	}
	return build
}
