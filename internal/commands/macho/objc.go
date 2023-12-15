// Package macho provides functionality for parsing Mach-O files.
package macho

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2/quick"
	"github.com/apex/log"
	"github.com/blacktop/go-macho"
	"github.com/blacktop/go-macho/types/objc"
	"github.com/blacktop/ipsw/internal/swift"
	"github.com/blacktop/ipsw/pkg/dyld"
)

// ErrNoObjc is returned when a MachO does not contain objc info
var ErrNoObjc = errors.New("macho does not contain objc info")

// ObjcConfig for MachO ObjC parser
type ObjcConfig struct {
	Name     string
	Verbose  bool
	Addrs    bool
	Headers  bool
	ObjcRefs bool
	Deps     bool
	Demangle bool

	IpswVersion string

	Color  bool
	Theme  string
	Output string
}

// Imports represents the imported symbols, local symbols, classes, and protocols for a ObjC header
type Imports struct {
	Imports []string
	Locals  []string
	Classes []string
	Protos  []string
}

func (i *Imports) uniq(foundation map[string][]string) {
	slices.Sort(i.Imports)
	slices.Sort(i.Locals)
	slices.Sort(i.Classes)
	slices.Sort(i.Protos)
	i.Imports = slices.Compact(i.Imports)
	i.Locals = slices.Compact(i.Locals)
	i.Classes = slices.Compact(i.Classes)
	i.Protos = slices.Compact(i.Protos)
	i.Locals = slices.DeleteFunc(i.Locals, func(l string) bool {
		l = strings.TrimSuffix(l, "-Protocol.h")
		l = strings.TrimSuffix(l, ".h")
		_, foundC := slices.BinarySearch(foundation["classes"], l)
		_, foundP := slices.BinarySearch(foundation["protocols"], l)
		return foundC || foundP
	})
	// remove Foundation classes
	i.Classes = slices.DeleteFunc(i.Classes, func(c string) bool {
		_, found := slices.BinarySearch(foundation["classes"], c)
		return found
	})
	// remove Foundation protocols
	i.Protos = slices.DeleteFunc(i.Protos, func(p string) bool {
		_, found := slices.BinarySearch(foundation["protocols"], p)
		return found
	})
}

type headerInfo struct {
	FileName      string
	IpswVersion   string
	BuildVersions []string
	SourceVersion string
	IsUmbrella    bool
	Name          string
	Imports       Imports
	Object        string
}

// ObjC represents a MachO ObjC parser
type ObjC struct {
	conf  *ObjcConfig
	file  *macho.File
	cache *dyld.File
	deps  []*macho.File

	foundation map[string][]string
}

// NewObjC returns a new MachO ObjC parser instance
func NewObjC(file *macho.File, dsc *dyld.File, conf *ObjcConfig) (*ObjC, error) {
	if !file.HasObjC() {
		return nil, ErrNoObjc
	}

	o := &ObjC{
		conf:       conf,
		file:       file,
		cache:      dsc,
		foundation: make(map[string][]string),
	}

	if o.conf.Deps {
		if dsc == nil {
			return nil, fmt.Errorf("dyld shared cache is required to dump imported private frameworks")
		}
		var deps []string
		for _, imp := range file.ImportedLibraries() {
			if o.conf.Headers {
				// only dump private frameworks when generating headers
				if strings.Contains(imp, "PrivateFrameworks") {
					deps = append(deps, imp)
				}
			} else {
				deps = append(deps, imp)
			}
		}
		for _, imageName := range deps {
			img, err := o.cache.Image(imageName)
			if err != nil {
				return nil, err
			}
			m, err := img.GetMacho()
			if err != nil {
				return nil, err
			}
			o.deps = append(o.deps, m)
		}
	}

	return o, nil
}

