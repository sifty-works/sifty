package main

import (
	"fmt"
	"io"

	"github.com/sifty-works/sifty-server/internal/config"
)

func cmdConfigDefaults(stdout io.Writer) error {
	fmt.Fprint(stdout, config.DefaultsYAML())
	return nil
}
