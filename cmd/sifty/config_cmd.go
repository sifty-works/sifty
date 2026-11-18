package main

import (
	"fmt"
	"io"

	"github.com/sifty-works/sifty-server/internal/config"
)

// cmdConfigDefaults prints the full settings file with every default value.
func cmdConfigDefaults(stdout io.Writer) error {
	fmt.Fprint(stdout, config.DefaultsYAML())
	return nil
}