// DumpClass returns a ObjC classes matching a given pattern from a MachO
func (o *ObjC) DumpClass(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("failed to compile regex: %v", err)
	}
	ms := []*macho.File{o.file}
	if o.conf.Deps {
		ms = append(ms, o.deps...)
	}
	for _, m := range ms {
		classes, err := m.GetObjCClasses()
		if err != nil {
			if errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			return err
		}

		slices.SortStableFunc(classes, func(a, b objc.Class) int {
			return cmp.Compare(a.Name, b.Name)
		})

		for _, class := range classes {
			if re.MatchString(class.Name) {
				if o.conf.Color {
					if o.conf.Addrs {
						quick.Highlight(os.Stdout, swift.DemangleBlob(class.WithAddrs()), "objc", "terminal256", o.conf.Theme)
					} else {
						quick.Highlight(os.Stdout, swift.DemangleBlob(class.Verbose()), "objc", "terminal256", o.conf.Theme)
					}
					quick.Highlight(os.Stdout, "\n/****************************************/\n\n", "objc", "terminal256", o.conf.Theme)
				} else {
					if o.conf.Addrs {
						fmt.Println(swift.DemangleBlob(class.WithAddrs()))
					} else {
						fmt.Println(swift.DemangleBlob(class.Verbose()))
					}
				}
			}
		}
	}
	return nil
}

// DumpProtocol returns a ObjC protocols matching a given pattern from a MachO
func (o *ObjC) DumpProtocol(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("failed to compile regex: %v", err)
	}
	ms := []*macho.File{o.file}
	if o.conf.Deps {
		ms = append(ms, o.deps...)
	}
	for _, m := range ms {
		protos, err := m.GetObjCProtocols()
		if err != nil {
			if errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			return err
		}

		slices.SortStableFunc(protos, func(a, b objc.Protocol) int {
			return cmp.Compare(a.Name, b.Name)
		})
		seen := make(map[uint64]bool)

		for _, proto := range protos {
			if re.MatchString(proto.Name) {
				if o.conf.Color {
					if o.conf.Addrs {
						quick.Highlight(os.Stdout, swift.DemangleBlob(proto.WithAddrs()), "objc", "terminal256", o.conf.Theme)
					} else {
						quick.Highlight(os.Stdout, swift.DemangleBlob(proto.Verbose()), "objc", "terminal256", o.conf.Theme)
					}
					quick.Highlight(os.Stdout, "\n/****************************************/\n\n", "objc", "terminal256", o.conf.Theme)
				} else {
					if o.conf.Addrs {
						fmt.Println(swift.DemangleBlob(proto.WithAddrs()))
					} else {
						fmt.Println(swift.DemangleBlob(proto.Verbose()))
					}
				}
				seen[proto.Ptr] = true
			}
		}
	}
	return nil
}

// DumpCategory returns a ObjC categories matching a given pattern from a MachO
func (o *ObjC) DumpCategory(pattern string) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("failed to compile regex: %v", err)
	}
	ms := []*macho.File{o.file}
	if o.conf.Deps {
		ms = append(ms, o.deps...)
	}
	for _, m := range ms {
		cats, err := m.GetObjCCategories()
		if err != nil {
			if errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			return err
		}

		slices.SortStableFunc(cats, func(a, b objc.Category) int {
			return cmp.Compare(a.Name, b.Name)
		})

		for _, cat := range cats {
			if re.MatchString(cat.Name) {
				if o.conf.Color {
					if o.conf.Addrs {
						quick.Highlight(os.Stdout, swift.DemangleBlob(cat.WithAddrs()), "objc", "terminal256", o.conf.Theme)
					} else {
						quick.Highlight(os.Stdout, swift.DemangleBlob(cat.Verbose()), "objc", "terminal256", o.conf.Theme)
					}
					quick.Highlight(os.Stdout, "\n/****************************************/\n\n", "objc", "terminal256", o.conf.Theme)
				} else {
					if o.conf.Addrs {
						fmt.Println(swift.DemangleBlob(cat.WithAddrs()))
					} else {
						fmt.Println(swift.DemangleBlob(cat.Verbose()))
					}
				}
			}
		}
	}
	return nil
}

