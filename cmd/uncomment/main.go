package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/format"
	"go/scanner"
	"go/token"
	"os"
	"regexp"
	"sort"
)

var directiveRE = regexp.MustCompile(`^//[a-z0-9]+:`)
var nolintRE = regexp.MustCompile(`^//nolint:`)

func isDirective(lit string) bool {
	return directiveRE.MatchString(lit) && !nolintRE.MatchString(lit)
}

type commentLoc struct {
	file string
	line int
}

func main() {
	listFlag := flag.Bool("l", false, "list file:line of every comment; exit non-zero when any found")
	writeFlag := flag.Bool("w", false, "rewrite files in place, removing all non-directive comments")
	flag.Parse()

	if !*listFlag && !*writeFlag {
		fmt.Fprintf(os.Stderr, "usage: uncomment -l file...\n       uncomment -w file...\n")
		os.Exit(2)
	}
	if flag.NArg() == 0 {
		return
	}

	var found []commentLoc
	for _, f := range flag.Args() {
		locs, err := processFile(f, *writeFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "uncomment: %v\n", err)
			os.Exit(1)
		}
		found = append(found, locs...)
	}

	if *listFlag {
		for _, c := range found {
			fmt.Printf("%s:%d\n", c.file, c.line)
		}
		if len(found) > 0 {
			os.Exit(1)
		}
	}
}

type span struct{ start, end int }

func processFile(filename string, write bool) ([]commentLoc, error) {
	src, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}

	fset := token.NewFileSet()
	file := fset.AddFile(filename, fset.Base(), len(src))

	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)

	var spans []span
	var locs []commentLoc

	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		if isDirective(lit) {
			continue
		}
		p := fset.Position(pos)
		locs = append(locs, commentLoc{file: filename, line: p.Line})
		spans = append(spans, span{start: p.Offset, end: p.Offset + len(lit)})
	}

	if !write || len(spans) == 0 {
		return locs, nil
	}

	removed, err := removeComments(src, spans)
	if err != nil {
		return locs, fmt.Errorf("%s: %w", filename, err)
	}
	if bytes.Equal(removed, src) {
		return locs, nil
	}
	if err := os.WriteFile(filename, removed, 0600); err != nil {
		return locs, fmt.Errorf("%s: %w", filename, err)
	}
	return locs, nil
}

func removeComments(src []byte, spans []span) ([]byte, error) {
	type removal struct{ from, to int }
	var removals []removal

	for _, sp := range spans {
		lit := src[sp.start:sp.end]
		if len(lit) >= 2 && lit[0] == '/' && lit[1] == '/' {
			lineStart := sp.start
			for lineStart > 0 && src[lineStart-1] != '\n' {
				lineStart--
			}
			prefix := bytes.TrimFunc(src[lineStart:sp.start], func(r rune) bool {
				return r == ' ' || r == '\t'
			})
			if len(prefix) == 0 {
				end := sp.end
				if end < len(src) && src[end] == '\n' {
					end++
				}
				removals = append(removals, removal{from: lineStart, to: end})
			} else {
				from := sp.start
				for from > 0 && (src[from-1] == ' ' || src[from-1] == '\t') {
					from--
				}
				removals = append(removals, removal{from: from, to: sp.end})
			}
		} else {
			lineStart := sp.start
			for lineStart > 0 && src[lineStart-1] != '\n' {
				lineStart--
			}
			lineEnd := sp.end
			for lineEnd < len(src) && src[lineEnd] != '\n' {
				lineEnd++
			}
			prefix := bytes.TrimSpace(src[lineStart:sp.start])
			suffix := bytes.TrimSpace(src[sp.end:lineEnd])
			if len(prefix) == 0 && len(suffix) == 0 {
				end := lineEnd
				if end < len(src) && src[end] == '\n' {
					end++
				}
				removals = append(removals, removal{from: lineStart, to: end})
			} else {
				removals = append(removals, removal{from: sp.start, to: sp.end})
			}
		}
	}

	sort.Slice(removals, func(i, j int) bool { return removals[i].from < removals[j].from })

	var out []byte
	pos := 0
	for _, r := range removals {
		if r.from >= pos {
			out = append(out, src[pos:r.from]...)
			pos = r.to
		}
	}
	out = append(out, src[pos:]...)

	return format.Source(out)
}
