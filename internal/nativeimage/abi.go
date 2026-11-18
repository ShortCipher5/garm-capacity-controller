package nativeimage

import (
	"debug/elf"
	"io"
)

// elfVersions is sampled from the same hash-verified ELF descriptor as the
// ordinary inventory. This verifies declared GNU version requirements only,
// not loader execution, symbol semantics or complete application ABI behavior.
type elfVersions struct {
	defined map[string]bool
	needed  map[string][]string
}

func readVersions(r io.ReaderAt) (elfVersions, error) {
	f, e := elf.NewFile(r)
	if e != nil {
		return elfVersions{}, ErrImage
	}
	defer f.Close()
	out := elfVersions{defined: map[string]bool{}, needed: map[string][]string{}}
	if f.SectionByType(elf.SHT_GNU_VERDEF) != nil {
		defs, e := f.DynamicVersions()
		if e != nil || len(defs) == 0 {
			return elfVersions{}, ErrImage
		}
		for _, d := range defs {
			if d.Name == "" || out.defined[d.Name] {
				return elfVersions{}, ErrImage
			}
			out.defined[d.Name] = true
		}
	}
	if f.SectionByType(elf.SHT_GNU_VERNEED) != nil {
		needs, e := f.DynamicVersionNeeds()
		if e != nil || len(needs) == 0 {
			return elfVersions{}, ErrImage
		}
		for _, n := range needs {
			if n.Name == "" || len(n.Needs) == 0 || out.needed[n.Name] != nil {
				return elfVersions{}, ErrImage
			}
			for _, v := range n.Needs {
				if v.Dep == "" {
					return elfVersions{}, ErrImage
				}
				out.needed[n.Name] = append(out.needed[n.Name], v.Dep)
			}
		}
	}
	return out, nil
}
func versionsSatisfied(m Manifest, actual map[string]elfVersions) bool {
	for _, v := range actual {
		for lib, needs := range v.needed {
			candidates := map[string]bool{}
			for p := range m.Files {
				if libraryCandidate(p, lib, m.Architecture) {
					candidates[p] = true
				}
			}
			for p := range m.Links {
				if libraryCandidate(p, lib, m.Architecture) {
					r, e := m.resolve(p)
					if e != nil {
						return false
					}
					candidates[r] = true
				}
			}
			if len(candidates) == 0 {
				return false
			}
			// Every possible loader candidate must satisfy the requirement. We do not
			// guess a search order that could select an older duplicate library.
			for p := range candidates {
				provider, ok := actual[p]
				if !ok {
					return false
				}
				for _, need := range needs {
					if !provider.defined[need] {
						return false
					}
				}
			}
		}
	}
	return true
}