// Dump outputs ObjC info from a MachO
func (o *ObjC) Dump() error {
	ms := []*macho.File{o.file}
	if o.conf.Deps {
		ms = append(ms, o.deps...)
	}
	for _, m := range ms {
		if info, err := m.GetObjCImageInfo(); err == nil {
			fmt.Println(info.Flags)
		} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
			return err
		}
		if o.conf.Verbose {
			fmt.Println(m.GetObjCToc())
		}
		/* ObjC Protocols */
		if protos, err := m.GetObjCProtocols(); err == nil {
			slices.SortStableFunc(protos, func(a, b objc.Protocol) int {
				return cmp.Compare(a.Name, b.Name)
			})
			seen := make(map[uint64]bool)
			for _, proto := range protos {
				if _, ok := seen[proto.Ptr]; !ok { // prevent displaying duplicates
					if o.conf.Verbose {
						if o.conf.Color {
							if o.conf.Addrs {
								quick.Highlight(os.Stdout, swift.DemangleBlob(proto.WithAddrs()), "objc", "terminal256", o.conf.Theme)
							} else {
								quick.Highlight(os.Stdout, swift.DemangleBlob(proto.Verbose()), "objc", "terminal256", o.conf.Theme)
							}
							quick.Highlight(os.Stdout, "\n/****************************************/\n\n", "objc", "terminal256", o.conf.Theme)
						} else {
							if o.conf.Addrs {
								fmt.Println(swift.DemangleBlob(proto.WithAddrs()))
							} else {
								fmt.Println(swift.DemangleBlob(proto.Verbose()))
							}
						}
					} else {
						if o.conf.Color {
							quick.Highlight(os.Stdout, proto.String()+"\n", "objc", "terminal256", o.conf.Theme)
						} else {
							fmt.Println(proto.String())
						}
					}
					seen[proto.Ptr] = true
				}
			}
		} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
			return err
		}
		/* ObjC Classes */
		if classes, err := m.GetObjCClasses(); err == nil {
			slices.SortStableFunc(classes, func(a, b objc.Class) int {
				return cmp.Compare(a.Name, b.Name)
			})
			for _, class := range classes {
				if o.conf.Verbose {
					if o.conf.Color {
						if o.conf.Addrs {
							quick.Highlight(os.Stdout, swift.DemangleBlob(class.WithAddrs()), "objc", "terminal256", o.conf.Theme)
						} else {
							quick.Highlight(os.Stdout, swift.DemangleBlob(class.Verbose()), "objc", "terminal256", o.conf.Theme)
						}
						quick.Highlight(os.Stdout, "\n/****************************************/\n\n", "objc", "terminal256", o.conf.Theme)
					} else {
						if o.conf.Addrs {
							fmt.Println(swift.DemangleBlob(class.WithAddrs()))
						} else {
							fmt.Println(swift.DemangleBlob(class.Verbose()))
						}
					}
				} else {
					if o.conf.Color {
						quick.Highlight(os.Stdout, class.String()+"\n", "objc", "terminal256", o.conf.Theme)
					} else {
						fmt.Println(class.String())
					}
				}
			}
		} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
			return err
		}
		/* ObjC Categories */
		if cats, err := m.GetObjCCategories(); err == nil {
			slices.SortStableFunc(cats, func(a, b objc.Category) int {
				return cmp.Compare(a.Name, b.Name)
			})
			for _, cat := range cats {
				if o.conf.Verbose {
					if o.conf.Color {
						if o.conf.Addrs {
							quick.Highlight(os.Stdout, swift.DemangleBlob(cat.WithAddrs()), "objc", "terminal256", o.conf.Theme)
						} else {
							quick.Highlight(os.Stdout, swift.DemangleBlob(cat.Verbose()), "objc", "terminal256", o.conf.Theme)
						}
						quick.Highlight(os.Stdout, "\n/****************************************/\n\n", "objc", "terminal256", o.conf.Theme)
					} else {
						if o.conf.Addrs {
							fmt.Println(swift.DemangleBlob(cat.WithAddrs()))
						} else {
							fmt.Println(swift.DemangleBlob(cat.Verbose()))
						}
					}
				} else {
					if o.conf.Color {
						quick.Highlight(os.Stdout, cat.String()+"\n", "objc", "terminal256", o.conf.Theme)
					} else {
						fmt.Println(cat.String())
					}
				}
			}
		} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
			return err
		}
		if o.conf.ObjcRefs {
			if protRefs, err := m.GetObjCProtoReferences(); err == nil {
				fmt.Printf("\n@protocol refs\n")
				for off, prot := range protRefs {
					fmt.Printf("0x%011x => 0x%011x: %s\n", off, prot.Ptr, prot.Name)
				}
			} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			if clsRefs, err := m.GetObjCClassReferences(); err == nil {
				fmt.Printf("\n@class refs\n")
				for off, cls := range clsRefs {
					fmt.Printf("0x%011x => 0x%011x: %s\n", off, cls.ClassPtr, cls.Name)
				}
			} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			if supRefs, err := m.GetObjCSuperReferences(); err == nil {
				fmt.Printf("\n@super refs\n")
				for off, sup := range supRefs {
					fmt.Printf("0x%011x => 0x%011x: %s\n", off, sup.ClassPtr, sup.Name)
				}
			} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			if selRefs, err := m.GetObjCSelectorReferences(); err == nil {
				fmt.Printf("\n@selectors refs\n")
				for off, sel := range selRefs {
					fmt.Printf("0x%011x => 0x%011x: %s\n", off, sel.VMAddr, sel.Name)
				}
			} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
			if o.conf.Verbose {
				if classes, err := m.GetObjCClassNames(); err == nil {
					fmt.Printf("\n@objc_classname\n")
					for vmaddr, className := range classes {
						fmt.Printf("0x%011x: %s\n", vmaddr, className)
					}
				} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
					return err
				}
				if methods, err := m.GetObjCMethodNames(); err == nil {
					fmt.Printf("\n@objc_methname\n")
					for vmaddr, method := range methods {
						fmt.Printf("0x%011x: %s\n", vmaddr, method)
					}
				} else if !errors.Is(err, macho.ErrObjcSectionNotFound) {
					return err
				}
			}
		}
	}
	return nil
}

