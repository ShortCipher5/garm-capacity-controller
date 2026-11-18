package nativeimage

import (
	"debug/elf"
	"io"
)

// elfVersions is sampled from the same hash-verified ELF descriptor as the
// ordinary inventory. This verifies declared GNU versions and mandatory
// versioned symbol names/types, not unversioned lookup/interposition, loader
// execution, symbol semantics or complete application ABI behavior.
type elfVersions struct {
	defined map[string]bool
	needed  map[string][]string
	imports map[string][]versionedSymbol
	exports map[string]map[string]elf.SymType
}

type versionedSymbol struct {
	name, version string
	kind          elf.SymType
}

func readVersions(r io.ReaderAt) (elfVersions, error) {
	f, e := elf.NewFile(r)
	if e != nil {
		return elfVersions{}, ErrImage
	}
	defer f.Close()
	out := elfVersions{defined: map[string]bool{}, needed: map[string][]string{}, imports: map[string][]versionedSymbol{}, exports: map[string]map[string]elf.SymType{}}
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
	if f.SectionByType(elf.SHT_DYNSYM) != nil {
		symbols, err := f.DynamicSymbols()
		if err != nil {
			return elfVersions{}, ErrImage
		}
		for _, sym := range symbols {
			if sym.Name == "" {
				continue
			}
			bind, kind := elf.ST_BIND(sym.Info), elf.ST_TYPE(sym.Info)
			if sym.Section == elf.SHN_UNDEF {
				// Optional weak imports may legitimately remain unresolved.
				// Unversioned lookup/interposition needs a complete loader model.
				if bind == elf.STB_GLOBAL && sym.Version != "" {
					if sym.Library == "" || out.needed[sym.Library] == nil {
						return elfVersions{}, ErrImage
					}
					out.imports[sym.Library] = append(out.imports[sym.Library], versionedSymbol{sym.Name, sym.Version, kind})
				}
				continue
			}
			// GNU ELF binding 10 is STB_GNU_UNIQUE (not named by debug/elf).
			visibility := elf.ST_VISIBILITY(sym.Other)
			if sym.Version == "" || (bind != elf.STB_GLOBAL && bind != elf.STB_WEAK && bind != elf.SymBind(10)) || (visibility != elf.STV_DEFAULT && visibility != elf.STV_PROTECTED) {
				continue
			}
			if !out.defined[sym.Version] {
				return elfVersions{}, ErrImage
			}
			if out.exports[sym.Version] == nil {
				out.exports[sym.Version] = map[string]elf.SymType{}
			}
			if prior, exists := out.exports[sym.Version][sym.Name]; exists && prior != kind {
				return elfVersions{}, ErrImage
			}
			out.exports[sym.Version][sym.Name] = kind
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
				for _, need := range v.imports[lib] {
					kind, ok := provider.exports[need.version][need.name]
					if !ok || !symbolTypeMatches(need.kind, kind) {
						return false
					}
				}
			}
		}
	}
	return true
}

// GNU indirect functions satisfy function imports; other incompatible types
// (notably TLS versus ordinary objects) do not. This is not a calling ABI proof.
func symbolTypeMatches(want, have elf.SymType) bool {
	return want == elf.STT_NOTYPE || want == have || want == elf.STT_FUNC && have == elf.STT_GNU_IFUNC
}
