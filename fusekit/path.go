package fusekit

import (
	"fmt"
	pathpkg "path"
	"strings"
)

// NormalizeVirtualPath cleans a path in the virtual tree.
func NormalizeVirtualPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = pathpkg.Clean(p)
	if p == "." {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("virtual path must be absolute: %s", p)
	}
	return p, nil
}

func mustNormalizeVirtualPath(p string) string {
	normalized, err := NormalizeVirtualPath(p)
	if err != nil {
		return "/"
	}
	return normalized
}

func pathMatchesBase(base, p string) bool {
	if base == "/" {
		return true
	}
	return p == base || strings.HasPrefix(p, base+"/")
}

func pathBelow(parent, child string) bool {
	if parent == "/" {
		return child != "/"
	}
	return strings.HasPrefix(child, parent+"/")
}

func relativeVirtual(base, p string) (string, bool) {
	if !pathMatchesBase(base, p) {
		return "", false
	}
	if base == "/" {
		return strings.TrimPrefix(p, "/"), true
	}
	if p == base {
		return "", true
	}
	return strings.TrimPrefix(p, base+"/"), true
}

func parentPath(p string) string {
	if p == "/" {
		return "/"
	}
	parent := pathpkg.Dir(p)
	if parent == "." {
		return "/"
	}
	return parent
}

func baseName(p string) string {
	if p == "/" {
		return ""
	}
	return pathpkg.Base(p)
}

func joinVirtual(parent, name string) string {
	if parent == "/" {
		return mustNormalizeVirtualPath("/" + name)
	}
	return mustNormalizeVirtualPath(parent + "/" + name)
}

func nextSegmentBelow(dir, child string) (string, bool) {
	if dir == child || !pathBelow(dir, child) {
		return "", false
	}
	rel := strings.TrimPrefix(child, "/")
	if dir != "/" {
		rel = strings.TrimPrefix(child, dir+"/")
	}
	segment, _, _ := strings.Cut(rel, "/")
	return segment, segment != ""
}