// Headers outputs ObjC class-dump headers from a MachO
func (o *ObjC) Headers() error {

	// scan DSC for Foundation/CoreFoundation classes and protocols
	if err := o.scanFoundation(); err != nil {
		return err
	}

	writeHeaders := func(m *macho.File) error {
		var headers []string

		if !m.HasObjC() {
			return nil
		}

		if id := m.DylibID(); id != nil {
			o.conf.Name = filepath.Base(id.Name)
		}
		var buildVersions []string
		if bvers := m.GetLoadsByName("LC_BUILD_VERSION"); len(bvers) > 0 {
			for _, bv := range bvers {
				buildVersions = append(buildVersions, bv.String())
			}
		}
		var sourceVersion string
		if svers := m.GetLoadsByName("LC_SOURCE_VERSION"); len(svers) > 0 {
			sourceVersion = svers[0].String()
		}

		imps, err := o.processForwardDeclarations(m)
		if err != nil {
			return err
		}

		/* generate ObjC class headers */
		classes, err := m.GetObjCClasses()
		if err != nil {
			if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
		}
		slices.SortStableFunc(classes, func(a, b objc.Class) int {
			return cmp.Compare(a.Name, b.Name)
		})
		for _, class := range classes {
			var props []string
			for _, prop := range class.Props {
				props = append(props, prop.Name)
			}
			slices.Sort(props)
			// remove ivars that are properties
			class.Ivars = slices.DeleteFunc(class.Ivars, func(i objc.Ivar) bool {
				// return slices.Contains(props, i.Name) || slices.Contains(props, strings.TrimPrefix(i.Name, "_")) TODO: use this instead
				return slices.Contains(props, strings.TrimPrefix(i.Name, "_"))
			})
			// remove methods that are property getter/setter
			class.InstanceMethods = slices.DeleteFunc(class.InstanceMethods, func(m objc.Method) bool {
				return slices.Contains(props, m.Name) || slices.Contains(props, transformSetter(m.Name))
			})
			fname := filepath.Join(o.conf.Output, o.conf.Name, class.Name+".h")
			if err := writeHeader(&headerInfo{
				FileName:      fname,
				IpswVersion:   o.conf.IpswVersion,
				BuildVersions: buildVersions,
				SourceVersion: sourceVersion,
				Name:          class.Name,
				Imports:       imps[class.Name],
				Object:        swift.DemangleBlob(class.Verbose()),
			}); err != nil {
				return err
			}
			headers = append(headers, filepath.Base(fname))
		}

		/* generate ObjC protocol headers */
		protos, err := m.GetObjCProtocols()
		if err != nil {
			if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
		}
		slices.SortStableFunc(protos, func(a, b objc.Protocol) int {
			return cmp.Compare(a.Name, b.Name)
		})
		seen := make(map[uint64]bool)
		for _, proto := range protos {
			if _, found := slices.BinarySearch(o.foundation["protocols"], proto.Name); found {
				continue // skip Foundation protocols
			}
			if _, ok := seen[proto.Ptr]; !ok { // prevent displaying duplicates

				fname := filepath.Join(o.conf.Output, o.conf.Name, proto.Name+"-Protocol.h")
				if err := writeHeader(&headerInfo{
					FileName:      fname,
					IpswVersion:   o.conf.IpswVersion,
					BuildVersions: buildVersions,
					SourceVersion: sourceVersion,
					Name:          proto.Name,
					Imports:       imps[proto.Name],
					Object:        swift.DemangleBlob(proto.Verbose()),
				}); err != nil {
					return err
				}
				headers = append(headers, filepath.Base(fname))
				seen[proto.Ptr] = true
			}
		}

		/* generate ObjC category headers */
		cats, err := m.GetObjCCategories()
		if err != nil {
			if !errors.Is(err, macho.ErrObjcSectionNotFound) {
				return err
			}
		}
		slices.SortStableFunc(cats, func(a, b objc.Category) int {
			return cmp.Compare(a.Name, b.Name)
		})
		for _, cat := range cats {
			fname := filepath.Join(o.conf.Output, o.conf.Name, cat.Name+".h")
			if cat.Class != nil && cat.Class.Name != "" {
				fname = filepath.Join(o.conf.Output, o.conf.Name, cat.Class.Name+"+"+cat.Name+".h")
			}
			if err := writeHeader(&headerInfo{
				FileName:      fname,
				IpswVersion:   o.conf.IpswVersion,
				BuildVersions: buildVersions,
				SourceVersion: sourceVersion,
				Name:          cat.Name,
				Imports:       imps[cat.Name],
				Object:        swift.DemangleBlob(cat.Verbose()),
			}); err != nil {
				return err
			}
			headers = append(headers, filepath.Base(fname))
		}

		/* generate umbrella header */
		if len(headers) > 0 {
			var umbrella string
			if slices.Contains(headers, o.conf.Name+".h") {
				umbrella = o.conf.Name + "-Umbrella"
			} else {
				umbrella = o.conf.Name
			}

			for i, header := range headers {
				headers[i] = "#import \"" + header + "\""
			}

			fname := filepath.Join(o.conf.Output, o.conf.Name, umbrella+".h")
			if err := writeHeader(&headerInfo{
				FileName:      fname,
				IpswVersion:   o.conf.IpswVersion,
				BuildVersions: buildVersions,
				SourceVersion: sourceVersion,
				IsUmbrella:    true,
				Name:          strings.ReplaceAll(umbrella, "-", "_"),
				Object:        strings.Join(headers, "\n") + "\n",
			}); err != nil {
				return err
			}
		}

		return nil
	}

	if len(o.deps) > 0 {
		for _, m := range o.deps {
			if err := writeHeaders(m); err != nil {
				return err
			}
		}
	}

	return writeHeaders(o.file)
}

/* utils */

func writeHeader(hinfo *headerInfo) error {
	var out string

	out += fmt.Sprintf(
		"//\n"+
			"//   Generated by https://github.com/blacktop/ipsw (%s)\n"+
			"//\n"+
			"//    - LC_BUILD_VERSION:  %s\n"+
			"//    - LC_SOURCE_VERSION: %s\n"+
			"//\n"+
			"#ifndef %s_h\n"+
			"#define %s_h\n",
		hinfo.IpswVersion,
		strings.Join(hinfo.BuildVersions, "\n//    - LC_BUILD_VERSION:  "),
		hinfo.SourceVersion,
		hinfo.Name,
		hinfo.Name)
	if !hinfo.IsUmbrella {
		out += fmt.Sprintf("@import Foundation;\n")
	}
	out += fmt.Sprintf("\n")
	if len(hinfo.Imports.Locals) > 0 {
		for _, local := range hinfo.Imports.Locals {
			out += fmt.Sprintf("#include \"%s\"\n", local)
		}
		out += fmt.Sprintf("\n")
	}
	if len(hinfo.Imports.Classes) > 0 {
		out += fmt.Sprintf("@class %s;\n", strings.Join(hinfo.Imports.Classes, ", "))
	}
	if len(hinfo.Imports.Protos) > 0 {
		out += fmt.Sprintf("@protocol %s;\n", strings.Join(hinfo.Imports.Protos, ", "))
	}
	if len(hinfo.Imports.Classes) > 0 || len(hinfo.Imports.Protos) > 0 {
		out += fmt.Sprintf("\n")
	}
	out += fmt.Sprintf("%s\n", hinfo.Object)
	out += fmt.Sprintf("#endif /* %s_h */\n", hinfo.Name)

	if err := os.MkdirAll(filepath.Dir(hinfo.FileName), 0o750); err != nil {
		return err
	}
	log.Infof("Creating %s", hinfo.FileName)
	if err := os.WriteFile(hinfo.FileName, []byte(out), 0644); err != nil {
		return fmt.Errorf("failed to write header %s: %v", hinfo.FileName, err)
	}

	return nil
}

func (o *ObjC) processForwardDeclarations(m *macho.File) (map[string]Imports, error) {
	var classNames []string
	var protoNames []string

	imps := make(map[string]Imports)

	classes, err := m.GetObjCClasses()
	if err != nil {
		if !errors.Is(err, macho.ErrObjcSectionNotFound) {
			return nil, err
		}
	}
	slices.SortStableFunc(classes, func(a, b objc.Class) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, class := range classes {
		classNames = append(classNames, class.Name)
	}

	protos, err := m.GetObjCProtocols()
	if err != nil {
		if !errors.Is(err, macho.ErrObjcSectionNotFound) {
			return nil, err
		}
	}
	slices.SortStableFunc(protos, func(a, b objc.Protocol) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for _, proto := range protos {
		protoNames = append(protoNames, proto.Name)
	}

	for _, class := range classes {
		imp := Imports{}
		if class.SuperClass == "NSObject" {
			imp.Imports = append(imp.Imports, "Foundation")
		}
		for _, prot := range class.Protocols {
			if slices.Contains(protoNames, prot.Name) {
				imp.Locals = append(imp.Locals, prot.Name+"-Protocol.h")
			} else {
				imp.Protos = append(imp.Protos, prot.Name)
			}
		}
		for _, ivar := range class.Ivars {
			typ := ivar.Type
			if strings.ContainsAny(typ, "<>") {
				typ = strings.Trim(typ, "@\"")
				if rest, ok := strings.CutPrefix(typ, "NSObject<"); ok {
					typ = strings.TrimSuffix(rest, ">")
					if slices.Contains(protoNames, typ) {
						imp.Locals = append(imp.Locals, typ+"-Protocol.h")
					} else {
						imp.Protos = append(imp.Protos, typ)
					}
				}
				typ = strings.Trim(typ, "<>")
				if slices.Contains(protoNames, typ) {
					imp.Locals = append(imp.Locals, typ+"-Protocol.h")
				} else {
					imp.Protos = append(imp.Protos, typ)
				}

			} else {
				if rest, ok := strings.CutPrefix(typ, "@\""); ok {
					typ = strings.TrimSuffix(rest, "\"")
					if slices.Contains(classNames, typ) {
						imp.Locals = append(imp.Locals, typ+".h")
					} else {
						imp.Classes = append(imp.Classes, typ)
					}
				}
			}
		}
		for _, prop := range class.Props {
			typ := prop.Type()
			if strings.ContainsAny(typ, "<>") {
				typ = strings.Trim(typ, "@\"")
				typ = strings.Trim(typ, " *")
				if rest, ok := strings.CutPrefix(typ, "NSObject<"); ok {
					typ = strings.TrimSuffix(rest, ">")
					if slices.Contains(protoNames, typ) {
						imp.Locals = append(imp.Locals, typ+"-Protocol.h")
					} else {
						imp.Protos = append(imp.Protos, typ)
					}
				}
				typ = strings.Trim(typ, "<>")
				if slices.Contains(protoNames, typ) {
					imp.Locals = append(imp.Locals, typ+"-Protocol.h")
				} else {
					imp.Protos = append(imp.Protos, typ)
				}
			} else {
				if (len(typ) > 0 && unicode.IsUpper(rune(typ[0])) && unicode.IsLetter(rune(typ[0]))) && strings.HasSuffix(typ, "*") {
					typ = strings.Trim(typ, " *")
					if slices.Contains(classNames, typ) {
						imp.Locals = append(imp.Locals, typ+".h")
					} else {
						imp.Classes = append(imp.Classes, typ)
					}
				}
			}
		}
		for _, method := range class.InstanceMethods {
			for i := 0; i < method.NumberOfArguments(); i++ {
				typ := method.ArgumentType(i)
				if (len(typ) > 0 && unicode.IsUpper(rune(typ[0])) && unicode.IsLetter(rune(typ[0]))) && strings.HasSuffix(typ, "*") { // or < >
					if slices.Contains(classNames, typ) {
						imp.Locals = append(imp.Locals, typ+".h")
					} else if slices.Contains(protoNames, strings.Trim(typ, "NSObject<>")) {
						imp.Locals = append(imp.Locals, strings.Trim(typ, "NSObject<>")+"-Protocol.h")
					} else {
						imp.Classes = append(imp.Classes, typ)
					}
				}
				if i == 0 {
					i += 2
				}
			}
		}
		for _, method := range class.ClassMethods {
			for i := 0; i < method.NumberOfArguments(); i++ {
				typ := method.ArgumentType(i)
				if (len(typ) > 0 && unicode.IsUpper(rune(typ[0])) && unicode.IsLetter(rune(typ[0]))) && strings.HasSuffix(typ, "*") { // or < >
					if slices.Contains(classNames, typ) {
						imp.Locals = append(imp.Locals, typ+".h")
					} else {
						imp.Classes = append(imp.Classes, typ)
					}
				}
				if i == 0 {
					i += 2
				}
			}
		}
		imp.uniq(o.foundation)
		imps[class.Name] = imp
	}

	return imps, nil
}

func (o *ObjC) scanFoundation() error {
	o.foundation["classes"] = []string{}
	o.foundation["protocols"] = []string{}
	if o.cache != nil {
		for _, name := range []string{"Foundation", "CoreFoundation"} {
			img, err := o.cache.Image(name)
			if err != nil {
				return err
			}
			m, err := img.GetMacho()
			if err != nil {
				return err
			}

			classes, err := m.GetObjCClasses()
			if err != nil {
				if !errors.Is(err, macho.ErrObjcSectionNotFound) {
					return err
				}
			}
			slices.SortStableFunc(classes, func(a, b objc.Class) int {
				return cmp.Compare(a.Name, b.Name)
			})
			for _, class := range classes {
				o.foundation["classes"] = append(o.foundation["classes"], class.Name)
			}

			protos, err := m.GetObjCProtocols()
			if err != nil {
				if !errors.Is(err, macho.ErrObjcSectionNotFound) {
					return err
				}
			}
			slices.SortStableFunc(protos, func(a, b objc.Protocol) int {
				return cmp.Compare(a.Name, b.Name)
			})
			for _, proto := range protos {
				o.foundation["protocols"] = append(o.foundation["protocols"], proto.Name)
			}
			slices.Sort(o.foundation["classes"])
			slices.Sort(o.foundation["protocols"])
		}
	}
	return nil
}

func transformSetter(in string) string {
	if strings.HasPrefix(in, "set") {
		in = strings.TrimSuffix(strings.TrimPrefix(in, "set"), ":")
		if len(in) > 0 {
			return strings.ToLower(in[:1]) + in[1:]
		}
		return ""
	}
	return in
}
